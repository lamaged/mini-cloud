package mobile

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mini-cloud/internal/driver"
)

// makeAuth 构造测试用 authorization：Base64("RCS:账号:tokenPart")，tokenPart 第 4 段为过期毫秒
func makeAuth(account string, expiry time.Time) string {
	tokenPart := fmt.Sprintf("a|b|c|%d", expiry.UnixMilli())
	return base64.StdEncoding.EncodeToString([]byte("RCS:" + account + ":" + tokenPart))
}

func TestValidateAuthorizationFormat(t *testing.T) {
	d := &Driver{}

	tests := []struct {
		name string
		auth string
	}{
		{"空值", ""},
		{"非 Base64", "not-valid-base64!!!"},
		{"格式过短", base64.StdEncoding.EncodeToString([]byte("only:two"))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := d.validateAuthorization(context.Background(), tc.auth, "http://127.0.0.1:1")
			if err == nil {
				t.Errorf("validateAuthorization(%q) should fail", tc.auth)
			}
		})
	}
}

func TestValidateAuthorizationExpired(t *testing.T) {
	d := &Driver{}
	auth := makeAuth("13800000000", time.Now().Add(-time.Hour))
	_, _, _, err := d.validateAuthorization(context.Background(), auth, "http://127.0.0.1:1")
	if err == nil || !strings.Contains(err.Error(), "已过期") {
		t.Errorf("expired auth should be rejected, got err=%v", err)
	}
}

func TestVerifyAuthSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/file/list" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"success":true,"code":"","message":"","data":{"items":[],"nextPageCursor":""}}`)
	}))
	defer ts.Close()

	c := NewClient()
	c.SetAuth(makeAuth("13800000000", time.Now().Add(24*time.Hour)), "13800000000", ts.URL)
	if err := verifyAuth(context.Background(), c); err != nil {
		t.Fatalf("verifyAuth should succeed: %v", err)
	}
}

func TestVerifyAuthRejectsInvalidCredential(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"success":false,"code":"HMEC0001","message":"unauthorized"}`)
	}))
	defer ts.Close()

	c := NewClient()
	c.SetAuth(makeAuth("13800000000", time.Now().Add(24*time.Hour)), "13800000000", ts.URL)
	if err := verifyAuth(context.Background(), c); err == nil {
		t.Fatal("verifyAuth should reject invalid credential")
	}
}

func TestCommitCredentialAppliesAndPersists(t *testing.T) {
	stateDir := t.TempDir()
	// 不调用 Init（其内部有真实网络请求）；New 即构造空凭证客户端
	d := New("移动云盘", makeAuth("13800000000", time.Now().Add(-time.Hour)), stateDir)

	newAuth := makeAuth("13800000000", time.Now().Add(30*24*time.Hour))
	newHost := "https://cloud.example.com"

	// 未经验证直接提交 → 报错
	if err := d.CommitCredential(map[string]string{"__req": "nope", "authorization": newAuth}); err == nil {
		t.Fatal("CommitCredential without pending should fail")
	}

	// 模拟验证通过后的暂存（按 __req 关联）
	d.setPending("req-1", newAuth, "13800000000", newHost)

	if err := d.CommitCredential(map[string]string{"__req": "req-1", "authorization": newAuth}); err != nil {
		t.Fatalf("CommitCredential: %v", err)
	}
	if got := d.client.getAuthorization(); got != newAuth {
		t.Errorf("client auth not applied: got %q, want %q", got, newAuth)
	}
	if got := d.client.getCloudHost(); got != newHost {
		t.Errorf("client host not applied: got %q, want %q", got, newHost)
	}

	// token state 已落盘，重启可恢复
	token, err := LoadToken(d.name, stateDir)
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if token.Authorization != newAuth {
		t.Errorf("persisted auth = %q, want %q", token.Authorization, newAuth)
	}
	if token.CloudHost != newHost {
		t.Errorf("persisted host = %q, want %q", token.CloudHost, newHost)
	}
	// 持久化的过期时间应取自新凭证真实过期时间（毫秒→秒）
	if _, _, _, expiryMs, err := parseAuthString(newAuth); err == nil {
		if token.ExpiresAt != expiryMs/1000 {
			t.Errorf("persisted ExpiresAt = %d, want %d", token.ExpiresAt, expiryMs/1000)
		}
	}

	// 二次提交（pending 已消费）→ 报错
	if err := d.CommitCredential(map[string]string{"__req": "req-1", "authorization": newAuth}); err == nil {
		t.Fatal("second CommitCredential should fail")
	}

	// Health 事实应消失（新凭证未过期）
	if h := d.Health(); h.AuthExpired || h.Warning != "" {
		t.Errorf("Health should be clean after commit, got %+v", h)
	}
}

func TestCommitCredentialStateWriteFailure(t *testing.T) {
	stateDir := t.TempDir()
	d := New("移动云盘", makeAuth("13800000000", time.Now().Add(-time.Hour)), stateDir)

	newAuth := makeAuth("13800000000", time.Now().Add(30*24*time.Hour))
	d.setPending("req-1", newAuth, "13800000000", "https://cloud.example.com")

	// 让 state 目录不可写：先删目录再放一个同名文件占位
	if err := os.RemoveAll(stateDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(stateDir, "移动云盘_token.json")
	if err := os.MkdirAll(blocker, 0755); err != nil { // 用目录占位使写文件失败
		t.Fatal(err)
	}

	err := d.CommitCredential(map[string]string{"__req": "req-1", "authorization": newAuth})
	if err == nil || !errors.Is(err, driver.ErrStateWriteFailed) {
		t.Fatalf("expected ErrStateWriteFailed, got %v", err)
	}
	// 凭证本身必须已生效
	if got := d.client.getAuthorization(); got != newAuth {
		t.Errorf("credential should still be applied, got %q", got)
	}
}

func TestPendingIsolationByReq(t *testing.T) {
	stateDir := t.TempDir()
	d := New("移动云盘", makeAuth("13800000000", time.Now().Add(-time.Hour)), stateDir)

	authA := makeAuth("13800000001", time.Now().Add(30*24*time.Hour))
	authB := makeAuth("13800000002", time.Now().Add(30*24*time.Hour))
	d.setPending("req-A", authA, "13800000001", "https://host-a.example.com")
	d.setPending("req-B", authB, "13800000002", "https://host-b.example.com")

	// 提交 A 只生效 A
	if err := d.CommitCredential(map[string]string{"__req": "req-A", "authorization": authA}); err != nil {
		t.Fatalf("commit A: %v", err)
	}
	if got := d.client.getAuthorization(); got != authA {
		t.Errorf("after commit A: auth = %q, want %q", got, authA)
	}
	if got := d.client.getCloudHost(); got != "https://host-a.example.com" {
		t.Errorf("after commit A: host = %q", got)
	}

	// 提交 B 只生效 B
	if err := d.CommitCredential(map[string]string{"__req": "req-B", "authorization": authB}); err != nil {
		t.Fatalf("commit B: %v", err)
	}
	if got := d.client.getAuthorization(); got != authB {
		t.Errorf("after commit B: auth = %q, want %q", got, authB)
	}
}

func TestValidateAuthorizationTooShort(t *testing.T) {
	// P7：剩余不足 24 小时拒绝接收（防止刚更新完就过期）
	d := &Driver{}
	auth := makeAuth("13800000000", time.Now().Add(23*time.Hour))
	_, _, _, err := d.validateAuthorization(context.Background(), auth, "http://127.0.0.1:1")
	if err == nil || !strings.Contains(err.Error(), "不足 24 小时") {
		t.Errorf("short-lived auth should be rejected, got err=%v", err)
	}
}

// TestAuthRefreshRaceWithCommit I5：401 刷新进行中发生提交，最终必须使用已提交凭证
func TestAuthRefreshRaceWithCommit(t *testing.T) {
	stateDir := t.TempDir()
	oldAuth := makeAuth("13800000000", time.Now().Add(30*24*time.Hour)) // 旧凭证（未过期但被替换）
	d := New("移动云盘", oldAuth, stateDir)
	d.client.SetAuth(oldAuth, "13800000000", "https://cloud-old.example.com")

	// 可控刷新端点：阻塞到测试放行，再返回一个「新」token
	release := make(chan struct{})
	refreshSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<root><return>0</return><token>a|b|c|9999999999999</token></root>`)
	}))
	defer refreshSrv.Close()
	d.client.refreshURL = refreshSrv.URL

	// 刷新开始（内部会阻塞在 refreshSrv）
	refreshDone := make(chan error, 1)
	go func() { refreshDone <- d.handleAuthFailed() }()

	// 刷新阻塞期间提交新凭证
	newAuth := makeAuth("13800000001", time.Now().Add(30*24*time.Hour))
	d.setPending("req-1", newAuth, "13800000001", "https://cloud-new.example.com")
	if err := d.CommitCredential(map[string]string{"__req": "req-1", "authorization": newAuth}); err != nil {
		t.Fatalf("CommitCredential: %v", err)
	}
	if d.credGen.Load() != 1 {
		t.Fatalf("credGen = %d, want 1", d.credGen.Load())
	}

	// 放行刷新
	close(release)
	if err := <-refreshDone; err != nil {
		t.Fatalf("handleAuthFailed: %v", err)
	}

	// 最终：内存与 token state 都必须是已提交凭证（刷新结果被丢弃）
	if got := d.client.getAuthorization(); got != newAuth {
		t.Errorf("client auth = %q, want committed %q", got, newAuth)
	}
	if got := d.client.getCloudHost(); got != "https://cloud-new.example.com" {
		t.Errorf("client host = %q, want committed host", got)
	}
	token, err := LoadToken(d.name, stateDir)
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if token.Authorization != newAuth {
		t.Errorf("token state auth = %q, want committed %q", token.Authorization, newAuth)
	}
}

func TestValidateAuthorizationWithMockHost(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"success":true,"code":"","message":"","data":{"items":[],"nextPageCursor":""}}`)
	}))
	defer ts.Close()

	d := &Driver{}
	auth := makeAuth("13800000000", time.Now().Add(30*24*time.Hour))
	account, host, expiry, err := d.validateAuthorization(context.Background(), auth, ts.URL)
	if err != nil {
		t.Fatalf("validateAuthorization: %v", err)
	}
	if account != "13800000000" {
		t.Errorf("account = %q", account)
	}
	if host != ts.URL {
		t.Errorf("host = %q, want %q", host, ts.URL)
	}
	if !expiry.After(time.Now()) {
		t.Errorf("expiry should be in the future")
	}
}


// ============ §5.3 启动健壮性 ============

// TestInitDegradedNoFatal：无效凭证 + 无缓存 → Init 不失败不阻塞
//（受限模式 + 健康提示；过期语义：List 空、Link 明确错误、不 panic）
func TestInitDegradedNoFatal(t *testing.T) {
	dir := t.TempDir()
	d := New("mobile", "!!not-base64!!", dir)
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init 不得因云主机发现失败而报错（§5.3）: %v", err)
	}
	if h := d.Health(); h.Warning == "" {
		t.Error("受限模式应有健康提示")
	}
	if objs, err := d.List(context.Background(), "/"); err != nil || len(objs) != 0 {
		t.Errorf("过期语义 List 应返回空: %v %+v", err, objs)
	}
	if _, err := d.Link(context.Background(), "/a.mkv"); err == nil {
		t.Error("受限模式 Link 应返回明确错误")
	}
}

// TestInitRestoreFromCache：state 缓存可解析即恢复（30 天有效 token + 缓存 host），
// 全程无网络请求（remaining > 15 天 → 跳过刷新；host 取自缓存 → 跳过发现）
func TestInitRestoreFromCache(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"success":true,"code":"","message":"","data":{"items":[],"nextPageCursor":""}}`)
	}))
	defer ts.Close()
	dir := t.TempDir()
	auth := makeAuth("13800000000", time.Now().Add(30*24*time.Hour))
	tok := &TokenState{Authorization: auth, Account: "13800000000", CloudHost: ts.URL}
	if err := SaveToken("mobile", dir, tok); err != nil {
		t.Fatal(err)
	}
	d := New("mobile", auth, dir)
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if h := d.Health(); h.Warning != "" {
		t.Errorf("有效缓存不应有提示，got %q", h.Warning)
	}
}

// TestHostWarnClearedOnCommit：提交新凭证后清除启动期发现失败提示
func TestHostWarnClearedOnCommit(t *testing.T) {
	stateDir := t.TempDir()
	d := New("移动云盘", makeAuth("13800000000", time.Now().Add(30*24*time.Hour)), stateDir)
	d.commitMu.Lock()
	d.hostWarn = "移动云盘云主机发现失败（网络或授权问题），服务受限"
	d.commitMu.Unlock()

	newAuth := makeAuth("13800000000", time.Now().Add(30*24*time.Hour))
	newHost := "https://cloud.example.com"
	d.setPending("req-1", newAuth, "13800000000", newHost)
	if err := d.CommitCredential(map[string]string{"__req": "req-1", "authorization": newAuth}); err != nil {
		t.Fatalf("CommitCredential: %v", err)
	}
	if h := d.Health(); h.Warning != "" {
		t.Errorf("提交成功后应清除发现失败提示，got %q", h.Warning)
	}
}

// TestNoHostDegradedList P4（RV-2026-11-19-04）：token 有效但云主机不可用
//（启动期发现失败）→ List 空目录、Link 明确错误（与过期场景对齐，不 500）
func TestNoHostDegradedList(t *testing.T) {
	d := New("移动云盘", makeAuth("13800000000", time.Now().Add(30*24*time.Hour)), t.TempDir())
	// 不调用 Init（避免真实网络）；host 为空即模拟「发现失败后受限启动」状态
	if objs, err := d.List(context.Background(), "/"); err != nil || len(objs) != 0 {
		t.Errorf("host 不可用时 List 应返回空目录: %v %+v", err, objs)
	}
	if _, err := d.Link(context.Background(), "/a.mkv"); err == nil {
		t.Error("host 不可用时 Link 应返回明确错误")
	}
}

// TestCredentialFieldsDynamic PM 拍板 2026-11-19：标签按状态动态变化
func TestCredentialFieldsDynamic(t *testing.T) {
	// 已过期 → 获取指引
	expiredAuth := makeAuth("13800000000", time.Now().Add(-time.Hour))
	d := New("移动云盘", expiredAuth, t.TempDir())
	d.client.SetAuth(expiredAuth, "13800000000", "")
	f := d.CredentialFields()[0]
	if f.Key != "authorization" || !strings.Contains(f.Label, "已过期") || !strings.Contains(f.Label, "开发者工具") {
		t.Errorf("过期标签不符: %+v", f)
	}
	// 30 天 → 剩余天数
	auth30 := makeAuth("13800000000", time.Now().Add(30*24*time.Hour))
	d2 := New("移动云盘", auth30, t.TempDir())
	d2.client.SetAuth(auth30, "13800000000", "")
	f2 := d2.CredentialFields()[0]
	if !strings.Contains(f2.Label, "有效期还有 30 天") {
		t.Errorf("30 天标签不符: %q", f2.Label)
	}
	// 12 小时 → 不足 1 天
	auth12h := makeAuth("13800000000", time.Now().Add(12*time.Hour))
	d3 := New("移动云盘", auth12h, t.TempDir())
	d3.client.SetAuth(auth12h, "13800000000", "")
	f3 := d3.CredentialFields()[0]
	if !strings.Contains(f3.Label, "不足 1 天") {
		t.Errorf("12 小时标签不符: %q", f3.Label)
	}
	// 过期事实（Health）：AuthExpired 标记 + 文案
	h := d.Health()
	if !h.AuthExpired || h.Warning != "令牌已过期，请更新令牌" {
		t.Errorf("过期事实不符: %+v", h)
	}
}

// TestRemainingDaysCeil 天数向上取整
func TestRemainingDaysCeil(t *testing.T) {
	a25h := makeAuth("13800000000", time.Now().Add(25*time.Hour))
	d := New("移动云盘", a25h, t.TempDir())
	d.client.SetAuth(a25h, "13800000000", "")
	if got := d.remainingDays(); got != 2 {
		t.Errorf("25 小时应向上取整为 2 天，got %d", got)
	}
	a23h := makeAuth("13800000000", time.Now().Add(23*time.Hour))
	d2 := New("移动云盘", a23h, t.TempDir())
	d2.client.SetAuth(a23h, "13800000000", "")
	if got := d2.remainingDays(); got != 1 {
		t.Errorf("23 小时应为 1 天，got %d", got)
	}
	d3 := New("移动云盘", "!!invalid!!", t.TempDir())
	if got := d3.remainingDays(); got != -1 {
		t.Errorf("解析失败应为 -1，got %d", got)
	}
}
