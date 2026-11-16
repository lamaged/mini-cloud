package conf

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config 应用配置
type Config struct {
	Server ServerConfig   `json:"server"`
	Cloud  *CloudConfig   `json:"cloud,omitempty"` // 单云盘（向后兼容）；指针 + omitempty：指针保证空值被省略（struct 值的 omitempty 不生效），避免 Save 输出空 "cloud" 块
	Clouds []CloudConfig  `json:"clouds"`          // 多云盘（优先）
}

// ServerConfig WebDAV 服务配置
type ServerConfig struct {
	Listen   string `json:"listen"`   // 监听地址，如 ":8080"
	Username string `json:"username"` // Basic Auth 用户名
	Password string `json:"password"` // Basic Auth 密码
}

// CloudConfig 云盘配置
type CloudConfig struct {
	Name          string `json:"name"`           // 挂载名称（URL 前缀），不填则用 type
	Type          string `json:"type"`           // 驱动类型："local" / "tianyi" / "mobile"
	Username      string `json:"username"`       // 云盘账号（tianyi/mobile 需要）
	Password      string `json:"password"`       // 云盘密码（tianyi/mobile 需要）
	RefreshToken  string `json:"refresh_token"`  // 云盘 refresh_token（可选，优先于账号密码）
	Authorization string `json:"authorization"`  // 移动云盘认证令牌（Base64）
	FamilyID      string `json:"family_id"`      // 天翼家庭云 ID（可选）
	ClientID      string `json:"client_id"`      // 百度网盘 client_id（可选，有默认值）
	ClientSecret  string `json:"client_secret"`  // 百度网盘 client_secret（可选，有默认值）
}

// MountName 返回挂载名称（优先用 name，否则用 type）
func (c *CloudConfig) MountName() string {
	if c.Name != "" {
		return c.Name
	}
	return c.Type
}

// DefaultConfig 返回默认配置
func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Listen:   ":8080",
			Username: "admin",
			Password: "admin",
		},
		Cloud: &CloudConfig{
			Type: "local",
		},
	}
}

// Load 从文件加载配置，如果文件不存在则创建默认配置
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		// 文件不存在，创建默认配置
		cfg := DefaultConfig()
		if err := cfg.Save(path); err != nil {
			return nil, err
		}
		return cfg, nil
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	cfg.normalize()
	// 存量文件收紧权限（旧版 Save 写 0644；升级后启动即改为 0600）。
	// 只读文件系统等场景下失败可忽略：下次 Save 会再次尝试。
	os.Chmod(path, 0600)
	return cfg, nil
}

// normalize 向后兼容：如果用了旧的 cloud 字段，包装为 clouds 数组；
// clouds 存在时清空 Cloud（clouds 优先，且避免默认值残留导致 Save 输出伪 cloud 块）
func (c *Config) normalize() {
	if len(c.Clouds) == 0 && c.Cloud != nil && c.Cloud.Type != "" {
		c.Clouds = []CloudConfig{*c.Cloud}
	}
	if len(c.Clouds) > 0 {
		c.Cloud = nil
	}
}

// Save 保存配置到文件（原子写：temp + fsync + rename；写后 chmod 0600，含真实凭证）
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "    ")
	if err != nil {
		return err
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	// 显式 chmod 在替换前完成：os.WriteFile 的 perm 只对新建文件生效，
	// 且 rename 会保留 temp 的权限，替换后正式文件即为 0600（D2）。
	// 若文件系统不支持 chmod，在此失败时尚未替换正式文件，无副作用。
	if err := os.Chmod(tmp, 0600); err != nil {
		os.Remove(tmp)
		return err
	}
	// fsync：确保内容落盘后再替换（防止断电/崩溃产生半截文件）；错误不得吞掉
	f, err := os.OpenFile(tmp, os.O_WRONLY, 0)
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Backup 备份配置到 path+".bak"（0600），供写回前留底与失败恢复
func Backup(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	bak := path + ".bak"
	if err := os.WriteFile(bak, data, 0600); err != nil {
		return err
	}
	return os.Chmod(bak, 0600)
}
