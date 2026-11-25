package baidu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// ============================================================
// Token 交换
// ============================================================

// exchangeToken 纯 token 交换（v1.3.0 批 1，DESIGN-2026-11-24-01 §6.2）：
// 只请求并返回结果，**不修改客户端内存**（无副作用——供 ValidateCredential
// 两阶段验证契约使用；旧实现在此处调 SetAccessToken 会破坏「验证失败零变化」）。
// 返回 accessToken, newRefreshToken, expiresIn (秒), error。
func (c *Client) exchangeToken(ctx context.Context, refreshToken, clientID, clientSecret string) (string, string, int, error) {
	params := url.Values{}
	params.Set("grant_type", "refresh_token")
	params.Set("refresh_token", refreshToken)
	params.Set("client_id", clientID)
	params.Set("client_secret", clientSecret)

	result, err := c.requestToken(ctx, params)
	if err != nil {
		return "", "", 0, err
	}
	expiresIn := result.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 2592000 // 默认 30 天
	}
	return result.AccessToken, result.RefreshToken, expiresIn, nil
}

// oauthRedirectURI 授权回调地址（批 2 §6.4，Q3 已定案：oob；备用
// https://openapi.baidu.com/oauth/2.0/login_success）
const oauthRedirectURI = "oob"

// exchangeCode 授权码换 token（批 2 §6.4；无副作用，与 exchangeToken 共用请求函数）
func (c *Client) exchangeCode(ctx context.Context, code, clientID, clientSecret string) (accessToken, refreshToken string, expiresIn int, err error) {
	params := url.Values{}
	params.Set("grant_type", "authorization_code")
	params.Set("code", code)
	params.Set("client_id", clientID)
	params.Set("client_secret", clientSecret)
	params.Set("redirect_uri", oauthRedirectURI)

	var result *TokenResp
	result, err = c.requestToken(ctx, params)
	if err != nil {
		return "", "", 0, err
	}
	if result.RefreshToken == "" {
		return "", "", 0, fmt.Errorf("empty refresh_token in response")
	}
	expiresIn = result.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 2592000
	}
	return result.AccessToken, result.RefreshToken, expiresIn, nil
}

// requestToken token 端点通用请求（无副作用 + 网络错误脱敏：
// url.Error 携带含凭据参数的完整 URL，refresh_token/code/client_secret 均需替换）
func (c *Client) requestToken(ctx context.Context, params url.Values) (*TokenResp, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.tokenURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("构造 token 请求失败: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token 交换网络错误: %s",
			redactSecrets(err.Error(), params.Get("refresh_token"), params.Get("code"),
				params.Get("client_id"), params.Get("client_secret")))
	}
	defer resp.Body.Close()

	var result TokenResp
	var errResp TokenErrResp
	body, _ := io.ReadAll(resp.Body)
	json.Unmarshal(body, &result)
	json.Unmarshal(body, &errResp)

	if errResp.Error != "" {
		return nil, fmt.Errorf("token exchange error: %s - %s", errResp.Error, errResp.ErrorDescription)
	}
	if result.AccessToken == "" {
		return nil, fmt.Errorf("empty access_token in response")
	}
	return &result, nil
}

// redactSecrets 把错误文本中的凭据值（含 URL 编码形式）替换为 ***。
// 双保险：既挡 url.Error 携带的完整 URL，也挡任何上游包装层透传的原文。
func redactSecrets(msg string, secrets ...string) string {
	for _, s := range secrets {
		if s == "" {
			continue
		}
		for _, form := range []string{s, url.QueryEscape(s)} {
			msg = strings.ReplaceAll(msg, form, "***")
		}
	}
	return msg
}

// refreshToken 兼容旧调用路径的包装：无副作用交换（调用方自行 SetAccessToken 与落盘）。
// 批 2（§6.7）将把 ctx 贯通到 Init/RefreshToken/watcher 全链路。
func (c *Client) refreshToken(refreshToken, clientID, clientSecret string) (string, string, int, error) {
	return c.exchangeToken(context.Background(), refreshToken, clientID, clientSecret)
}

// ============================================================
// Token 持久化
// ============================================================

// SaveToken 保存 token 缓存（原子写：temp + chmod 0600 + fsync + rename + 父目录 fsync；
// 目录 0700——DESIGN-2026-11-24-01 §6.6.3，批 2 交付）
func SaveToken(name, stateDir string, token *TokenState) error {
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(stateDir, 0700); err != nil {
		slog.Warn("state 目录权限收紧失败（尽力而为）", "dir", stateDir, "err", err)
	}
	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(stateDir, name+"_token.json"), data)
}

// writeFileAtomic 原子写（与 conf 同款：temp + chmod + fsync + rename + 父目录 fsync）
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0600); err != nil {
		os.Remove(tmp)
		return err
	}
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

// syncDir 目录 fsync（尽力而为：失败仅 WARN）
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

func LoadToken(name, stateDir string) (*TokenState, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, name+"_token.json"))
	if err != nil {
		return nil, err
	}
	var token TokenState
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, err
	}
	return &token, nil
}
