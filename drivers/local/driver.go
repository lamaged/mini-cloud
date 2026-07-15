package local

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"mini-cloud/internal/driver"
	"mini-cloud/internal/model"
)

// Driver 本地文件系统驱动
type Driver struct {
	root string // 根目录的绝对路径
}

// New 创建本地驱动
func New(root string) (*Driver, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	// 确保目录存在
	if err := os.MkdirAll(abs, 0755); err != nil {
		return nil, err
	}
	return &Driver{root: abs}, nil
}

func (d *Driver) Name() string {
	return "local"
}

func (d *Driver) Init(ctx context.Context) error {
	return nil // 本地驱动无需初始化
}

func (d *Driver) List(ctx context.Context, path string) ([]model.Obj, error) {
	fullPath, err := d.safePath(path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		return nil, err
	}

	objs := make([]model.Obj, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		objs = append(objs, model.Obj{
			Name:    entry.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
			IsDir:   entry.IsDir(),
		})
	}
	return objs, nil
}

func (d *Driver) Link(ctx context.Context, path string) (*model.Link, error) {
	fullPath, err := d.safePath(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, os.ErrInvalid
	}

	file, err := os.Open(fullPath)
	if err != nil {
		return nil, err
	}

	return &model.Link{
		Reader: file,
		Header: http.Header{
			"Content-Type": {mimeType(path)},
		},
	}, nil
}

func (d *Driver) safePath(path string) (string, error) {
	// 安全防护：防止路径穿越
	clean := filepath.Clean(strings.TrimPrefix(path, "/"))
	// 使用 filepath.Join 后再 Clean 以确保解析所有 .. 组件
	result := filepath.Clean(filepath.Join(d.root, clean))
	// 确保结果路径在 root 之内
	if !strings.HasPrefix(result, d.root) {
		return "", os.ErrPermission
	}
	return result, nil
}

func mimeType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".mp4":
		return "video/mp4"
	case ".mkv":
		return "video/x-matroska"
	case ".avi":
		return "video/x-msvideo"
	case ".mp3":
		return "audio/mpeg"
	case ".flac":
		return "audio/flac"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".pdf":
		return "application/pdf"
	case ".txt":
		return "text/plain"
	case ".srt", ".ass", ".ssa":
		return "text/plain"
	default:
		return "application/octet-stream"
	}
}

// 确保实现接口
var _ driver.Driver = (*Driver)(nil)
