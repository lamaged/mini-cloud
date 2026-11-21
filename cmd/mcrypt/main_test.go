package main

import (
	"os/exec"
	"runtime"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mini-cloud/internal/vault"
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

// buildVaultDir 经 cmdKeygen+cmdEncrypt 构造一个加密目录（含 Movies/movie.mkv）
func buildVaultDir(t *testing.T) (vaultDir, keyPath string) {
	t.Helper()
	dir := t.TempDir()
	keyPath = filepath.Join(dir, "vault.key")
	if err := cmdKeygen([]string{"-key", keyPath}); err != nil {
		t.Fatal(err)
	}
	plainDir := filepath.Join(dir, "plain")
	if err := os.MkdirAll(filepath.Join(plainDir, "Movies"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plainDir, "Movies", "movie.mkv"), []byte("movie-content-0123456789"), 0644); err != nil {
		t.Fatal(err)
	}
	vaultDir = filepath.Join(dir, "vault")
	if err := cmdEncrypt([]string{"-key", keyPath, "-in", plainDir, "-out", vaultDir}); err != nil {
		t.Fatal(err)
	}
	return vaultDir, keyPath
}

// TestDecryptSymlinkEscape P2-1：输出目录预置符号链接目录 → 解密必须拒绝，
// 链接目标（外部目录）分毫不动
func TestDecryptSymlinkEscape(t *testing.T) {
	vaultDir, keyPath := buildVaultDir(t)
	dir := t.TempDir()
	outDir := filepath.Join(dir, "restore")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(dir, "victim")
	if err := os.MkdirAll(external, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(outDir, "Movies")); err != nil {
		t.Skipf("无法创建符号链接（Windows 需管理员权限等）：%v", err)
	}
	err := cmdDecrypt([]string{"-key", keyPath, "-in", vaultDir, "-out", outDir})
	if err == nil || !strings.Contains(err.Error(), "符号链接") {
		t.Fatalf("符号链接目录应被拒绝，got %v", err)
	}
	entries, _ := os.ReadDir(external)
	if len(entries) != 0 {
		t.Errorf("外部目录不得被写入，实际 %d 项", len(entries))
	}
}

// TestDecryptDirsInvalidPath P2-1：manifest.Dirs 非法路径（此前完全未校验）→ 拒绝
func TestDecryptDirsInvalidPath(t *testing.T) {
	dir := t.TempDir()
	master := bytes.Repeat([]byte{0x42}, 32)
	keyPath := filepath.Join(dir, "vault.key")
	if err := os.WriteFile(keyPath, master, 0600); err != nil {
		t.Fatal(err)
	}
	inDir := filepath.Join(dir, "vault")
	if err := os.MkdirAll(filepath.Join(inDir, "_mc"), 0755); err != nil {
		t.Fatal(err)
	}
	// 清单：无文件，仅一个非法目录（../ 逃逸）
	m := &vault.Manifest{V: 1, Dirs: []string{"/../evil"}}
	var mf bytes.Buffer
	if err := vault.SaveManifest(&mf, vault.ManifestKey(master), m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inDir, "_mc", "manifest.mcf"), mf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "restore")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := cmdDecrypt([]string{"-key", keyPath, "-in", inDir, "-out", outDir}); err == nil {
		t.Fatal("非法 Dirs 应被拒绝")
	}
	if _, err := os.Stat(filepath.Join(dir, "evil")); !os.IsNotExist(err) {
		t.Error("../evil 不得被创建")
	}
}

// TestCmdKeygenWriteFailureOldIntact P2-2：真实写失败（目录只读）→ 旧钥不变。
// root 用户绕过目录权限，自动跳过。
func TestCmdKeygenWriteFailureOldIntact(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 用户不受目录只读权限约束，跳过")
	}
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "vault.key")
	if err := cmdKeygen([]string{"-key", keyPath}); err != nil {
		t.Fatal(err)
	}
	old, _ := os.ReadFile(keyPath)
	// 目录只读：temp 创建即失败，--force 原子替换全程失败，旧钥不得受影响
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0755)
	if err := cmdKeygen([]string{"-key", keyPath, "--force"}); err == nil {
		t.Fatal("只读目录下 --force 应失败")
	}
	after, _ := os.ReadFile(keyPath)
	if !bytes.Equal(old, after) {
		t.Error("写失败时旧密钥不得改变")
	}
}

// TestDecryptWindowsJunction 外部审核 2026-11-20 第四轮：Windows 目录交接点（junction）
// 逃逸拒绝。仅在 Windows 且 mklink 可用时执行；其他平台/不可用时**明确跳过**
//（不把普通符号链接测试通过等同于 junction 安全）。
func TestDecryptWindowsJunction(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows 专属测试（junction 无法在其他平台创建）")
	}
	vaultDir, keyPath := buildVaultDir(t)
	dir := t.TempDir()
	outDir := filepath.Join(dir, "restore")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(dir, "victim")
	if err := os.MkdirAll(external, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(outDir, "Movies")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, external).CombinedOutput(); err != nil {
		t.Skipf("无法创建 junction：%v（%s）", err, out)
	}
	err := cmdDecrypt([]string{"-key", keyPath, "-in", vaultDir, "-out", outDir})
	if err == nil || !strings.Contains(err.Error(), "符号链接") {
		t.Fatalf("junction 目录应被拒绝，got %v", err)
	}
	entries, _ := os.ReadDir(external)
	if len(entries) != 0 {
		t.Errorf("外部目录不得被写入，实际 %d 项", len(entries))
	}
}
