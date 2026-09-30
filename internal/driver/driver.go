package driver

import (
	"context"
	"mini-cloud/internal/model"
)

// Driver 云盘驱动接口（只读）
type Driver interface {
	Meta
	Reader
}

// Meta 驱动元信息和生命周期
type Meta interface {
	Name() string
	Init(ctx context.Context) error
}

// Reader 读取操作
type Reader interface {
	// List 列出目录内容。path 是相对于云盘根目录的路径
	List(ctx context.Context, path string) ([]model.Obj, error)
	// Link 获取文件的下载链接或数据流。path 是相对于云盘根目录的文件路径
	Link(ctx context.Context, path string) (*model.Link, error)
}

// TokenManager Token 生命周期管理（可选接口，云盘驱动可实现此接口支持后台自动刷新）
type TokenManager interface {
	// NeedRefresh 检查 Token 是否需要刷新
	NeedRefresh() bool
	// RefreshToken 刷新 Token
	RefreshToken(ctx context.Context) error
}

// HealthReporter 健康报告（可选接口，云盘驱动可实现此接口向上层展示异常提示）
type HealthReporter interface {
	// HealthWarning 返回需要展示给用户的提示信息；空字符串表示正常
	HealthWarning() string
}
