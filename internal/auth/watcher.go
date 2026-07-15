package auth

import (
	"context"
	"log/slog"
	"time"

	"mini-cloud/internal/driver"
)

// TokenWatcher 后台 Token 维护器
type TokenWatcher struct {
	manager  driver.TokenManager
	interval time.Duration
}

// NewTokenWatcher 创建 Token 维护器
func NewTokenWatcher(m driver.TokenManager, interval time.Duration) *TokenWatcher {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &TokenWatcher{
		manager:  m,
		interval: interval,
	}
}

// Start 启动后台 Token 维护循环
func (w *TokenWatcher) Start(ctx context.Context) {
	slog.Info("Token Watcher 启动", "interval", w.interval)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if !w.manager.NeedRefresh() {
				slog.Debug("Token 有效，跳过刷新")
				continue
			}
			slog.Info("Token 即将过期，开始刷新")
			if err := w.manager.RefreshToken(ctx); err != nil {
				slog.Error("Token 刷新失败", "err", err)
			} else {
				slog.Info("Token 刷新成功")
			}
		case <-ctx.Done():
			slog.Info("Token Watcher 停止")
			return
		}
	}
}

// StartWatcher 便捷启动函数：检查驱动是否支持 TokenManager，支持则启动维护
func StartWatcher(ctx context.Context, drv driver.Driver, interval time.Duration) {
	if tm, ok := drv.(driver.TokenManager); ok {
		watcher := NewTokenWatcher(tm, interval)
		go watcher.Start(ctx)
	} else {
		slog.Debug("驱动不支持 TokenManager，跳过后台维护")
	}
}
