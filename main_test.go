package main

import (
	"net/http/httptest"
	"strings"
	"testing"

	"mini-cloud/drivers/baidu"
	"mini-cloud/drivers/local"
	"mini-cloud/drivers/mobile"
	"mini-cloud/drivers/tianyi"
	"mini-cloud/internal/driver"
)

func TestXMLTextEscapes(t *testing.T) {
	got := xmlText(`a&b<c>"d"`)
	want := "a&amp;b&lt;c&gt;&#34;d&#34;"
	if got != want {
		t.Errorf("xmlText = %q, want %q", got, want)
	}
}

func TestServeRootPropfindEscapesXML(t *testing.T) {
	mounts := []mountedDriver{{Name: "mobile", Prefix: "/移&动", Drv: nil}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PROPFIND", "/", nil)
	serveRootPropfind(rec, req, mounts)

	body := rec.Body.String()
	if strings.Contains(body, "移&动") {
		t.Errorf("prefix not XML-escaped: %s", body)
	}
	// 前缀先百分号编码（移 → %E7%A7%BB；& 在 URL 路径中合法、无需编码），XML 层再转义 &
	if !strings.Contains(body, "%E7%A7%BB&amp;%E5%8A%A8") {
		t.Errorf("expected percent-encoded+XML-escaped prefix, got: %s", body)
	}
}

func TestServeRootIndexEscapesHTML(t *testing.T) {
	mounts := []mountedDriver{{Name: "mobile", Prefix: "/<script>", Drv: nil}}
	rec := httptest.NewRecorder()
	serveRootIndex(rec, mounts, true)

	body := rec.Body.String()
	if strings.Contains(body, "/<script>") {
		t.Errorf("prefix not escaped: %s", body)
	}
	// 前缀百分号编码后无 < > 字符，HTML 层无需再转义
	if !strings.Contains(body, "%3Cscript%3E") {
		t.Errorf("expected percent-encoded prefix in HTML, got: %s", body)
	}
	if !strings.Contains(body, `href="/admin"`) {
		t.Errorf("admin link missing when hasAdmin=true: %s", body)
	}
}

func TestServeRootIndexNoAdminLink(t *testing.T) {
	mounts := []mountedDriver{{Name: "mobile", Prefix: "/mobile", Drv: nil}}
	rec := httptest.NewRecorder()
	serveRootIndex(rec, mounts, false)

	if strings.Contains(rec.Body.String(), "/admin") {
		t.Error("admin link should be absent when hasAdmin=false")
	}
}

// TestVaultSupported 铁律 2：加密仅装配天翼/移动。
// P1（RV-2026-11-19-07）：对**真实驱动实例**断言——锁死「Name() 返回值 ↔ 允许列表」
// 不变量（回归教训：曾手写名单与 Name() 实际值不符导致全云盘加密失效）。
func TestVaultSupported(t *testing.T) {
	tests := []struct {
		name string
		drv  driver.Driver
		want bool
	}{
		{"tianyi 个人", tianyi.New("t", "u", "p", "", t.TempDir()), true},
		{"tianyi 家庭", tianyi.New("t", "u", "p", "family-1", t.TempDir()), true},
		{"mobile", mobile.New("m", "auth", t.TempDir()), true},
		{"local", func() driver.Driver {
			d, err := local.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			return d
		}(), true},
		{"baidu", baidu.New("b", "rt", "cid", "cs", t.TempDir()), false},
	}
	for _, tt := range tests {
		if got := vaultSupported(tt.drv); got != tt.want {
			t.Errorf("vaultSupported(%s) = %v, want %v（驱动 Name()=%q）", tt.name, got, tt.want, tt.drv.Name())
		}
	}
}
