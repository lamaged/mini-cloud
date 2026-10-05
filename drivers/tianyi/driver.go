package tianyi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"mini-cloud/internal/driver"
	"mini-cloud/internal/model"
)

// Driver 天翼云盘驱动
type Driver struct {
	// 配置
	name     string // 挂载名称，用于 state 文件命名
	username string
	password string
	familyID string // 家庭云 ID（非空时使用家庭云 API）
	stateDir string

	// API 客户端
	client *Client

	// 缓存：路径 → 文件夹 ID 的映射（减少 API 调用）
	pathCache map[string]string
	cacheMu   sync.RWMutex
}

// New 创建天翼云盘驱动
func New(name, username, password, familyID, stateDir string) *Driver {
	return &Driver{
		name:      name,
		username:  username,
		password:  password,
		familyID:  familyID,
		stateDir:  stateDir,
		client:    NewClient(),
		pathCache: make(map[string]string),
	}
}

func (d *Driver) isFamily() bool {
	return d.familyID != ""
}

func (d *Driver) Name() string {
	return "tianyi"
}

// Init 初始化：登录或从缓存恢复会话
func (d *Driver) Init(ctx context.Context) error {
	// 设置 401 自动刷新回调（运行时安全网）
	d.client.SetAuthRefreshCallback(func() error {
		slog.Info("天翼云盘 session 过期，自动刷新")
		if err := d.client.refreshSession(); err != nil {
			return err
		}
		sk, ss, at, rt, fsk, fss := d.client.snapshot()
		token := NewTokenState(sk, ss, at, rt, d.familyID, fsk, fss, "")
		SaveToken(d.name, d.stateDir, token)
		return nil
	})

	// 尝试从持久化状态恢复会话
	token, err := LoadToken(d.name, d.stateDir)
	if err == nil && token != nil {
		slog.Info("从缓存恢复天翼云盘会话", "loginName", token.LoginName, "family", token.FamilyID)
		d.client.SetSession(token.SessionKey, token.SessionSecret, token.AccessToken, token.RefreshToken)
		if token.FamilyID != "" && token.FamilySessionKey != "" {
			d.client.SetFamilySession(token.FamilySessionKey, token.FamilySessionSecret)
			d.familyID = token.FamilyID
			slog.Info("已恢复家庭云会话", "familyID", token.FamilyID)
		} else if d.isFamily() {
			slog.Warn("配置了 family_id 但缓存中没有家庭云会话，将在登录后重新获取")
		}

		// 尝试用现有会话请求（触发自动刷新）
		if err := d.tryRefreshSession(); err != nil {
			slog.Warn("缓存会话已失效，重新登录", "err", err)
			// 继续走完整登录
		} else {
			return nil
		}
	}

	// 完整登录
	if d.username == "" || d.password == "" {
		return fmt.Errorf("天翼云盘需要配置 username 和 password")
	}

	if err := d.client.login(d.username, d.password); err != nil {
		return fmt.Errorf("天翼云盘登录失败: %w", err)
	}

	// 保存 token（含家庭云 session）
	sk, ss, at, rt, fsk, fss := d.client.snapshot()
	token = NewTokenState(sk, ss, at, rt, d.familyID, fsk, fss, "")
	if err := SaveToken(d.name, d.stateDir, token); err != nil {
		slog.Warn("保存 token 失败", "err", err)
	}

	if d.isFamily() && !d.client.hasFamilySession() {
		slog.Warn("家庭云登录成功但未获取到 familySessionKey，家庭云功能可能不可用")
	}

	return nil
}

// tryRefreshSession 尝试用 refresh token 刷新会话
func (d *Driver) tryRefreshSession() error {
	// 简单验证：尝试获取用户信息
	fullUrl := API_URL + "/getUserInfo.action"
	params := clientSuffix()
	params.Set("params", "")

	reqUrl := fullUrl + "?" + params.Encode()
	req, _ := http.NewRequest("GET", reqUrl, nil)
	for k, v := range d.client.signatureHeader("GET", fullUrl, "") {
		req.Header.Set(k, v)
	}

	resp, err := d.client.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("会话验证失败: status=%d", resp.StatusCode)
	}
	return nil
}

// ============================================================
// List 实现
// ============================================================

func (d *Driver) List(ctx context.Context, path string) ([]model.Obj, error) {
	folderID, err := d.resolveFolderID(ctx, path)
	if err != nil {
		return nil, err
	}

	return d.listFolder(ctx, folderID)
}

// resolveFolderID 根据路径解析文件夹 ID
func (d *Driver) resolveFolderID(ctx context.Context, reqPath string) (string, error) {
	if reqPath == "/" || reqPath == "" {
		if d.isFamily() {
			return "", nil // 家庭云根目录 ID 为空
		}
		return ROOT_FOLDER_ID, nil
	}

	// 检查缓存
	d.cacheMu.RLock()
	if id, ok := d.pathCache[reqPath]; ok {
		d.cacheMu.RUnlock()
		return id, nil
	}
	d.cacheMu.RUnlock()

	// 逐级从根目录递归查找
	parts := splitPath(reqPath)
	rootID := ROOT_FOLDER_ID
	if d.isFamily() {
		rootID = ""
	}
	id, err := d.resolveByWalk(ctx, rootID, parts)
	if err != nil {
		return "", err
	}

	// 缓存结果
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

	// 获取原始 API 数据来获取文件夹 ID
	data, err := d.listRawFolder(ctx, parentID)
	if err != nil {
		return "", err
	}

	targetName := parts[0]

	// 在文件夹列表中查找
	for _, folder := range data.FileListAO.FolderList {
		if folder.Name == targetName {
			// 找到了，继续向下
			return d.resolveByWalk(ctx, string(folder.ID), parts[1:])
		}
	}

	slog.Error("resolveByWalk失败", "parentID", parentID, "target", targetName, "folders", len(data.FileListAO.FolderList))
		return "", fmt.Errorf("目录不存在: %s", targetName)
}

// ============================================================
// Link 实现
// ============================================================

func (d *Driver) Link(ctx context.Context, filePath string) (*model.Link, error) {
	// 解析文件路径：父目录路径 + 文件名
	parentPath := path.Dir(filePath)
	fileName := path.Base(filePath)

	if parentPath == "." {
		parentPath = "/"
	}

	// 获取父目录的文件夹 ID
	folderID, err := d.resolveFolderID(ctx, parentPath)
	if err != nil {
		return nil, fmt.Errorf("查找父目录失败: %w", err)
	}

	// 在文件夹中查找文件
	data, err := d.listRawFolder(ctx, folderID)
	if err != nil {
		return nil, fmt.Errorf("列出目录失败: %w", err)
	}

	var fileID string
	for _, f := range data.FileListAO.FileList {
		if f.Name == fileName {
			fileID = string(f.ID)
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
	var result DownloadURLResp

	fullUrl := API_URL
	if d.isFamily() {
		fullUrl += "/family/file"
	}
	fullUrl += "/getFileDownloadUrl.action"

	params := Params{
		"fileId": fileID,
	}
	if d.isFamily() {
		params.Set("familyId", d.familyID)
	} else {
		params.Set("dt", "3")
		params.Set("flag", "1")
	}

	_, err := d.client.apiRequestEx("GET", fullUrl, params, &result, d.isFamily())
	if err != nil {
		return nil, fmt.Errorf("获取下载链接失败: %w", err)
	}

	// 清理 URL（替换 &amp; 和强制 https）
	downloadURL := strings.ReplaceAll(result.URL, "&amp;", "&")
	downloadURL = strings.Replace(downloadURL, "http://", "https://", 1)

	// 跟随重定向获取真实链接
	redirectURL, err := d.followRedirect(ctx, downloadURL)
	if err != nil {
		slog.Warn("跟随下载重定向失败，使用原始URL", "err", err)
		redirectURL = downloadURL
	}

	return &model.Link{
		URL: redirectURL,
		Header: http.Header{
			"User-Agent": {"Mozilla/5.0"},
		},
	}, nil
}

// followRedirect 跟随 302 重定向获取真实下载 URL
func (d *Driver) followRedirect(ctx context.Context, urlStr string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := NoRedirectClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 302 {
		loc := resp.Header.Get("Location")
		if loc != "" {
			return loc, nil
		}
	}
	// 没有重定向，返回原始 URL
	return urlStr, nil
}

// ============================================================
// 内部方法
// ============================================================

// listFolder 列出文件夹内容（返回 model.Obj 列表）
func (d *Driver) listFolder(ctx context.Context, folderID string) ([]model.Obj, error) {
	data, err := d.listRawFolder(ctx, folderID)
	if err != nil {
		return nil, err
	}

	objs := make([]model.Obj, 0, len(data.FileListAO.FolderList)+len(data.FileListAO.FileList))

	for _, folder := range data.FileListAO.FolderList {
		objs = append(objs, model.Obj{
			Name:    folder.Name,
			Size:    0,
			ModTime: parseTime(folder.LastOpTime),
			IsDir:   true,
		})
	}

	for _, file := range data.FileListAO.FileList {
		objs = append(objs, model.Obj{
			Name:    file.Name,
			Size:    file.Size,
			ModTime: parseTime(file.LastOpTime),
			IsDir:   false,
		})
	}

	return objs, nil
}

// listRawFolder 列出文件夹原始数据（返回 API 原始响应，自动处理分页）
func (d *Driver) listRawFolder(ctx context.Context, folderID string) (*Cloud189FilesResp, error) {
	const pageSize = 1000
	var merged Cloud189FilesResp

	fullUrl := API_URL
	if d.isFamily() {
		fullUrl += "/family/file"
	}
	fullUrl += "/listFiles.action"

	for pageNum := 1; ; pageNum++ {
		params := Params{
			"folderId":   folderID,
			"fileType":   "0",
			"mediaAttr":  "0",
			"iconOption": "5",
			"pageNum":    fmt.Sprintf("%d", pageNum),
			"pageSize":   fmt.Sprintf("%d", pageSize),
			"recursive":  "0",
			"orderBy":    "filename",
			"descending": "false",
		}

		if d.isFamily() {
			params.Set("familyId", d.familyID)
			params.Set("orderBy", "1")
			params.Set("descending", "false")
		}

		var result Cloud189FilesResp
		_, err := d.client.apiRequestEx("GET", fullUrl, params, &result, d.isFamily())
		if err != nil {
			return nil, err
		}

		merged.FileListAO.FolderList = append(merged.FileListAO.FolderList, result.FileListAO.FolderList...)
		merged.FileListAO.FileList = append(merged.FileListAO.FileList, result.FileListAO.FileList...)
		merged.FileListAO.Count = result.FileListAO.Count // 每页返回相同的总数

		// 判断是否还有下一页：当前页条目数不足 pageSize 或累积数量已达总数
		thisPageItems := len(result.FileListAO.FolderList) + len(result.FileListAO.FileList)
		totalItems := len(merged.FileListAO.FolderList) + len(merged.FileListAO.FileList)
		if thisPageItems < pageSize || totalItems >= result.FileListAO.Count {
			break
		}
	}

	return &merged, nil
}

// ============================================================
// 工具函数
// ============================================================

// splitPath 分割路径
func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// parseTime 解析云盘时间格式 "2024-01-15 20:30:00"
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	// 尝试多种格式
	formats := []string{
		"2006-01-02 15:04:05",
		"2006-01-02 15:04:05 -07",
	}
	for _, f := range formats {
		t, err := time.ParseInLocation(f, strings.TrimSpace(s)+" +08", time.Local)
		if err == nil {
			return t
		}
	}
	// 最后尝试用 UTC
	t, err := time.Parse("2006-01-02 15:04:05", strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t
}

var _ driver.Driver = (*Driver)(nil)
