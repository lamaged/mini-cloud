package model

import (
	"context"
	"io"
	"net/http"
	"time"
)

// ctxKey 用于 context 传参
type ctxKey struct{}

// ReqHeadersKey Context key for passing client request headers to drivers
var ReqHeadersKey = &ctxKey{}

// ReqHeadersFromCtx 从 context 提取请求头
func ReqHeadersFromCtx(ctx context.Context) http.Header {
	if h, ok := ctx.Value(ReqHeadersKey).(http.Header); ok {
		return h
	}
	return nil
}

// Obj 文件/目录信息
type Obj struct {
	Name    string
	Size    int64
	ModTime time.Time
	IsDir   bool
}

// Link 文件下载链接或数据流
type Link struct {
	URL        string       // 重定向 URL（非零时使用 302 重定向）
	Reader     io.ReadCloser // 数据流（非零时直接代理数据）
	Header     http.Header  // 响应头（Content-Type, Content-Length 等）
	StatusCode int          // HTTP 状态码（0 表示默认 200），由 upstream 驱动设置（如 206）
}
