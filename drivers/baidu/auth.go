package baidu

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
)

// 默认 OAuth 凭证（百度开放平台公开的 client_id/secret）
const (
	DefaultClientID     = "hq9yQ9w9kR4YHj1kyYafLygVocobh7Sf"
	DefaultClientSecret = "YH2VpZcFJHYNnV6vLfHQXDBhcE7ZChyE"
)

// ============================================================
// Token 刷新
// ============================================================

// refreshToken 使用 refresh_token 获取新的 access_token
// 返回 accessToken, refreshToken, expiresIn (秒), error
func (c *Client) refreshToken(refreshToken, clientID, clientSecret string) (string, string, int, error) {
	slog.Info("刷新百度网盘 token")
	u := "https://openapi.baidu.com/oauth/2.0/token"
	params := url.Values{}
	params.Set("grant_type", "refresh_token")
	params.Set("refresh_token", refreshToken)
	params.Set("client_id", clientID)
	params.Set("client_secret", clientSecret)

	resp, err := c.httpClient.Get(u + "?" + params.Encode())
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

	c.accessToken = result.AccessToken
	expiresIn := result.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 2592000 // 默认 30 天
	}
	return result.AccessToken, result.RefreshToken, expiresIn, nil
}

// ============================================================
// Token 持久化
// ============================================================

func SaveToken(name, stateDir string, token *TokenState) error {
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return err
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
