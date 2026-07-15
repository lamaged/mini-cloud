package mobile

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"mini-cloud/internal/driver"
	"mini-cloud/internal/model"
)

// Driver 中国移动云盘驱动（和彩云/139云盘）
type Driver struct {
	name          string // 挂载名称，用于 state 文件命名
	authorization string // Base64 编码的认证令牌
	stateDir      string

	client   *Client
	rootID   string   // 根目录 ID
	pathCache map[string]string // 路径→ID 缓存
	cacheMu   sync.RWMutex
}

// New 创建移动云盘驱动
func New(name, authorization, stateDir string) *Driver {
	return &Driver{
		name:          name,
		authorization: authorization,
		stateDir:      stateDir,
		client:        NewClient(),
		rootID:        "/",
		pathCache:     make(map[string]string),
	}
}

func (d *Driver) Name() string {
	return "mobile"
}

// Init 初始化：设置认证、发现主机、刷新 Token
func (d *Driver) Init(ctx context.Context) error {
	if d.authorization == "" {
		return fmt.Errorf("移动云盘需要配置 authorization（Base64 编码的认证令牌）")
	}

	// 尝试从持久化状态恢复
	token, err := LoadToken(d.name, d.stateDir)
	if err == nil && token != nil {
		slog.Info("从缓存恢复移动云盘会话", "account", token.Account)
		d.client.SetAuth(token.Authorization, token.Account, token.CloudHost)
		d.authorization = token.Authorization

		// 检查主机是否仍然可用
		if token.CloudHost != "" {
			return nil
		}
	}

	// 设置认证信息
	d.client.SetAuth(d.authorization, "", "")

	// 设置 401 自动刷新回调（运行时安全网）
	d.client.SetAuthRefreshCallback(func() error {
		slog.Info("移动云盘 401，触发 Token 刷新")
		if err := d.client.refreshToken(); err != nil {
			return err
		}
		// 更新持久化状态
		token := &TokenState{
			Authorization: d.client.authorization,
			Account:       d.client.account,
			CloudHost:     d.client.cloudHost,
			ExpiresAt:     time.Now().Unix() + 86400*30,
		}
		SaveToken(d.name, d.stateDir, token)
		return nil
	})

	// 刷新 Token（如果需要）
	if err := d.client.refreshToken(); err != nil {
		slog.Warn("移动云盘 token 刷新失败，尝试直接使用", "err", err)
	}

	// 发现云主机
	if err := d.client.discoverCloudHost(); err != nil {
		return fmt.Errorf("发现云主机失败: %w", err)
	}

	// 保存状态
	token = &TokenState{
		Authorization: d.client.authorization,
		Account:       d.client.account,
		CloudHost:     d.client.cloudHost,
		ExpiresAt:     time.Now().Unix() + 86400*30,
	}
	if err := SaveToken(d.name, d.stateDir, token); err != nil {
		slog.Warn("保存 token 失败", "err", err)
	}

	return nil
}

// ============================================================
// List 实现
// ============================================================

func (d *Driver) List(ctx context.Context, reqPath string) ([]model.Obj, error) {
	folderID, err := d.resolveFolderID(ctx, reqPath)
	if err != nil {
		return nil, err
	}

	return d.listFolder(ctx, folderID)
}

// resolveFolderID 根据路径解析文件夹 ID
func (d *Driver) resolveFolderID(ctx context.Context, reqPath string) (string, error) {
	if reqPath == "/" || reqPath == "" {
		return d.rootID, nil
	}

	// 检查缓存
	d.cacheMu.RLock()
	if id, ok := d.pathCache[reqPath]; ok {
		d.cacheMu.RUnlock()
		return id, nil
	}
	d.cacheMu.RUnlock()

	// 逐级查找
	parts := splitPath(reqPath)
	id, err := d.resolveByWalk(ctx, d.rootID, parts)
	if err != nil {
		return "", err
	}

	d.cacheMu.Lock()
	d.pathCache[reqPath] = id
	d.cacheMu.Unlock()
	return id, nil
}

// resolveByWalk 从指定文件夹开始，逐级查找子文件夹 ID
func (d *Driver) resolveByWalk(ctx context.Context, parentID string, parts []string) (string, error) {
	if len(parts) == 0 {
		return parentID, nil
	}

	items, err := d.listRawFolder(ctx, parentID)
	if err != nil {
		return "", err
	}

	targetName := parts[0]

	for _, item := range items {
		if item.Type == "folder" && item.Name == targetName {
			return d.resolveByWalk(ctx, item.FileId, parts[1:])
		}
	}

	return "", fmt.Errorf("目录不存在: %s", targetName)
}

// ============================================================
// Link 实现
// ============================================================

func (d *Driver) Link(ctx context.Context, filePath string) (*model.Link, error) {
	parentPath := path.Dir(filePath)
	fileName := path.Base(filePath)

	if parentPath == "." {
		parentPath = "/"
	}

	// 获取父目录 ID
	folderID, err := d.resolveFolderID(ctx, parentPath)
	if err != nil {
		return nil, fmt.Errorf("查找父目录失败: %w", err)
	}

	// 查找文件
	items, err := d.listRawFolder(ctx, folderID)
	if err != nil {
		return nil, fmt.Errorf("列出目录失败: %w", err)
	}

	var fileID string
	for _, item := range items {
		if item.Type == "file" && item.Name == fileName {
			fileID = item.FileId
			break
		}
	}
	if fileID == "" {
		return nil, os.ErrNotExist
	}

	// 获取下载链接
	return d.getDownloadLink(ctx, fileID)
}

// getDownloadLink 获取文件下载链接
func (d *Driver) getDownloadLink(ctx context.Context, fileID string) (*model.Link, error) {
	var result DownloadUrlResp

	_, err := d.client.personalRequest("/file/getDownloadUrl", map[string]interface{}{
		"fileId": fileID,
	}, &result)
	if err != nil {
		return nil, fmt.Errorf("获取下载链接失败: %w", err)
	}

	downloadURL := result.Data.CdnUrl
	if downloadURL == "" {
		downloadURL = result.Data.Url
	}
	if downloadURL == "" {
		return nil, fmt.Errorf("no download URL available")
	}

	return &model.Link{
		URL: downloadURL,
	}, nil
}

// ============================================================
// 内部方法
// ============================================================

// listFolder 列出文件夹内容（返回 model.Obj）
func (d *Driver) listFolder(ctx context.Context, folderID string) ([]model.Obj, error) {
	items, err := d.listRawFolder(ctx, folderID)
	if err != nil {
		return nil, err
	}

	objs := make([]model.Obj, 0, len(items))
	for _, item := range items {
		objs = append(objs, model.Obj{
			Name:    item.Name,
			Size:    item.Size,
			ModTime: parseTime(item.UpdatedAt),
			IsDir:   item.Type == "folder",
		})
	}

	return objs, nil
}

// listRawFolder 列出文件夹原始数据
func (d *Driver) listRawFolder(ctx context.Context, folderID string) ([]PersonalFileItem, error) {
	var allItems []PersonalFileItem
	nextPageCursor := ""

	for {
		var result PersonalListResp
		_, err := d.client.personalRequest("/file/list", map[string]interface{}{
			"imageThumbnailStyleList": []string{"Small", "Large"},
			"orderBy":                 "updated_at",
			"orderDirection":          "DESC",
			"pageInfo": map[string]interface{}{
				"pageCursor": nextPageCursor,
				"pageSize":   100,
			},
			"parentFileId": folderID,
		}, &result)
		if err != nil {
			return nil, err
		}

		allItems = append(allItems, result.Data.Items...)

		nextPageCursor = result.Data.NextPageCursor
		if nextPageCursor == "" {
			break
		}
	}

	return allItems, nil
}

// ============================================================
// 工具函数
// ============================================================

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	// 个人云新版 API 时间格式："2024-01-15T20:30:00.000+08:00"
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		// 尝试旧格式 "20060102150405"
		t2, err2 := time.ParseInLocation("20060102150405", s, time.Local)
		if err2 != nil {
			return time.Time{}
		}
		return t2
	}
	return t
}

var _ driver.Driver = (*Driver)(nil)
