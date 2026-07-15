package conf

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Server.Listen != ":8080" {
		t.Errorf("default listen: got %q, want %q", cfg.Server.Listen, ":8080")
	}
	if cfg.Server.Username != "admin" {
		t.Errorf("default username: got %q", cfg.Server.Username)
	}
	if cfg.Server.Password != "admin" {
		t.Errorf("default password: got %q", cfg.Server.Password)
	}
	if cfg.Cloud.Type != "local" {
		t.Errorf("default cloud type: got %q, want %q", cfg.Cloud.Type, "local")
	}
}

func TestLoadNonExistentFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent", "config.json")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load of nonexistent file should create it: %v", err)
	}
	if cfg.Server.Listen != ":8080" {
		t.Errorf("loaded listen: got %q", cfg.Server.Listen)
	}

	// 验证文件已创建
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("Load should create the config file for missing paths")
	}
}

func TestLoadExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	original := `{
    "server": {
        "listen": ":9090",
        "username": "user",
        "password": "pass"
    },
    "cloud": {
        "type": "tianyi",
        "refresh_token": "test-token-123"
    }
}`
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Listen != ":9090" {
		t.Errorf("listen: got %q, want %q", cfg.Server.Listen, ":9090")
	}
	if cfg.Server.Username != "user" {
		t.Errorf("username: got %q", cfg.Server.Username)
	}
	// normalize() 将 cloud 转为 clouds[0]
	if len(cfg.Clouds) != 1 || cfg.Clouds[0].Type != "tianyi" {
		t.Errorf("cloud type: got %q", cfg.Clouds[0].Type)
	}
	if len(cfg.Clouds) != 1 || cfg.Clouds[0].RefreshToken != "test-token-123" {
		t.Errorf("refresh_token: got %q", cfg.Clouds[0].RefreshToken)
	}
}

func TestLoadPartialConfig(t *testing.T) {
	// 部分配置：只指定了 listen，其余应使用默认值
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	partial := `{"server": {"listen": ":7777"}}`
	if err := os.WriteFile(path, []byte(partial), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Listen != ":7777" {
		t.Errorf("listen: got %q", cfg.Server.Listen)
	}
	// 未指定的字段应该保持默认值
	if cfg.Server.Username != "admin" {
		t.Errorf("username should default to 'admin', got %q", cfg.Server.Username)
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if err := os.WriteFile(path, []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil {
		t.Error("Load of invalid JSON should return error")
	}
}

func TestSaveAndReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg := DefaultConfig()
	cfg.Server.Listen = ":1234"
	cfg.Cloud.Type = "mobile"

	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 验证文件可读
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var parsed Config
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("saved file should be valid JSON: %v", err)
	}
	if parsed.Server.Listen != ":1234" {
		t.Errorf("saved listen: got %q", parsed.Server.Listen)
	}
	if parsed.Cloud.Type != "mobile" {
		t.Errorf("saved cloud type: got %q", parsed.Cloud.Type)
	}

	// 重新加载
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Server.Listen != ":1234" {
		t.Errorf("reloaded listen: got %q", loaded.Server.Listen)
	}
}

func TestSaveCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "deep", "config.json")

	cfg := DefaultConfig()
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save to deep path: %v", err)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("Save should create parent directories")
	}
}
