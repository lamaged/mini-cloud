package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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
}

func (f *fakeUpdater) CredentialFields() []driver.CredentialField { return f.fields }
func (f *fakeUpdater) ValidateCredential(_ context.Context, values map[string]string) error {
	f.validated = true
	f.lastValidate = values
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
	}, func(ctx context.Context, mount string, values map[string]string) error {
		*calls = append(*calls, "persist:"+mount)
		if persistErr != nil {
			return persistErr
		}
		return nil
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
	}, func(ctx context.Context, mount string, values map[string]string) error { return nil })

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

func TestUpdateValidationFailureShortCircuits(t *testing.T) {
	u := &fakeUpdater{fields: []driver.CredentialField{{Key: "authorization", Kind: "password"}}, validateErr: fmt.Errorf("token 无效")}
	h, fu, calls := newTestHandler(u, nil)
	csrf := fetchCSRF(t, h)

	rec := postUpdate(h, csrf, "移动云盘", "bad-token")
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
	if !strings.Contains(second.Body.String(), "安全校验失败") {
		t.Errorf("token reuse should fail: %s", second.Body.String())
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
