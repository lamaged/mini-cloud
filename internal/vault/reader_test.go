package vault

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// countingSource 统计实际读取字节数的 RangeSource（验证 Seek 不整文件下载）
type countingSource struct {
	rs        io.ReaderAt
	closer    io.Closer
	bytesRead int64
}

func (c *countingSource) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.rs.ReadAt(p, off)
	c.bytesRead += int64(n)
	return n, err
}
func (c *countingSource) Close() error { return c.closer.Close() }

// encryptTestObject 构造一个加密对象（含头/元数据/分块），返回密文、头与权威哈希
func encryptTestObject(t *testing.T, master []byte, fileID [16]byte, plain []byte, path string) ([]byte, *Header, string) {
	t.Helper()
	meta := &Metadata{V: 1, P: path, N: filepath.Base(path), S: int64(len(plain)),
		M: time.Now().Unix(), T: "application/octet-stream"}
	var buf bytes.Buffer
	h, shaHex, err := EncryptObject(&buf, master, bytes.NewReader(plain), int64(len(plain)), fileID, meta)
	if err != nil {
		t.Fatalf("EncryptObject: %v", err)
	}
	return buf.Bytes(), h, shaHex
}

// TestLoadChunkReusesBuffers P2（RV-2026-11-18-02）：块加载复用 cbuf/pbuf——
// 预热后每次跨块加载不得出现 MiB 级分配（用 Benchmark 的 AllocedBytesPerOp 锁定，
// 防将来改回每块 make 无人发现）
func TestLoadChunkReusesBuffers(t *testing.T) {
	master := bytes.Repeat([]byte{0xAA}, 32)
	fileID, _ := NewFileID()
	plain := make([]byte, 3*(1<<20)) // 3 块，交替读触发每次 loadChunk
	rand.Read(plain)
	cipher, h, _ := encryptTestObject(t, master, fileID, plain, "/a.bin")

	cs := &countingSource{rs: bytes.NewReader(cipher), closer: io.NopCloser(nil)}
	r, err := OpenDecryptReader(cs, master, fileID, h.HeaderHash())
	if err != nil {
		t.Fatalf("OpenDecryptReader: %v", err)
	}
	defer r.Close()

	// 预热：cbuf/pbuf 扩容到位
	if _, err := r.Seek(2*(1<<20), io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(r, make([]byte, 16)); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 16)
	res := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			off := int64((i % 3) * (1 << 20)) // 0/1/2 块交替，每轮一次 loadChunk
			if _, err := r.Seek(off, io.SeekStart); err != nil {
				b.Fatal(err)
			}
			if _, err := io.ReadFull(r, buf); err != nil {
				b.Fatal(err)
			}
		}
	})
	if res.AllocedBytesPerOp() > 64<<10 {
		t.Errorf("每块加载分配应 < 64 KiB（复用缓冲），实际 %d B/op", res.AllocedBytesPerOp())
	}
}

func TestDecryptReaderFullRead(t *testing.T) {
	master := bytes.Repeat([]byte{0xAA}, 32)
	fileID, _ := NewFileID()
	// 3 MiB + 123 字节：跨 4 块
	plain := make([]byte, 3*(1<<20)+123)
	rand.Read(plain)
	cipher, h, wantSHA := encryptTestObject(t, master, fileID, plain, "/电影/大文件.mkv")

	cs := &countingSource{rs: bytes.NewReader(cipher), closer: io.NopCloser(nil)}
	r, err := OpenDecryptReader(cs, master, fileID, h.HeaderHash())
	if err != nil {
		t.Fatalf("OpenDecryptReader: %v", err)
	}
	defer r.Close()

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("明文不一致")
	}
	if hex.EncodeToString(got) != "" && wantSHA != "" {
		sum := sha256.Sum256(got)
		if hex.EncodeToString(sum[:]) != wantSHA {
			t.Fatal("SHA-256 不一致（验收：全文件 SHA-256 一致）")
		}
	}
	// 顺序全读：恰好读完整个密文
	if cs.bytesRead != int64(len(cipher)) {
		t.Errorf("顺序读取字节 = %d, want %d", cs.bytesRead, len(cipher))
	}
}

func TestDecryptReaderRandomSeek(t *testing.T) {
	master := bytes.Repeat([]byte{0xAA}, 32)
	fileID, _ := NewFileID()
	plain := make([]byte, 10*(1<<20)+77) // 10 MiB：10 块 + 尾巴
	rand.Read(plain)
	cipher, h, _ := encryptTestObject(t, master, fileID, plain, "/a.bin")

	cs := &countingSource{rs: bytes.NewReader(cipher), closer: io.NopCloser(nil)}
	r, err := OpenDecryptReader(cs, master, fileID, h.HeaderHash())
	if err != nil {
		t.Fatalf("OpenDecryptReader: %v", err)
	}
	defer r.Close()

	// 拖动到第 7 块中间（验收：随机 Seek 不重新下载整个文件）
	seekOff := int64(7*(1<<20)) + 12345
	if _, err := r.Seek(seekOff, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	got := make([]byte, 1000)
	if _, err := io.ReadFull(r, got); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(got, plain[seekOff:seekOff+1000]) {
		t.Fatal("Seek 后读取内容不符")
	}
	// 只应读取 1~2 个块（含 tag），远小于整个密文
	maxExpected := int64(2*(DefaultChunkSize+TagSize)) + HeaderSize + 4096
	if cs.bytesRead > maxExpected {
		t.Errorf("随机 Seek 读取 %d 字节，超过单块上限 %d（不应整文件下载）", cs.bytesRead, maxExpected)
	}

	// Seek(0, End) 返回明文大小且不读密文
	before := cs.bytesRead
	if off, err := r.Seek(0, io.SeekEnd); err != nil || off != int64(len(plain)) {
		t.Errorf("Seek End = %d, %v", off, err)
	}
	if cs.bytesRead != before {
		t.Error("Seek End 不应读密文")
	}

	// 负偏移拒绝
	if _, err := r.Seek(-1, io.SeekStart); err == nil {
		t.Error("负偏移应报错")
	}
}

func TestDecryptReaderRejectsTampered(t *testing.T) {
	master := bytes.Repeat([]byte{0xAA}, 32)
	fileID, _ := NewFileID()
	plain := make([]byte, 2*(1<<20))
	rand.Read(plain)
	cipher, h, _ := encryptTestObject(t, master, fileID, plain, "/a.bin")

	// 篡改第一块密文一个字节
	bad := append([]byte(nil), cipher...)
	bad[len(bad)-100] ^= 0xFF

	cs := &countingSource{rs: bytes.NewReader(bad), closer: io.NopCloser(nil)}
	r, err := OpenDecryptReader(cs, master, fileID, h.HeaderHash())
	if err != nil {
		t.Fatalf("OpenDecryptReader: %v", err)
	}
	defer r.Close()
	if _, err := io.ReadAll(r); !errors.Is(err, ErrAuth) {
		t.Errorf("篡改密文应返回 ErrAuth，got %v", err)
	}
}

func TestDecryptReaderEmptyFile(t *testing.T) {
	master := bytes.Repeat([]byte{0xAA}, 32)
	fileID, _ := NewFileID()
	cipher, h, _ := encryptTestObject(t, master, fileID, nil, "/空文件.dat")

	cs := &countingSource{rs: bytes.NewReader(cipher), closer: io.NopCloser(nil)}
	r, err := OpenDecryptReader(cs, master, fileID, h.HeaderHash())
	if err != nil {
		t.Fatalf("OpenDecryptReader: %v", err)
	}
	defer r.Close()
	if _, err := io.ReadAll(r); err != nil {
		t.Fatalf("空文件读取应返回 EOF，got %v", err)
	}
	// 头部认证（I2）：篡改头部 → header_sha256 校验拒绝
	badHdr := append([]byte(nil), cipher...)
	badHdr[20] ^= 0x01 // 改 fileID 一个字节
	cs2 := &countingSource{rs: bytes.NewReader(badHdr), closer: io.NopCloser(nil)}
	if _, err := OpenDecryptReader(cs2, master, fileID, h.HeaderHash()); err == nil {
		t.Error("头部被篡改时应被 header_sha256 校验拒绝")
	}
}

func TestDecryptReaderFileIDConsistency(t *testing.T) {
	master := bytes.Repeat([]byte{0xAA}, 32)
	fileID, _ := NewFileID()
	otherID, _ := NewFileID()
	cipher, h, _ := encryptTestObject(t, master, fileID, []byte("hello"), "/a.bin")

	// I12：manifest 记录的 fileID 与对象头不一致 → 拒绝（防跨对象替换）
	cs := &countingSource{rs: bytes.NewReader(cipher), closer: io.NopCloser(nil)}
	if _, err := OpenDecryptReader(cs, master, otherID, h.HeaderHash()); err == nil {
		t.Fatal("fileID 不一致必须被拒绝")
	}
}

func TestDecryptReaderStat(t *testing.T) {
	master := bytes.Repeat([]byte{0xAA}, 32)
	fileID, _ := NewFileID()
	plain := []byte("stat test data")
	cipher, h, _ := encryptTestObject(t, master, fileID, plain, "/电影/星际穿越.mkv")

	cs := &countingSource{rs: bytes.NewReader(cipher), closer: io.NopCloser(nil)}
	r, err := OpenDecryptReader(cs, master, fileID, h.HeaderHash())
	if err != nil {
		t.Fatalf("OpenDecryptReader: %v", err)
	}
	defer r.Close()
	fi, err := r.Stat()
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Size() != int64(len(plain)) {
		t.Errorf("Stat size = %d, want %d", fi.Size(), len(plain))
	}
	if fi.Name() != "星际穿越.mkv" {
		t.Errorf("Stat name = %q（应为原始文件名）", fi.Name())
	}
	if fi.IsDir() {
		t.Error("文件不应是目录")
	}
}

// shortMetaSource 模拟 Phase 1 网络源短读：头部给足 64B，元数据只给 5 字节且不报错
type shortMetaSource struct {
	data []byte
}

func (s *shortMetaSource) ReadAt(p []byte, off int64) (int, error) {
	if off == 0 {
		return copy(p, s.data[:HeaderSize]), nil
	}
	if off == HeaderSize {
		return copy(p, s.data[off:off+5]), nil
	}
	return copy(p, s.data[off:]), nil
}
func (s *shortMetaSource) Close() error { return nil }

// TestOpenDecryptReaderMetaTruncated P1：短读且无错误不得被当成完整元数据
func TestOpenDecryptReaderMetaTruncated(t *testing.T) {
	master := bytes.Repeat([]byte{0xAA}, 32)
	fileID, _ := NewFileID()
	cipher, h, _ := encryptTestObject(t, master, fileID, []byte("hello world"), "/a.bin")

	cs := &shortMetaSource{data: cipher}
	if _, err := OpenDecryptReader(cs, master, fileID, h.HeaderHash()); err == nil ||
		!strings.Contains(err.Error(), "截断") {
		t.Errorf("元数据短读应报「截断」，got %v", err)
	}
}

// 测试用临时 vault 根目录构建（后续 Vault 层测试共用）
func writeTestFile(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
		t.Fatal(err)
	}
}
