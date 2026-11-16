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
	validateHook func() // 阻塞点（并发/时序测试用）
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
	return f.commitErr
}

func newTestHandler(u *fakeUpdater, persistErr error) (*Handler, *fakeUpdater, *[]string) {
	calls := &[]string{}
	h := New([]Mount{
		{
			Name:    "移动云盘",
			Prefix:  "/移动云盘",
			Fields:  []driver.CredentialField{{Key: "authorization", Label: "authorization", Kind: "password"}},
			Warning: func() string { return "" },
			Update:  u,
		},
	}, func(ctx context.Context, mount string, values map[string]string) (func(), error) {
		*calls = append(*calls, "persist:"+mount)
		if persistErr != nil {
			return nil, persistErr
		}
		return func() {}, nil
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
			Warning: func() string { return `过期<script>提示</script>` },
			Update:  &fakeUpdater{},
		},
	}, func(ctx context.Context, mount string, values map[string]string) (func(), error) { return func() {}, nil })

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
	if len(*calls) != 1 || (*calls)[0] != "persist:移动云盘" {
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
	h, _, _ := newTestHandler(u, nil)
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
}

// TestCommitPlainErrorRollsBack 提交失败时用回滚函数还原配置（I6）
func TestCommitPlainErrorRollsBack(t *testing.T) {
	u := &fakeUpdater{fields: []driver.CredentialField{{Key: "authorization", Kind: "password"}}, commitErr: fmt.Errorf("没有待提交的凭证，请先验证")}
	rolledBack := false
	h := New([]Mount{
		{Name: "移动云盘", Prefix: "/移动云盘", Fields: u.CredentialFields(), Update: u},
	}, func(ctx context.Context, mount string, values map[string]string) (func(), error) {
		return func() { rolledBack = true }, nil
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
