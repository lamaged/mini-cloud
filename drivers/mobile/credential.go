package mobile

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
// CredentialUpdater 实现（两阶段：先验证后提交，docs/design.md §3.2）
// values 保留键：__req（更新请求唯一标识，关联 Validate 与 Commit）、
// __expiry（驱动回填，显示用）。并发更新按 __req 隔离。
// ============================================================

// CredentialFields 返回可更新的凭证字段（供管理页渲染表单）。
// 标签按当前状态动态变化（PM 拍板 2026-11-19）：未过期显示剩余天数、已过期显示获取指引；
// 渲染时现取（admin Mount.FieldsFunc），非启动时定死。
func (d *Driver) CredentialFields() []driver.CredentialField {
	label := "authorization"
	if d.client.isExpired() {
		label = "authorization 已过期，可登录移动云盘网页后，用浏览器开发者工具获得"
	} else if _, _, _, expiration, err := d.client.parseAuthorization(); err == nil {
		remain := time.Duration(expiration-time.Now().UnixMilli()) * time.Millisecond
		if remain < 24*time.Hour {
			label = "authorization（有效期不足 1 天，请尽快更新）"
		} else {
			label = fmt.Sprintf("authorization（有效期还有 %d 天）", (remain+24*time.Hour-1)/(24*time.Hour))
		}
	}
	return []driver.CredentialField{
		{Key: "authorization", Label: label, Kind: "password"},
	}
}

// remainingDays 剩余有效天数（向上取整；解析失败返回 -1）
func (d *Driver) remainingDays() int {
	_, _, _, expiration, err := d.client.parseAuthorization()
	if err != nil {
		return -1
	}
	remain := time.Duration(expiration-time.Now().UnixMilli()) * time.Millisecond
	if remain <= 0 {
		return 0
	}
	return int((remain + 24*time.Hour - 1) / (24 * time.Hour))
}

// ValidateCredential 校验 authorization 格式并用一次性临时客户端做网络验证。
// 不改变正式客户端的任何生效状态（内存凭证、token state、config 均不动）。
// 验证通过后按 __req 暂存待提交凭证，并回填 values["__expiry"]（显示用）。
func (d *Driver) ValidateCredential(ctx context.Context, values map[string]string) error {
	auth := strings.TrimSpace(values["authorization"])
	account, host, expiry, err := d.validateAuthorization(ctx, auth, "")
	if err != nil {
		return err
	}

	d.setPending(values["__req"], auth, account, host)
	values["__expiry"] = expiry.Format("2006-01-02 15:04")
	return nil
}

// validateAuthorization 验证 authorization 是否可用。
// verifyHost 非空时跳过云主机路由发现（测试注入用）。
// 返回解析出的账号、云主机与过期时间。
func (d *Driver) validateAuthorization(ctx context.Context, auth, verifyHost string) (account, host string, expiry time.Time, err error) {
	_, account, _, expiryMs, err := parseAuthString(auth)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("authorization 格式错误: %w", err)
	}
	expiry = time.UnixMilli(expiryMs)
	if !expiry.After(time.Now()) {
		return "", "", time.Time{}, fmt.Errorf("authorization 已过期，请获取新的 authorization")
	}
	// 门槛（P7）：剩余不足 24 小时拒绝接收，防止刚更新完就过期
	if time.Until(expiry) < 24*time.Hour {
		return "", "", time.Time{}, fmt.Errorf("该凭证剩余有效期不足 24 小时（至 %s），请获取新的 authorization",
			expiry.Format("2006-01-02 15:04"))
	}

	// 一次性临时客户端：只验证，不碰正式客户端
	temp := NewClient()
	temp.SetAuth(auth, account, verifyHost)
	if err := verifyAuth(ctx, temp); err != nil {
		return "", "", time.Time{}, fmt.Errorf("凭证验证失败: %w", err)
	}
	return account, temp.getCloudHost(), expiry, nil
}

// verifyAuth 验证客户端凭证可用：云主机未设置时先发现，再列根目录。
func verifyAuth(ctx context.Context, c *Client) error {
	if c.getCloudHost() == "" {
		if err := c.discoverCloudHost(ctx); err != nil {
			return fmt.Errorf("发现云主机失败: %w", err)
		}
	}
	var result PersonalListResp
	if _, err := c.personalRequest(ctx, "/file/list", map[string]interface{}{
		"imageThumbnailStyleList": []string{},
		"orderBy":                 "updated_at",
		"orderDirection":          "DESC",
		"pageInfo": map[string]interface{}{
			"pageCursor": "",
			"pageSize":   1,
		},
		"parentFileId": "/",
	}, &result); err != nil {
		return fmt.Errorf("调用云盘接口失败: %w", err)
	}
	return nil
}

// CommitCredential 把该次验证（按 __req 关联）的凭证应用到正式客户端并落 token state。
// 同步、不联网、不回滚；token state 写入失败时凭证仍生效（重启后以 config.json 为准），
// 返回 %w(driver.ErrStateWriteFailed) 由调用方按告警提示。
func (d *Driver) CommitCredential(values map[string]string) error {
	p := d.takePending(values["__req"])
	if p == nil {
		return fmt.Errorf("没有待提交的凭证，请先验证")
	}

	d.client.SetAuth(p.auth, p.account, p.host)

	// 记录已提交凭证并递增代次：401 刷新回调据此丢弃过期刷新结果
	d.commitMu.Lock()
	d.commitAuth, d.commitAccount, d.commitHost = p.auth, p.account, p.host
	d.hostWarn = "" // 提交成功即清除启动期发现失败提示（§5.3）
	d.authorization = p.auth // 同步 Init 期字段（P5）：防止未来重新 Init 回退旧值
	d.commitMu.Unlock()
	d.credGen.Add(1)

	// 先递增代次使在途路径解析作废，再换缓存（账号可能已变化）
	d.cacheGen.Add(1)
	d.cacheMu.Lock()
	d.pathCache = make(map[string]string)
	d.cacheMu.Unlock()

	if err := d.saveTokenState(); err != nil {
		// 删除残留的旧账号缓存，保证重启后以 config.json 为准
		os.Remove(filepath.Join(d.stateDir, d.name+"_token.json"))
		return fmt.Errorf("%w: %v（重启后仍以 config.json 为准）", driver.ErrStateWriteFailed, err)
	}
	return nil
}

// setPending / takePending：按 __req 暂存与取走（互斥保护；顺带清理过期暂存防泄漏）
func (d *Driver) setPending(reqKey, auth, account, host string) {
	d.pendingMu.Lock()
	defer d.pendingMu.Unlock()
	now := time.Now()
	for k, p := range d.pending {
		if now.Sub(p.createdAt) > 5*time.Minute {
			delete(d.pending, k)
		}
	}
	d.pending[reqKey] = &pendingCred{auth: auth, account: account, host: host, createdAt: now}
}

func (d *Driver) takePending(reqKey string) *pendingCred {
	d.pendingMu.Lock()
	defer d.pendingMu.Unlock()
	p := d.pending[reqKey]
	delete(d.pending, reqKey)
	return p
}

var _ driver.CredentialUpdater = (*Driver)(nil)
