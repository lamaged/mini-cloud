package vault

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func newTestHeader(plainSize int64) *Header {
	return &Header{
		Magic:       [4]byte{'M', 'C', 'F', '1'},
		Version:     Version1,
		Flags:       0,
		ChunkSize:   DefaultChunkSize,
		PlainSize:   plainSize,
		FileID:      [16]byte{1, 2, 3, 4},
		NoncePrefix: [4]byte{9, 9, 9, 9},
		MetaLen:     100,
		DataOffset:  HeaderSize + 100,
	}
}

func TestHeaderRoundtrip(t *testing.T) {
	for _, size := range []int64{0, 1, 1000, 1 << 20, (1 << 20) + 1} {
		h := newTestHeader(size)
		h2, err := UnmarshalHeader(h.Marshal())
		if err != nil {
			t.Fatalf("size=%d: %v", size, err)
		}
		if !bytes.Equal(h.Marshal(), h2.Marshal()) {
			t.Errorf("size=%d: roundtrip mismatch", size)
		}
		if h2.ChunkCount() != h.ChunkCount() {
			t.Errorf("size=%d: chunk count %d != %d", size, h2.ChunkCount(), h.ChunkCount())
		}
	}
}

func TestChunkMath(t *testing.T) {
	// 1 MiB 分块：2 MiB + 1 字节 = 3 块
	h := newTestHeader(2*(1<<20) + 1)
	if h.ChunkCount() != 3 {
		t.Fatalf("ChunkCount = %d, want 3", h.ChunkCount())
	}
	if h.ChunkPlainLen(0) != 1<<20 || h.ChunkPlainLen(1) != 1<<20 || h.ChunkPlainLen(2) != 1 {
		t.Errorf("ChunkPlainLen = %d,%d,%d", h.ChunkPlainLen(0), h.ChunkPlainLen(1), h.ChunkPlainLen(2))
	}
	if h.ChunkCipherLen(2) != 1+TagSize {
		t.Errorf("last chunk cipher len = %d", h.ChunkCipherLen(2))
	}
	// 密文总大小 = 头+元数据 + 明文大小 + 每块 16B tag（I2 修正后公式）
	want := h.DataOffset + uint64(h.PlainSize) + 3*TagSize
	if h.CipherSize() != want {
		t.Errorf("CipherSize = %d, want %d", h.CipherSize(), want)
	}
	// 空文件：0 块
	h0 := newTestHeader(0)
	if h0.ChunkCount() != 0 || h0.CipherSize() != h0.DataOffset {
		t.Errorf("empty file math wrong: chunks=%d size=%d", h0.ChunkCount(), h0.CipherSize())
	}
	// I2 非对齐明文：1.5 MiB = 2 块，密文 = 头+元数据 + 1.5MiB + 2*16B
	hNonAlign := newTestHeader((1<<20)*3/2)
	wantNonAlign := hNonAlign.DataOffset + uint64(hNonAlign.PlainSize) + 2*TagSize
	if hNonAlign.CipherSize() != wantNonAlign {
		t.Errorf("非对齐 CipherSize = %d, want %d", hNonAlign.CipherSize(), wantNonAlign)
	}
}

func TestUnmarshalHeaderValidations(t *testing.T) {
	valid := newTestHeader(1000).Marshal()

	mutate := func(fn func(b []byte)) []byte {
		b := append([]byte(nil), valid...)
		fn(b)
		return b
	}

	tests := []struct {
		name string
		data []byte
	}{
		{"过短", valid[:40]},
		{"坏 magic", mutate(func(b []byte) { b[0] = 'X' })},
		{"坏版本", mutate(func(b []byte) { b[4] = 2 })},
		{"非零 flags", mutate(func(b []byte) { b[5] = 1 })},
		{"chunk_size 过小", mutate(func(b []byte) { b[6], b[7], b[8], b[9] = 0, 0, 1, 0 })},
		{"chunk_size 过大", mutate(func(b []byte) { b[6] = 0xFF; b[7] = 0xFF; b[8] = 0xFF; b[9] = 0xFF })},
		{"plain_size 为负", mutate(func(b []byte) { for i := 10; i < 18; i++ { b[i] = 0xFF } })},
		{"meta_len 超上限", mutate(func(b []byte) { b[38] = 0xFF; b[39] = 0xFF; b[40] = 0xFF; b[41] = 0xFF })},
		{"data_offset 不符", mutate(func(b []byte) { b[42] = 0x01 })},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := UnmarshalHeader(tc.data); err == nil {
				t.Error("应拒绝，但解析成功")
			}
		})
	}
}

func TestChunkNonceAndAAD(t *testing.T) {
	h := newTestHeader(1 << 20)
	n0 := h.ChunkNonce(0)
	n1 := h.ChunkNonce(1)
	if len(n0) != 12 || len(n1) != 12 {
		t.Fatalf("nonce 长度错误")
	}
	// 前缀相同、序号不同（防重排）
	if !bytes.Equal(n0[:4], n1[:4]) {
		t.Error("nonce 前缀应相同")
	}
	if bytes.Equal(n0, n1) {
		t.Error("不同块的 nonce 必须不同")
	}
	// AAD = SHA256(header) + idx
	a0 := h.ChunkAAD(0)
	a1 := h.ChunkAAD(1)
	if len(a0) != 40 || len(a1) != 40 {
		t.Fatalf("AAD 长度 = %d, %d", len(a0), len(a1))
	}
	if !bytes.Equal(a0[:32], a1[:32]) {
		t.Error("AAD 头哈希部分应相同")
	}
	if bytes.Equal(a0, a1) {
		t.Error("不同块的 AAD 必须不同")
	}
}

// TestHKDFRFC5869 用 RFC 5869 A.1 测试向量验证自实现 HKDF-SHA256 的正确性
func TestHKDFRFC5869(t *testing.T) {
	ikm := bytes.Repeat([]byte{0x0b}, 22)
	salt := make([]byte, 13)
	for i := range salt {
		salt[i] = byte(i)
	}
	info := make([]byte, 10)
	for i := range info {
		info[i] = byte(0xf0 + i)
	}
	got := hkdfSHA256(ikm, salt, info, 42)
	want, _ := hex.DecodeString("3cb25f25faacd57a90434f64d0362f2a2d2d0a90cf1a5a4c5db02d56ecc4c5bf34007208d5b887185865")
	if !bytes.Equal(got, want) {
		t.Errorf("HKDF 与 RFC 5869 测试向量不符\ngot  %x\nwant %x", got, want)
	}
}
