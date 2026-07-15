package fs

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"mini-cloud/internal/model"
)

// RetryReader 带重试的 Reader 包装器
type RetryReader struct {
	inner    Reader
	maxRetry int
	delay    time.Duration
}

// NewRetryReader 创建重试包装器
func NewRetryReader(inner Reader) *RetryReader {
	return &RetryReader{
		inner:    inner,
		maxRetry: 3,
		delay:    500 * time.Millisecond,
	}
}

// List 带重试的目录列表
func (r *RetryReader) List(ctx context.Context, path string) ([]model.Obj, error) {
	var lastErr error
	for i := 0; i <= r.maxRetry; i++ {
		if i > 0 {
			slog.Info("重试 List", "path", path, "attempt", i, "err", lastErr)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(r.delay):
			}
		}
		objs, err := r.inner.List(ctx, path)
		if err == nil {
			return objs, nil
		}
		lastErr = err

		// 404 不重试
		if isNotFound(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

// Link 带重试的文件链接
func (r *RetryReader) Link(ctx context.Context, path string) (*model.Link, error) {
	var lastErr error
	for i := 0; i <= r.maxRetry; i++ {
		if i > 0 {
			slog.Info("重试 Link", "path", path, "attempt", i, "err", lastErr)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(r.delay):
			}
		}
		link, err := r.inner.Link(ctx, path)
		if err == nil {
			return link, nil
		}
		lastErr = err

		// 404 不重试
		if isNotFound(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

// isNotFound 判断错误是否为"文件不存在"
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") ||
		strings.Contains(msg, "no such file") ||
		strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "cannot find")
}
