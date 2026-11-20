package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMimeForExt 内置 MIME 回退表（Windows 无系统表，mkv/ts 等常见类型必须有类型）
func TestMimeForExt(t *testing.T) {
	tests := []struct {
		ext  string
		want string
	}{
		{".mkv", "video/x-matroska"},
		{".MKV", "video/x-matroska"}, // 大小写不敏感
		{".ts", "video/mp2t"},
		{".m2ts", "video/mp2t"},
		{".srt", "application/x-subrip"},
		{".mp3", "audio/mpeg"},
		{".txt", "text/plain"},
	}
	for _, tc := range tests {
		got := mimeForExt(tc.ext)
		if got != tc.want {
			t.Errorf("mimeForExt(%q) = %q, want %q", tc.ext, got, tc.want)
		}
	}
	// 未知扩展名：返回空（I5：Vault 层已支持空 MIME）
	if got := mimeForExt(".madeup-ext"); got != "" {
		t.Errorf("未知扩展名应返回空，got %q", got)
	}
}

// TestCmdKeygen P0（外部审核 2026-11-20）：keygen 默认拒绝覆盖已有密钥
func TestCmdKeygen(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "vault.key")

	// 1. 不存在 → 成功生成
	if err := cmdKeygen([]string{"-key", keyPath}); err != nil {
		t.Fatalf("首先生成应成功: %v", err)
	}
	first, err := os.ReadFile(keyPath)
	if err != nil || len(first) != 32 {
		t.Fatalf("生成的密钥应为 32 字节: %v", err)
	}

	// 2. 已存在 → 拒绝，且原密钥内容不变
	err = cmdKeygen([]string{"-key", keyPath})
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("已存在应拒绝并提示 --force，got %v", err)
	}
	after, _ := os.ReadFile(keyPath)
	if !bytes.Equal(first, after) {
		t.Error("拒绝覆盖时原密钥内容不得改变")
	}

	// 3. --force → 覆盖成功（内容为新密钥）
	if err := cmdKeygen([]string{"-key", keyPath, "--force"}); err != nil {
		t.Fatalf("--force 覆盖应成功: %v", err)
	}
	overwritten, _ := os.ReadFile(keyPath)
	if bytes.Equal(first, overwritten) {
		t.Error("--force 后应为新密钥（内容变化）")
	}

	// 4. 失败路径（父目录不存在）→ 报错且不留半成品
	badPath := filepath.Join(dir, "no-such-dir", "x.key")
	if err := cmdKeygen([]string{"-key", badPath}); err == nil {
		t.Error("父目录不存在应报错")
	}
	if _, err := os.Stat(badPath); !os.IsNotExist(err) {
		t.Error("失败后不得残留密钥文件")
	}
}

// TestCmdKeygenForceAtomic R2-2：--force 原子替换——覆盖成功 0600；失败旧钥不动
func TestCmdKeygenForceAtomic(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "vault.key")
	if err := cmdKeygen([]string{"-key", keyPath}); err != nil {
		t.Fatal(err)
	}
	old, _ := os.ReadFile(keyPath)
	// 权限收窄为 0644（模拟存量老文件），--force 后应回到 0600
	if err := os.Chmod(keyPath, 0644); err != nil {
		t.Fatal(err)
	}
	if err := cmdKeygen([]string{"-key", keyPath, "--force"}); err != nil {
		t.Fatalf("--force 覆盖: %v", err)
	}
	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Errorf("--force 后权限应 0600，got %o", fi.Mode().Perm())
	}
	after, _ := os.ReadFile(keyPath)
	if bytes.Equal(old, after) {
		t.Error("--force 后应为新密钥")
	}

	// 失败路径：目标为目录 → rename 失败 → 目录（旧钥代理）原样不动
	dirPath := filepath.Join(dir, "kdir")
	if err := os.Mkdir(dirPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := cmdKeygen([]string{"-key", dirPath, "--force"}); err == nil {
		t.Error("目标为目录时 --force 应失败")
	}
	if fi, err := os.Stat(dirPath); err != nil || !fi.IsDir() {
		t.Error("失败后原目标必须原样保留")
	}
	// 写失败旧钥不变：父目录不存在（连 temp 都建不了）
	badPath := filepath.Join(dir, "no-such-dir", "vault.key")
	if err := cmdKeygen([]string{"-key", badPath, "--force"}); err == nil {
		t.Error("父目录不存在应失败")
	}
}
