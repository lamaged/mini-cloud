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

	// HealthWarning 应消失（新凭证未过期）
	if w := d.HealthWarning(); w != "" {
		t.Errorf("HealthWarning should be empty after commit, got %q", w)
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

func TestValidateAuthorizationWithMockHost(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"success":true,"code":"","message":"","data":{"items":[],"nextPageCursor":""}}`)
	}))
	defer ts.Close()

	d := &Driver{}
	auth := makeAuth("13800000000", time.Now().Add(24*time.Hour))
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
