package conf

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config 应用配置
type Config struct {
	Server ServerConfig  `json:"server"`
	Cloud  CloudConfig   `json:"cloud"`  // 单云盘（向后兼容）
	Clouds []CloudConfig `json:"clouds"` // 多云盘（优先）
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
		Cloud: CloudConfig{
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
	return cfg, nil
}

// normalize 向后兼容：如果用了旧的 cloud 字段，包装为 clouds 数组
func (c *Config) normalize() {
	if len(c.Clouds) == 0 && c.Cloud.Type != "" {
		c.Clouds = []CloudConfig{c.Cloud}
		c.Cloud = CloudConfig{} // 清空旧字段
	}
}

// Save 保存配置到文件
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "    ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
