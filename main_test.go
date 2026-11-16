package main

import (
	"net/http/httptest"
	"strings"
	"testing"
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
