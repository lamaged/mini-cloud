package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"mini-cloud/internal/driver"
)

// fakeUpdater 记录调用顺序的假凭证更新器
type fakeUpdater struct {
	fields       []driver.CredentialField
	validateErr  error
	commitErr    error
	validated    bool
	committed    bool
	lastValidate map[string]string
	lastCommitVal string // 最后一次提交的凭证值（P12 最终一致性断言用）
	validateHook func()  // 阻塞点（并发/时序测试用）
}

func (f *fakeUpdater) CredentialFields() []driver.CredentialField { return f.fields }
func (f *fakeUpdater) ValidateCredential(_ context.Context, values map[string]string) error {
	f.validated = true
	f.lastValidate = values
	if f.validateHook != nil {
		f.validateHook()
	}
	if f.validateErr != nil {
		return f.validateErr
	}
	values["__expiry"] = "2026-12-16 12:00"
	return nil
}
func (f *fakeUpdater) CommitCredential(values map[string]string) error {
	f.committed = true
	f.lastCommitVal = values["authorization"]
	return f.commitErr
}

func newTestHandler(u *fakeUpdater, persistErr error) (*Handler, *fakeUpdater, *[]string) {
	calls := &[]string{}
	h := New([]Mount{
		{
			Name:      "移动云盘",
			Prefix:    "/移动云盘",
			Available: true,
			Fields:    []driver.CredentialField{{Key: "authorization", Label: "authorization", Kind: "password"}},
			Status:    func() MountStatus { return MountStatus{Label: "正常", Color: "ok"} },
			Update:    u,
		},
	}, func(ctx context.Context, mount string, values map[string]string) (func() error, error) {
		*calls = append(*calls, "persist:"+mount+":"+values["authorization"])
		if persistErr != nil {
			return nil, persistErr
		}
		return func() error { return nil }, nil
	})
	return h, u, calls
}

// fetchCSRF 从管理页 HTML 中提取一次性令牌
func fetchCSRF(t *testing.T, h *Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Page(rec, httptest.NewRequest("GET", "/admin", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("page status = %d", rec.Code)
	}
	idx := strings.Index(rec.Body.String(), `name="csrf" value="`)
	if idx < 0 {
		t.Fatal("csrf token not found in page")
	}
	rest := rec.Body.String()[idx+len(`name="csrf" value="`):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatal("csrf token malformed")
	}
	return rest[:end]
}

func postUpdate(h *Handler, csrf, mount, auth string) *httptest.ResponseRecorder {
	form := url.Values{"csrf": {csrf}, "mount": {mount}, "authorization": {auth}}
	req := httptest.NewRequest("POST", "/admin/update", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	return rec
}

func TestPageRendersAndEscapes(t *testing.T) {
	h := New([]Mount{
		{
			Name:   `移动<script>alert(1)</script>`,
			Prefix: "/x",
			Fields: []driver.CredentialField{{Key: "authorization", Label: `a"b`, Kind: "password"}},
			Status: func() MountStatus { return MountStatus{Label: "警告", Color: "warning", Detail: `过期<script>提示</script>`} },
			Update: &fakeUpdater{},
		},
	}, func(ctx context.Context, mount string, values map[string]string) (func() error, error) { return func() error { return nil }, nil })

	rec := httptest.NewRecorder()
	h.Page(rec, httptest.NewRequest("GET", "/admin", nil))
	body := rec.Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("mount name not HTML-escaped")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("expected escaped script tag in output")
	}
}

func TestUpdateHappyPathOrder(t *testing.T) {
	u := &fakeUpdater{fields: []driver.CredentialField{{Key: "authorization", Kind: "password"}}}
	h, fu, calls := newTestHandler(u, nil)
	csrf := fetchCSRF(t, h)

	rec := postUpdate(h, csrf, "移动云盘", "new-auth-token")
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !fu.validated || !fu.committed {
		t.Errorf("validated=%v committed=%v, want both true", fu.validated, fu.committed)
	}
	if len(*calls) != 1 || !strings.HasPrefix((*calls)[0], "persist:移动云盘") {
		t.Errorf("persist calls = %v", *calls)
	}
	if !strings.Contains(rec.Body.String(), "有效期至 2026-12-16 12:00") {
		t.Errorf("success page should show expiry: %s", rec.Body.String())
	}
}

// TestUpdateSerialized 双标签并发提交必须串行化（I1）
func TestUpdateSerialized(t *testing.T) {
	blocked := make(chan struct{})
	release := make(chan struct{})
	u := &fakeUpdater{
		fields: []driver.CredentialField{{Key: "authorization", Kind: "password"}},
		validateHook: func() {
			select {
			case <-blocked:
			default:
				close(blocked) // 仅第一次验证阻塞
			}
			<-release
		},
	}
	h, _, calls := newTestHandler(u, nil)
	csrfA := fetchCSRF(t, h)
	csrfB := fetchCSRF(t, h)

	doneA := make(chan int, 1)
	doneB := make(chan int, 1)
	go func() { doneA <- postUpdate(h, csrfA, "移动云盘", "auth-A").Code }()
	<-blocked // A 已进入验证（持有更新锁）

	go func() { doneB <- postUpdate(h, csrfB, "移动云盘", "auth-B").Code }()
	select {
	case code := <-doneB:
		t.Fatalf("B 应在 A 完成前被锁阻塞，却先完成（status=%d）", code)
	case <-time.After(100 * time.Millisecond):
		// 预期：B 被锁挡住
	}
	close(release)
	if codeA := <-doneA; codeA != http.StatusOK {
		t.Fatalf("A status = %d", codeA)
	}
	if codeB := <-doneB; codeB != http.StatusOK {
		t.Fatalf("B status = %d", codeB)
	}
	if !u.committed {
		t.Fatal("commit should have run")
	}

	// P12：最终一致性——最后一次写盘的值 == 最后一次生效的值
	if len(*calls) < 2 || (*calls)[len(*calls)-1] != "persist:移动云盘:auth-B" {
		t.Errorf("last persist should be auth-B, calls=%v", *calls)
	}
	if u.lastCommitVal != "auth-B" {
		t.Errorf("last commit = %q, want auth-B（运行时与配置必须一致）", u.lastCommitVal)
	}
}

// TestCommitPlainErrorRollsBack 提交失败时用回滚函数还原配置（I6）
func TestCommitPlainErrorRollsBack(t *testing.T) {
	u := &fakeUpdater{fields: []driver.CredentialField{{Key: "authorization", Kind: "password"}}, commitErr: fmt.Errorf("没有待提交的凭证，请先验证")}
	rolledBack := false
	h := New([]Mount{
		{Name: "移动云盘", Prefix: "/移动云盘", Fields: u.CredentialFields(), Update: u},
	}, func(ctx context.Context, mount string, values map[string]string) (func() error, error) {
		return func() error { rolledBack = true; return nil }, nil
	})
	csrf := fetchCSRF(t, h)

	rec := postUpdate(h, csrf, "移动云盘", "new-auth-token")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !rolledBack {
		t.Error("rollback must be called when commit fails")
	}
	if !strings.Contains(rec.Body.String(), "配置已还原") {
		t.Errorf("expected rollback message, got: %s", rec.Body.String())
	}
}

// TestCommitPlainErrorRollbackFails 回滚失败必须如实告知（I8），不得假装已还原
func TestCommitPlainErrorRollbackFails(t *testing.T) {
	u := &fakeUpdater{fields: []driver.CredentialField{{Key: "authorization", Kind: "password"}}, commitErr: fmt.Errorf("没有待提交的凭证，请先验证")}
	h := New([]Mount{
		{Name: "移动云盘", Prefix: "/移动云盘", Fields: u.CredentialFields(), Update: u},
	}, func(ctx context.Context, mount string, values map[string]string) (func() error, error) {
		return func() error { return fmt.Errorf("还原配置失败: 磁盘只读") }, nil
	})
	csrf := fetchCSRF(t, h)

	rec := postUpdate(h, csrf, "移动云盘", "new-auth-token")
	body := rec.Body.String()
	if strings.Contains(body, "配置已还原") {
		t.Errorf("must not claim restored when rollback failed: %s", body)
	}
	if !strings.Contains(body, "回滚失败") || !strings.Contains(body, "请手工核对") {
		t.Errorf("expected honest rollback-failure message, got: %s", body)
	}
}

func TestUpdateValidationFailureShortCircuits(t *testing.T) {
	u := &fakeUpdater{fields: []driver.CredentialField{{Key: "authorization", Kind: "password"}}, validateErr: fmt.Errorf("token 无效")}
	h, fu, calls := newTestHandler(u, nil)
	csrf := fetchCSRF(t, h)

	rec := postUpdate(h, csrf, "移动云盘", "bad-token")
	if rec.Code != http.StatusBadGateway {
		t.Errorf("validation failure status = %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "验证失败") {
		t.Errorf("expected validation failure message, got: %s", rec.Body.String())
	}
	if fu.committed {
		t.Error("commit must not run after validation failure")
	}
	if len(*calls) != 0 {
		t.Errorf("persist must not run after validation failure, calls=%v", *calls)
	}
}

func TestUpdatePersistFailureShortCircuits(t *testing.T) {
	u := &fakeUpdater{fields: []driver.CredentialField{{Key: "authorization", Kind: "password"}}}
	h, fu, _ := newTestHandler(u, fmt.Errorf("磁盘只读"))
	csrf := fetchCSRF(t, h)

	rec := postUpdate(h, csrf, "移动云盘", "new-auth-token")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("persist failure status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "写入配置文件失败") {
		t.Errorf("expected persist failure message, got: %s", rec.Body.String())
	}
	if fu.committed {
		t.Error("commit must not run after persist failure")
	}
}

func TestUpdateCommitWarningStillSucceeds(t *testing.T) {
	u := &fakeUpdater{fields: []driver.CredentialField{{Key: "authorization", Kind: "password"}}, commitErr: fmt.Errorf("%w: 本地缓存写入失败", driver.ErrStateWriteFailed)}
	h, _, _ := newTestHandler(u, nil)
	csrf := fetchCSRF(t, h)

	rec := postUpdate(h, csrf, "移动云盘", "new-auth-token")
	body := rec.Body.String()
	if !strings.Contains(body, "更新成功") {
		t.Errorf("commit warning should still show success, got: %s", body)
	}
	if !strings.Contains(body, "本地缓存写入失败") {
		t.Errorf("commit warning detail should be shown, got: %s", body)
	}
}

func TestCSRFOneTime(t *testing.T) {
	u := &fakeUpdater{fields: []driver.CredentialField{{Key: "authorization", Kind: "password"}}}
	h, _, _ := newTestHandler(u, nil)
	csrf := fetchCSRF(t, h)

	first := postUpdate(h, csrf, "移动云盘", "new-auth-token")
	if !strings.Contains(first.Body.String(), "更新成功") {
		t.Fatalf("first use should succeed: %s", first.Body.String())
	}
	second := postUpdate(h, csrf, "移动云盘", "new-auth-token")
	if second.Code != http.StatusBadRequest {
		t.Errorf("token reuse status = %d, want 400", second.Code)
	}
	if !strings.Contains(second.Body.String(), "安全校验失败") {
		t.Errorf("token reuse should fail: %s", second.Body.String())
	}
}

// TestPageSecurityHeaders 管理页必须带 no-store / X-Frame-Options / CSP（P2）
func TestPageSecurityHeaders(t *testing.T) {
	u := &fakeUpdater{}
	h, _, _ := newTestHandler(u, nil)
	rec := httptest.NewRecorder()
	h.Page(rec, httptest.NewRequest("GET", "/admin", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("page status = %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "form-action 'self'") {
		t.Errorf("CSP = %q", got)
	}
}

func TestUpdateUnknownMount(t *testing.T) {
	u := &fakeUpdater{}
	h, _, _ := newTestHandler(u, nil)
	csrf := fetchCSRF(t, h)

	rec := postUpdate(h, csrf, "不存在的挂载", "x")
	if !strings.Contains(rec.Body.String(), "未找到挂载") {
		t.Errorf("expected unknown-mount message, got: %s", rec.Body.String())
	}
}

func TestUpdateCommitPlainErrorNotSuccess(t *testing.T) {
	// 非哨兵错误（如「没有待提交的凭证」）不得显示为成功
	u := &fakeUpdater{fields: []driver.CredentialField{{Key: "authorization", Kind: "password"}}, commitErr: fmt.Errorf("没有待提交的凭证，请先验证")}
	h, _, _ := newTestHandler(u, nil)
	csrf := fetchCSRF(t, h)

	rec := postUpdate(h, csrf, "移动云盘", "new-auth-token")
	body := rec.Body.String()
	if strings.Contains(body, "更新成功") {
		t.Errorf("plain commit error must not show success, got: %s", body)
	}
	if !strings.Contains(body, "凭证生效失败") {
		t.Errorf("expected commit failure message, got: %s", body)
	}
}

func TestOnCommittedCalled(t *testing.T) {
	u := &fakeUpdater{fields: []driver.CredentialField{{Key: "authorization", Kind: "password"}}}
	h, _, _ := newTestHandler(u, nil)
	committed := ""
	h.OnCommitted = func(mount string) { committed = mount }
	csrf := fetchCSRF(t, h)

	rec := postUpdate(h, csrf, "移动云盘", "new-auth-token")
	if !strings.Contains(rec.Body.String(), "更新成功") {
		t.Fatalf("update should succeed: %s", rec.Body.String())
	}
	if committed != "移动云盘" {
		t.Errorf("OnCommitted = %q, want 移动云盘", committed)
	}
}

// TestUpdateUnavailableShowsRestartPrompt D3（DESIGN-2026-11-24-01 §6.3）：
// 更新成功 ≠ 恢复——挂载当前不可用时，成功消息必须提示「重启后生效」。
func TestUpdateUnavailableShowsRestartPrompt(t *testing.T) {
	u := &fakeUpdater{fields: []driver.CredentialField{{Key: "authorization", Kind: "password"}}}
	h := New([]Mount{
		{Name: "移动云盘", Prefix: "/移动云盘", Available: false, Fields: u.CredentialFields(), Update: u},
	}, func(ctx context.Context, mount string, values map[string]string) (func() error, error) {
		return func() error { return nil }, nil
	})
	csrf := fetchCSRF(t, h)

	rec := postUpdate(h, csrf, "移动云盘", "new-auth-token")
	body := rec.Body.String()
	if !strings.Contains(body, "更新成功") {
		t.Fatalf("update should succeed: %s", body)
	}
	if !strings.Contains(body, "重启后生效") {
		t.Errorf("unavailable mount success message must prompt restart, got: %s", body)
	}
}

// fakeOAuth 假一键授权实现
type fakeOAuth struct {
	startURL     string
	startErr     error
	exchangeErr  error
	lastValues   map[string]string
	exchangedState string
}

func (f *fakeOAuth) OAuthStart() (string, error) { return f.startURL, f.startErr }
func (f *fakeOAuth) OAuthExchange(_ context.Context, state, code string, values map[string]string) error {
	if f.exchangeErr != nil {
		return f.exchangeErr
	}
	f.exchangedState = state
	values["__refresh_token"] = "rt-final-" + code
	values["__expiry"] = "2027-01-01 00:00"
	f.lastValues = values
	return nil
}

// TestOAuthStartRendersPage 批 2（§6.4）：开始授权渲染授权页（含链接与回填表单）
func TestOAuthStartRendersPage(t *testing.T) {
	oa := &fakeOAuth{startURL: "https://openapi.baidu.com/oauth/2.0/authorize?x=1"}
	u := &fakeUpdater{fields: []driver.CredentialField{{Key: "refresh_token", Kind: "password"}}}
	h := New([]Mount{
		{Name: "百度", Prefix: "/百度", Available: false, Fields: u.CredentialFields(), Update: u, OAuth: oa},
	}, func(ctx context.Context, mount string, values map[string]string) (func() error, error) {
		return func() error { return nil }, nil
	})
	csrf := fetchCSRF(t, h)

	form := url.Values{"csrf": {csrf}, "mount": {"百度"}}
	req := httptest.NewRequest("POST", "/admin/oauth/start", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.OAuthStart(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "oauth/2.0/authorize") {
		t.Errorf("授权页应含授权链接: %s", body)
	}
	if !strings.Contains(body, "/admin/oauth/finish") {
		t.Errorf("授权页应含回填表单: %s", body)
	}
}

// TestOAuthFinishHappyPath 批 2（§6.4）：粘贴回调网址 → state/code 解析 →
// 兑换（__refresh_token 最终值贯穿）→ 写配置 → 提交生效。
func TestOAuthFinishHappyPath(t *testing.T) {
	oa := &fakeOAuth{startURL: "https://x/authorize"}
	u := &fakeUpdater{fields: []driver.CredentialField{{Key: "refresh_token", Kind: "password"}}}
	calls := &[]string{}
	h := New([]Mount{
		{Name: "百度", Prefix: "/百度", Available: false, Fields: u.CredentialFields(), Update: u, OAuth: oa},
	}, func(ctx context.Context, mount string, values map[string]string) (func() error, error) {
		*calls = append(*calls, "persist:"+mount+":"+values["__refresh_token"])
		return func() error { return nil }, nil
	})
	csrf := fetchCSRF(t, h)

	form := url.Values{"csrf": {csrf}, "mount": {"百度"}, "url": {"https://x/callback?code=c1&state=s1"}}
	req := httptest.NewRequest("POST", "/admin/oauth/finish", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.OAuthFinish(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "更新成功") {
		t.Fatalf("应更新成功: %s", body)
	}
	if oa.exchangedState != "s1" {
		t.Errorf("exchange state = %q, want s1", oa.exchangedState)
	}
	if len(*calls) != 1 || (*calls)[0] != "persist:百度:rt-final-c1" {
		t.Errorf("persist 应使用最终 refresh_token：calls=%v", *calls)
	}
	// 挂载不可用 → 提示重启后生效（D3）
	if !strings.Contains(body, "重启后生效") {
		t.Errorf("不可用挂载应提示重启后生效: %s", body)
	}
}

// TestOAuthFinishBadURL 缺 code/state 的网址 → 拒绝且不落盘
func TestOAuthFinishBadURL(t *testing.T) {
	oa := &fakeOAuth{startURL: "https://x/authorize"}
	u := &fakeUpdater{fields: []driver.CredentialField{{Key: "refresh_token", Kind: "password"}}}
	persisted := false
	h := New([]Mount{
		{Name: "百度", Prefix: "/百度", Available: false, Fields: u.CredentialFields(), Update: u, OAuth: oa},
	}, func(ctx context.Context, mount string, values map[string]string) (func() error, error) {
		persisted = true
		return func() error { return nil }, nil
	})

	for _, bad := range []string{"not-a-url", "https://x/callback?code=c1"} { // 缺 state
		csrf := fetchCSRF(t, h) // 一次性令牌，每次重新取
		form := url.Values{"csrf": {csrf}, "mount": {"百度"}, "url": {bad}}
		req := httptest.NewRequest("POST", "/admin/oauth/finish", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.OAuthFinish(rec, req)
		if !strings.Contains(rec.Body.String(), "缺少") && !strings.Contains(rec.Body.String(), "无效") {
			t.Errorf("坏网址应明确报错: %s", rec.Body.String())
		}
	}
	if persisted {
		t.Error("解析失败不得落盘")
	}
}

// TestOAuthExchangeFailureNoPersist 兑换失败（如 state 无效）→ 未做任何修改
func TestOAuthExchangeFailureNoPersist(t *testing.T) {
	oa := &fakeOAuth{startURL: "https://x/authorize", exchangeErr: fmt.Errorf("授权状态无效，请重新开始授权")}
	u := &fakeUpdater{fields: []driver.CredentialField{{Key: "refresh_token", Kind: "password"}}}
	persisted := false
	h := New([]Mount{
		{Name: "百度", Prefix: "/百度", Available: false, Fields: u.CredentialFields(), Update: u, OAuth: oa},
	}, func(ctx context.Context, mount string, values map[string]string) (func() error, error) {
		persisted = true
		return func() error { return nil }, nil
	})
	csrf := fetchCSRF(t, h)

	form := url.Values{"csrf": {csrf}, "mount": {"百度"}, "url": {"https://x/callback?code=c1&state=s1"}}
	req := httptest.NewRequest("POST", "/admin/oauth/finish", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.OAuthFinish(rec, req)
	if !strings.Contains(rec.Body.String(), "未做任何修改") {
		t.Errorf("兑换失败应提示未做任何修改: %s", rec.Body.String())
	}
	if persisted {
		t.Error("兑换失败不得落盘")
	}
}

// TestRestartService 批 2（§6.12）：CSRF 校验 → 渲染「正在重启」页 → 延迟调用重启回调；
// 无 CSRF 拒绝；回调失败记日志不影响响应。
func TestRestartService(t *testing.T) {
	restarted := make(chan error, 1)
	u := &fakeUpdater{fields: []driver.CredentialField{{Key: "authorization", Kind: "password"}}}
	h, _, _ := newTestHandler(u, nil)
	h.Restart = func() error { restarted <- nil; return nil }

	csrf := fetchCSRF(t, h)
	form := url.Values{"csrf": {csrf}}
	req := httptest.NewRequest("POST", "/admin/restart", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.RestartService(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "正在重启") {
		t.Fatalf("应渲染重启页: %d %s", rec.Code, rec.Body.String())
	}
	select {
	case err := <-restarted:
		if err != nil {
			t.Fatalf("Restart: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("重启回调未在延迟后被调用")
	}

	// 无有效 CSRF → 拒绝且不触发重启
	bad := url.Values{"csrf": {"forged"}}
	req2 := httptest.NewRequest("POST", "/admin/restart", strings.NewReader(bad.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	h.RestartService(rec2, req2)
	if !strings.Contains(rec2.Body.String(), "安全校验失败") {
		t.Errorf("伪造 CSRF 应被拒绝: %s", rec2.Body.String())
	}
}

// TestRestartButtonRendered 管理页存在重启入口（Restart 非 nil 时）
func TestRestartButtonRendered(t *testing.T) {
	u := &fakeUpdater{fields: []driver.CredentialField{{Key: "authorization", Kind: "password"}}}
	h, _, _ := newTestHandler(u, nil)
	h.Restart = func() error { return nil }
	rec := httptest.NewRecorder()
	h.Page(rec, httptest.NewRequest("GET", "/admin", nil))
	if !strings.Contains(rec.Body.String(), "/admin/restart") {
		t.Errorf("Restart 注入后管理页应含重启按钮: %s", rec.Body.String())
	}
}
