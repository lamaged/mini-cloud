package mobile

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// ============================================================
// 签名和加密工具
// ============================================================

// calSign 计算 mcloud-sign 请求头签名
// body: JSON 请求体字符串
// ts: 时间戳
// randStr: 随机字符串
func calSign(body, ts, randStr string) string {
	// 1. URL 编码 body
	encoded := encodeURIComponent(body)
	// 2. 按字符排序
	chars := strings.Split(encoded, "")
	sort.Strings(chars)
	sorted := strings.Join(chars, "")
	// 3. Base64 编码
	b64 := base64.StdEncoding.EncodeToString([]byte(sorted))
	// 4. MD5(bodyBase64) + MD5(ts+":"+randStr)
	h1 := fmt.Sprintf("%x", md5.Sum([]byte(b64)))
	h2 := fmt.Sprintf("%x", md5.Sum([]byte(ts+":"+randStr)))
	// 5. MD5(h1+h2) 大写
	sum := fmt.Sprintf("%x", md5.Sum([]byte(h1+h2)))
	return strings.ToUpper(sum)
}

// encodeURIComponent JS 风格的 URL 编码
func encodeURIComponent(str string) string {
	r := strings.ReplaceAll(str, "+", "%20")
	r = strings.ReplaceAll(r, "%21", "!")
	r = strings.ReplaceAll(r, "%27", "'")
	r = strings.ReplaceAll(r, "%28", "(")
	r = strings.ReplaceAll(r, "%29", ")")
	r = strings.ReplaceAll(r, "%2A", "*")
	return r
}

// randomString 生成随机字符串
func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		b[i] = letters[idx.Int64()]
	}
	return string(b)
}

// ============================================================
// HTTP 客户端
// ============================================================

// Client 移动云盘 API 客户端
type Client struct {
	httpClient    *http.Client
	mu            sync.RWMutex // 保护 authorization/account/cloudHost
	authorization string       // Base64 编码的认证令牌
	account       string       // 账号
	cloudHost     string       // 个人云 API 主机地址
	onAuthFailed  func() error // 401 回调：刷新 token 并更新 authorization
}

// NewClient 创建客户端
func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// SetAuth 设置认证信息
func (c *Client) SetAuth(authorization, account, cloudHost string) {
	c.mu.Lock()
	c.authorization = authorization
	c.account = account
	c.cloudHost = cloudHost
	c.mu.Unlock()
}

func (c *Client) getAuthorization() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.authorization
}

func (c *Client) setAuthorization(authorization string) {
	c.mu.Lock()
	c.authorization = authorization
	c.mu.Unlock()
}

func (c *Client) getAccount() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.account
}

func (c *Client) setAccount(account string) {
	c.mu.Lock()
	c.account = account
	c.mu.Unlock()
}

func (c *Client) getCloudHost() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cloudHost
}

func (c *Client) setCloudHost(cloudHost string) {
	c.mu.Lock()
	c.cloudHost = cloudHost
	c.mu.Unlock()
}

// SetAuthRefreshCallback 设置 401 时的自动刷新回调
func (c *Client) SetAuthRefreshCallback(cb func() error) {
	c.onAuthFailed = cb
}

// ============================================================
// 个人云新版 API 请求
// ============================================================

// personalRequest 发送个人云 API 请求（自动签名）
func (c *Client) personalRequest(pathname string, body interface{}, result interface{}) ([]byte, error) {
	cloudHost := c.getCloudHost()
	if cloudHost == "" {
		return nil, fmt.Errorf("cloud host not set")
	}

	url := cloudHost + pathname
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	randStr := randomString(16)
	ts := time.Now().Format("2006-01-02 15:04:05")
	sign := calSign(string(bodyBytes), ts, randStr)

	req, err := http.NewRequest("POST", url, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	req.Header.Set("Authorization", "Basic "+c.getAuthorization())
	req.Header.Set("Caller", "web")
	req.Header.Set("Cms-Device", "default")
	req.Header.Set("Mcloud-Channel", "1000101")
	req.Header.Set("Mcloud-Client", "10701")
	req.Header.Set("Mcloud-Route", "001")
	req.Header.Set("Mcloud-Sign", fmt.Sprintf("%s,%s,%s", ts, randStr, sign))
	req.Header.Set("Mcloud-Version", "7.14.0")
	req.Header.Set("Origin", "https://yun.139.com")
	req.Header.Set("Referer", "https://yun.139.com/w/")
	req.Header.Set("x-DeviceInfo", "||9|7.14.0|chrome|120.0.0.0|||windows 10||zh-CN|||")
	req.Header.Set("x-huawei-channelSrc", "10000034")
	req.Header.Set("x-inner-ntwk", "2")
	req.Header.Set("x-m4c-caller", "PC")
	req.Header.Set("x-m4c-src", "10002")
	req.Header.Set("x-SvcType", "1")
	req.Header.Set("X-Yun-Api-Version", "v1")
	req.Header.Set("X-Yun-App-Channel", "10000034")
	req.Header.Set("X-Yun-Channel-Source", "10000034")
	req.Header.Set("X-Yun-Client-Info", "||9|7.14.0|chrome|120.0.0.0|||windows 10||zh-CN|||dW5kZWZpbmVk||")
	req.Header.Set("X-Yun-Module-Type", "100")
	req.Header.Set("X-Yun-Svc-Type", "1")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// 检查基础错误
	var base BaseResp
	json.Unmarshal(respBody, &base)
	if !base.Success {
		err := fmt.Errorf("mobile api error: %s", base.Message)
		// 检测 401/认证失败 → 自动刷新 Token 并重试
		if c.onAuthFailed != nil && isAuthError(base.Code, base.Message) {
			if refreshErr := c.onAuthFailed(); refreshErr == nil {
				// 重建请求体（第一次 Do 已消耗 body），然后重试
				req.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))
				req.ContentLength = int64(len(bodyBytes))
				req.Header.Set("Authorization", "Basic "+c.getAuthorization())
				// 重算签名：ts/randStr 已更新，Mcloud-Sign 需重新计算
				newRandStr := randomString(16)
				newTs := time.Now().Format("2006-01-02 15:04:05")
				newSign := calSign(string(bodyBytes), newTs, newRandStr)
				req.Header.Set("Mcloud-Sign", fmt.Sprintf("%s,%s,%s", newTs, newRandStr, newSign))
				resp2, retryErr := c.httpClient.Do(req)
				if retryErr == nil {
					defer resp2.Body.Close()
					respBody2, _ := io.ReadAll(resp2.Body)
					var base2 BaseResp
					json.Unmarshal(respBody2, &base2)
					if base2.Success {
						if result != nil {
							json.Unmarshal(respBody2, result)
						}
						return respBody2, nil
					}
				}
			}
		}
		return nil, err
	}

	if result != nil {
		if err := json.Unmarshal(respBody, result); err != nil {
			return nil, fmt.Errorf("parse response: %w", err)
		}
	}

	return respBody, nil
}

// isAuthError 判断错误码是否表示认证失败
func isAuthError(code, msg string) bool {
	msgLower := strings.ToLower(msg)
	return code == "HMEC0001" || // 移动云盘未登录错误码
		strings.Contains(msgLower, "unauthorized") ||
		strings.Contains(msgLower, "auth") ||
		strings.Contains(msgLower, "login") ||
		strings.Contains(msgLower, "token") ||
		strings.Contains(msgLower, "credential") ||
		strings.Contains(msgLower, "session")
}

// ============================================================
// 路由策略请求（获取云主机地址）
// ============================================================

// requestRoute 查询路由策略获取个人云主机地址
func (c *Client) requestRoute() (*QueryRoutePolicyResp, error) {
	url := "https://user-njs.yun.139.com/user/route/qryRoutePolicy"

	body := map[string]interface{}{
		"userInfo": map[string]interface{}{
			"userType":    1,
			"accountType": 1,
			"accountName": c.getAccount(),
		},
		"modAddrType": 1,
	}

	bodyBytes, _ := json.Marshal(body)
	randStr := randomString(16)
	ts := time.Now().Format("2006-01-02 15:04:05")
	sign := calSign(string(bodyBytes), ts, randStr)

	req, err := http.NewRequest("POST", url, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	req.Header.Set("Authorization", "Basic "+c.getAuthorization())
	req.Header.Set("Cms-Device", "default")
	req.Header.Set("Mcloud-Channel", "1000101")
	req.Header.Set("Mcloud-Client", "10701")
	req.Header.Set("Mcloud-Sign", fmt.Sprintf("%s,%s,%s", ts, randStr, sign))
	req.Header.Set("Mcloud-Version", "7.14.0")
	req.Header.Set("Origin", "https://yun.139.com")
	req.Header.Set("Referer", "https://yun.139.com/w/")
	req.Header.Set("x-DeviceInfo", "||9|7.14.0|chrome|120.0.0.0|||windows 10||zh-CN|||")
	req.Header.Set("x-huawei-channelSrc", "10000034")
	req.Header.Set("x-inner-ntwk", "2")
	req.Header.Set("x-m4c-caller", "PC")
	req.Header.Set("x-m4c-src", "10002")
	req.Header.Set("x-SvcType", "1")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result QueryRoutePolicyResp
	json.Unmarshal(respBody, &result)
	return &result, nil
}
