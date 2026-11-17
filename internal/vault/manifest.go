package vault

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"io"
)

// Entry manifest 文件条目：虚拟路径 → 加密对象（含一致性校验字段）
type Entry struct {
	Path         string `json:"path"`          // 虚拟路径（相对 vault 根，如 /电影/a.mkv）
	Name         string `json:"name"`          // 原始文件名
	Size         int64  `json:"size"`          // 明文大小
	MTime        int64  `json:"mtime"`         // 原始修改时间（unix 秒）
	MIME         string `json:"mime"`          // 原始 MIME
	FileID       string `json:"file_id"`       // fileID 的 Base32（26 字符，无填充）
	Obj          string `json:"obj"`           // 对象相对路径（_mc/o/xx/yy/<id>.bin）
	HeaderSHA256 string `json:"header_sha256"` // 对象 64B 文件头 SHA-256（I2：空文件/头部防篡改）
	SHA256       string `json:"sha256"`        // 明文内容 SHA-256
}

// Manifest vault 根的加密清单（虚拟树唯一来源）
type Manifest struct {
	V     int      `json:"v"`
	Files []Entry  `json:"files"`
	Dirs  []string `json:"dirs"` // 虚拟目录（相对路径，含嵌套子目录；空目录也在此记录）
}

// EncodeFileID fileID 的 Base32（RFC 4648 无填充，26 字符）
func EncodeFileID(id [16]byte) string {
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(id[:])
}

// DecodeFileID 解析 Base32 fileID（I12：manifest 与对象头的 fileID 一致性校验用）
func DecodeFileID(s string) ([16]byte, error) {
	var id [16]byte
	b, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(s)
	if err != nil {
		return id, fmt.Errorf("vault: fileID 解析失败: %w", err)
	}
	if len(b) != 16 {
		return id, fmt.Errorf("vault: fileID 长度 %d != 16", len(b))
	}
	copy(id[:], b)
	return id, nil
}

// ObjectPath 对象相对路径：_mc/o/<前2字符>/<次2字符>/<fileID>.bin
func ObjectPath(fileID string) string {
	return "_mc/o/" + fileID[:2] + "/" + fileID[2:4] + "/" + fileID + ".bin"
}

// SaveManifest gzip(JSON) 后以 manifest 密钥 GCM 加密写盘（nonce 12B 前置）
func SaveManifest(dst io.Writer, key []byte, m *Manifest) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("vault: manifest 序列化失败: %w", err)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(raw); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	nonce, err := randomBytes(12)
	if err != nil {
		return err
	}
	ct, err := seal(key, nonce, gz.Bytes(), nil)
	if err != nil {
		return err
	}
	if _, err := dst.Write(nonce); err != nil {
		return err
	}
	if _, err := dst.Write(ct); err != nil {
		return err
	}
	return nil
}

// LoadManifest 解密并解析 manifest（认证失败返回 ErrAuth 包装错误）
func LoadManifest(src io.Reader, key []byte) (*Manifest, error) {
	data, err := io.ReadAll(src)
	if err != nil {
		return nil, fmt.Errorf("vault: manifest 读取失败: %w", err)
	}
	if len(data) < 12+TagSize {
		return nil, fmt.Errorf("vault: manifest 文件过短: %d", len(data))
	}
	plain, err := open(key, data[:12], data[12:], nil)
	if err != nil {
		return nil, fmt.Errorf("vault: manifest 解密失败: %w", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(plain))
	if err != nil {
		return nil, fmt.Errorf("vault: manifest gzip 解析失败: %w", err)
	}
	defer zr.Close()
	var m Manifest
	if err := json.NewDecoder(zr).Decode(&m); err != nil {
		return nil, fmt.Errorf("vault: manifest JSON 解析失败: %w", err)
	}
	if m.V != 1 {
		return nil, fmt.Errorf("vault: 不支持的 manifest 版本 %d", m.V)
	}
	return &m, nil
}

// randomBytes 加密安全随机字节
func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("vault: 随机数生成失败: %w", err)
	}
	return b, nil
}
