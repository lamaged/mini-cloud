package tianyi

import (
	"bytes"
	"crypto/aes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ============================================================
// API 常量和端点
// ============================================================

const (
	API_URL  = "https://api.cloud.189.cn"
	AUTH_URL = "https://open.e.189.cn"
	WEB_URL  = "https://cloud.189.cn"

	APP_ID      = "8025431004"
	CLIENT_TYPE = "10020"
	ACCOUNT_TYPE = "02"
	VERSION     = "6.2"
	CHANNEL_ID  = "web_cloud.189.cn"
	PC          = "TELEPC"

	RETURN_URL = "https://m.cloud.189.cn/zhuanti/2020/loginErrorPc/index.html"

	ROOT_FOLDER_ID = "-11"
)

// ============================================================
// 加密和签名工具
// ============================================================

// RsaEncrypt RSA 加密（用于加密用户名密码）
func RsaEncrypt(publicKey, origData string) string {
	block, _ := pem.Decode([]byte(publicKey))
	if block == nil {
		return ""
	}
	pubInterface, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return ""
	}
	pub, ok := pubInterface.(*rsa.PublicKey)
	if !ok {
		return ""
	}
	data, err := rsa.EncryptPKCS1v15(rand.Reader, pub, []byte(origData))
	if err != nil {
		return ""
	}
	return strings.ToUpper(hex.EncodeToString(data))
}

// signatureOfHmac 计算 API 请求的 HMAC-SHA1 签名
func signatureOfHmac(sessionSecret, sessionKey, method, fullUrl, dateOfGmt, param string) string {
	urlPath := regexp.MustCompile(`://[^/]+((/[^/\s?#]*)*)`).FindStringSubmatch(fullUrl)[1]
	mac := hmac.New(sha1.New, []byte(sessionSecret))
	data := fmt.Sprintf("SessionKey=%s&Operate=%s&RequestURI=%s&Date=%s",
		sessionKey, method, urlPath, dateOfGmt)
	if param != "" {
		data += "&params=" + param
	}
	mac.Write([]byte(data))
	return strings.ToUpper(hex.EncodeToString(mac.Sum(nil)))
}

// AesECBEncrypt AES-ECB 加密（用于加密 params 参数）
func AesECBEncrypt(data, key string) string {
	block, err := aes.NewCipher([]byte(key))
	if err != nil {
		return ""
	}
	paddingData := pkcs7Padding([]byte(data), block.BlockSize())
	encrypted := make([]byte, len(paddingData))
	size := block.BlockSize()
	for src, dst := paddingData, encrypted; len(src) > 0; src, dst = src[size:], dst[size:] {
		block.Encrypt(dst[:size], src[:size])
	}
	return strings.ToUpper(hex.EncodeToString(encrypted))
}

func pkcs7Padding(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	padtext := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(data, padtext...)
}

func getHttpDateStr() string {
	return time.Now().UTC().Format(http.TimeFormat)
}

// ============================================================
// 参数编码（排序后拼接，用于 AES 加密前的 params 串）
// ============================================================

type Params map[string]string

func (p Params) Set(k, v string) {
	p[k] = v
}

func (p Params) Encode() string {
	if len(p) == 0 {
		return ""
	}
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf strings.Builder
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte('&')
		}
		buf.WriteString(k)
		buf.WriteByte('=')
		buf.WriteString(p[k])
	}
	return buf.String()
}

// ============================================================
// HTTP 客户端封装
// ============================================================

// Client 天翼云盘 HTTP 客户端
type Client struct {
	httpClient    *http.Client
	mu            sync.RWMutex // 保护会话凭据字段
	sessionKey    string
	sessionSecret string
	accessToken   string
	refreshToken  string

	familySessionKey    string
	familySessionSecret string

	onAuthFailed func() error // 401 回调：刷新 session 并更新状态
}

// SetFamilySession 设置家庭云会话密钥
func (c *Client) SetFamilySession(key, secret string) {
	c.mu.Lock()
	c.familySessionKey = key
	c.familySessionSecret = secret
	c.mu.Unlock()
}

// sessionFor 返回签名用的 session（家庭云用 family session）
func (c *Client) sessionFor(family bool) (key, secret string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if family && c.familySessionKey != "" {
		return c.familySessionKey, c.familySessionSecret
	}
	return c.sessionKey, c.sessionSecret
}

// getAccessToken 线程安全地读取 access_token
func (c *Client) getAccessToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.accessToken
}

// getSessionSecret 线程安全地读取 session_secret
func (c *Client) getSessionSecret() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessionSecret
}

// hasFamilySession 判断是否已配置家庭云会话
func (c *Client) hasFamilySession() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.familySessionKey != ""
}

// snapshot 线程安全地读取全部会话字段
func (c *Client) snapshot() (sessionKey, sessionSecret, accessToken, refreshToken, familySessionKey, familySessionSecret string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessionKey, c.sessionSecret, c.accessToken, c.refreshToken, c.familySessionKey, c.familySessionSecret
}

// setSession 更新会话密钥（保留 accessToken/refreshToken 不变）
func (c *Client) setSession(sessionKey, sessionSecret, familySessionKey, familySessionSecret string) {
	c.mu.Lock()
	c.sessionKey = sessionKey
	c.sessionSecret = sessionSecret
	if familySessionKey != "" {
		c.familySessionKey = familySessionKey
		c.familySessionSecret = familySessionSecret
	}
	c.mu.Unlock()
}

// NewClient 创建 HTTP 客户端
func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		},
	}
}

// SetAuthRefreshCallback 设置 401 时的自动刷新回调
func (c *Client) SetAuthRefreshCallback(cb func() error) {
	c.onAuthFailed = cb
}

// SetSession 设置会话信息
func (c *Client) SetSession(sessionKey, sessionSecret, accessToken, refreshToken string) {
	c.mu.Lock()
	c.sessionKey = sessionKey
	c.sessionSecret = sessionSecret
	c.accessToken = accessToken
	c.refreshToken = refreshToken
	c.mu.Unlock()
}

// clientSuffix 返回所有请求都需要携带的公共参数
func clientSuffix() url.Values {
	v := url.Values{}
	v.Set("clientType", PC)
	v.Set("version", VERSION)
	v.Set("channelId", CHANNEL_ID)
	// 随机种子
	rand1, _ := rand.Int(rand.Reader, big.NewInt(1e5))
	rand2, _ := rand.Int(rand.Reader, big.NewInt(1e10))
	v.Set("rand", fmt.Sprintf("%d_%d", rand1.Int64(), rand2.Int64()))
	return v
}

// signatureHeader 生成签名请求头（family 为 true 时使用家庭云 session）
func (c *Client) signatureHeader(method, fullUrl, param string) map[string]string {
	return c.signatureHeaderEx(method, fullUrl, param, false)
}

// signatureHeaderEx 生成签名请求头，family 参数控制使用哪个 session
func (c *Client) signatureHeaderEx(method, fullUrl, param string, family bool) map[string]string {
	dateOfGmt := getHttpDateStr()
	sk, ss := c.sessionFor(family)
	return map[string]string{
		"Date":         dateOfGmt,
		"SessionKey":   sk,
		"X-Request-ID": randomUUID(),
		"Signature":    signatureOfHmac(ss, sk, method, fullUrl, dateOfGmt, param),
		"Accept":       "application/json;charset=UTF-8",
		"Referer":      WEB_URL,
	}
}

// encryptParams 加密 params 并返回可用于 URL 的参数字符串
func (c *Client) encryptParams(params Params) string {
	if len(params) == 0 {
		return ""
	}
	return AesECBEncrypt(params.Encode(), c.getSessionSecret()[:16])
}

// apiRequest 发送签名后的 API 请求（params 作为明文查询参数，非加密）
func (c *Client) apiRequest(method, fullUrl string, params Params, result interface{}) ([]byte, error) {
	return c.apiRequestEx(method, fullUrl, params, result, false)
}

// apiRequestEx 可指定 family 参数的签名请求
func (c *Client) apiRequestEx(method, fullUrl string, params Params, result interface{}, family bool) ([]byte, error) {
	return c.apiRequestExWithRetry(method, fullUrl, params, result, family, true)
}

// apiRequestExWithRetry 内部实现，allowRetry 控制是否允许 401 自动重试（防止无限递归）
func (c *Client) apiRequestExWithRetry(method, fullUrl string, params Params, result interface{}, family bool, allowRetry bool) ([]byte, error) {
	// 构建查询参数：公共参数 + 业务参数（明文，不加密）
	queryParams := clientSuffix()
	for k, v := range params {
		queryParams.Set(k, v)
	}

	reqUrl := fullUrl + "?" + queryParams.Encode()

	req, err := http.NewRequest(method, reqUrl, nil)
	if err != nil {
		return nil, err
	}

	// 签名头
	for k, v := range c.signatureHeaderEx(method, fullUrl, "", family) {
		req.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// 检查是否需要刷新会话 → 自动刷新并重试一次（仅一次）
	if allowRetry && (strings.Contains(string(body), "userSessionBO is null") ||
		strings.Contains(string(body), "InvalidSessionKey") ||
		strings.Contains(string(body), "FamilySessionKey") ||
		strings.Contains(string(body), "familySession")) {
		if c.onAuthFailed != nil {
			slog.Info("天翼云盘 session 过期，自动刷新", "family", family, "body_preview", string(body)[:min(len(body), 200)])
			if err := c.onAuthFailed(); err == nil {
				// 刷新成功，重试请求（仅一次，不再递归）
				return c.apiRequestExWithRetry(method, fullUrl, params, result, family, false)
			}
		}
		return nil, &sessionExpiredError{msg: "session expired"}
	}

	// 检查通用错误
	var erron RespErr
	json.Unmarshal(body, &erron)
	if erron.HasError() {
		return nil, &erron
	}

	if result != nil {
		if err := json.Unmarshal(body, result); err != nil {
			return nil, fmt.Errorf("parse response: %w, body: %s", err, string(body)[:min(len(body), 200)])
		}
	}

	return body, nil
}

// apiGet GET 请求（无 params，无签名）
func (c *Client) apiGetRaw(fullUrl string, queryParams url.Values, result interface{}) ([]byte, error) {
	reqUrl := fullUrl
	if len(queryParams) > 0 {
		reqUrl = fullUrl + "?" + queryParams.Encode()
	}

	req, err := http.NewRequest("GET", reqUrl, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json;charset=UTF-8")
	req.Header.Set("Referer", WEB_URL)

	resp, err := c.httpClient.Do(req)
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

// apiPostForm POST 请求（表单格式，无签名）
func (c *Client) apiPostForm(fullUrl string, formData map[string]string, headers map[string]string, result interface{}) ([]byte, error) {
	form := url.Values{}
	for k, v := range formData {
		form.Set(k, v)
	}

	req, err := http.NewRequest("POST", fullUrl, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json;charset=UTF-8")
	req.Header.Set("Referer", WEB_URL)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(req)
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

// sessionExpiredError 会话过期错误（用于触发刷新）
type sessionExpiredError struct {
	msg string
}

func (e *sessionExpiredError) Error() string {
	return e.msg
}

func isSessionExpired(err error) bool {
	_, ok := err.(*sessionExpiredError)
	return ok
}

// ============================================================
// 无重定向客户端（用于获取下载链接的 302 重定向）
// ============================================================

// NoRedirectClient HTTP 客户端（不跟随重定向，用于获取下载链接）
var NoRedirectClient = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// randomUUID 生成随机 UUID v4 格式字符串
func randomUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
