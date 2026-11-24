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

	req, err := http.NewRequestWithContext(ctx, "GET", c.tokenURL+"?"+params.Encode(), nil)
	if err != nil {
		return "", "", 0, fmt.Errorf("构造 token 请求失败: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", 0, fmt.Errorf("token refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	var result TokenResp
	var errResp TokenErrResp
	body, _ := io.ReadAll(resp.Body)
	json.Unmarshal(body, &result)
	json.Unmarshal(body, &errResp)

	if errResp.Error != "" {
		return "", "", 0, fmt.Errorf("token refresh error: %s - %s", errResp.Error, errResp.ErrorDescription)
	}
	if result.AccessToken == "" {
		return "", "", 0, fmt.Errorf("empty access_token in response")
	}

	expiresIn := result.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 2592000 // 默认 30 天
	}
	return result.AccessToken, result.RefreshToken, expiresIn, nil
}

// refreshToken 兼容旧调用路径的包装：无副作用交换（调用方自行 SetAccessToken 与落盘）。
// 批 2（§6.7）将把 ctx 贯通到 Init/RefreshToken/watcher 全链路。
func (c *Client) refreshToken(refreshToken, clientID, clientSecret string) (string, string, int, error) {
	return c.exchangeToken(context.Background(), refreshToken, clientID, clientSecret)
}

// ============================================================
// Token 持久化
// ============================================================

// SaveToken 保存 token 状态文件（0600；目录 0700——凭证目录收紧权限，
// DESIGN-2026-11-24-01 §6.6.3）。批 2 将改为原子写（temp+rename）。
func SaveToken(name, stateDir string, token *TokenState) error {
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return err
	}
	if err := os.Chmod(stateDir, 0700); err != nil {
		slog.Warn("state 目录权限收紧失败（尽力而为）", "dir", stateDir, "err", err)
	}
	return os.WriteFile(filepath.Join(stateDir, name+"_token.json"), data, 0600)
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
