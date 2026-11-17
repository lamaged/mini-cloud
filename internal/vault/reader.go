package vault

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"time"
)

// RangeSource 密文上游：按偏移随机读取。
// Phase 0 由 *os.File 实现；Phase 1 由云盘 Range 请求实现（internal/vault/upstream.go）。
type RangeSource interface {
	ReadAt(p []byte, off int64) (int, error)
	Close() error
}

// DecryptReader 对单个加密对象提供明文随机访问（io.ReadSeeker + Stat）。
// 非并发安全：与 io.Reader 惯例一致，调用方串行使用（http.ServeContent 满足）。
type DecryptReader struct {
	src     RangeSource
	header  Header
	fileKey []byte
	meta    *Metadata

	offset   int64  // 当前明文偏移
	chunkIdx uint64 // 当前缓存块
	chunk    []byte // 当前块明文（≤1 MiB）
	hasChunk bool
}

// OpenDecryptReader 打开加密对象：
// 读头 → 严格校验（9 项字段规则）→ fileID 一致性（I12）→ header_sha256 一致性（I2）
// → 解密元数据。wantHeaderSHA 可为 nil（跳过 I2 校验，仅 mcrypt 内部用）。
func OpenDecryptReader(src RangeSource, master []byte, wantFileID [16]byte, wantHeaderSHA []byte) (*DecryptReader, error) {
	hdr := make([]byte, HeaderSize)
	n, err := src.ReadAt(hdr, 0)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("vault: 读文件头失败: %w", err)
	}
	if n < HeaderSize {
		return nil, fmt.Errorf("vault: 文件头不足 64 字节: %d", n)
	}
	h, err := UnmarshalHeader(hdr)
	if err != nil {
		return nil, err
	}
	if h.FileID != wantFileID {
		return nil, fmt.Errorf("vault: fileID 不一致（I12：manifest 与对象头必须一致）")
	}
	if wantHeaderSHA != nil && !bytes.Equal(h.HeaderHash(), wantHeaderSHA) {
		return nil, fmt.Errorf("vault: 文件头哈希不一致（I2：头部被篡改）")
	}

	var meta *Metadata
	if h.MetaLen > 0 {
		blob := make([]byte, h.MetaLen)
		n, err := src.ReadAt(blob, HeaderSize)
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("vault: 读元数据失败: %w", err)
		}
		if n != int(h.MetaLen) {
			// P1：短读且无错误（Phase 1 网络源常见）不得被当成完整元数据
			return nil, fmt.Errorf("vault: 元数据被截断: %d/%d", n, h.MetaLen)
		}
		meta, err = OpenMeta(FileMetaKey(master, h.FileID), h, blob)
		if err != nil {
			return nil, err
		}
		if err := ValidateMetadata(h, meta); err != nil {
			return nil, err
		}
	}

	return &DecryptReader{
		src:     src,
		header:  *h,
		fileKey: FileDataKey(master, h.FileID),
		meta:    meta,
	}, nil
}

// Read 顺序读取明文（跨块自动加载下一块）
func (r *DecryptReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.offset >= r.header.PlainSize {
		return 0, io.EOF
	}
	n := 0
	for n < len(p) {
		idx := uint64(r.offset / int64(r.header.ChunkSize))
		if !r.hasChunk || r.chunkIdx != idx {
			if err := r.loadChunk(idx); err != nil {
				return n, err
			}
		}
		chunkOff := r.offset - int64(r.chunkIdx)*int64(r.header.ChunkSize)
		c := copy(p[n:], r.chunk[chunkOff:])
		if c == 0 {
			// P5 防御：块内容与偏移矛盾（未来逻辑变化时避免死循环）
			return n, io.ErrUnexpectedEOF
		}
		n += c
		r.offset += int64(c)
		if r.offset >= r.header.PlainSize {
			break
		}
	}
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

// loadChunk 读取并解密第 idx 块（认证失败返回 ErrAuth 包装错误）
func (r *DecryptReader) loadChunk(idx uint64) error {
	cLen := r.header.ChunkCipherLen(idx)
	if cLen <= 0 {
		return fmt.Errorf("vault: 块 %d 越界", idx)
	}
	buf := make([]byte, cLen)
	n, err := r.src.ReadAt(buf, int64(r.header.ChunkCipherOffset(idx)))
	if err != nil && err != io.EOF {
		return fmt.Errorf("vault: 读密文块 %d 失败: %w", idx, err)
	}
	if int64(n) != cLen {
		return fmt.Errorf("vault: 密文块 %d 不完整: %d/%d（文件被截断）", idx, n, cLen)
	}
	plain, err := open(r.fileKey, r.header.ChunkNonce(idx), buf, r.header.ChunkAAD(idx))
	if err != nil {
		return err
	}
	r.chunkIdx = idx
	r.chunk = plain
	r.hasChunk = true
	return nil
}

// Seek 随机访问（视频拖动）：Seek(0, End) 直接返回明文大小、不读密文
func (r *DecryptReader) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = r.offset + offset
	case io.SeekEnd:
		abs = r.header.PlainSize + offset
	default:
		return 0, fmt.Errorf("vault: 非法 whence %d", whence)
	}
	if abs < 0 {
		return 0, fmt.Errorf("vault: 负偏移 %d", abs)
	}
	r.offset = abs
	r.hasChunk = false // 下一读时按新位置加载
	return abs, nil
}

// Close 关闭上游
func (r *DecryptReader) Close() error { return r.src.Close() }

// Stat 明文文件信息（http.ServeContent 的 Content-Length/Last-Modified 来源）
func (r *DecryptReader) Stat() (os.FileInfo, error) {
	return &plainFileInfo{
		name: r.headerName(),
		size: r.header.PlainSize,
		mod:  r.modTime(),
	}, nil
}

// OriginalPath 元数据中的原始虚拟路径（I3：与请求路径比对，防跨对象替换；Phase 1 强校验）
func (r *DecryptReader) OriginalPath() string {
	if r.meta != nil {
		return r.meta.P
	}
	return ""
}

func (r *DecryptReader) headerName() string {
	if r.meta != nil && r.meta.N != "" {
		return r.meta.N
	}
	return ""
}

func (r *DecryptReader) modTime() time.Time {
	if r.meta != nil && r.meta.M > 0 {
		return time.Unix(r.meta.M, 0)
	}
	return time.Time{}
}

// plainFileInfo 明文文件信息
type plainFileInfo struct {
	name string
	size int64
	mod  time.Time
}

func (f *plainFileInfo) Name() string       { return f.name }
func (f *plainFileInfo) Size() int64        { return f.size }
func (f *plainFileInfo) Mode() os.FileMode  { return 0 }
func (f *plainFileInfo) ModTime() time.Time { return f.mod }
func (f *plainFileInfo) IsDir() bool        { return false }
func (f *plainFileInfo) Sys() interface{}   { return nil }
