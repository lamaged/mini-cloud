package vault

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// Metadata 加密元数据（文件头后 MetaLen 字节 = nonce(12) || gcm(metaJSON)）。
// AAD = SHA256(头（MetaLen/DataOffset 为 0 的占位版）)：把元数据绑定到
// magic/version/flags/chunk_size/plain_size/fileID/nonce_prefix；
// 分块 AAD 则绑定最终完整头（含 MetaLen/DataOffset），两者合起来覆盖整个头。
type Metadata struct {
	V int    `json:"v"`
	P string `json:"p"` // 原始虚拟路径（Phase 1 起用于路径绑定强校验）
	N string `json:"n"` // 原始文件名
	S int64  `json:"s"` // 明文大小
	M int64  `json:"m"` // 原始修改时间（unix 秒）
	T string `json:"t"` // MIME
	H string `json:"h"` // 明文 SHA-256（hex）——已废弃（I1）：单遍流式下元数据先于数据落盘，
	// 无法自包含哈希；权威哈希只存 manifest.Entry.SHA256。字段保留仅为格式兼容，读取方不得依赖。
}

// metaAADHeader 元数据 AAD 用的头：MetaLen/DataOffset 置零的占位版
func metaAADHeader(h *Header) []byte {
	clone := *h
	clone.MetaLen = 0
	clone.DataOffset = 0
	return clone.Marshal()
}

// SealMeta 加密元数据：nonce(12) || ct
func SealMeta(metaKey []byte, h *Header, m *Metadata) ([]byte, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("vault: 元数据序列化失败: %w", err)
	}
	nonce, err := randomBytes(12)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(metaAADHeader(h))
	ct, err := seal(metaKey, nonce, raw, sum[:])
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, 12+len(ct))
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, nil
}

// OpenMeta 解密元数据
func OpenMeta(metaKey []byte, h *Header, data []byte) (*Metadata, error) {
	if len(data) < 12+TagSize {
		return nil, fmt.Errorf("vault: 元数据过短: %d", len(data))
	}
	sum := sha256.Sum256(metaAADHeader(h))
	plain, err := open(metaKey, data[:12], data[12:], sum[:])
	if err != nil {
		return nil, fmt.Errorf("vault: 元数据解密失败: %w", err)
	}
	var m Metadata
	if err := json.Unmarshal(plain, &m); err != nil {
		return nil, fmt.Errorf("vault: 元数据解析失败: %w", err)
	}
	return &m, nil
}

// ValidateMetadata 校验元数据与头的一致性（防字段矛盾）
func ValidateMetadata(h *Header, m *Metadata) error {
	if m.S != h.PlainSize {
		return fmt.Errorf("vault: 元数据大小 %d 与头 plain_size %d 不一致", m.S, h.PlainSize)
	}
	return nil
}

// headerBytesOf 便捷：比较用
func headerBytesOf(a, b []byte) bool { return bytes.Equal(a, b) }
