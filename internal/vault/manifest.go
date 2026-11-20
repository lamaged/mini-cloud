package vault

import (
	"path"
	"strings"
	"crypto/sha256"
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
	// #6（外部审核 2026-11-20）：三层尺寸与读取端一致——
	// 工具不得生成服务端拒绝读取的清单
	raw, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("vault: manifest 序列化失败: %w", err)
	}
	if len(raw) > MaxManifestSize {
		return fmt.Errorf("vault: manifest JSON 超上限 %d MiB（%d B）", MaxManifestSize>>20, len(raw))
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(raw); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if gz.Len() > MaxManifestSize {
		return fmt.Errorf("vault: manifest 压缩后超上限 %d MiB（%d B）", MaxManifestSize>>20, gz.Len())
	}
	nonce, err := randomBytes(12)
	if err != nil {
		return err
	}
	ct, err := seal(key, nonce, gz.Bytes(), nil)
	if err != nil {
		return err
	}
	if len(ct)+len(nonce) > MaxManifestSize {
		return fmt.Errorf("vault: manifest 密文超上限 %d MiB（%d B）", MaxManifestSize>>20, len(ct)+len(nonce))
	}
	if _, err := dst.Write(nonce); err != nil {
		return err
	}
	if _, err := dst.Write(ct); err != nil {
		return err
	}
	return nil
}

// ValidateVirtualPath 校验 manifest 虚拟路径规范（#3，外部审核 2026-11-20）：
// 必须以 / 开头、path.Clean 不变、无 . / .. 段、非根、长度与深度有上限。
// 不规范的路径（如相对路径）会使 buildRoot 的 path.Dir 循环永不收敛（死循环）。
const (
	maxVirtualPathLen   = 1024 // 单条虚拟路径长度上限
	maxVirtualPathDepth = 64   // 虚拟路径深度上限
)

// ValidateVirtualPath 校验单条虚拟路径（Entry.Path 或 Dirs 元素）
func ValidateVirtualPath(p string) error {
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("必须以 / 开头：%q", p)
	}
	if path.Clean(p) != p {
		return fmt.Errorf("路径不规范（含冗余 ./ ../ 或尾斜杠）：%q", p)
	}
	if p == "/" {
		return fmt.Errorf("不允许根路径 / 作为条目路径")
	}
	if len(p) > maxVirtualPathLen {
		return fmt.Errorf("路径超长（%d > %d）：%q", len(p), maxVirtualPathLen, p)
	}
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if len(segs) > maxVirtualPathDepth {
		return fmt.Errorf("路径过深（%d > %d）：%q", len(segs), maxVirtualPathDepth, p)
	}
	for _, seg := range segs {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("路径含非法段：%q", p)
		}
	}
	return nil
}

// MaxManifestSize manifest 密文大小上限（资源约束：伪造超大清单不得撑爆路由器内存）
const MaxManifestSize = 16 << 20 // 16 MiB（PM 拍板 2026-11-20：实测 KB 级清单，16 MiB 足够；原 32 MiB 下调）

// LoadManifest 解密并解析 manifest（认证失败返回 ErrAuth 包装错误）。
// 读取总量受 MaxManifestSize 限制，解压后 JSON 再受同样限制（防 gzip 炸弹）。
func LoadManifest(src io.Reader, key []byte) (*Manifest, error) {
	m, _, _, err := LoadManifestSum(src, key)
	return m, err
}

// LoadManifestSum 同 LoadManifest，并返回解密后清单内容的 SHA-256 摘要与解压后原始大小
// （Phase 2 I4：变更检测用——manifest 为随机 nonce 加密，密文哈希不可用；
// 摘要取解压后 JSON 内容，免疫 gzip mtime/压缩级别差异）。
func LoadManifestSum(src io.Reader, key []byte) (*Manifest, [32]byte, int, error) {
	var zero [32]byte
	data, err := io.ReadAll(io.LimitReader(src, MaxManifestSize+1))
	if err != nil {
		return nil, zero, 0, fmt.Errorf("vault: manifest 读取失败: %w", err)
	}
	if len(data) > MaxManifestSize {
		return nil, zero, 0, fmt.Errorf("vault: manifest 超过大小上限 %d MiB", MaxManifestSize>>20)
	}
	if len(data) < 12+TagSize {
		return nil, zero, 0, fmt.Errorf("vault: manifest 文件过短: %d", len(data))
	}
	plain, err := open(key, data[:12], data[12:], nil)
	if err != nil {
		return nil, zero, 0, fmt.Errorf("vault: manifest 解密失败: %w", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(plain))
	if err != nil {
		return nil, zero, 0, fmt.Errorf("vault: manifest gzip 解析失败: %w", err)
	}
	defer zr.Close()
	// 解压总量同样受限（gzip 炸弹防护）
	limited := io.LimitReader(zr, MaxManifestSize+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, zero, 0, fmt.Errorf("vault: manifest 解压失败: %w", err)
	}
	if len(raw) > MaxManifestSize {
		return nil, zero, 0, fmt.Errorf("vault: manifest 解压后超过大小上限 %d MiB", MaxManifestSize>>20)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, zero, 0, fmt.Errorf("vault: manifest JSON 解析失败: %w", err)
	}
	if m.V != 1 {
		return nil, zero, 0, fmt.Errorf("vault: 不支持的 manifest 版本 %d", m.V)
	}
	return &m, sha256.Sum256(raw), len(raw), nil
}

// randomBytes 加密安全随机字节
func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("vault: 随机数生成失败: %w", err)
	}
	return b, nil
}
