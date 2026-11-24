package baidu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"mini-cloud/internal/driver"
)

// newFakeTokenServer 返回模拟百度 OAuth token 端点的 httptest 服务器。
// 每次调用可切换行为：rotate 控制响应是否轮换 refresh_token；fail 控制是否返回错误。
func newFakeTokenServer(t *testing.T, rotate, fail bool, onReq func(r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if onReq != nil {
			onReq(r)
		}
		w.Header().Set("Content-Type", "application/json")
		if fail {
			json.NewEncoder(w).Encode(map[string]string{"error": "expired_token", "error_description": "refresh token has been used"})
			return
		}
		resp := map[string]interface{}{"access_token": "at-fresh", "expires_in": 2592000}
		if rotate {
			resp["refresh_token"] = "rt-rotated"
		}
		json.NewEncoder(w).Encode(resp)
	}))
}

func newTestDriver(stateDir string) *Driver {
	d := New("百度", "rt-input", "cid-test", "cs-test", stateDir)
	d.client.tokenURL = "unset" // 测试必须显式指向假服务器
	return d
}

// TestValidateCredentialNoSideEffect 无副作用验证契约（审核 RV-26-24-02 阻断项）：
// 验证成功/失败均不得改变客户端内存 access token。
func TestValidateCredentialNoSideEffect(t *testing.T) {
	d := newTestDriver(t.TempDir())
	d.client.SetAccessToken("at-old")

	ts := newFakeTokenServer(t, true, false, nil)
	defer ts.Close()
	d.client.tokenURL = ts.URL

	values := map[string]string{"__req": "req-1", "refresh_token": "rt-input"}
	if err := d.ValidateCredential(context.Background(), values); err != nil {
		t.Fatalf("ValidateCredential: %v", err)
	}
	if got := d.client.getAccessToken(); got != "at-old" {
		t.Errorf("验证成功不得改变内存 access token：got %q, want at-old", got)
	}
	if got := d.getRefreshToken(); got != "rt-input" {
		t.Errorf("验证不得改变内存 refresh_token：got %q", got)
	}

	// 验证失败也不得改变内存
	tsFail := newFakeTokenServer(t, false, true, nil)
	defer tsFail.Close()
	d.client.tokenURL = tsFail.URL
	if err := d.ValidateCredential(context.Background(), map[string]string{"__req": "req-2", "refresh_token": "rt-bad"}); err == nil {
		t.Fatal("验证失败应报错")
	}
	if got := d.client.getAccessToken(); got != "at-old" {
		t.Errorf("验证失败不得改变内存 access token：got %q", got)
	}
}

// TestValidateCredentialReservedKeys 保留键契约（§6.2）：
// __refresh_token = 验证后的最终值（上游轮换过则用新值，不丢弃）；__expiry 回填；
// 验证失败不得留下任何残余值。
func TestValidateCredentialReservedKeys(t *testing.T) {
	d := newTestDriver(t.TempDir())
	ts := newFakeTokenServer(t, true, false, nil)
	defer ts.Close()
	d.client.tokenURL = ts.URL

	values := map[string]string{"__req": "req-1", "refresh_token": "rt-input"}
	if err := d.ValidateCredential(context.Background(), values); err != nil {
		t.Fatalf("ValidateCredential: %v", err)
	}
	if values["__refresh_token"] != "rt-rotated" {
		t.Errorf("__refresh_token = %q, want rt-rotated（轮换值不得丢弃）", values["__refresh_token"])
	}
	if values["__expiry"] == "" {
		t.Error("__expiry 应回填")
	}

	// 失败路径：不得写入保留键
	tsFail := newFakeTokenServer(t, false, true, nil)
	defer tsFail.Close()
	d.client.tokenURL = tsFail.URL
	bad := map[string]string{"__req": "req-2", "refresh_token": "rt-bad"}
	if err := d.ValidateCredential(context.Background(), bad); err == nil {
		t.Fatal("验证失败应报错")
	}
	if _, ok := bad["__refresh_token"]; ok {
		t.Error("验证失败不得留下 __refresh_token 残余值")
	}
}

// TestValidateMissingAppCredentials 应用凭据未配置 → 明确报错（§6.1）
func TestValidateMissingAppCredentials(t *testing.T) {
	d := New("百度", "rt", "", "", t.TempDir())
	err := d.ValidateCredential(context.Background(), map[string]string{"__req": "r", "refresh_token": "rt"})
	if err == nil || !strings.Contains(err.Error(), "client_id") {
		t.Errorf("缺少应用凭据应报明确错误，got %v", err)
	}
}

// TestCommitCredentialAppliesAndRotates 提交使用验证后的最终值（含轮换）；
// pending 一次性消费，二次提交失败。
func TestCommitCredentialAppliesAndRotates(t *testing.T) {
	dir := t.TempDir()
	d := newTestDriver(dir)
	ts := newFakeTokenServer(t, true, false, nil)
	defer ts.Close()
	d.client.tokenURL = ts.URL

	values := map[string]string{"__req": "req-1", "refresh_token": "rt-input"}
	if err := d.ValidateCredential(context.Background(), values); err != nil {
		t.Fatalf("ValidateCredential: %v", err)
	}
	if err := d.CommitCredential(values); err != nil {
		t.Fatalf("CommitCredential: %v", err)
	}

	if got := d.getRefreshToken(); got != "rt-rotated" {
		t.Errorf("内存 refresh_token = %q, want rt-rotated", got)
	}
	if got := d.client.getAccessToken(); got != "at-fresh" {
		t.Errorf("内存 access_token = %q, want at-fresh", got)
	}
	tok, err := LoadToken("百度", dir)
	if err != nil {
		t.Fatalf("state 应已落盘: %v", err)
	}
	if tok.RefreshToken != "rt-rotated" || tok.AccessToken != "at-fresh" {
		t.Errorf("state 内容不符: %+v", tok)
	}

	if err := d.CommitCredential(values); err == nil {
		t.Fatal("二次提交应失败（pending 已消费）")
	}
}

// TestCommitStateWriteFailure ErrStateWriteFailed 语义（审核 RV-26-24-04）：
// state 写失败时凭据仍生效、返回哨兵错误；state 残留被清理。
func TestCommitStateWriteFailure(t *testing.T) {
	dir := t.TempDir()
	d := newTestDriver(dir)
	ts := newFakeTokenServer(t, false, false, nil)
	defer ts.Close()
	d.client.tokenURL = ts.URL

	values := map[string]string{"__req": "req-1", "refresh_token": "rt-input"}
	if err := d.ValidateCredential(context.Background(), values); err != nil {
		t.Fatalf("ValidateCredential: %v", err)
	}

	// state 文件位置用目录占位 → SaveToken 写文件必然失败（root 环境下权限位不可靠）
	blocker := dir + "/百度_token.json"
	if err := os.MkdirAll(blocker, 0755); err != nil {
		t.Fatal(err)
	}

	err := d.CommitCredential(values)
	if err == nil || !strings.Contains(err.Error(), driver.ErrStateWriteFailed.Error()) {
		t.Fatalf("CommitCredential = %v, want ErrStateWriteFailed", err)
	}
	if got := d.getRefreshToken(); got != "rt-input" {
		t.Errorf("state 写失败时凭据仍应生效：refresh_token = %q", got)
	}
}

// TestInitMissingAppCredentialsAndAuth 初始化前置校验（§6.1）：应用凭据必填、未授权明确提示。
func TestInitMissingAppCredentialsAndAuth(t *testing.T) {
	d1 := New("百度", "rt", "", "", t.TempDir())
	if err := d1.Init(context.Background()); err == nil || !strings.Contains(err.Error(), "client_id") {
		t.Errorf("缺少应用凭据应报明确错误，got %v", err)
	}
	d2 := New("百度", "", "cid", "cs", t.TempDir())
	if err := d2.Init(context.Background()); err == nil || !strings.Contains(err.Error(), "尚未授权") {
		t.Errorf("缺少 refresh_token 应提示尚未授权，got %v", err)
	}
}

// TestInitConfigWinsOverStaleCache 防旧缓存覆盖新授权（D2 重启恢复）：
// state 中 refresh_token 与 config 不一致时不得复用缓存会话，应以 config 为准重新交换。
func TestInitConfigWinsOverStaleCache(t *testing.T) {
	dir := t.TempDir()
	// 预置旧缓存（不同 refresh_token + 看似有效的 access_token）
	if err := SaveToken("百度", dir, &TokenState{RefreshToken: "rt-old", AccessToken: "at-old", ExpiresAt: time.Now().Unix() + 3600}); err != nil {
		t.Fatal(err)
	}

	d := New("百度", "rt-config", "cid", "cs", dir)
	ts := newFakeTokenServer(t, false, false, nil)
	defer ts.Close()
	d.client.tokenURL = ts.URL

	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// 缓存 rt-old 与 config rt-config 不一致 → 不应复用缓存 at-old，应重新交换得 at-fresh
	if got := d.client.getAccessToken(); got != "at-fresh" {
		t.Errorf("旧缓存不得覆盖新授权：access_token = %q, want at-fresh", got)
	}
	if got := d.getRefreshToken(); got != "rt-config" {
		t.Errorf("refresh_token = %q, want rt-config（config 为唯一真值源）", got)
	}
}

// TestExchangeTokenErrorRedacted P1（代码审核）：网络错误文本不得泄露凭据——
// Go 的 url.Error 携带完整请求 URL（含 refresh_token/client_secret，且经 URL 编码），
// exchangeToken 必须把凭据值（原文与编码形式）替换后再对外返回。
func TestExchangeTokenErrorRedacted(t *testing.T) {
	secret := "rt-abc/def+g==" // 含 URL 需编码的特殊字符，验证编码形式同样被替换
	clientSecret := "cs-xyz/789=="
	d := New("百度", secret, "cid-public", clientSecret, t.TempDir())
	ts := newFakeTokenServer(t, false, false, nil)
	ts.Close() // 已关闭的服务器 → 连接被拒，走网络错误路径
	d.client.tokenURL = ts.URL

	err := d.ValidateCredential(context.Background(), map[string]string{"__req": "r", "refresh_token": secret})
	if err == nil {
		t.Fatal("连接失败应报错")
	}
	msg := err.Error()
	for _, s := range []string{secret, clientSecret, url.QueryEscape(secret), url.QueryEscape(clientSecret)} {
		if strings.Contains(msg, s) {
			t.Errorf("错误文本泄露凭据 %q：%s", s, msg)
		}
	}

	// Init 路径同样走 exchangeToken，脱敏必须一致
	if err := d.Init(context.Background()); err == nil {
		t.Fatal("Init 应报错")
	} else if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), url.QueryEscape(secret)) {
		t.Errorf("Init 错误泄露凭据: %s", err.Error())
	}
}

// TestAPIErrorRedacted 安全审核 RV-2026-11-24-06：
// apiGet 网络错误文本（url.Error 携带含 access_token 的完整 URL）必须脱敏。
func TestAPIErrorRedacted(t *testing.T) {
	d := newTestDriver(t.TempDir())
	d.client.SetAccessToken("at-sec/ret+1==")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	ts.Close() // 立即关闭 → 连接被拒
	d.client.baseURL = ts.URL

	_, err := d.List(context.Background(), "/")
	if err == nil {
		t.Fatal("连接失败应报错")
	}
	msg := err.Error()
	for _, s := range []string{"at-sec/ret+1==", url.QueryEscape("at-sec/ret+1==")} {
		if strings.Contains(msg, s) {
			t.Errorf("API 错误泄露 access_token %q：%s", s, msg)
		}
	}
}
