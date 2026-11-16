package conf

import (
	"bytes"
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

func TestSavePermissions0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg := DefaultConfig()
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	checkPerm(t, path, 0600)

	// 存量文件（如旧版 0644）再次 Save 后也必须是 0600
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save existing: %v", err)
	}
	checkPerm(t, path, 0600)

	// 无残留临时文件
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("Save should not leave .tmp behind")
	}
}

func TestSaveOmitsEmptyCloud(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg := DefaultConfig()
	cfg.Clouds = []CloudConfig{{Name: "test", Type: "local"}}
	cfg.Cloud = nil // 归一化后 Cloud 为空指针，输出中不得出现 "cloud" 键
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"cloud"`)) {
		t.Errorf("saved config should not contain empty cloud block: %s", data)
	}
}

func TestBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg := DefaultConfig()
	cfg.Server.Listen = ":1"
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	cfg.Server.Listen = ":2"
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := Backup(path); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	bakData, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("read .bak: %v", err)
	}
	var bak Config
	if err := json.Unmarshal(bakData, &bak); err != nil {
		t.Fatal(err)
	}
	if bak.Server.Listen != ":2" {
		t.Errorf("backup should contain latest config, got listen %q", bak.Server.Listen)
	}
	checkPerm(t, path+".bak", 0600)
}

func checkPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s perm = %o, want %o", path, got, want)
	}
}
