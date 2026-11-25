package baidu

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"time"

	"mini-cloud/internal/driver"
)

// ============================================================
// OAuthAuthorizer 实现（批 2，DESIGN-2026-11-24-01 §6.4）：
// 授权码手工回填——OAuthStart 生成一次性 state（10 分钟）与授权 URL
// （redirect_uri=oob）；用户登录授权后把浏览器地址栏完整网址（含 code 与 state）
// 粘贴回管理页，OAuthExchange 校验 state（一次性消费）并兑换。
// 兑换为无副作用（不碰正式客户端内存），最终 refresh_token 经
// values["__refresh_token"] 传递（配置写回与 CommitCredential 用同一值）。
// ============================================================

// OAuthStart 生成一次性 state 并返回百度授权 URL
func (d *Driver) OAuthStart() (string, error) {
	if d.clientID == "" || d.clientSecret == "" {
		return "", fmt.Errorf("OAuth 应用凭据未配置（config.json 需填写 client_id 与 client_secret）")
	}
	state, err := randomHex(16)
	if err != nil {
		return "", fmt.Errorf("生成授权状态失败: %w", err)
	}

	d.oauthMu.Lock()
	now := time.Now()
	for k, exp := range d.oauthStates {
		if now.After(exp) {
			delete(d.oauthStates, k)
		}
	}
	if len(d.oauthStates) >= 20 {
		d.oauthMu.Unlock()
		return "", fmt.Errorf("待处理授权过多，请稍后再试")
	}
	d.oauthStates[state] = now.Add(10 * time.Minute)
	d.oauthMu.Unlock()

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", d.clientID)
	q.Set("redirect_uri", oauthRedirectURI)
	q.Set("scope", "basic,netdisk")
	q.Set("state", state)
	q.Set("display", "page")
	return "https://openapi.baidu.com/oauth/2.0/authorize?" + q.Encode(), nil
}

// OAuthExchange 校验 state（存在、未过期、一次性）并用 code 兑换 token；
// 兑换成功用独立临时客户端调用户信息接口轻量验证，随后按 __req 暂存待提交凭据、
// 回填 values["__refresh_token"] 与 values["__expiry"]。
func (d *Driver) OAuthExchange(ctx context.Context, state, code string, values map[string]string) error {
	if state == "" || code == "" {
		return fmt.Errorf("缺少授权 state 或 code（请粘贴浏览器回调后的完整网址）")
	}

	d.oauthMu.Lock()
	exp, ok := d.oauthStates[state]
	if !ok {
		d.oauthMu.Unlock()
		return fmt.Errorf("授权状态无效（可能已使用或从未发起），请重新开始授权")
	}
	delete(d.oauthStates, state) // 一次性消费（I5：防重放）
	d.oauthMu.Unlock()
	if time.Now().After(exp) {
		return fmt.Errorf("授权状态已过期（10 分钟），请重新开始授权")
	}

	access, finalRT, expiresIn, err := d.client.exchangeCode(ctx, code, d.clientID, d.clientSecret)
	if err != nil {
		return fmt.Errorf("授权码兑换失败: %w", err)
	}

	// 轻量验证：独立临时客户端，不碰正式客户端（无副作用）
	verify := NewClient()
	verify.baseURL = d.client.baseURL
	verify.SetAccessToken(access)
	if errno, _ := verify.apiGetWithErrno("/nas", map[string]string{"method": "uinfo"}, nil); errno != 0 {
		return fmt.Errorf("授权 token 验证失败（errno=%d）", errno)
	}

	d.setPending(values["__req"], finalRT, access, expiresIn)
	values["__refresh_token"] = finalRT
	values["__expiry"] = time.Now().Add(time.Duration(expiresIn) * time.Second).Format("2006-01-02 15:04")
	return nil
}

// randomHex crypto/rand 十六进制随机串（state 用）
func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

var _ driver.OAuthAuthorizer = (*Driver)(nil)
