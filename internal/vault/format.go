// Package vault 提供加密文件格式（MCF1）、加解密、manifest 与解密读取器。
// 格式规范见 docs/vault-feasibility.md §7.3 与格式不变量（评审 RV-01 I1–I4 / RV-02 I12/I14 定稿）。
package vault

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// MCF1 文件格式常量
const (
	Magic      = "MCF1"
	HeaderSize = 64
	Version1   = 1

	DefaultChunkSize = 1 << 20 // 1 MiB
	MinChunkSize     = 64 << 10
	MaxChunkSize     = 1 << 20
	MaxMetaLen       = 4 << 10 // 4 KiB
	TagSize          = 16      // GCM tag
)

// Header MCF1 文件头（64 字节，大端）。
// 字段语义（I14 定稿）：key_id Phase 0 恒零、Phase 2 轮换启用；
// flags 非零即拒绝；reserved 忽略但保留。
type Header struct {
	Magic       [4]byte  // "MCF1"
	Version     uint8    // 1
	Flags       uint8    // Phase 0 必须为 0；读取非零即拒绝
	ChunkSize   uint32   // 明文分块大小 [64KiB, 1MiB]
	PlainSize   int64    // 明文总大小（>= 0）
	FileID      [16]byte // 文件唯一 ID（每次加密新生成，禁止复用，I1）
	NoncePrefix [4]byte  // 随机前缀（每文件生成一次）
	MetaLen     uint32   // 加密元数据长度（<= 4KiB）
	DataOffset  uint64   // == 64 + MetaLen
	KeyID       [8]byte  // Phase 0 恒零；Phase 2 轮换启用
	Reserved    [6]byte  // Phase 0 必须为零；读取忽略但保留
}

// Marshal 序列化为 64 字节（确定性：所有字段固定）
func (h *Header) Marshal() []byte {
	buf := make([]byte, HeaderSize)
	copy(buf[0:4], h.Magic[:])
	buf[4] = h.Version
	buf[5] = h.Flags
	binary.BigEndian.PutUint32(buf[6:10], h.ChunkSize)
	binary.BigEndian.PutUint64(buf[10:18], uint64(h.PlainSize))
	copy(buf[18:34], h.FileID[:])
	copy(buf[34:38], h.NoncePrefix[:])
	binary.BigEndian.PutUint32(buf[38:42], h.MetaLen)
	binary.BigEndian.PutUint64(buf[42:50], h.DataOffset)
	copy(buf[50:58], h.KeyID[:])
	copy(buf[58:64], h.Reserved[:])
	return buf
}

// UnmarshalHeader 解析并严格校验（I2：违反即拒绝，绝不夹取）
func UnmarshalHeader(data []byte) (*Header, error) {
	if len(data) < HeaderSize {
		return nil, fmt.Errorf("vault: 文件头过短: %d < %d", len(data), HeaderSize)
	}
	var h Header
	copy(h.Magic[:], data[0:4])
	if string(h.Magic[:]) != Magic {
		return nil, fmt.Errorf("vault: 非法 magic %q", h.Magic)
	}
	h.Version = data[4]
	if h.Version != Version1 {
		return nil, fmt.Errorf("vault: 不支持的版本 %d", h.Version)
	}
	h.Flags = data[5]
	if h.Flags != 0 {
		return nil, fmt.Errorf("vault: 未定义的 flags 0x%x", h.Flags)
	}
	h.ChunkSize = binary.BigEndian.Uint32(data[6:10])
	if h.ChunkSize < MinChunkSize || h.ChunkSize > MaxChunkSize {
		return nil, fmt.Errorf("vault: chunk_size %d 超出范围 [%d, %d]", h.ChunkSize, MinChunkSize, MaxChunkSize)
	}
	h.PlainSize = int64(binary.BigEndian.Uint64(data[10:18]))
	if h.PlainSize < 0 {
		return nil, fmt.Errorf("vault: plain_size 为负")
	}
	copy(h.FileID[:], data[18:34])
	copy(h.NoncePrefix[:], data[34:38])
	h.MetaLen = binary.BigEndian.Uint32(data[38:42])
	if h.MetaLen > MaxMetaLen {
		return nil, fmt.Errorf("vault: meta_len %d 超上限 %d", h.MetaLen, MaxMetaLen)
	}
	h.DataOffset = binary.BigEndian.Uint64(data[42:50])
	if h.DataOffset != HeaderSize+uint64(h.MetaLen) {
		return nil, fmt.Errorf("vault: data_offset %d != 64+meta_len（%d）", h.DataOffset, HeaderSize+h.MetaLen)
	}
	copy(h.KeyID[:], data[50:58])
	copy(h.Reserved[:], data[58:64])
	return &h, nil
}

// ChunkCount 分块总数（空文件为 0）
func (h *Header) ChunkCount() uint64 {
	if h.PlainSize <= 0 {
		return 0
	}
	return uint64((h.PlainSize + int64(h.ChunkSize) - 1) / int64(h.ChunkSize))
}

// CipherSize 密文总大小（头 + 元数据 + 分块）
func (h *Header) CipherSize() uint64 {
	return h.DataOffset + h.ChunkCount()*(uint64(h.ChunkSize)+TagSize)
}

// ChunkCipherOffset 第 idx 块的密文起始偏移
func (h *Header) ChunkCipherOffset(idx uint64) uint64 {
	return h.DataOffset + idx*(uint64(h.ChunkSize)+TagSize)
}

// ChunkPlainLen 第 idx 块的明文长度（最后一块可短；越界为 0）
func (h *Header) ChunkPlainLen(idx uint64) int64 {
	start := int64(idx) * int64(h.ChunkSize)
	if start >= h.PlainSize {
		return 0
	}
	remain := h.PlainSize - start
	if remain > int64(h.ChunkSize) {
		remain = int64(h.ChunkSize)
	}
	return remain
}

// ChunkCipherLen 第 idx 块密文长度（含 16B tag）
func (h *Header) ChunkCipherLen(idx uint64) int64 {
	n := h.ChunkPlainLen(idx)
	if n <= 0 {
		return 0
	}
	return n + TagSize
}

// ChunkNonce 第 idx 块 nonce：4B 随机前缀 + 8B 块序号（12B）
func (h *Header) ChunkNonce(idx uint64) []byte {
	var nonce [12]byte
	copy(nonce[0:4], h.NoncePrefix[:])
	binary.BigEndian.PutUint64(nonce[4:12], idx)
	return nonce[:]
}

// ChunkAAD 第 idx 块附加认证数据：SHA256(64B 文件头) + 8B 块序号（防头篡改与块重排）
func (h *Header) ChunkAAD(idx uint64) []byte {
	sum := sha256.Sum256(h.Marshal())
	aad := make([]byte, 0, sha256.Size+8)
	aad = append(aad, sum[:]...)
	var idxBuf [8]byte
	binary.BigEndian.PutUint64(idxBuf[:], idx)
	aad = append(aad, idxBuf[:]...)
	return aad
}

// HeaderHash 64B 文件头的 SHA-256（manifest 的 header_sha256 字段，I2 空文件头部认证）
func (h *Header) HeaderHash() []byte {
	sum := sha256.Sum256(h.Marshal())
	return sum[:]
}
