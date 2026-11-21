package vault

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"testing"
)

func TestSealOpenRoundtrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	nonce := bytes.Repeat([]byte{0x07}, 12)
	plain := []byte("hello vault 你好")
	aad := []byte("aad-data")

	ct, err := seal(key, nonce, plain, aad)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	got, err := open(key, nonce, ct, aad)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Error("roundtrip mismatch")
	}
}

func TestOpenDetectsTampering(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	nonce := bytes.Repeat([]byte{0x07}, 12)
	plain := []byte("sensitive data")
	ct, _ := seal(key, nonce, plain, nil)

	// 篡改密文任一字节 → ErrAuth
	bad := append([]byte(nil), ct...)
	bad[len(bad)/2] ^= 0x01
	if _, err := open(key, nonce, bad, nil); !errors.Is(err, ErrAuth) {
		t.Errorf("篡改密文应返回 ErrAuth，got %v", err)
	}
	// 篡改 AAD → ErrAuth
	if _, err := open(key, nonce, ct, []byte("other")); !errors.Is(err, ErrAuth) {
		t.Errorf("篡改 AAD 应返回 ErrAuth，got %v", err)
	}
	// 截断（丢 tag 半截）→ ErrAuth
	if _, err := open(key, nonce, ct[:len(ct)-3], nil); !errors.Is(err, ErrAuth) {
		t.Errorf("截断应返回 ErrAuth，got %v", err)
	}
}

func TestKeyDerivation(t *testing.T) {
	master := bytes.Repeat([]byte{0x11}, 32)
	var idA [16]byte
	var idB [16]byte
	idB[0] = 1

	// 同 fileID 派生稳定
	if !bytes.Equal(FileDataKey(master, idA), FileDataKey(master, idA)) {
		t.Error("同 fileID 应派生相同密钥")
	}
	// 不同 fileID 派生不同（防跨文件碰撞）
	if bytes.Equal(FileDataKey(master, idA), FileDataKey(master, idB)) {
		t.Error("不同 fileID 密钥必须不同")
	}
	// 不同用途信息不同（数据/元数据/manifest 三把钥匙互不相同）
	if bytes.Equal(FileDataKey(master, idA), FileMetaKey(master, idA)) {
		t.Error("数据密钥与元数据密钥必须不同")
	}
	if bytes.Equal(FileDataKey(master, idA), ManifestKey(master)) {
		t.Error("数据密钥与 manifest 密钥必须不同")
	}
	for _, k := range [][]byte{FileDataKey(master, idA), FileMetaKey(master, idA), ManifestKey(master)} {
		if len(k) != 32 {
			t.Errorf("密钥长度 = %d, want 32", len(k))
		}
	}
}

func TestManifestRoundtrip(t *testing.T) {
	master := bytes.Repeat([]byte{0x33}, 32)
	key := ManifestKey(master)

	m := &Manifest{
		V: 1,
		Files: []Entry{
			{Path: "/电影/a.mkv", Name: "a.mkv", Size: 12345, MTime: 1712345678,
				MIME: "video/x-matroska", FileID: EncodeFileID([16]byte{1}), Obj: ObjectPath(EncodeFileID([16]byte{1})),
				HeaderSHA256: "abc", SHA256: "def"},
		},
		Dirs: []string{"/电影", "/电影/子目录"},
	}

	var buf bytes.Buffer
	if err := SaveManifest(&buf, key, m); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	raw := buf.Bytes()

	loaded, err := LoadManifest(bytes.NewReader(raw), key)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(loaded.Files) != 1 || loaded.Files[0].Path != "/电影/a.mkv" || loaded.Files[0].Size != 12345 {
		t.Errorf("manifest 还原不符: %+v", loaded.Files)
	}
	if len(loaded.Dirs) != 2 {
		t.Errorf("dirs 还原不符: %v", loaded.Dirs)
	}

	// 错误密钥 → 认证失败
	wrongKey := bytes.Repeat([]byte{0x99}, 32)
	if _, err := LoadManifest(bytes.NewReader(raw), wrongKey); !errors.Is(err, ErrAuth) {
		t.Errorf("错误密钥应返回 ErrAuth，got %v", err)
	}
}

func TestFileIDEncoding(t *testing.T) {
	id := [16]byte{0xde, 0xad, 0xbe, 0xef}
	s := EncodeFileID(id)
	if len(s) != 26 {
		t.Errorf("Base32 fileID 长度 = %d, want 26", len(s))
	}
	back, err := DecodeFileID(s)
	if err != nil {
		t.Fatalf("DecodeFileID: %v", err)
	}
	if back != id {
		t.Error("fileID 编解码不一致")
	}
	if p := ObjectPath(s); p != "_mc/o/"+s[:2]+"/"+s[2:4]+"/"+s+".bin" {
		t.Errorf("ObjectPath = %q", p)
	}
}

// TestChunkEncryptDecrypt 完整分块加解密流程（含 AAD 绑定）
func TestChunkEncryptDecrypt(t *testing.T) {
	master := bytes.Repeat([]byte{0x55}, 32)
	h := newTestHeader(2*(1<<20) + 1000)
	key := FileDataKey(master, h.FileID)

	for idx := uint64(0); idx < h.ChunkCount(); idx++ {
		plain := bytes.Repeat([]byte{byte(idx)}, int(h.ChunkPlainLen(idx)))
		ct, err := seal(key, h.ChunkNonce(idx), plain, h.ChunkAAD(idx))
		if err != nil {
			t.Fatalf("chunk %d seal: %v", idx, err)
		}
		if len(ct) != int(h.ChunkPlainLen(idx))+TagSize {
			t.Fatalf("chunk %d 密文长度 = %d", idx, len(ct))
		}
		got, err := open(key, h.ChunkNonce(idx), ct, h.ChunkAAD(idx))
		if err != nil {
			t.Fatalf("chunk %d open: %v", idx, err)
		}
		if !bytes.Equal(got, plain) {
			t.Errorf("chunk %d roundtrip mismatch", idx)
		}
		// 块重排攻击：用 idx 的 nonce 解密其它块内容 → ErrAuth
		if idx > 0 {
			if _, err := open(key, h.ChunkNonce(idx-1), ct, h.ChunkAAD(idx-1)); !errors.Is(err, ErrAuth) {
				t.Errorf("块重排应被拒绝")
			}
		}
	}
}

// 明文 SHA-256 帮助函数（加密工具与测试共用）
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return string(sum[:])
}
