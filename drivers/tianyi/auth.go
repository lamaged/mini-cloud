package tianyi

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// ============================================================
// 登录流程
// ============================================================

// login 完整登录流程（使用用户名+密码）
func (c *Client) login(username, password string) error {
	slog.Info("开始天翼云盘登录", "username", username)

	// Step 1: 获取登录页面参数
	loginParam, err := c.initLoginParam()
	if err != nil {
		return fmt.Errorf("获取登录参数失败: %w", err)
	}

	// Step 2: 获取 RSA 公钥
	var encryptConf EncryptConfResp
	_, err = c.apiPostForm(AUTH_URL+"/api/logbox/config/encryptConf.do",
		map[string]string{"appId": APP_ID}, nil, &encryptConf)
	if err != nil {
		return fmt.Errorf("获取加密配置失败: %w", err)
	}
	if encryptConf.Result != 0 {
		return fmt.Errorf("获取加密配置失败: result=%d", encryptConf.Result)
	}

	pubKey := fmt.Sprintf("-----BEGIN PUBLIC KEY-----\n%s\n-----END PUBLIC KEY-----", encryptConf.Data.PubKey)
	loginParam.RsaUsername = encryptConf.Data.Pre + RsaEncrypt(pubKey, username)
	loginParam.RsaPassword = encryptConf.Data.Pre + RsaEncrypt(pubKey, password)

	// Step 3: 检查是否需要验证码
	resp, err := c.apiPostForm(AUTH_URL+"/api/logbox/oauth2/needcaptcha.do",
		map[string]string{
			"appKey":      APP_ID,
			"accountType": ACCOUNT_TYPE,
			"userName":    loginParam.RsaUsername,
		},
		map[string]string{"REQID": loginParam.ReqId},
		nil,
	)
	if err != nil {
		return fmt.Errorf("检查验证码失败: %w", err)
	}
	if string(resp) != "0" {
		return fmt.Errorf("需要验证码，请先在浏览器中登录一次天翼云盘，或使用 refresh_token 方式")
	}

	// Step 4: 提交登录
	var loginResp LoginResp
	_, err = c.apiPostForm(AUTH_URL+"/api/logbox/oauth2/loginSubmit.do",
		map[string]string{
			"appKey":       APP_ID,
			"accountType":  ACCOUNT_TYPE,
			"userName":     loginParam.RsaUsername,
			"password":     loginParam.RsaPassword,
			"validateCode": "",
			"captchaToken": loginParam.CaptchaToken,
			"returnUrl":    RETURN_URL,
			"dynamicCheck": "FALSE",
			"clientType":   CLIENT_TYPE,
			"cb_SaveName":  "1",
			"isOauth2":     "false",
			"state":        "",
			"paramId":      loginParam.ParamId,
		},
		map[string]string{
			"REQID": loginParam.ReqId,
			"lt":    loginParam.Lt,
		},
		&loginResp,
	)
	if err != nil {
		return fmt.Errorf("登录提交失败: %w", err)
	}
	if loginResp.ToUrl == "" {
		return fmt.Errorf("登录失败: %s", loginResp.Msg)
	}

	// Step 5: 获取 Session
	var tokenInfo AppSessionResp
	queryParams := clientSuffix()
	queryParams.Set("redirectURL", loginResp.ToUrl)

	reqUrl := API_URL + "/getSessionForPC.action?" + queryParams.Encode()
	req, _ := http.NewRequest("POST", reqUrl, nil)
	req.Header.Set("Accept", "application/json;charset=UTF-8")
	req.Header.Set("Referer", WEB_URL)

	resp2, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("获取会话失败: %w", err)
	}
	defer resp2.Body.Close()

	if err := json.NewDecoder(resp2.Body).Decode(&tokenInfo); err != nil {
		return fmt.Errorf("解析会话响应失败: %w", err)
	}

	if tokenInfo.ResCode != 0 {
		return fmt.Errorf("获取会话失败: %s", tokenInfo.ResMessage)
	}

	c.SetSession(tokenInfo.SessionKey, tokenInfo.SessionSecret, tokenInfo.AccessToken, tokenInfo.RefreshToken)
	if tokenInfo.FamilySessionKey != "" {
		c.SetFamilySession(tokenInfo.FamilySessionKey, tokenInfo.FamilySessionSecret)
	}
	slog.Info("天翼云盘登录成功", "loginName", tokenInfo.LoginName)
	return nil
}

// initLoginParam 获取登录页面参数（lt, reqId, paramId, captchaToken）
func (c *Client) initLoginParam() (*LoginParam, error) {
	queryParams := url.Values{}
	queryParams.Set("appId", APP_ID)
	queryParams.Set("clientType", CLIENT_TYPE)
	queryParams.Set("returnURL", RETURN_URL)
	queryParams.Set("timeStamp", fmt.Sprintf("%d", time.Now().UnixNano()/1e6))

	body, err := c.apiGetRaw(WEB_URL+"/api/portal/unifyLoginForPC.action", queryParams, nil)
	if err != nil {
		return nil, err
	}

	html := string(body)

	// 从 HTML 中提取参数
	ltMatch := regexp.MustCompile(`lt = "(.+?)"`).FindStringSubmatch(html)
	reqIdMatch := regexp.MustCompile(`reqId = "(.+?)"`).FindStringSubmatch(html)
	paramIdMatch := regexp.MustCompile(`paramId = "(.+?)"`).FindStringSubmatch(html)
	captchaMatch := regexp.MustCompile(`'captchaToken' value='(.+?)'`).FindStringSubmatch(html)

	if len(ltMatch) < 2 || len(reqIdMatch) < 2 || len(paramIdMatch) < 2 {
		return nil, fmt.Errorf("无法从登录页面提取参数")
	}

	param := &LoginParam{
		Lt:      ltMatch[1],
		ReqId:   reqIdMatch[1],
		ParamId: paramIdMatch[1],
	}
	if len(captchaMatch) >= 2 {
		param.CaptchaToken = captchaMatch[1]
	}

	return param, nil
}

// ============================================================
// 会话刷新
// ============================================================

// refreshSession 使用 AccessToken 刷新会话
func (c *Client) refreshSession() error {
	slog.Info("刷新天翼云盘会话")

	var tokenInfo AppSessionResp
	var erron RespErr

	queryParams := clientSuffix()
	queryParams.Set("appId", APP_ID)
	queryParams.Set("accessToken", c.accessToken)

	reqUrl := API_URL + "/getSessionForPC.action?" + queryParams.Encode()
	req, _ := http.NewRequest("GET", reqUrl, nil)
	req.Header.Set("Accept", "application/json;charset=UTF-8")
	req.Header.Set("Referer", WEB_URL)
	req.Header.Set("X-Request-ID", "mini-cloud-refresh")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("刷新会话失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("读取刷新响应失败: %w", err)
	}

	json.Unmarshal(body, &tokenInfo)
	json.Unmarshal(body, &erron)

	if erron.HasError() {
		_ = erron // 继续尝试解析 tokenInfo
	}

	if tokenInfo.SessionKey != "" {
		// 更新会话，但保留原有的 AccessToken 和 RefreshToken
		c.sessionKey = tokenInfo.SessionKey
		c.sessionSecret = tokenInfo.SessionSecret
		// 同时更新家庭云会话（如果 API 返回了）
		if tokenInfo.FamilySessionKey != "" {
			c.familySessionKey = tokenInfo.FamilySessionKey
			c.familySessionSecret = tokenInfo.FamilySessionSecret
			slog.Info("会话刷新成功（含家庭云）")
		} else {
			slog.Info("会话刷新成功")
		}
		return nil
	}

	// 如果刷新也失败了，检查错误
	if erron.HasError() {
		if erron.ResCode == "UserInvalidOpenToken" {
			return fmt.Errorf("refresh_token 已失效，需要重新登录")
		}
		return &erron
	}

	return fmt.Errorf("会话刷新返回空数据")
}

// ============================================================
// Token 状态持久化
// ============================================================

// SaveToken 保存 Token 到 state/token.json
func SaveToken(name, stateDir string, token *TokenState) error {
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(stateDir, name+"_token.json")
	return os.WriteFile(path, data, 0600)
}

// LoadToken 从 state/token.json 加载 Token
func LoadToken(name, stateDir string) (*TokenState, error) {
	path := filepath.Join(stateDir, name+"_token.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var token TokenState
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, err
	}
	return &token, nil
}

// NewTokenState 从登录响应创建 Token 状态
func NewTokenState(sessionKey, sessionSecret, accessToken, refreshToken, familyID, familySessionKey, familySessionSecret, loginName string) *TokenState {
	return &TokenState{
		AccessToken:         accessToken,
		RefreshToken:        refreshToken,
		SessionKey:          sessionKey,
		SessionSecret:       sessionSecret,
		ExpiresAt:           time.Now().Unix() + 7200,
		LoginName:           loginName,
		FamilyID:            familyID,
		FamilySessionKey:    familySessionKey,
		FamilySessionSecret: familySessionSecret,
	}
}
