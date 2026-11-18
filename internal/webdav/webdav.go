package webdav

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"mini-cloud/internal/driver"
	"mini-cloud/internal/model"
)

// Handler WebDAV 协议处理器
type Handler struct {
	Driver Reader // 云盘驱动（只读接口）
	Prefix string // URL 前缀，如 "/dav"，为空表示无前缀
}

// Reader 驱动读取接口（避免循环依赖，在此处定义精简版）
type Reader interface {
	List(ctx context.Context, path string) ([]model.Obj, error)
	Link(ctx context.Context, path string) (*model.Link, error)
}

// Close 释放底层资源（如后台 goroutine）。如果 Driver 不支持则无操作。
func (h *Handler) Close() error {
	type stopper interface{ Stop() }
	if s, ok := h.Driver.(stopper); ok {
		s.Stop()
	}
	return nil
}

// ServeHTTP 实现 http.Handler
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	slog.Debug("WebDAV request", "method", r.Method, "path", r.URL.Path, "user-agent", r.Header.Get("User-Agent"))

	var status int
	var err error

	switch r.Method {
	case "OPTIONS":
		status, err = h.handleOptions(w, r)
	case "PROPFIND":
		status, err = h.handlePropfind(w, r)
	case "GET", "HEAD":
		status, err = h.handleGetHead(w, r)
	default:
		status = http.StatusMethodNotAllowed
		w.Header().Set("Allow", "OPTIONS, PROPFIND, GET, HEAD")
	}

	if err != nil {
		// E（RV-2026-11-18-01）：客户端断开/取消不是服务端故障，降为 Debug
		//（播放器 seek/取消频繁发生，记 ERROR 会刷屏并掩盖真实异常）
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			slog.Debug("WebDAV 请求被取消", "method", r.Method, "path", r.URL.Path)
		} else {
			slog.Error("WebDAV error", "method", r.Method, "path", r.URL.Path, "err", err)
		}
	}
	if status != 0 {
		w.WriteHeader(status)
		if status >= 400 {
			w.Write([]byte(http.StatusText(status)))
		}
	}
}

// handleOptions 返回支持的方法
func (h *Handler) handleOptions(w http.ResponseWriter, r *http.Request) (int, error) {
	// Windows WebDAV Mini-Redirector 要求始终包含 GET, HEAD
	allow := "OPTIONS, GET, HEAD, PROPFIND"

	w.Header().Set("Allow", allow)
	w.Header().Set("DAV", "1, 2")
	w.Header().Set("MS-Author-Via", "DAV")
	return http.StatusOK, nil
}

// handleGetHead 处理 GET 和 HEAD 请求
func (h *Handler) handleGetHead(w http.ResponseWriter, r *http.Request) (int, error) {
	reqPath := h.stripPrefix(r.URL.Path)

	if h.isDir(r.Context(), reqPath) {
		return h.serveDirHTML(w, r, reqPath)
	}

	// 将请求头注入 context，供驱动转发 Range 等头
	ctx := context.WithValue(r.Context(), model.ReqHeadersKey, r.Header)
	link, err := h.Driver.Link(ctx, reqPath)
	if err != nil {
		if os.IsNotExist(err) {
			return http.StatusNotFound, nil
		}
		if errors.Is(err, driver.ErrForbidden) {
			// Vault _mc 密文对象：403 屏蔽（I4）
			return http.StatusForbidden, nil
		}
		if errors.Is(err, driver.ErrTooManyStreams) {
			// 并发解密流超上限（design §7.7：超出返回 503 防止 OOM）
			// I1（RV-2026-11-18-03）：记 Info 供压测统计 503 次数与来源（HEAD 探测/seek 二次连接/真第 4 路）
			slog.Info("并发解密流超上限（503）", "path", reqPath)
			return http.StatusServiceUnavailable, nil
		}
		if errors.Is(err, driver.ErrUpstream) {
			// 上游（云盘 CDN）失败：网络/签名/截断等（契约 design §7.7 → 502）
			return http.StatusBadGateway, nil
		}
		return http.StatusInternalServerError, err
	}

	// 如果有数据流
	if link.Reader != nil {
		defer link.Reader.Close()

		seeker, ok := link.Reader.(io.ReadSeeker)
		if ok {
			return h.serveContent(w, r, path.Base(reqPath), seeker, link)
		}
		// 非 seekable → 流式传输（不缓冲全文件）
		return h.streamReader(w, r, link)
	}

		// 如果有 URL，302 重定向到云盘 CDN
		if link.URL != "" {
			if r.Method == http.MethodHead {
				return http.StatusOK, nil
			}
			http.Redirect(w, r, link.URL, http.StatusFound)
			return 0, nil
		}

		return http.StatusInternalServerError, fmt.Errorf("no valid link")
	}

// serveContent 使用 http.ServeContent 处理文件内容（支持 Range）
func (h *Handler) serveContent(w http.ResponseWriter, r *http.Request, name string, seeker io.ReadSeeker, link *model.Link) (int, error) {
	if link.Header != nil {
		for k, vs := range link.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
	}

	// 获取文件信息（用于 Last-Modified 和 Content-Length）
	var modTime time.Time
	var contentLen int64
	type fileInfo interface {
		Stat() (os.FileInfo, error)
	}
	if f, ok := seeker.(fileInfo); ok {
		if info, err := f.Stat(); err == nil {
			modTime = info.ModTime()
			contentLen = info.Size()
		}
	}

	if r.Method == http.MethodHead {
		if contentLen > 0 {
			w.Header().Set("Content-Length", fmt.Sprintf("%d", contentLen))
		}
		w.Header().Set("Accept-Ranges", "bytes")
		if !modTime.IsZero() {
			w.Header().Set("Last-Modified", modTime.UTC().Format(http.TimeFormat))
		}
		return http.StatusOK, nil
	}

	http.ServeContent(w, r, name, modTime, seeker)
	return 0, nil
}

// bytesReadSeeker 将 []byte 包装为 io.ReadSeeker
type bytesReadSeeker struct {
	data   []byte
	offset int64
}

func (b *bytesReadSeeker) Read(p []byte) (int, error) {
	if b.offset >= int64(len(b.data)) {
		return 0, io.EOF
	}
	n := copy(p, b.data[b.offset:])
	b.offset += int64(n)
	return n, nil
}

func (b *bytesReadSeeker) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = b.offset + offset
	case io.SeekEnd:
		abs = int64(len(b.data)) + offset
	}
	if abs < 0 {
		return 0, fmt.Errorf("negative position")
	}
	b.offset = abs
	return abs, nil
}

// isDir 判断路径是否为目录
func (h *Handler) isDir(ctx context.Context, reqPath string) bool {
	if reqPath == "" || reqPath == "/" {
		return true
	}
	parent := path.Dir(reqPath)
	name := path.Base(reqPath)
	objs, err := h.Driver.List(ctx, parent)
	if err != nil {
		return false
	}
	for _, obj := range objs {
		if obj.Name == name {
			return obj.IsDir
		}
	}
	return false
}

// stripPrefix 去除 URL 前缀
func (h *Handler) stripPrefix(urlPath string) string {
	p := strings.TrimPrefix(urlPath, h.Prefix)
	return cleanPath(p)
}

// cleanPath 清理并规范化路径
func cleanPath(p string) string {
	if p == "" {
		return "/"
	}
	p = path.Clean(p)
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "/"
	}
	return "/" + p
}

// serveDirHTML 为目录 GET 请求生成 HTML 浏览页面。
// 动态内容（文件名等）经 html/template 转义（v0.10.0 独立修复：文件名 XSS）。
func (h *Handler) serveDirHTML(w http.ResponseWriter, r *http.Request, reqPath string) (int, error) {
	if r.Method == http.MethodHead {
		return http.StatusOK, nil
	}

	objs, err := h.Driver.List(r.Context(), reqPath)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	type entry struct {
		Href  string
		Name  string
		Size  string
		Time  string
		IsDir bool
	}
	data := struct {
		Path      string
		Parent    string
		HasParent bool
		Entries   []entry
	}{Path: reqPath}

	if reqPath != "/" {
		parent := path.Dir(reqPath)
		if parent == "." {
			parent = "/"
		}
		data.Parent = h.escPath(parent)
		data.HasParent = true
	}

	for _, obj := range objs {
		e := entry{Name: obj.Name, IsDir: obj.IsDir, Href: h.escPath(path.Join(reqPath, obj.Name))}
		if obj.IsDir {
			e.Href += "/"
		} else {
			e.Size = formatSize(obj.Size)
		}
		if !obj.ModTime.IsZero() {
			e.Time = obj.ModTime.Format("2006-01-02 15:04")
		}
		data.Entries = append(data.Entries, e)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if err := dirHTMLTmpl.Execute(w, data); err != nil {
		return 0, err
	}
	return 0, nil
}

var dirHTMLTmpl = template.Must(template.New("dir").Parse(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Index of {{.Path}}</title>
<style>
body{font-family:sans-serif;max-width:800px;margin:20px auto;padding:0 15px}
h1{font-size:18px;border-bottom:1px solid #ddd;padding-bottom:10px}
a{text-decoration:none;color:#0366d6}
a:hover{text-decoration:underline}
.dir:before{content:"📁 "}
.file:before{content:"📄 "}
table{width:100%;border-collapse:collapse}
td{padding:6px 10px;border-bottom:1px solid #eee}
.size{text-align:right;color:#666;white-space:nowrap}
.time{color:#999;font-size:13px;white-space:nowrap}
</style></head><body>
<h1>Index of {{.Path}}</h1>
<table>
{{if .HasParent}}<tr><td><a class="dir" href="{{.Parent}}">..</a></td><td></td><td></td></tr>{{end}}
{{range .Entries}}<tr><td><a class="{{if .IsDir}}dir{{else}}file{{end}}" href="{{.Href}}">{{.Name}}</a></td><td class="size">{{.Size}}</td><td class="time">{{.Time}}</td></tr>
{{end}}</table></body></html>
`))

// escPath 返回带 Prefix 的完整 URL 路径（Prefix 与路径均逐段百分号编码）
func (h *Handler) escPath(p string) string {
	p = EncodePathSegments(p)
	if h.Prefix != "" {
		p = path.Join("/", EncodePathSegments(h.Prefix), p)
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

// EncodePathSegments 对路径逐段百分号编码（保留 "/" 分隔符）：
// 中文、空格、#、? 等在严格客户端与浏览器中需要编码才能正确访问。
// 导出供 main 的根页面/根 PROPFIND 复用（P6：避免两份实现漂移）。
func EncodePathSegments(p string) string {
	segs := strings.Split(p, "/")
	for i := range segs {
		segs[i] = url.PathEscape(segs[i])
	}
	return strings.Join(segs, "/")
}

// formatSize 格式化文件大小
func formatSize(size int64) string {
	switch {
	case size < 1024:
		return fmt.Sprintf("%d B", size)
	case size < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(size)/1024)
	case size < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(size)/(1024*1024))
	default:
		return fmt.Sprintf("%.1f GB", float64(size)/(1024*1024*1024))
	}
}

// streamReader 流式传输非 seekable reader
func (h *Handler) streamReader(w http.ResponseWriter, r *http.Request, link *model.Link) (int, error) {
	if link.Header != nil {
		for k, vs := range link.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
	}

	if r.Method == http.MethodHead {
		w.Header().Set("Accept-Ranges", "bytes")
		status := http.StatusOK
		if link.StatusCode != 0 {
			status = link.StatusCode
		}
		return status, nil
	}

	// 写状态码（上游可能返回 206 等）
	status := http.StatusOK
	if link.StatusCode != 0 {
		status = link.StatusCode
	}
	w.WriteHeader(status)

	_, err := io.Copy(w, link.Reader)
	if err != nil {
		return 0, err
	}
	return 0, nil
}
