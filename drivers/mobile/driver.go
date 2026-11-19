package mobile

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
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

	// 待提交凭证（CredentialUpdater 两阶段：Validate 暂存、Commit 生效；按 __req 隔离并发更新）
	pendingMu sync.Mutex
	pending   map[string]*pendingCred

	// 已提交凭证快照与代次：401 回调刷新期间若发生管理页提交，丢弃过期刷新结果
	credGen      atomic.Uint64
	commitMu     sync.Mutex
	commitAuth   string
	commitAccount string
	commitHost   string
	hostWarn     string // 启动期云主机发现失败提示（§5.3；commitMu 保护，随提交成功清除）

	// 路径缓存代次：提交新凭证时递增，使在途解析结果作废
	cacheGen atomic.Uint64
}

// pendingCred 一次 ValidateCredential 暂存的待提交凭证
type pendingCred struct {
	auth      string
	account   string
	host      string
	createdAt time.Time
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
		pending:       make(map[string]*pendingCred),
	}
}

func (d *Driver) Name() string {
	return "mobile"
}

// HealthWarning 返回健康提示：authorization 过期时提示用户更新（文案 PM 拍板 2026-11-19）；
// 未过期但启动期云主机发现失败时提示服务受限（§5.3，接 /admin 展示 §5.2）
func (d *Driver) HealthWarning() string {
	if d.client.isExpired() {
		return "令牌已过期，请更新令牌"
	}
	d.commitMu.Lock()
	w := d.hostWarn
	d.commitMu.Unlock()
	return w
}

// Init 初始化：设置认证、发现主机、刷新 Token
func (d *Driver) Init(ctx context.Context) error {
	if d.authorization == "" {
		return fmt.Errorf("移动云盘需要配置 authorization（Base64 编码的认证令牌）")
	}

	// 设置 401 自动刷新回调（必须在任何 early return 之前，确保运行时安全网始终生效）
	d.client.SetAuthRefreshCallback(d.handleAuthFailed)

	// 尝试从持久化状态恢复
	token, err := LoadToken(d.name, d.stateDir)
	if err == nil && token != nil {
		// 比较 config 与缓存的 token 过期时间，选择更晚（更新）的：
		// - 用户手动更新 config → config token 更新 → 用 config，丢弃缓存
		// - 服务自动刷新 → 缓存 token 更新 → 用缓存
		useCache := true
		if token.Authorization != d.authorization {
			_, _, _, cacheExp, _ := parseAuthString(token.Authorization)
			_, _, _, configExp, _ := parseAuthString(d.authorization)
			if configExp > cacheExp {
				useCache = false
			}
		}

		if useCache {
			slog.Info("从缓存恢复移动云盘会话", "account", token.Account)
			d.client.SetAuth(token.Authorization, token.Account, token.CloudHost)
			d.authorization = token.Authorization

			// 检查主机是否仍然可用
			if token.CloudHost != "" {
				// 检查 token 是否临近过期/已过期，若是则启动时刷新
				if d.client.needRefresh() {
					slog.Info("缓存 token 临近过期或已过期，启动时刷新")
					if refreshErr := d.client.refreshToken(); refreshErr != nil {
						slog.Warn("缓存 token 刷新失败，将使用现有 token（运行时 401 会再次尝试）", "err", refreshErr)
					} else if err := d.saveTokenState(); err != nil {
						slog.Warn("保存 token 失败", "err", err)
					}
				}
				return nil
			}
		} else {
			slog.Info("config 的 authorization 比缓存更新，使用 config token")
		}
	}

	// 设置认证信息
	d.client.SetAuth(d.authorization, "", "")

	// 刷新 Token（如果需要）
	if err := d.client.refreshToken(); err != nil {
		slog.Warn("移动云盘 token 刷新失败，尝试直接使用", "err", err)
	}

	// 发现云主机（§5.3 启动健壮性：短超时 ≤10s；失败不阻断启动、不重试——
	// 无 host 时 List/Link 已按「过期语义」返回空目录/明确错误，
	// HealthWarning 提示用户去 /admin 更新；云主机可从缓存恢复或随凭证更新获得）
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = d.client.discoverCloudHost(dctx)
	cancel()
	if err != nil {
		d.commitMu.Lock()
		d.hostWarn = "移动云盘云主机发现失败（网络或授权问题），服务受限，请稍后重启或到 /admin 更新凭证"
		d.commitMu.Unlock()
		slog.Warn("移动云盘云主机发现失败，挂载按受限模式启动", "err", err)
	}

	if err := d.saveTokenState(); err != nil {
		slog.Warn("保存 token 失败", "err", err)
	}
	return nil
}

// handleAuthFailed 401 回调：刷新 token 并更新状态。
// 若刷新期间管理页提交了新凭证（credGen 变化），丢弃本次刷新结果并恢复已提交凭证。
func (d *Driver) handleAuthFailed() error {
	slog.Info("移动云盘 401，触发 Token 刷新")
	gen := d.credGen.Load()
	if err := d.client.refreshToken(); err != nil {
		return err
	}
	if d.credGen.Load() != gen {
		// 刷新期间管理页提交了新凭证：丢弃本次刷新结果，恢复已提交凭证
		d.commitMu.Lock()
		a, ac, h := d.commitAuth, d.commitAccount, d.commitHost
		d.commitMu.Unlock()
		if a != "" {
			d.client.SetAuth(a, ac, h)
			if err := d.saveTokenState(); err != nil {
				slog.Warn("保存 token 失败", "err", err)
			}
		}
		return nil
	}
	if err := d.saveTokenState(); err != nil {
		slog.Warn("保存 token 失败", "err", err)
	}
	return nil
}

// saveTokenState 保存当前认证状态到磁盘（过期时间从 Authorization 真实提取）
func (d *Driver) saveTokenState() error {
	expiresAt := time.Now().Unix() + 86400*30 // 兜底值
	if _, _, _, expiration, err := d.client.parseAuthorization(); err == nil {
		expiresAt = expiration / 1000
	}
	token := &TokenState{
		Authorization: d.client.getAuthorization(),
		Account:       d.client.getAccount(),
		CloudHost:     d.client.getCloudHost(),
		ExpiresAt:     expiresAt,
	}
	if err := SaveToken(d.name, d.stateDir, token); err != nil {
		return fmt.Errorf("保存 token 失败: %w", err)
	}
	return nil
}

// ============================================================
// List 实现
// ============================================================

func (d *Driver) List(ctx context.Context, reqPath string) ([]model.Obj, error) {
	if d.client.isExpired() || d.client.getCloudHost() == "" {
		// authorization 已过期或云主机不可用（启动期发现失败，P4）：返回空目录，
		// 避免 500，由 HealthWarning 提示用户
		return nil, nil
	}

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

	// 逐级查找（记录代次：若期间凭证被提交并清空了缓存，本次结果作废）
	parts := splitPath(reqPath)
	gen := d.cacheGen.Load()
	id, err := d.resolveByWalk(ctx, d.rootID, parts)
	if err != nil {
		return "", err
	}

	d.cacheMu.Lock()
	if d.cacheGen.Load() == gen {
		d.pathCache[reqPath] = id
	}
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
	if d.client.isExpired() {
		return nil, fmt.Errorf("移动云盘 authorization 已过期，请更新 config.json")
	}
	if d.client.getCloudHost() == "" {
		return nil, fmt.Errorf("移动云盘云主机不可用（启动期发现失败），请到 /admin 更新凭证或稍后重启")
	}

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

	_, err := d.client.personalRequest(ctx, "/file/getDownloadUrl", map[string]interface{}{
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
		_, err := d.client.personalRequest(ctx, "/file/list", map[string]interface{}{
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
var _ driver.HealthReporter = (*Driver)(nil)
