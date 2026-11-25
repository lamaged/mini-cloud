package conf

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// writeCloudsConfig 写入一个含 server + 两个 clouds 的配置文件
func writeCloudsConfig(t *testing.T, path string) {
	t.Helper()
	content := `{
    "server": {"listen": ":8088", "username": "admin", "password": "pw"},
    "clouds": [
        {"name": "移动云盘", "type": "mobile", "authorization": "old-auth"},
        {"name": "天翼个人", "type": "tianyi", "username": "u", "password": "p"}
    ]
}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestWriteCredentialNormal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeCloudsConfig(t, path)

	cfg, err := WriteCredential(path, "移动云盘", map[string]string{"authorization": "new-auth"})
	if err != nil {
		t.Fatalf("WriteCredential: %v", err)
	}
	// 其它 clouds 条目必须保留
	if len(cfg.Clouds) != 2 || cfg.Clouds[1].Name != "天翼个人" || cfg.Clouds[1].Username != "u" {
		t.Errorf("other cloud entries changed: %+v", cfg.Clouds)
	}
	// 磁盘内容一致 + 权限 0600 + 备份存在
	reloaded, err := LoadStrict(path)
	if err != nil {
		t.Fatalf("LoadStrict: %v", err)
	}
	if reloaded.Clouds[0].Authorization != "new-auth" {
		t.Errorf("disk auth = %q, want new-auth", reloaded.Clouds[0].Authorization)
	}
	checkPerm(t, path, 0600)
	checkPerm(t, path+".bak", 0600)
	// 无残留 temp
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error(".tmp should not remain")
	}
}

func TestWriteCredentialMountNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeCloudsConfig(t, path)
	before, _ := os.ReadFile(path)

	_, err := WriteCredential(path, "不存在的挂载", map[string]string{"authorization": "x"})
	if err == nil || !strings.Contains(err.Error(), "未找到挂载") {
		t.Fatalf("expected mount-not-found error, got %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("file must not be modified when mount not found")
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Error(".bak should not be created on failure")
	}
}

func TestWriteCredentialMigratesLegacyCloud(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	legacy := `{
    "server": {"listen": ":8088", "username": "admin", "password": "pw"},
    "cloud": {"name": "移动云盘", "type": "mobile", "authorization": "legacy-auth"}
}`
	if err := os.WriteFile(path, []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := WriteCredential(path, "移动云盘", map[string]string{"authorization": "new-auth"})
	if err != nil {
		t.Fatalf("WriteCredential on legacy config: %v", err)
	}
	if len(cfg.Clouds) != 1 || cfg.Clouds[0].Authorization != "new-auth" {
		t.Errorf("legacy cloud not migrated correctly: %+v", cfg.Clouds)
	}
	reloaded, err := LoadStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Clouds) != 1 || reloaded.Clouds[0].Name != "移动云盘" || reloaded.Clouds[0].Type != "mobile" {
		t.Errorf("legacy migration lost fields: %+v", reloaded.Clouds)
	}
	if reloaded.Clouds[0].Authorization != "new-auth" {
		t.Errorf("legacy migration lost new auth: %+v", reloaded.Clouds[0])
	}
}

func TestWriteCredentialMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	_, err := WriteCredential(path, "移动云盘", map[string]string{"authorization": "x"})
	if err == nil {
		t.Fatal("WriteCredential on missing file must fail")
	}
	// 不得重建默认配置（I2：防止认证降级为 admin/admin）
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("missing file must not be recreated by write path")
	}
}

func TestWriteCredentialInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteCredential(path, "移动云盘", map[string]string{"authorization": "x"}); err == nil {
		t.Fatal("WriteCredential on invalid JSON must fail")
	}
}

func TestRestoreBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeCloudsConfig(t, path)

	// 模拟：备份 → 写入新值 → 还原
	if err := Backup(path); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteCredential(path, "移动云盘", map[string]string{"authorization": "new-auth"}); err != nil {
		t.Fatal(err)
	}
	if err := RestoreBackup(path); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	reloaded, err := LoadStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Clouds[0].Authorization != "old-auth" {
		t.Errorf("after restore auth = %q, want old-auth", reloaded.Clouds[0].Authorization)
	}
	checkPerm(t, path, 0600)
}

// TestLoadSelfHealFromBackup 手工编辑写坏 config.json 时自动用 .bak 恢复（真机经验）
func TestLoadSelfHealFromBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeCloudsConfig(t, path)

	// 备份「好」配置
	if err := Backup(path); err != nil {
		t.Fatal(err)
	}
	// 写坏 config.json（模拟手工编辑漏逗号）
	brokenContent := `{"server": {"listen": ":8088",} "clouds": []}`
	if err := os.WriteFile(path, []byte(brokenContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("损坏配置应自愈加载: %v", err)
	}
	if len(cfg.Clouds) != 2 || cfg.Clouds[0].Name != "移动云盘" {
		t.Errorf("自愈后配置不符: %+v", cfg.Clouds)
	}
	// I4：损坏原文件必须保留副本（不静默丢失用户编辑内容）
	brokenFiles, _ := filepath.Glob(path + ".broken-*")
	if len(brokenFiles) != 1 {
		t.Errorf("应保留 1 个损坏副本，实际 %v", brokenFiles)
	}
	data, _ := os.ReadFile(brokenFiles[0])
	if string(data) != brokenContent {
		t.Error("损坏副本内容与原损坏文件不一致")
	}
	checkPerm(t, brokenFiles[0], 0600)
}

// TestLoadSelfHealBrokenBackup I4：备份也损坏时不覆盖原文件、如实报错
func TestLoadSelfHealBrokenBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeCloudsConfig(t, path)

	// 备份是坏的
	if err := os.WriteFile(path+".bak", []byte(`{bad bak`), 0600); err != nil {
		t.Fatal(err)
	}
	brokenContent := `{"server": {"listen": ":8088",} "clouds": []}`
	if err := os.WriteFile(path, []byte(brokenContent), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("备份也坏时应报错")
	}
	// 原文件不得被覆盖（I4：先验证备份再替换）
	data, _ := os.ReadFile(path)
	if string(data) != brokenContent {
		t.Errorf("备份坏时原文件不得被覆盖: %s", data)
	}
	// 不得留下 broken 副本误导排查？——本实现此时未进入恢复流程，不应产生副本
	if matches, _ := filepath.Glob(path + ".broken-*"); len(matches) != 0 {
		t.Errorf("备份坏时不应产生损坏副本: %v", matches)
	}
}

// TestLoadBrokenJSONNoBackup 无备份时仍如实报错
func TestLoadBrokenJSONNoBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{broken`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("无备份的损坏配置应报错")
	}
}

// TestLoadCreatesBackupIfMissing I6：从未用过 /admin 的机器加载成功后自动补 .bak
func TestLoadCreatesBackupIfMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeCloudsConfig(t, path)

	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatal("前置：.bak 不应存在")
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("加载成功后应生成 .bak: %v", err)
	}
	var bakCfg Config
	if err := json.Unmarshal(bak, &bakCfg); err != nil || len(bakCfg.Clouds) != 2 {
		t.Errorf(".bak 内容不符: %v", err)
	}
	checkPerm(t, path+".bak", 0600)
}

// TestLoadSelfHealEmptyFile I7：0 字节 config.json 同样自愈（编辑器截断/断电形态）
func TestLoadSelfHealEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeCloudsConfig(t, path)
	if err := Backup(path); err != nil {
		t.Fatal(err)
	}
	// 清成 0 字节
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("空文件应自愈: %v", err)
	}
	if len(cfg.Clouds) != 2 {
		t.Errorf("自愈后配置不符: %+v", cfg.Clouds)
	}
	// 空文件无需损坏副本
	if matches, _ := filepath.Glob(path + ".broken-*"); len(matches) != 0 {
		t.Errorf("空文件不应产生损坏副本: %v", matches)
	}
}

// TestLoadSelfHealPrunesBroken P10：损坏副本保留最近 3 份
func TestLoadSelfHealPrunesBroken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeCloudsConfig(t, path)
	if err := Backup(path); err != nil {
		t.Fatal(err)
	}
	// 预置 5 份历史损坏副本（名字带时间戳、各不相同；同秒冲突用递增序号规避）
	for i := 0; i < 5; i++ {
		old := fmt.Sprintf("%s.broken-1000000%d", path, i)
		if err := os.WriteFile(old, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// 再触发一次自愈（产生新副本 → 总数 6 → 剪到 3）
	if err := os.WriteFile(path, []byte(`{broken-now}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("自愈: %v", err)
	}
	matches, _ := filepath.Glob(path + ".broken-*")
	if len(matches) != 3 {
		t.Errorf("应保留最近 3 份损坏副本，实际 %d 份 %v", len(matches), matches)
	}
	// 最旧的两份（…000、…001）应被删除
	for _, old := range []string{path + ".broken-10000000", path + ".broken-10000001"} {
		if _, err := os.Stat(old); !os.IsNotExist(err) {
			t.Errorf("最旧副本应被清理: %s", old)
		}
	}
}

// TestLoadDefaultConfigNoBackup I8：全新安装生成默认配置时不写 .bak（不得成为自愈回退目标）
func TestLoadDefaultConfigNoBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "config.json")
	if _, err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Error("默认配置创建时不应生成 .bak")
	}
}

// TestLoadSelfHealRefusesDefaultBackup I8：备份为默认配置时拒绝自愈（防 admin/admin 回退）
func TestLoadSelfHealRefusesDefaultBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	// .bak 是默认配置
	if err := DefaultConfig().Save(path + ".bak"); err != nil {
		t.Fatal(err)
	}
	broken := `{"server": {"listen": ":8088",} "clouds": []}`
	if err := os.WriteFile(path, []byte(broken), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "拒绝自愈") {
		t.Fatalf("默认备份应拒绝自愈且错误带原因，got %v", err)
	}
	// 原文件不得被覆盖
	data, _ := os.ReadFile(path)
	if string(data) != broken {
		t.Error("拒绝自愈时原文件不得被覆盖")
	}
}

// TestEnsureBackupRefreshesCorrupt P15：.bak 损坏时用当前好配置刷新
func TestEnsureBackupRefreshesCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeCloudsConfig(t, path)
	if err := os.WriteFile(path+".bak", []byte(`{corrupt`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStrict(path + ".bak"); err != nil {
		t.Errorf(".bak 损坏后应被刷新为好配置: %v", err)
	}
}

// TestEnsureBackupRefreshesLegacyDefault P19：升级机器遗留的默认 .bak 被当前好配置刷新
func TestEnsureBackupRefreshesLegacyDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeCloudsConfig(t, path)
	// 遗留的默认 .bak
	if err := DefaultConfig().Save(path + ".bak"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	bak, err := LoadStrict(path + ".bak")
	if err != nil {
		t.Fatalf(".bak 应被刷新: %v", err)
	}
	if len(bak.Clouds) != 2 {
		t.Errorf("刷新后 .bak 应为当前配置（2 个云盘），实际 %d", len(bak.Clouds))
	}
}

// TestSelfHealLocalOnlyCustom P20：local-only 自定义配置（非默认）不受默认判定影响，可自愈
func TestSelfHealLocalOnlyCustom(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	content := `{
    "server": {"listen": ":8088", "username": "myuser", "password": "mypass"},
    "clouds": [{"name": "本地", "type": "local"}]
}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Backup(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{broken`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("local-only 自定义配置应可自愈: %v", err)
	}
	if cfg.Server.Username != "myuser" {
		t.Errorf("自愈后配置不符: %+v", cfg.Server)
	}
}

// TestLoadSemanticErrorNoSelfHeal R2-3：JSON 合法但 vault 数值非法 → 直接报错，
// 不触发 .bak 自愈（原文件不动、.bak 不动）
func TestLoadSemanticErrorNoSelfHeal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	content := `{
    "server": {"listen": ":8088", "username": "myuser", "password": "mypass"},
    "clouds": [{"name": "本地", "type": "local"}],
    "vault": {"enabled": true, "key_file": "vault.key", "paths": ["backup/videos"]}
}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	// 有效 .bak（另一份好配置）
	if err := os.WriteFile(path+".bak", []byte(strings.Replace(content, "backup/videos", "backup", 1)), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "vault.paths") {
		t.Fatalf("语义错误应直接报错，got %v", err)
	}
	// 原文件未被回退覆盖
	got, _ := os.ReadFile(path)
	if string(got) != content {
		t.Error("语义错误时原 config.json 不得被 .bak 回退覆盖")
	}
}

// TestValidateVaultRules validateVault 边界
func TestValidateVaultRules(t *testing.T) {
	cases := []struct {
		name    string
		vault   *VaultConfig
		wantErr string
	}{
		{"正常", &VaultConfig{Enabled: true, KeyFile: "k", Paths: []string{"backup", "Archive"}, MaxStreams: 4}, ""},
		{"空路径", &VaultConfig{Paths: []string{"", "x"}}, "空字符串"},
		{"多段", &VaultConfig{Paths: []string{"backup/videos"}}, "单段"},
		{"点", &VaultConfig{Paths: []string{".."}}, "不允许"},
		{"保留名", &VaultConfig{Paths: []string{"_mc"}}, "保留名"},
		{"重复", &VaultConfig{Paths: []string{"backup", "backup"}}, "重复"},
		{"上限", &VaultConfig{Paths: []string{"backup"}, MaxStreams: 5}, "超上限"},
		{"负数", &VaultConfig{Paths: []string{"backup"}, MaxStreams: -1}, "负数"},
	}
	for _, tc := range cases {
		err := validateVault(tc.vault)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%s 应通过，got %v", tc.name, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s 应含 %q，got %v", tc.name, tc.wantErr, err)
		}
	}
}

// TestValidateMounts A13（DESIGN-2026-11-24-01 §4.3）：挂载名结构校验全局 fail-fast
func TestValidateMounts(t *testing.T) {
	base := func(name string) *Config {
		return &Config{Clouds: []CloudConfig{{Name: name, Type: "mobile"}}}
	}
	bad := []struct {
		label string
		cfg   *Config
	}{
		{"空名称", &Config{Clouds: []CloudConfig{{}}}},
		{"路径分隔符", base("a/b")},
		{"反斜杠", base(`a\b`)},
		{"空格", base("my cloud")},
		{"花括号", base("{x}")},
		{"百分号", base("%61")},
		{"点", base(".")},
		{"点点", base("..")},
		{"超长", base(strings.Repeat("长", 65))},
		{"重复", &Config{Clouds: []CloudConfig{{Name: "x", Type: "mobile"}, {Name: "x", Type: "tianyi"}}}},
	}
	for _, b := range bad {
		if err := validateMounts(b.cfg); err == nil {
			t.Errorf("%s：应校验失败", b.label)
		}
	}

	// 中文合法（现有挂载名均为中文）
	ok := &Config{Clouds: []CloudConfig{
		{Name: "天翼个人", Type: "tianyi"},
		{Name: "移动云盘", Type: "mobile"},
		{Name: "百度网盘", Type: "baidu"},
	}}
	if err := validateMounts(ok); err != nil {
		t.Errorf("中文挂载名应合法: %v", err)
	}
	// 控制字符拒绝
	if err := validateMounts(base("a\x00b")); err == nil {
		t.Error("控制字符应校验失败")
	}
}

// TestWriteCredentialBaidu 百度写回（§6.2/C02）：
// 写「验证后的最终 refresh_token」（__refresh_token 优先）+ 授权代次 +1、修订号置 1。
func TestWriteCredentialBaidu(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.json"
	cfg := &Config{Clouds: []CloudConfig{
		{Name: "百度", Type: "baidu", RefreshToken: "rt-old", ClientID: "cid", ClientSecret: "cs", CredentialGeneration: 3, TokenRevision: 7},
		{Name: "移动", Type: "mobile", Authorization: "auth-old"},
	}}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}

	fresh, err := WriteCredential(path, "百度", map[string]string{
		"__refresh_token": "rt-final", // 验证后的最终值（上游轮换过）
		"refresh_token":   "rt-input", // 表单原始值（不得使用）
	})
	if err != nil {
		t.Fatal(err)
	}
	got := fresh.Clouds[0]
	if got.RefreshToken != "rt-final" {
		t.Errorf("RefreshToken = %q, want rt-final（最终值优先于表单原始值）", got.RefreshToken)
	}
	if got.CredentialGeneration != 4 || got.TokenRevision != 1 {
		t.Errorf("代次 = (%d,%d), want (4,1)", got.CredentialGeneration, got.TokenRevision)
	}
	// 移动挂载不受影响
	if fresh.Clouds[1].Authorization != "auth-old" {
		t.Errorf("移动挂载不应被改动: %+v", fresh.Clouds[1])
	}
	// 落盘验证
	reloaded, err := LoadStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Clouds[0].RefreshToken != "rt-final" || reloaded.Clouds[0].CredentialGeneration != 4 {
		t.Errorf("落盘内容不符: %+v", reloaded.Clouds[0])
	}
}

// TestWriteCredentialMobileUnchanged 移动写回保持原行为（authorization）
func TestWriteCredentialMobileUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.json"
	cfg := &Config{Clouds: []CloudConfig{{Name: "移动", Type: "mobile", Authorization: "auth-old"}}}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	fresh, err := WriteCredential(path, "移动", map[string]string{"authorization": "auth-new"})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Clouds[0].Authorization != "auth-new" {
		t.Errorf("Authorization = %q, want auth-new", fresh.Clouds[0].Authorization)
	}
	if fresh.Clouds[0].CredentialGeneration != 0 {
		t.Errorf("移动挂载不应动代次字段: %+v", fresh.Clouds[0])
	}
}

// TestWriteCredentialBaiduMissingReservedKey P2（代码审核）：
// 缺 __refresh_token 必须 fail-fast——不备份、不写盘，原配置完全不变。
func TestWriteCredentialBaiduMissingReservedKey(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.json"
	cfg := &Config{Clouds: []CloudConfig{
		{Name: "百度", Type: "baidu", RefreshToken: "rt-old", ClientID: "cid", ClientSecret: "cs", CredentialGeneration: 3},
	}}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	_, err = WriteCredential(path, "百度", map[string]string{"refresh_token": "rt-input"})
	if err == nil || !strings.Contains(err.Error(), "__refresh_token") {
		t.Fatalf("缺失保留键应报错，got %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Error("写回失败时配置文件必须完全不变")
	}
	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Error("写回失败不得产生备份")
	}
}

// TestRotateBaiduToken 批 2（§6.5）：轮换写回——代次相符写新票 + 修订号 +1 + 备份；
// 代次不符跳过（返回 nil 配置、原配置不变）；未找到挂载报错。
func TestRotateBaiduToken(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.json"
	cfg := &Config{Clouds: []CloudConfig{
		{Name: "百度", Type: "baidu", RefreshToken: "rt-old", ClientID: "cid", ClientSecret: "cs", CredentialGeneration: 5, TokenRevision: 2},
		{Name: "移动", Type: "mobile", Authorization: "auth"},
	}}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}

	// 代次相符：写回 + 修订号 +1
	fresh, err := RotateBaiduToken(path, "百度", 5, "rt-rotated")
	if err != nil {
		t.Fatal(err)
	}
	if fresh == nil {
		t.Fatal("代次相符应返回新配置")
	}
	if fresh.Clouds[0].RefreshToken != "rt-rotated" || fresh.Clouds[0].TokenRevision != 3 {
		t.Errorf("轮换写回不符: %+v", fresh.Clouds[0])
	}
	if fresh.Clouds[0].CredentialGeneration != 5 {
		t.Errorf("轮换不得改授权代次: %+v", fresh.Clouds[0])
	}
	if fresh.Clouds[1].Authorization != "auth" {
		t.Errorf("移动挂载不应被改动: %+v", fresh.Clouds[1])
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Error("写回前应备份 .bak")
	}

	// 代次不符（用户已提交新授权）：跳过写回、原配置不变
	before, _ := os.ReadFile(path)
	skipped, err := RotateBaiduToken(path, "百度", 4, "rt-late")
	if err != nil {
		t.Fatal(err)
	}
	if skipped != nil {
		t.Error("代次不符应返回 nil 配置（跳过）")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Error("代次不符时配置文件不得变化")
	}

	// 未找到挂载
	if _, err := RotateBaiduToken(path, "不存在", 5, "x"); err == nil {
		t.Error("未找到挂载应报错")
	}
}
