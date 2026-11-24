package conf

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Config 应用配置
type Config struct {
	Server ServerConfig   `json:"server"`
	Cloud  *CloudConfig   `json:"cloud,omitempty"` // 单云盘（向后兼容）；指针 + omitempty：指针保证空值被省略（struct 值的 omitempty 不生效），避免 Save 输出空 "cloud" 块
	Clouds []CloudConfig  `json:"clouds"`          // 多云盘（优先）
	Vault  *VaultConfig   `json:"vault,omitempty"` // 加密文件播放（docs/design.md §4.3；指针 + omitempty：struct 值的 omitempty 不生效，P11）
}

// VaultConfig Vault 配置（全局一套，按挂载生效）
type VaultConfig struct {
	Enabled    bool     `json:"enabled"`     // 总开关
	KeyFile    string   `json:"key_file"`    // master key 文件路径（32 字节，永不传网盘）
	Paths      []string `json:"paths"`       // vault 根目录名列表（无前导斜杠，如 ["backup"]）
	MaxStreams int      `json:"max_streams"` // 全局并发解密流上限（0 = 默认 3；PM 拍板 2–3 路极限）
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
	Type          string `json:"type"`           // 驱动类型："local" / "tianyi" / "mobile" / "baidu"
	Username      string `json:"username"`       // 云盘账号（tianyi/mobile 需要）
	Password      string `json:"password"`       // 云盘密码（tianyi/mobile 需要）
	RefreshToken  string `json:"refresh_token"`  // 百度网盘 refresh_token（可空 = 尚未授权；管理页可更新）
	Authorization string `json:"authorization"`  // 移动云盘认证令牌（Base64）
	FamilyID      string `json:"family_id"`      // 天翼家庭云 ID（可选）
	ClientID      string `json:"client_id"`      // 百度网盘 client_id（必填，无默认值——内置公共凭据已按 PM 拍板删除）
	ClientSecret  string `json:"client_secret"`  // 百度网盘 client_secret（必填，无默认值）
	// 凭据代次元数据（DESIGN-2026-11-24-01 §6.5；配置元数据而非 token 缓存，
	// 旧配置未填写按 0 迁移，向后兼容）
	CredentialGeneration int `json:"credential_generation,omitempty"` // 授权代次：仅用户提交 +1
	TokenRevision        int `json:"token_revision,omitempty"`        // 轮换修订号：仅后台轮换 +1
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

// Load 从文件加载配置，如果文件不存在则创建默认配置（仅限启动路径使用）。
// 自愈（真机经验 2026-11-17）：手工编辑导致 JSON 语法错误时，自动用 .bak 备份恢复
// 并继续启动（日志明确警告）——服务不因配置笔误停摆。.bak 不存在则按原样报错。
func Load(path string) (*Config, error) {
	cfg, err := LoadStrict(path)
	if err == nil {
		ensureBackup(path) // I6：从未用过管理页的机器没有 .bak——加载成功后补一份
		return cfg, nil
	}
	if !os.IsNotExist(err) {
		// R2-3（外部审核 2026-11-20）：语义错误（JSON 合法但数值不合法）直接报错，
		// 不得进自愈——自愈仅针对 JSON 语法损坏，否则会静默回退用户手工编辑
		if isSemanticError(err) {
			return nil, err
		}
		// JSON 损坏：自愈（I4/I5，真机经验 2026-11-17）——
		// ① 备份先在内存中验证可解析，通过才替换（避免「覆盖后才发现备份也坏」）；
		// ② 损坏原文件另存副本 config.json.broken-<时间戳>（0600），不静默丢失；
		// ③ 恢复成功日志为 ERROR 并明示旧凭证风险。
		if bakCfg, bakErr := LoadStrict(path + ".bak"); bakErr == nil {
			// I8 防线：备份若是默认配置（admin/admin + 单个 local），拒绝恢复——
			// 自愈不得把服务"恢复"成弱口令空挂载
			if isDefaultBackup(bakCfg) {
				refuseErr := fmt.Errorf("备份为默认配置，拒绝自愈（防 admin/admin 回退），请手工修复 config.json：%w", err)
				slog.Error("config.json 语法损坏，且备份为默认配置——拒绝自愈", "orig_err", err)
				return nil, refuseErr
			}
			// ① 非空损坏文件另存副本（I7：空文件无需副本、恢复照常执行）
			broken := ""
			if data, rdErr := os.ReadFile(path); rdErr == nil && len(data) > 0 {
				broken = fmt.Sprintf("%s.broken-%d", path, time.Now().Unix())
				if wErr := os.WriteFile(broken, data, 0600); wErr != nil {
					broken = ""
				} else {
					pruneBroken(path) // P10：只保留最近 3 份损坏副本
				}
			}
			// ② 恢复
			if rbErr := RestoreBackup(path); rbErr == nil {
				if cfg2, loadErr := LoadStrict(path); loadErr == nil {
					bakInfo := ""
					if fi, statErr := os.Stat(path + ".bak"); statErr == nil {
						bakInfo = fi.ModTime().Format("2006-01-02 15:04:05")
					}
					logArgs := []interface{}{"bak_mtime", bakInfo, "bak_clouds", len(bakCfg.Clouds), "orig_err", err}
					if broken != "" {
						logArgs = append([]interface{}{"broken_saved", broken}, logArgs...)
					}
					slog.Error("config.json 语法损坏，已自动回退到备份并恢复——其中凭证可能已过期，请在 /admin 核对或更新", logArgs...)
					return cfg2, nil
				}
			}
		}
		return nil, err
	}
	// 文件不存在，创建默认配置。
	// I8：此处不写 .bak——默认配置（admin/admin）不得成为自愈回退目标（安全降级）。
	cfg = DefaultConfig()
	if err := cfg.Save(path); err != nil {
		return nil, err
	}
	return cfg, nil
}

// pruneBroken 保留最近 3 份损坏副本，删除更旧的（P10：路由器 flash 有限）。
// 排序前提（P17）：副本名时间戳为 10 位秒级 unix 时间，字典序 = 时间序（2286 年前成立）；
// 若将来改成毫秒/纳秒或非定长，需改用 mtime 排序。
func pruneBroken(path string) {
	matches, _ := filepath.Glob(path + ".broken-*")
	if len(matches) <= 3 {
		return
	}
	sort.Strings(matches)
	for _, m := range matches[:len(matches)-3] {
		os.Remove(m)
	}
}

// isDefaultBackup 是否为默认配置——与 DefaultConfig() 字段级精确比较（P20：
// 仅「admin/admin + 单个 local + 无 vault」命中；local-only 的自定义配置不受影响）
func isDefaultBackup(cfg *Config) bool {
	d := DefaultConfig()
	return cfg.Server == d.Server && len(cfg.Clouds) == 1 && cfg.Clouds[0] == *d.Cloud && cfg.Vault == nil
}

// ensureBackup 确保 .bak 存在且可解析（I6：自愈的前提；启动路径调用，写回路径不受影响）。
// 缺失或损坏时用当前成功加载的配置覆盖（P15：.bak 损坏不自愈失效）；
// 仅在缺失/损坏时刷新（P16：不每次覆盖，保护管理页写前备份的回滚语义）。
func ensureBackup(path string) {
	refresh := false
	if _, err := os.Stat(path + ".bak"); os.IsNotExist(err) {
		refresh = true
	} else if bakCfg, err := LoadStrict(path + ".bak"); err != nil {
		refresh = true
	} else if isDefaultBackup(bakCfg) {
		// P19：旧版本首启曾写入默认 .bak 的升级机器——当前配置非默认时刷新，恢复自愈能力
		if cur, curErr := LoadStrict(path); curErr == nil && !isDefaultBackup(cur) {
			refresh = true
		}
	}
	if refresh {
		if err := Backup(path); err != nil {
			slog.Warn("生成配置备份失败", "err", err)
		}
	}
}

// LoadStrict 严格读取配置：文件缺失或解析失败直接报错，绝不自动创建默认配置。
// 供管理页写回路径使用（避免 config.json 被误删后重建出 admin/admin 默认认证）。
func LoadStrict(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	cfg.normalize()
	if err := validateVault(cfg.Vault); err != nil {
		return nil, err
	}
	if err := validateMounts(cfg); err != nil {
		return nil, err
	}
	// 存量文件收紧权限（旧版 Save 写 0644；升级后启动即改为 0600）。
	// 只读文件系统等场景下失败可忽略：下次 Save 会再次尝试。
	os.Chmod(path, 0600)
	return cfg, nil
}

// semanticError 配置语义错误（JSON 合法但数值不合法）。
// R2-3（外部审核 2026-11-20）：语义错误必须直接报错退出，不得触发 .bak 自愈
//（自愈仅针对 JSON 语法损坏；否则用户的手工编辑会被静默回退、可能复活旧凭证）
type semanticError struct{ msg string }

func (e *semanticError) Error() string { return e.msg }

func isSemanticError(err error) bool {
	var se *semanticError
	return errors.As(err, &se)
}

// validateVault vault 段语义校验（外部审核 #2，2026-11-20）：
// 不合法配置必须启动失败，不得静默忽略——否则「backup/videos」类多段路径
// 会让容器识别失效、密文回落物理视图泄露。
func validateVault(v *VaultConfig) error {
	if v == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, p := range v.Paths {
		switch {
		case p == "":
			return &semanticError{msg: "vault.paths 含空字符串"}
		case strings.ContainsAny(p, "/\\"):
			return &semanticError{msg: fmt.Sprintf("vault.paths 必须为单段目录名，不允许路径分隔符：%q", p)}
		case p == "." || p == "..":
			return &semanticError{msg: fmt.Sprintf("vault.paths 不允许 . 或 ..：%q", p)}
		case strings.HasPrefix(p, "_"):
			return &semanticError{msg: fmt.Sprintf("vault.paths 不允许下划线开头的保留名：%q", p)}
		case seen[p]:
			return &semanticError{msg: fmt.Sprintf("vault.paths 重复配置：%q", p)}
		}
		seen[p] = true
	}
	if v.MaxStreams < 0 {
		return &semanticError{msg: fmt.Sprintf("vault.max_streams 不得为负数：%d", v.MaxStreams)}
	}
	if v.MaxStreams > 4 {
		// PM 拍板 2026-11-20：实测天花板 4，再高非常危险（AX6 内存）
		return &semanticError{msg: fmt.Sprintf("vault.max_streams 超上限：%d > 4（PM 拍板天花板，防止误配拖垮路由器）", v.MaxStreams)}
	}
	return nil
}

// validateMounts 挂载名结构校验（v1.3.0 批 1，DESIGN-2026-11-24-01 §4.3）：
// 全局 fail-fast（与 vault.paths 校验同口径）。中文合法（现有挂载名均为中文）。
func validateMounts(cfg *Config) error {
	seen := map[string]bool{}
	for i := range cfg.Clouds {
		name := cfg.Clouds[i].MountName()
		switch {
		case name == "":
			return &semanticError{msg: fmt.Sprintf("clouds[%d] 挂载名称为空（name 与 type 均未填）", i)}
		case len(name) > 64:
			return &semanticError{msg: fmt.Sprintf("挂载名超长（≤64 字节）：%q", name)}
		case strings.ContainsAny(name, "/\\"):
			return &semanticError{msg: fmt.Sprintf("挂载名不允许路径分隔符：%q", name)}
		case name == "." || name == "..":
			return &semanticError{msg: fmt.Sprintf("挂载名不允许 . 或 ..：%q", name)}
		case containsControl(name):
			return &semanticError{msg: fmt.Sprintf("挂载名不允许控制字符：%q", name)}
		case seen[name]:
			return &semanticError{msg: fmt.Sprintf("挂载名重复：%q", name)}
		}
		seen[name] = true
	}
	return nil
}

// containsControl 是否包含控制字符
func containsControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
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

	return writeFileAtomic(path, data)
}

// writeFileAtomic 原子写：temp + chmod + fsync + rename + 父目录 fsync。
// 父目录 fsync 保证 rename 本身断电可持久（I3，尽力而为：失败仅记 WARN）。
func writeFileAtomic(path string, data []byte) error {
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
	syncDir(filepath.Dir(path))
	return nil
}

// syncDir 对目录 fsync，使其中文件的新建/替换断电可持久（Linux 支持；失败仅 WARN）
func syncDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		slog.Warn("目录 fsync 失败（尽力而为）", "dir", dir, "err", err)
	}
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
	if err := os.Chmod(bak, 0600); err != nil {
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}

// RestoreBackup 用 .bak 还原配置（管理页提交失败回滚用）；.bak 缺失时报错
func RestoreBackup(path string) error {
	data, err := os.ReadFile(path + ".bak")
	if err != nil {
		return fmt.Errorf("读取备份失败: %w", err)
	}
	return writeFileAtomic(path, data)
}

// WriteCredential 严格读取配置并把 mount 的凭证字段写回（先备份后原子写）。
// 文件缺失/解析失败直接报错，绝不重建默认配置（I2）。
// 返回写入后的完整配置，供调用方更新内存副本。
func WriteCredential(path, mountName string, values map[string]string) (*Config, error) {
	cfg, err := LoadStrict(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	found := false
	for i := range cfg.Clouds {
		if cfg.Clouds[i].MountName() == mountName {
			switch cfg.Clouds[i].Type {
			case "baidu":
				// 百度：写「验证后的最终 refresh_token」（保留键 __refresh_token，
				// DESIGN-2026-11-24-01 §6.2——上游轮换过的新值不得丢弃）；
				// 用户提交 = 新授权代次（credential_generation+1、token_revision 置 1，§6.5）
				rt := values["__refresh_token"]
				if rt == "" {
					rt = values["refresh_token"]
				}
				cfg.Clouds[i].RefreshToken = rt
				cfg.Clouds[i].CredentialGeneration++
				cfg.Clouds[i].TokenRevision = 1
			default:
				// 移动云盘：字段固定为 authorization
				cfg.Clouds[i].Authorization = values["authorization"]
			}
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("未找到挂载: %s", mountName)
	}
	if err := Backup(path); err != nil {
		return nil, fmt.Errorf("备份配置失败: %w", err)
	}
	if err := cfg.Save(path); err != nil {
		return nil, fmt.Errorf("写入配置失败: %w", err)
	}
	return cfg, nil
}
