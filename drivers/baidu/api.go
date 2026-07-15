package baidu

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	API_BASE = "https://pan.baidu.com/rest/2.0/xpan"
)

// Client 百度网盘 API 客户端
type Client struct {
	httpClient   *http.Client
	accessToken  string
	onAuthFailed func() error // 401 回调：刷新 token 并更新状态
}

// NewClient 创建客户端
func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// SetAccessToken 设置 access_token
func (c *Client) SetAccessToken(token string) {
	c.accessToken = token
}

// SetAuthRefreshCallback 设置 401 时的自动刷新回调
func (c *Client) SetAuthRefreshCallback(cb func() error) {
	c.onAuthFailed = cb
}

// apiGet 发送 GET 请求，自动附加 access_token
func (c *Client) apiGet(path string, params map[string]string, result interface{}) ([]byte, error) {
	u, _ := url.Parse(API_BASE + path)
	q := u.Query()
	q.Set("access_token", c.accessToken)
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()

	resp, err := c.httpClient.Get(u.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// 检查 errno → 自动刷新 token 并重试一次
	var base struct {
		Errno int `json:"errno"`
	}
	json.Unmarshal(body, &base)

	// errno 111 = token expired, -6 = access token invalid
	if (base.Errno == 111 || base.Errno == -6) && c.onAuthFailed != nil {
		if err := c.onAuthFailed(); err == nil {
			// 刷新成功，重试请求（递归一次）
			return c.apiGet(path, params, result)
		}
	}

	if result != nil {
		json.Unmarshal(body, result)
	}

	return body, nil
}

// apiGetWithErrno 发送请求并检查 errno
func (c *Client) apiGetWithErrno(path string, params map[string]string, result interface{}) (int, error) {
	body, err := c.apiGet(path, params, result)
	if err != nil {
		return -1, err
	}
	var base struct {
		Errno int `json:"errno"`
	}
	json.Unmarshal(body, &base)
	return base.Errno, nil
}

// ============================================================
// 无重定向客户端（获取下载链接）
// ============================================================

var NoRedirectClient = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// apiGetRaw 发送 GET 请求到任意 URL，手动拼接 query（不编码特殊字符）
func (c *Client) apiGetRaw(rawURL string, params map[string]string, result interface{}) ([]byte, error) {
	// 手动拼接 URL，保留 target 参数中的 [ ] " 等字符
	var parts []string
	parts = append(parts, "access_token="+c.accessToken)
	for k, v := range params {
		parts = append(parts, k+"="+v)
	}
	fullURL := rawURL + "?" + strings.Join(parts, "&")

	resp, err := c.httpClient.Get(fullURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if result != nil {
		json.Unmarshal(body, result)
	}
	return body, nil
}
