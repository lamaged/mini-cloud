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

// refreshToken 刷新 Authorization Token
func (c *Client) refreshToken() error {
	decode, err := base64.StdEncoding.DecodeString(c.authorization)
	if err != nil {
		return fmt.Errorf("authorization decode failed: %w", err)
	}
	decodeStr := string(decode)
	parts := strings.Split(decodeStr, ":")
	if len(parts) < 3 {
		return fmt.Errorf("invalid authorization format, expected: type:account:token")
	}

	c.account = parts[1]
	tokenPart := parts[2]
	tokenFields := strings.Split(tokenPart, "|")
	if len(tokenFields) < 4 {
		return fmt.Errorf("invalid token format, expected at least 4 fields")
	}

	// 检查过期时间（第4个字段是毫秒时间戳）
	expiration, err := strconv.ParseInt(tokenFields[3], 10, 64)
	if err != nil {
		return fmt.Errorf("invalid expiration: %w", err)
	}

	remaining := expiration - time.Now().UnixMilli()
	if remaining > 1000*60*60*24*15 {
		// 有效期 > 15 天，无需刷新
		slog.Info("移动云盘 token 有效期充足，跳过刷新", "remaining_days", remaining/(1000*60*60*24))
		return nil
	}
	if remaining < 0 {
		return fmt.Errorf("authorization has expired, please update it")
	}

	// 刷新 token
	url := "https://aas.caiyun.feixin.10086.cn:443/tellin/authTokenRefresh.do"
	reqBody := fmt.Sprintf("<root><token>%s</token><account>%s</account><clienttype>656</clienttype></root>",
		tokenPart, c.account)

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
		slog.Warn("移动云盘 token 刷新 XML 解析失败", "err", err)
		return nil // 不阻塞启动
	}

	if refreshResp.Return == "0" && refreshResp.Token != "" {
		c.authorization = base64.StdEncoding.EncodeToString([]byte(parts[0] + ":" + parts[1] + ":" + refreshResp.Token))
		slog.Info("移动云盘 token 刷新成功")
		return nil
	}

	if refreshResp.Desc != "" {
		slog.Warn("移动云盘 token 刷新失败", "desc", refreshResp.Desc)
	}
	return nil
}

// ============================================================
// 主机发现
// ============================================================

// discoverCloudHost 发现个人云 API 主机地址
func (c *Client) discoverCloudHost() error {
	if c.account == "" {
		// 从 authorization 解析 account
		decode, err := base64.StdEncoding.DecodeString(c.authorization)
		if err != nil {
			return fmt.Errorf("decode authorization failed: %w", err)
		}
		parts := strings.Split(string(decode), ":")
		if len(parts) < 2 {
			return fmt.Errorf("invalid authorization")
		}
		c.account = parts[1]
	}

	resp, err := c.requestRoute()
	if err != nil {
		return fmt.Errorf("query route failed: %w", err)
	}

	for _, policy := range resp.Data.RoutePolicyList {
		if policy.ModName == "personal" && policy.HttpsUrl != "" {
			c.cloudHost = strings.TrimRight(policy.HttpsUrl, "/")
			slog.Info("移动云盘主机发现成功", "host", c.cloudHost)
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
