package mobile

import (
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ============================================================
// Token 刷新
// ============================================================

// parseAuthString 解析 Authorization 字符串（独立函数，便于比较不同 token）
// 返回类型、账号、token 部分、过期时间（毫秒时间戳，token 第 4 个字段）
func parseAuthString(authorization string) (tokenType, account, tokenPart string, expirationMillis int64, err error) {
	decode, err := base64.StdEncoding.DecodeString(authorization)
	if err != nil {
		return "", "", "", 0, fmt.Errorf("authorization decode failed: %w", err)
	}
	parts := strings.Split(string(decode), ":")
	if len(parts) < 3 {
		return "", "", "", 0, fmt.Errorf("invalid authorization format, expected: type:account:token")
	}

	tokenType = parts[0]
	account = parts[1]
	tokenPart = parts[2]
	tokenFields := strings.Split(tokenPart, "|")
	if len(tokenFields) < 4 {
		return "", "", "", 0, fmt.Errorf("invalid token format, expected at least 4 fields")
	}

	expirationMillis, err = strconv.ParseInt(tokenFields[3], 10, 64)
	if err != nil {
		return "", "", "", 0, fmt.Errorf("invalid expiration: %w", err)
	}
	return tokenType, account, tokenPart, expirationMillis, nil
}

// parseAuthorization 解析当前 Client 的 Authorization
func (c *Client) parseAuthorization() (tokenType, account, tokenPart string, expirationMillis int64, err error) {
	return parseAuthString(c.getAuthorization())
}

// needRefresh 判断 token 是否需要刷新（临近过期或已过期）
// 解析失败时返回 true，视为需要刷新以便尽早暴露问题
func (c *Client) needRefresh() bool {
	_, _, _, expiration, err := c.parseAuthorization()
	if err != nil {
		return true
	}
	remaining := expiration - time.Now().UnixMilli()
	return remaining < 1000*60*60*24*15
}

// isExpired 判断 token 是否已过期/无效
// 解析失败或过期时间不晚于当前时间均视为过期
func (c *Client) isExpired() bool {
	_, _, _, expiration, err := c.parseAuthorization()
	if err != nil {
		return true
	}
	return expiration <= time.Now().UnixMilli()
}

// refreshToken 刷新 Authorization Token
func (c *Client) refreshToken() error {
	tokenType, account, tokenPart, expiration, err := c.parseAuthorization()
	if err != nil {
		return err
	}
	c.setAccount(account)

	remaining := expiration - time.Now().UnixMilli()
	if remaining > 1000*60*60*24*15 {
		// 有效期 > 15 天，无需刷新
		slog.Info("移动云盘 token 有效期充足，跳过刷新", "remaining_days", remaining/(1000*60*60*24))
		return nil
	}
	if remaining < 0 {
		// 已过期仍尝试刷新：服务端可能在宽限期内仍接受旧 token
		slog.Warn("移动云盘 token 已过期，仍尝试刷新", "expired_days", -remaining/(1000*60*60*24))
	} else {
		slog.Info("移动云盘 token 临近过期，刷新", "remaining_days", remaining/(1000*60*60*24))
	}

	// 刷新 token
	url := "https://aas.caiyun.feixin.10086.cn:443/tellin/authTokenRefresh.do"
	reqBody := fmt.Sprintf("<root><token>%s</token><account>%s</account><clienttype>656</clienttype></root>",
		tokenPart, account)

	req, err := http.NewRequest("POST", url, strings.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("token refresh request failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/xml")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("token refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read refresh response failed: %w", err)
	}

	var refreshResp RefreshTokenResp
	if err := xml.Unmarshal(body, &refreshResp); err != nil {
		return fmt.Errorf("token refresh XML 解析失败: %w", err)
	}

	if refreshResp.Return == "0" && refreshResp.Token != "" {
		c.setAuthorization(base64.StdEncoding.EncodeToString([]byte(tokenType + ":" + account + ":" + refreshResp.Token)))
		slog.Info("移动云盘 token 刷新成功")
		return nil
	}

	// 刷新失败：返回错误，让调用方决定如何处理
	desc := refreshResp.Desc
	if desc == "" {
		desc = "return=" + refreshResp.Return
	}
	return fmt.Errorf("移动云盘 token 刷新失败: %s", desc)
}

// ============================================================
// 主机发现
// ============================================================

// discoverCloudHost 发现个人云 API 主机地址
func (c *Client) discoverCloudHost() error {
	if c.getAccount() == "" {
		// 从 authorization 解析 account
		decode, err := base64.StdEncoding.DecodeString(c.getAuthorization())
		if err != nil {
			return fmt.Errorf("decode authorization failed: %w", err)
		}
		parts := strings.Split(string(decode), ":")
		if len(parts) < 2 {
			return fmt.Errorf("invalid authorization")
		}
		c.setAccount(parts[1])
	}

	resp, err := c.requestRoute()
	if err != nil {
		return fmt.Errorf("query route failed: %w", err)
	}

	for _, policy := range resp.Data.RoutePolicyList {
		if policy.ModName == "personal" && policy.HttpsUrl != "" {
			cloudHost := strings.TrimRight(policy.HttpsUrl, "/")
			c.setCloudHost(cloudHost)
			slog.Info("移动云盘主机发现成功", "host", cloudHost)
			return nil
		}
	}

	return fmt.Errorf("personal cloud host not found in route policy")
}

// ============================================================
// Token 持久化
// ============================================================

// SaveToken 保存 Token
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

// LoadToken 加载 Token
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
