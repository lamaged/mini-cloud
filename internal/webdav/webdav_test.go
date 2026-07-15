package webdav

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"mini-cloud/internal/model"
)

// mockDriver 模拟驱动器，用于测试 WebDAV handler
type mockDriver struct {
	files map[string]*fileEntry
}

type fileEntry struct {
	obj  model.Obj
	data []byte
}

func newMockDriver() *mockDriver {
	now := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	return &mockDriver{
		files: map[string]*fileEntry{
			"/": {
				obj: model.Obj{Name: "/", Size: 0, ModTime: now, IsDir: true},
			},
			"/readme.txt": {
				obj:  model.Obj{Name: "readme.txt", Size: 18, ModTime: now, IsDir: false},
				data: []byte("Hello Mini Cloud!\n"),
			},
			"/music": {
				obj: model.Obj{Name: "music", Size: 0, ModTime: now, IsDir: true},
			},
			"/music/sample.mp3": {
				obj:  model.Obj{Name: "sample.mp3", Size: 11, ModTime: now, IsDir: false},
				data: []byte("fake-mp3-data"),
			},
			"/videos": {
				obj: model.Obj{Name: "videos", Size: 0, ModTime: now, IsDir: true},
			},
			"/videos/sample.mp4": {
				obj:  model.Obj{Name: "sample.mp4", Size: 100, ModTime: now, IsDir: false},
				data: make([]byte, 100),
			},
			"/empty-dir": {
				obj: model.Obj{Name: "empty-dir", Size: 0, ModTime: now, IsDir: true},
			},
		},
	}
}

func (m *mockDriver) List(ctx context.Context, path string) ([]model.Obj, error) {
	path = cleanPath(path)
	prefix := path
	if prefix == "/" {
		prefix = ""
	}
	prefix += "/"

	var result []model.Obj
	for key, entry := range m.files {
		if key == "/" || key == path {
			continue
		}
		if strings.HasPrefix(key, prefix) {
			rest := strings.TrimPrefix(key, prefix)
			if !strings.Contains(rest, "/") {
				result = append(result, entry.obj)
			}
		}
	}

	if result == nil {
		if _, ok := m.files[path]; !ok {
			return nil, os.ErrNotExist
		}
		result = []model.Obj{}
	}
	return result, nil
}

func (m *mockDriver) Link(ctx context.Context, path string) (*model.Link, error) {
	path = cleanPath(path)
	entry, ok := m.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	if entry.obj.IsDir {
		return nil, os.ErrInvalid
	}

	return &model.Link{
		Reader: &readSeekCloser{Reader: strings.NewReader(string(entry.data))},
		Header: http.Header{
			"Content-Type": {"application/octet-stream"},
		},
	}, nil
}

// ============================================================
// OPTIONS 测试
// ============================================================

func TestOptions(t *testing.T) {
	handler := &Handler{Driver: newMockDriver()}
	req := httptest.NewRequest("OPTIONS", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("OPTIONS: expected 200, got %d", resp.StatusCode)
	}
	if resp.Header.Get("DAV") != "1, 2" {
		t.Errorf("OPTIONS: expected DAV: 1, 2, got %q", resp.Header.Get("DAV"))
	}
	if resp.Header.Get("MS-Author-Via") != "DAV" {
		t.Errorf("OPTIONS: expected MS-Author-Via: DAV, got %q", resp.Header.Get("MS-Author-Via"))
	}
	allow := resp.Header.Get("Allow")
	if !strings.Contains(allow, "PROPFIND") || !strings.Contains(allow, "GET") || !strings.Contains(allow, "HEAD") {
		t.Errorf("OPTIONS: Allow header missing methods: %q", allow)
	}
}

// ============================================================
// PROPFIND 测试
// ============================================================

func TestPropfindRootDepth0(t *testing.T) {
	handler := &Handler{Driver: newMockDriver()}
	req := httptest.NewRequest("PROPFIND", "/", nil)
	req.Header.Set("Depth", "0")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusMultiStatus {
		t.Errorf("PROPFIND Depth:0: expected 207, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var ms multistatus
	if err := xml.Unmarshal(body, &ms); err != nil {
		t.Fatalf("PROPFIND Depth:0: invalid XML: %v\nBody: %s", err, string(body))
	}
	if len(ms.Responses) != 1 {
		t.Errorf("PROPFIND Depth:0: expected 1 response, got %d", len(ms.Responses))
	}
	if ms.Responses[0].Href != "/" {
		t.Errorf("PROPFIND Depth:0: expected href '/', got %q", ms.Responses[0].Href)
	}
	// 验证 XML 不包含未声明的 D: 前缀
	if strings.Contains(string(body), "<D:") {
		t.Errorf("PROPFIND Depth:0: XML contains undeclared D: prefix")
	}
}

func TestPropfindRootDepth1(t *testing.T) {
	handler := &Handler{Driver: newMockDriver()}
	req := httptest.NewRequest("PROPFIND", "/", nil)
	req.Header.Set("Depth", "1")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusMultiStatus {
		t.Errorf("PROPFIND Depth:1: expected 207, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var ms multistatus
	if err := xml.Unmarshal(body, &ms); err != nil {
		t.Fatalf("PROPFIND Depth:1: invalid XML: %v", err)
	}

	// 应该有根目录 + 各子项：/、/readme.txt、/music/、/videos/、/empty-dir/
	if len(ms.Responses) < 5 {
		t.Errorf("PROPFIND Depth:1: expected >=5 responses, got %d", len(ms.Responses))
	}

	// 验证返回的资源类型
	foundRoot := false
	foundReadme := false
	foundMusic := false
	for _, r := range ms.Responses {
		switch {
		case r.Href == "/":
			foundRoot = true
		case strings.HasSuffix(r.Href, "/readme.txt") || r.Href == "/readme.txt":
			foundReadme = true
		case strings.Contains(r.Href, "music"):
			foundMusic = true
		}
	}
	if !foundRoot {
		t.Error("PROPFIND Depth:1: missing root entry")
	}
	if !foundReadme {
		t.Error("PROPFIND Depth:1: missing readme.txt entry")
	}
	if !foundMusic {
		t.Error("PROPFIND Depth:1: missing music entry")
	}
}

func TestPropfindSubDir(t *testing.T) {
	handler := &Handler{Driver: newMockDriver()}
	req := httptest.NewRequest("PROPFIND", "/music/", nil)
	req.Header.Set("Depth", "1")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusMultiStatus {
		t.Errorf("PROPFIND subdir: expected 207, got %d", resp.StatusCode)
	}
}

func TestPropfindEmptyDir(t *testing.T) {
	handler := &Handler{Driver: newMockDriver()}
	req := httptest.NewRequest("PROPFIND", "/empty-dir/", nil)
	req.Header.Set("Depth", "1")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusMultiStatus {
		t.Errorf("PROPFIND empty dir: expected 207, got %d", resp.StatusCode)
	}
}

func TestPropfindNotFound(t *testing.T) {
	handler := &Handler{Driver: newMockDriver()}
	req := httptest.NewRequest("PROPFIND", "/nonexistent/", nil)
	req.Header.Set("Depth", "1")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("PROPFIND not found: expected 404, got %d", resp.StatusCode)
	}
}

func TestPropfindContentType(t *testing.T) {
	handler := &Handler{Driver: newMockDriver()}
	req := httptest.NewRequest("PROPFIND", "/", nil)
	req.Header.Set("Depth", "0")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	contentType := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/xml") {
		t.Errorf("PROPFIND: expected text/xml Content-Type, got %q", contentType)
	}
}

// ============================================================
// GET 测试
// ============================================================

func TestGetFile(t *testing.T) {
	handler := &Handler{Driver: newMockDriver()}
	req := httptest.NewRequest("GET", "/readme.txt", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET file: expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "Hello Mini Cloud!\n" {
		t.Errorf("GET file: expected 'Hello Mini Cloud!\\n', got %q", string(body))
	}
}

func TestGetFileNotFound(t *testing.T) {
	handler := &Handler{Driver: newMockDriver()}
	req := httptest.NewRequest("GET", "/nonexistent.txt", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET not found: expected 404, got %d", resp.StatusCode)
	}
}

func TestGetDirectory(t *testing.T) {
	handler := &Handler{Driver: newMockDriver()}
	req := httptest.NewRequest("GET", "/music/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	// GET on directory now returns 200 with HTML listing
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET directory: expected 200, got %d", resp.StatusCode)
	}
	contentType := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/html") {
		t.Errorf("GET directory: expected text/html, got %q", contentType)
	}
}

// ============================================================
// HEAD 测试
// ============================================================

func TestHeadFile(t *testing.T) {
	handler := &Handler{Driver: newMockDriver()}
	req := httptest.NewRequest("HEAD", "/readme.txt", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("HEAD file: expected 200, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Errorf("HEAD file: expected Accept-Ranges: bytes, got %q", resp.Header.Get("Accept-Ranges"))
	}
	// 注意：mock driver 的 reader 不实现 Stat()，所以 Last-Modified 可能为空
	// 但 Accept-Ranges 和 Content-Length 应该始终存在
	// HEAD 不应该有 body
	body, _ := io.ReadAll(resp.Body)
	if len(body) > 0 {
		t.Errorf("HEAD file: expected empty body, got %d bytes", len(body))
	}
}

func TestHeadFileNotFound(t *testing.T) {
	handler := &Handler{Driver: newMockDriver()}
	req := httptest.NewRequest("HEAD", "/nonexistent.txt", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("HEAD not found: expected 404, got %d", resp.StatusCode)
	}
}

// ============================================================
// 不支持的 HTTP 方法
// ============================================================

func TestMethodNotAllowed(t *testing.T) {
	handler := &Handler{Driver: newMockDriver()}
	for _, method := range []string{"PUT", "DELETE", "MKCOL", "MOVE", "COPY"} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/test", nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			resp := w.Result()
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("%s: expected 405, got %d", method, resp.StatusCode)
			}
		})
	}
}

// ============================================================
// 辅助函数
// ============================================================

func TestCleanPath(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "/"},
		{"/", "/"},
		{"test", "/test"},
		{"/test", "/test"},
		{"/test/", "/test"},
		{"test/path", "/test/path"},
		{"/test/path", "/test/path"},
		{"//test//path", "/test/path"},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("%q", tc.input), func(t *testing.T) {
			result := cleanPath(tc.input)
			if result != tc.expected {
				t.Errorf("cleanPath(%q) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

func TestBuildHref(t *testing.T) {
	h := &Handler{Prefix: ""}

	tests := []struct {
		path  string
		isDir bool
		want  string
	}{
		{"/", true, "/"},
		{"/music", true, "/music/"},
		{"/readme.txt", false, "/readme.txt"},
	}

	for _, tc := range tests {
		result := h.buildHref(tc.path, tc.isDir)
		if result != tc.want {
			t.Errorf("buildHref(%q, %v) = %q, want %q", tc.path, tc.isDir, result, tc.want)
		}
	}
}

func TestBuildHrefWithPrefix(t *testing.T) {
	h := &Handler{Prefix: "/dav"}

	result := h.buildHref("/music", true)
	if result != "/dav/music/" {
		t.Errorf("buildHref with prefix: got %q, want %q", result, "/dav/music/")
	}
}

// ============================================================
// XML props 测试
// ============================================================

func TestBuildPropsXML(t *testing.T) {
	now := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)

	t.Run("directory", func(t *testing.T) {
		obj := &model.Obj{Name: "music", Size: 0, ModTime: now, IsDir: true}
		h := &Handler{}
		xml := h.buildPropsXML(obj)

		if !strings.Contains(xml, "<displayname>music</displayname>") {
			t.Error("directory props: missing displayname")
		}
		if !strings.Contains(xml, "<resourcetype><collection/></resourcetype>") {
			t.Error("directory props: missing resourcetype/collection")
		}
		if !strings.Contains(xml, "<getcontenttype>httpd/unix-directory</getcontenttype>") {
			t.Error("directory props: missing getcontenttype")
		}
		// 确保没有 D: 前缀
		if strings.Contains(xml, "D:") {
			t.Error("directory props: should not contain D: prefix")
		}
	})

	t.Run("file", func(t *testing.T) {
		obj := &model.Obj{Name: "test.mp4", Size: 1024, ModTime: now, IsDir: false}
		h := &Handler{}
		xml := h.buildPropsXML(obj)

		if !strings.Contains(xml, "<getcontentlength>1024</getcontentlength>") {
			t.Error("file props: missing getcontentlength")
		}
		if !strings.Contains(xml, "<getcontenttype>video/mp4</getcontenttype>") {
			t.Error("file props: missing content type")
		}
		if strings.Contains(xml, "<resourcetype><collection/></resourcetype>") {
			t.Error("file props: should not have collection")
		}
	})
}

func TestBuildPropXMLHelper(t *testing.T) {
	props := map[string]string{
		"displayname": "test.txt",
		"getetag":     `"abc123"`,
	}
	dirProps := map[string]string{
		"resourcetype": "<collection/>",
	}

	result := buildPropXML(props, dirProps)

	// 验证不包含 D: 前缀
	if strings.Contains(result, "D:") {
		t.Errorf("buildPropXML should not contain D: prefix, got: %s", result)
	}
	if !strings.Contains(result, "<displayname>test.txt</displayname>") {
		t.Error("buildPropXML: missing displayname")
	}
}

func TestXMLEscape(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"hello", "hello"},
		{"a & b", "a &amp; b"},
		{"a < b", "a &lt; b"},
		{"a > b", "a &gt; b"},
		{`"hello"`, "&quot;hello&quot;"},
	}

	for _, tc := range tests {
		result := xmlEscape(tc.input)
		if result != tc.expected {
			t.Errorf("xmlEscape(%q) = %q, want %q", tc.input, result, tc.expected)
		}
	}
}

// readSeekCloser 包装 ReadSeeker，添加无操作 Close
type readSeekCloser struct {
	*strings.Reader
}
func (r *readSeekCloser) Close() error { return nil }
