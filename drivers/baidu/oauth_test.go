package baidu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// newOAuthServers 构造一键授权测试双服务器：token 端点（校验 authorization_code 授权）+
// 网盘 API 端点（uinfo 验证）
func newOAuthServers(t *testing.T, withRefresh bool, apiErrno int, onToken func(r *http.Request)) (*httptest.Server, *httptest.Server) {
	t.Helper()
	token := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if onToken != nil {
			onToken(r)
		}
		q := r.URL.Query()
		if q.Get("grant_type") != "authorization_code" {
			json.NewEncoder(w).Encode(map[string]string{"error": "unsupported_grant_type", "error_description": "bad"})
			return
		}
		resp := map[string]interface{}{"access_token": "at-oauth", "expires_in": 2592000}
		if withRefresh {
			resp["refresh_token"] = "rt-oauth"
		}
		json.NewEncoder(w).Encode(resp)
	}))
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]int{"errno": apiErrno})
	}))
	t.Cleanup(func() { token.Close(); api.Close() })
	return token, api
}

// TestOAuthStartURL 批 2（§6.4）：授权 URL 参数完整（response_type/client_id/redirect_uri=oob/scope/state）
func TestOAuthStartURL(t *testing.T) {
	d := newTestDriver(t.TempDir())
	authURL, err := d.OAuthStart()
	if err != nil {
		t.Fatalf("OAuthStart: %v", err)
	}
	u, _ := url.Parse(authURL)
	q := u.Query()
	if u.Host != "openapi.baidu.com" || u.Path != "/oauth/2.0/authorize" {
		t.Errorf("授权地址不符: %s", authURL)
	}
	if q.Get("response_type") != "code" || q.Get("client_id") != "cid-test" || q.Get("redirect_uri") != "oob" {
		t.Errorf("授权参数不符: %v", q)
	}
	if len(q.Get("state")) != 32 {
		t.Errorf("state 应为 32 位随机十六进制: %q", q.Get("state"))
	}
	// 应用凭据缺失 → 明确报错
	d2 := New("百度", "rt", "", "", t.TempDir())
	if _, err := d2.OAuthStart(); err == nil || !strings.Contains(err.Error(), "client_id") {
		t.Errorf("缺应用凭据应报错: %v", err)
	}
}

// TestOAuthExchangeHappyPath 批 2（§6.4）：state 校验 → code 兑换 → uinfo 验证 →
// 回填 __refresh_token/__expiry 并按 __req 暂存 → CommitCredential 生效；state 一次性。
func TestOAuthExchangeHappyPath(t *testing.T) {
	dir := t.TempDir()
	d := newTestDriver(dir)
	tokenTS, apiTS := newOAuthServers(t, true, 0, nil)
	d.client.tokenURL = tokenTS.URL
	d.client.baseURL = apiTS.URL

	authURL, err := d.OAuthStart()
	if err != nil {
		t.Fatalf("OAuthStart: %v", err)
	}
	state := mustQuery(t, authURL).Get("state")

	values := map[string]string{"__req": "req-oauth"}
	if err := d.OAuthExchange(context.Background(), state, "code-1", values); err != nil {
		t.Fatalf("OAuthExchange: %v", err)
	}
	if values["__refresh_token"] != "rt-oauth" || values["__expiry"] == "" {
		t.Errorf("回填不符: %v", values)
	}
	if err := d.CommitCredential(values); err != nil {
		t.Fatalf("CommitCredential: %v", err)
	}
	if got := d.getRefreshToken(); got != "rt-oauth" {
		t.Errorf("内存 refresh_token = %q, want rt-oauth", got)
	}
	// state 一次性：复用失败
	if err := d.OAuthExchange(context.Background(), state, "code-1", map[string]string{"__req": "r2"}); err == nil {
		t.Error("state 复用应失败")
	}
}

// TestOAuthExchangeStateInvalid 未发起的 state / 过期 state 均拒绝
func TestOAuthExchangeStateInvalid(t *testing.T) {
	d := newTestDriver(t.TempDir())
	tokenTS, apiTS := newOAuthServers(t, true, 0, nil)
	d.client.tokenURL = tokenTS.URL
	d.client.baseURL = apiTS.URL

	if err := d.OAuthExchange(context.Background(), "not-exist", "code", map[string]string{"__req": "r"}); err == nil {
		t.Error("未发起的 state 应拒绝")
	}
	// 过期 state（直接注入过期条目）
	d.oauthMu.Lock()
	d.oauthStates["expired-state"] = time.Now().Add(-time.Minute)
	d.oauthMu.Unlock()
	if err := d.OAuthExchange(context.Background(), "expired-state", "code", map[string]string{"__req": "r"}); err == nil {
		t.Error("过期 state 应拒绝")
	}
}

// TestOAuthExchangeNoRefreshToken 响应缺 refresh_token → 报错（不可提交）
func TestOAuthExchangeNoRefreshToken(t *testing.T) {
	d := newTestDriver(t.TempDir())
	tokenTS, apiTS := newOAuthServers(t, false, 0, nil)
	d.client.tokenURL = tokenTS.URL
	d.client.baseURL = apiTS.URL
	authURL, _ := d.OAuthStart()
	state := mustQuery(t, authURL).Get("state")

	if err := d.OAuthExchange(context.Background(), state, "code", map[string]string{"__req": "r"}); err == nil {
		t.Error("响应缺 refresh_token 应报错")
	}
}

// TestOAuthExchangeVerificationFails uinfo 验证失败（errno 非 0）→ 拒绝
func TestOAuthExchangeVerificationFails(t *testing.T) {
	d := newTestDriver(t.TempDir())
	tokenTS, apiTS := newOAuthServers(t, true, 111, nil)
	d.client.tokenURL = tokenTS.URL
	d.client.baseURL = apiTS.URL
	authURL, _ := d.OAuthStart()
	state := mustQuery(t, authURL).Get("state")

	if err := d.OAuthExchange(context.Background(), state, "code", map[string]string{"__req": "r"}); err == nil {
		t.Error("uinfo 验证失败应拒绝")
	}
}

// TestOAuthExchangeErrorRedacted 兑换网络错误文本不得泄露授权码（code 也在 URL 中）
func TestOAuthExchangeErrorRedacted(t *testing.T) {
	d := newTestDriver(t.TempDir())
	tokenTS, _ := newOAuthServers(t, true, 0, nil)
	tokenTS.Close() // 连接被拒
	d.client.tokenURL = tokenTS.URL
	authURL, _ := d.OAuthStart()
	state := mustQuery(t, authURL).Get("state")

	code := "code-secret/value+x"
	err := d.OAuthExchange(context.Background(), state, code, map[string]string{"__req": "r"})
	if err == nil {
		t.Fatal("应报错")
	}
	for _, s := range []string{code, url.QueryEscape(code)} {
		if strings.Contains(err.Error(), s) {
			t.Errorf("错误文本泄露授权码 %q：%s", s, err.Error())
		}
	}
}

func mustQuery(t *testing.T, rawURL string) url.Values {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("URL 解析失败: %v", err)
	}
	return u.Query()
}

// TestInitCacheReuseByGeneration 批 2（§6.5 + 审核 P1-2）：缓存复用条件——
// V=1 且代次匹配 → 复用；代次不符 → 重新兑换（token 端点不请求 = 缓存命中）。
func TestInitCacheReuseByGeneration(t *testing.T) {
	// 代次匹配 → 复用缓存
	dir := t.TempDir()
	if err := SaveToken("百度", dir, &TokenState{V: 1, AccessToken: "at-cached", ExpiresAt: time.Now().Unix() + 3600, CredentialGeneration: 3}); err != nil {
		t.Fatal(err)
	}
	d := New("百度", "rt-config", "cid", "cs", dir)
	apiTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]int{"errno": 0})
	}))
	defer apiTS.Close()
	d.client.baseURL = apiTS.URL
	// token 端点为已关闭服务器：缓存命中时不得被请求（哨兵）
	tokenTS := newFakeTokenServer(t, false, false, nil)
	tokenTS.Close()
	d.client.tokenURL = tokenTS.URL
	d.SetCredentialGeneration(3)

	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got := d.client.getAccessToken(); got != "at-cached" {
		t.Errorf("代次匹配应复用缓存: %q, want at-cached", got)
	}

	// 代次不符 → 重新兑换
	dir2 := t.TempDir()
	if err := SaveToken("百度", dir2, &TokenState{V: 1, AccessToken: "at-stale", ExpiresAt: time.Now().Unix() + 3600, CredentialGeneration: 2}); err != nil {
		t.Fatal(err)
	}
	d2 := New("百度", "rt-config", "cid", "cs", dir2)
	tokenTS2 := newFakeTokenServer(t, false, false, nil)
	defer tokenTS2.Close()
	d2.client.tokenURL = tokenTS2.URL
	d2.SetCredentialGeneration(3)

	if err := d2.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got := d2.client.getAccessToken(); got != "at-fresh" {
		t.Errorf("代次不符应重新兑换: %q, want at-fresh", got)
	}
}

// TestLinkErrorRedacted 审核 I1（RV-2026-11-25-02）：Link 网络错误文本不得含 access_token
// （dlink URL 携带令牌，url.Error 会经 webdav 进日志）。
func TestLinkErrorRedacted(t *testing.T) {
	d := newTestDriver(t.TempDir())
	d.client.SetAccessToken("at-sec/ret+1==")
	dl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dl.Close() // 下载端点已关闭 → Do 网络失败

	apiTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/file"):
			json.NewEncoder(w).Encode(map[string]interface{}{
				"errno": 0,
				"list":  []map[string]interface{}{{"server_filename": "f.mkv", "isdir": 0, "fs_id": 1, "size": 10}},
			})
		case strings.Contains(r.URL.Path, "/multimedia"):
			json.NewEncoder(w).Encode(map[string]interface{}{
				"errno": 0,
				// 真实 dlink 带查询串；Link 会追加 &access_token=（无查询串时 URL 非法）
				"list": []map[string]interface{}{{"dlink": dl.URL + "?x=1"}},
			})
		}
	}))
	defer apiTS.Close()
	d.client.baseURL = apiTS.URL

	_, err := d.Link(context.Background(), "/f.mkv")
	if err == nil {
		t.Fatal("下载端点已关闭，应报错")
	}
	for _, s := range []string{"at-sec/ret+1==", url.QueryEscape("at-sec/ret+1==")} {
		if strings.Contains(err.Error(), s) {
			t.Errorf("Link 错误泄露 access_token %q：%s", s, err.Error())
		}
	}
}
