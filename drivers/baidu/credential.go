package baidu

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mini-cloud/internal/driver"
)

// ============================================================
// CredentialUpdater 实现（两阶段：先验证后提交）
// v1.3.0 批 1（DESIGN-2026-11-24-01 §6.2）：
// - ValidateCredential 用**无副作用** token 交换函数做网络验证（不碰客户端内存）；
// - 验证返回的最终 refresh_token（上游可能轮换）经保留键 __refresh_token 传递，
//   配置写回与内存提交使用同一最终值，不丢轮换结果；
// - 提交结果语义（§6.2 表）：验证失败/配置写入失败零变化；Commit 真失败回滚；
//   state 写失败返回 ErrStateWriteFailed（凭据仍生效，缓存告警）。
// ============================================================

// pendingCred 一次 ValidateCredential 暂存的待提交凭证
type pendingCred struct {
	refreshToken string // 验证后的最终 refresh_token（上游轮换过则为新值）
	accessToken  string // 本次交换得到的 access_token（提交时写入缓存）
	expiresIn    int
	createdAt    time.Time
}

// CredentialFields 返回可更新的凭证字段（供管理页渲染表单）。
// 标签按当前配置状态动态提示：应用凭据未配置 → 指引先补 config；未授权 → 引导粘贴。
func (d *Driver) CredentialFields() []driver.CredentialField {
	label := "refresh_token（授权票据，与 client_id/client_secret 配套）"
	if d.clientID == "" || d.clientSecret == "" {
		label = "refresh_token（需先在 config.json 填写 client_id 与 client_secret，来自百度网盘开放平台应用）"
	} else if d.getRefreshToken() == "" {
		label = "refresh_token（尚未授权：粘贴授权票据后点更新）"
	}
	return []driver.CredentialField{
		{Key: "refresh_token", Label: label, Kind: "password"},
	}
}

// ValidateCredential 用无副作用 token 交换验证 refresh_token 可用。
// 不改变正式客户端的任何生效状态（内存凭据、token state、config 均不动）。
// 验证通过后按 __req 暂存最终凭据，回填 values["__refresh_token"]（配置写回与
// Commit 的唯一凭据值）与 values["__expiry"]（显示用）。
func (d *Driver) ValidateCredential(ctx context.Context, values map[string]string) error {
	input := strings.TrimSpace(values["refresh_token"])
	if input == "" {
		return fmt.Errorf("refresh_token 不能为空")
	}
	if d.clientID == "" || d.clientSecret == "" {
		return fmt.Errorf("OAuth 应用凭据未配置：请先在 config.json 填写 client_id 与 client_secret（百度网盘开放平台应用）")
	}

	accessToken, newRefresh, expiresIn, err := d.client.exchangeToken(ctx, input, d.clientID, d.clientSecret)
	if err != nil {
		return fmt.Errorf("凭证验证失败: %w", err)
	}

	// 最终 refresh_token：上游轮换过则用新值，否则用输入值（绝不丢弃轮换结果）
	final := newRefresh
	if final == "" {
		final = input
	}

	d.setPending(values["__req"], final, accessToken, expiresIn)
	values["__refresh_token"] = final
	values["__expiry"] = time.Now().Add(time.Duration(expiresIn) * time.Second).Format("2006-01-02 15:04")
	return nil
}

// CommitCredential 把该次验证（按 __req 关联）的最终凭据应用到正式客户端并落 token state。
// 同步、不联网、不回滚；token state 写入失败时凭据仍生效（重启后以 config.json 为准），
// 返回 %w(driver.ErrStateWriteFailed) 由调用方按告警提示。
func (d *Driver) CommitCredential(values map[string]string) error {
	p := d.takePending(values["__req"])
	if p == nil {
		return fmt.Errorf("没有待提交的凭证，请先验证")
	}

	// 应用最终凭据到内存（互斥锁 getter/setter，铁律 8）。
	// 与刷新单飞同锁（§6.5）：提交与在途轮换串行化——谁后执行谁生效，
	// 代次检查（refreshSingleFlight 写回后复检）防迟到轮换覆盖新授权。
	d.refreshMu.Lock()
	d.setRefreshToken(p.refreshToken)
	d.client.SetAccessToken(p.accessToken)
	d.clearRefreshFailure() // 新凭据提交成功：清除旧刷新失败事实（§6.10）
	d.refreshMu.Unlock()

	// token state 缓存（§6.5 批 2：纯缓存，V=1 + 当前代次；写失败凭据仍生效，
	// config 为唯一真值源，按告警返回）
	if err := SaveToken(d.name, d.stateDir, &TokenState{
		V:                   1,
		AccessToken:         p.accessToken,
		ExpiresAt:           time.Now().Unix() + int64(p.expiresIn),
		CredentialGeneration: int(d.credGen.Load()),
	}); err != nil {
		// 删除残留旧缓存，保证重启后以 config.json 为准
		os.Remove(filepath.Join(d.stateDir, d.name+"_token.json"))
		return fmt.Errorf("%w: %v（重启后仍以 config.json 为准）", driver.ErrStateWriteFailed, err)
	}
	return nil
}

// setPending / takePending：按 __req 暂存与取走（互斥保护；顺带清理过期暂存防泄漏）
func (d *Driver) setPending(reqKey, refreshToken, accessToken string, expiresIn int) {
	d.pendingMu.Lock()
	defer d.pendingMu.Unlock()
	now := time.Now()
	for k, p := range d.pending {
		if now.Sub(p.createdAt) > 5*time.Minute {
			delete(d.pending, k)
		}
	}
	d.pending[reqKey] = &pendingCred{refreshToken: refreshToken, accessToken: accessToken, expiresIn: expiresIn, createdAt: now}
}

func (d *Driver) takePending(reqKey string) *pendingCred {
	d.pendingMu.Lock()
	defer d.pendingMu.Unlock()
	p := d.pending[reqKey]
	delete(d.pending, reqKey)
	return p
}

var _ driver.CredentialUpdater = (*Driver)(nil)
