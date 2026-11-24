// Package baidu 百度网盘驱动。
// 2026-11-24 起（铁律 2 修订，PM 拍板）重新纳入支持范围：OAuth 凭据体系正式维护——
// 用户自有百度开放平台应用凭据必填（client_id/client_secret），管理页可更新 refresh_token；
// 加密（Vault）仍不支持百度（铁律 2 保留条款）。
package baidu

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path"
	"sync"
	"time"

	"mini-cloud/internal/driver"
	"mini-cloud/internal/model"
)

// Driver 百度网盘驱动
type Driver struct {
	name         string
	mu           sync.RWMutex // 保护 refreshToken
	refreshToken string
	clientID     string
	clientSecret string
	stateDir     string

	client *Client

	// 待提交凭证（CredentialUpdater 两阶段：Validate 暂存、Commit 生效；按 __req 隔离并发更新）
	pendingMu sync.Mutex
	pending   map[string]*pendingCred
}

// New 创建百度网盘驱动（client_id/client_secret 必填，无内置默认值——
// 内置公共凭据已按 PM 拍板删除，DESIGN-2026-11-24-01 §6.1）
func New(name, refreshToken, clientID, clientSecret, stateDir string) *Driver {
	return &Driver{
		name:         name,
		refreshToken: refreshToken,
		clientID:     clientID,
		clientSecret: clientSecret,
		stateDir:     stateDir,
		client:       NewClient(),
		pending:      make(map[string]*pendingCred),
	}
}

func (d *Driver) Name() string {
	return "baidu"
}

// getRefreshToken 线程安全地读取 refresh_token
func (d *Driver) getRefreshToken() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.refreshToken
}

// setRefreshToken 线程安全地设置 refresh_token
func (d *Driver) setRefreshToken(t string) {
	d.mu.Lock()
	d.refreshToken = t
	d.mu.Unlock()
}

// Init 初始化：校验 OAuth 应用凭据与 refresh_token，刷新或从缓存恢复会话。
// config 为 refresh_token 唯一真值源（DESIGN-2026-11-24-01 §6.5）：state 缓存仅当
// 其 refresh_token 与 config 一致时才复用 access_token——防旧缓存覆盖新授权
// （否则管理页更新后重启会被旧缓存吞掉，D2 重启恢复验收）。
func (d *Driver) Init(ctx context.Context) error {
	// 设置 401 自动刷新回调（运行时安全网——必须在任何 early return 之前注册）
	d.client.SetAuthRefreshCallback(func() error {
		slog.Info("百度网盘 token 过期，自动刷新")
		rt := d.getRefreshToken()
		accessToken, newRefresh, expiresIn, err := d.client.refreshToken(rt, d.clientID, d.clientSecret)
		if err != nil {
			return err
		}
		d.client.SetAccessToken(accessToken)
		if newRefresh != "" {
			d.setRefreshToken(newRefresh)
			rt = newRefresh
		}
		SaveToken(d.name, d.stateDir, &TokenState{
			RefreshToken: rt,
			AccessToken:  accessToken,
			ExpiresAt:    time.Now().Unix() + int64(expiresIn),
		})
		return nil
	})

	if d.clientID == "" || d.clientSecret == "" {
		return fmt.Errorf("百度网盘 OAuth 应用凭据未配置（config.json 需填写 client_id 与 client_secret，来自百度网盘开放平台应用）")
	}
	if d.getRefreshToken() == "" {
		return fmt.Errorf("百度网盘尚未授权（请在管理页粘贴 refresh_token 更新）")
	}

	token, err := LoadToken(d.name, d.stateDir)
	if err == nil && token != nil && token.RefreshToken == d.getRefreshToken() {
		d.client.SetAccessToken(token.AccessToken)
		errno, _ := d.client.apiGetWithErrno("/nas", map[string]string{"method": "uinfo"}, nil)
		if errno == 0 {
			slog.Info("从缓存恢复百度网盘会话")
			return nil
		}
		slog.Warn("缓存 token 已失效，重新刷新")
	}

	accessToken, newRefresh, expiresIn, err := d.client.refreshToken(d.getRefreshToken(), d.clientID, d.clientSecret)
	if err != nil {
		return fmt.Errorf("百度网盘 token 刷新失败: %w", err)
	}

	d.client.SetAccessToken(accessToken)
	if newRefresh != "" {
		// 批 1：轮换的新 refresh_token 只进内存与 state 缓存；
		// 批 2（§6.5/§6.6）实现 token_revision +1 原子写回 config。
		d.setRefreshToken(newRefresh)
	}

	token = &TokenState{RefreshToken: d.getRefreshToken(), AccessToken: accessToken, ExpiresAt: time.Now().Unix() + int64(expiresIn)}
	if err := SaveToken(d.name, d.stateDir, token); err != nil {
		slog.Warn("保存 token 失败", "err", err)
	}
	return nil
}

// ============================================================
// List 实现
// ============================================================

func (d *Driver) List(ctx context.Context, reqPath string) ([]model.Obj, error) {
	var result ListResp

	_, err := d.client.apiGet("/file", map[string]string{
		"method": "list", "dir": reqPath, "limit": "1000", "web": "1",
	}, &result)
	if err != nil {
		return nil, err
	}
	if result.Errno != 0 {
		return nil, fmt.Errorf("百度网盘 errno=%d", result.Errno)
	}

	objs := make([]model.Obj, 0, len(result.List))
	for _, f := range result.List {
		objs = append(objs, model.Obj{
			Name:    f.ServerFilename,
			Size:    f.Size,
			ModTime: time.Unix(f.ServerMtime, 0),
			IsDir:   f.Isdir == 1,
		})
	}
	return objs, nil
}

// ============================================================
// Link 实现（服务端代理下载）
// ============================================================

func (d *Driver) Link(ctx context.Context, filePath string) (*model.Link, error) {
	parentPath := path.Dir(filePath)
	fileName := path.Base(filePath)
	if parentPath == "." {
		parentPath = "/"
	}

	// 1. 从父目录列表中找到文件 fs_id
	var listResp ListResp
	_, err := d.client.apiGet("/file", map[string]string{
		"method": "list", "dir": parentPath, "limit": "1000", "web": "1",
	}, &listResp)
	if err != nil {
		return nil, err
	}

	var fsID int64
	for _, f := range listResp.List {
		if f.ServerFilename == fileName && f.Isdir == 0 {
			fsID = f.FsId
			break
		}
	}
	if fsID == 0 {
		return nil, os.ErrNotExist
	}

	// 2. 获取 dlink
	var dlResp DownloadResp
	_, err = d.client.apiGet("/multimedia", map[string]string{
		"method": "filemetas", "fsids": fmt.Sprintf("[%d]", fsID), "dlink": "1",
	}, &dlResp)
	if err != nil || dlResp.Errno != 0 || len(dlResp.List) == 0 {
		return nil, fmt.Errorf("获取下载链接失败")
	}

	// 3. 服务端代理下载（绕过客户端 UA 限制）
	dlink := dlResp.List[0].Dlink + "&access_token=" + d.client.getAccessToken()
	req, _ := http.NewRequestWithContext(ctx, "GET", dlink, nil)
	req.Header.Set("User-Agent", "netdisk")

	// 转发客户端 Range 头给 CDN（支持视频拖动/seek）
	if rh := model.ReqHeadersFromCtx(ctx); rh != nil {
		if rng := rh.Get("Range"); rng != "" {
			req.Header.Set("Range", rng)
		}
	}

	resp, err := d.client.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	hdr := http.Header{}
	for k, vs := range resp.Header {
		for _, v := range vs {
			hdr.Set(k, v)
		}
	}

	return &model.Link{
		Reader:     resp.Body,
		Header:     hdr,
		StatusCode: resp.StatusCode,
	}, nil
}

// ============================================================
// TokenManager 实现
// ============================================================

func (d *Driver) NeedRefresh() bool {
	token, err := LoadToken(d.name, d.stateDir)
	if err != nil {
		return true
	}
	return time.Now().Unix()+300 > token.ExpiresAt
}

func (d *Driver) RefreshToken(ctx context.Context) error {
	rt := d.getRefreshToken()
	accessToken, newRefresh, expiresIn, err := d.client.refreshToken(rt, d.clientID, d.clientSecret)
	if err != nil {
		return err
	}
	d.client.SetAccessToken(accessToken)
	if newRefresh != "" {
		d.setRefreshToken(newRefresh)
		rt = newRefresh
	}
	SaveToken(d.name, d.stateDir, &TokenState{
		RefreshToken: rt,
		AccessToken:  accessToken,
		ExpiresAt:    time.Now().Unix() + int64(expiresIn),
	})
	return nil
}

var _ driver.Driver = (*Driver)(nil)
var _ driver.TokenManager = (*Driver)(nil)
