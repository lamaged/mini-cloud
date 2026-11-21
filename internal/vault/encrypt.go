package vault

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
)

// EncryptObject 流式加密单个文件到 dst（内存占用 = 一个分块 + 元数据，支持 55GB 级大文件）。
// 返回最终文件头（供 manifest 条目记录 header_sha256）与明文 SHA-256（hex，权威哈希，
// 写入 manifest.Entry.SHA256；对象内嵌元数据的 H 字段已废弃，见 meta.go I1 说明）。
// 不变量（I1）：fileID 必须每次新生成；调用方负责随机生成并保证不复用。
func EncryptObject(dst io.Writer, master []byte, plain io.Reader, size int64, fileID [16]byte, meta *Metadata) (*Header, string, error) {
	if size < 0 {
		return nil, "", fmt.Errorf("vault: 明文大小不得为负数：%d", size)
	}
	prefix, err := randomBytes(4)
	if err != nil {
		return nil, "", err
	}
	// P10：强制元数据大小与头部一致（消除调用方传入不一致 meta.S 的隐患）
	if meta != nil {
		meta.S = size
	}
	h := &Header{
		Magic:     [4]byte{'M', 'C', 'F', '1'},
		Version:   Version1,
		Flags:     0,
		ChunkSize: DefaultChunkSize,
		PlainSize: size,
		FileID:    fileID,
	}
	copy(h.NoncePrefix[:], prefix)

	// 元数据先加密（AAD 绑定占位头，见 meta.go），再定 MetaLen/DataOffset。
	// I1 说明：单遍流式下元数据先于数据落盘，无法自包含明文哈希——meta.H 保持空，
	// 权威哈希由本函数计算后返回，调用方写入 manifest.Entry.SHA256。
	metaBlob, err := SealMeta(FileMetaKey(master, fileID), h, meta)
	if err != nil {
		return nil, "", err
	}
	if len(metaBlob) > MaxMetaLen {
		return nil, "", fmt.Errorf("vault: 元数据超上限 %d", MaxMetaLen)
	}
	h.MetaLen = uint32(len(metaBlob))
	h.DataOffset = HeaderSize + uint64(len(metaBlob))

	// 写头 + 元数据
	if _, err := dst.Write(h.Marshal()); err != nil {
		return nil, "", err
	}
	if _, err := dst.Write(metaBlob); err != nil {
		return nil, "", err
	}

	// 流式分块加密
	key := FileDataKey(master, fileID)
	hasher := sha256.New()
	buf := make([]byte, h.ChunkSize)
	var idx uint64
	var total int64 // #5（外部审核 2026-11-20）：累计实际读入字节数，防「头说 N、密文 M」坏对象
	for {
		n, rerr := io.ReadFull(plain, buf)
		if n > 0 {
			total += int64(n)
			hasher.Write(buf[:n])
			ct, err := seal(key, h.ChunkNonce(idx), buf[:n], h.ChunkAAD(idx))
			if err != nil {
				return nil, "", err
			}
			if _, err := dst.Write(ct); err != nil {
				return nil, "", err
			}
			idx++
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return nil, "", rerr
		}
	}
	// #5：块数一致不等于字节一致（如加密中文件被改动 1000→999，同为一块）——
	// 必须校验实际总字节数，否则产出头部声明与实际密文不符的坏对象
	if total != size {
		return nil, "", fmt.Errorf("vault: 明文大小不符：期望 %d 字节，实际 %d 字节（加密中文件被改动？）", size, total)
	}
	if idx != h.ChunkCount() {
		return nil, "", fmt.Errorf("vault: 明文大小不符：期望 %d 块，实际 %d 块", h.ChunkCount(), idx)
	}
	return h, hex.EncodeToString(hasher.Sum(nil)), nil
}

// NewFileID 生成全新 fileID（I1：每次加密必须新生成，禁止复用）
func NewFileID() ([16]byte, error) {
	var id [16]byte
	b, err := randomBytes(16)
	if err != nil {
		return id, err
	}
	copy(id[:], b)
	return id, nil
}

// NewMasterKey 生成 32B master key
func NewMasterKey() ([]byte, error) {
	return randomBytes(32)
}
