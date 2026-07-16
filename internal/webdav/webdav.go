package webdav

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

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
		slog.Error("WebDAV error", "method", r.Method, "path", r.URL.Path, "err", err)
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

// serveDirHTML 为目录 GET 请求生成 HTML 浏览页面
func (h *Handler) serveDirHTML(w http.ResponseWriter, r *http.Request, reqPath string) (int, error) {
	if r.Method == http.MethodHead {
		return http.StatusOK, nil
	}

	objs, err := h.Driver.List(r.Context(), reqPath)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	// 简易 HTML 目录列表
	fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>%s</title>
<style>
body{font-family:sans-serif;max-width:800px;margin:20px auto;padding:0 15px}
h1{font-size:18px;border-bottom:1px solid #ddd;padding-bottom:10px}
a{text-decoration:none;color:#0366d6}
a:hover{text-decoration:underline}
.dir:before{content:"📁 "}
.file:before{content:"📄 "}
table{width:100%%;border-collapse:collapse}
td{padding:6px 10px;border-bottom:1px solid #eee}
.size{text-align:right;color:#666;white-space:nowrap}
.time{color:#999;font-size:13px;white-space:nowrap}
</style></head><body>
<h1>Index of %s</h1>
<table>`, reqPath, reqPath)

	// 返回上级链接（根目录除外）
	if reqPath != "/" {
		parent := path.Dir(reqPath)
		if parent == "." {
			parent = "/"
		}
		fmt.Fprintf(w, `<tr><td><a class="dir" href="%s">..</a></td><td></td><td></td></tr>`, h.escPath(parent))
	}

	for _, obj := range objs {
		name := obj.Name
		href := h.escPath(path.Join(reqPath, name))
		class := "file"
		if obj.IsDir {
			class = "dir"
			href += "/"
		}
		size := ""
		if !obj.IsDir {
			size = formatSize(obj.Size)
		}
		modTime := ""
		if !obj.ModTime.IsZero() {
			modTime = obj.ModTime.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, `<tr><td><a class="%s" href="%s">%s</a></td><td class="size">%s</td><td class="time">%s</td></tr>`,
			class, href, name, size, modTime)
	}

	fmt.Fprint(w, "</table></body></html>")
	return 0, nil
}

// escPath 返回带 Prefix 的完整 URL 路径
func (h *Handler) escPath(p string) string {
	if h.Prefix != "" {
		p = path.Join("/", h.Prefix, p)
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
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
