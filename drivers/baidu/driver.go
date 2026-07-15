package baidu

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path"
	"time"

	"mini-cloud/internal/driver"
	"mini-cloud/internal/model"
)

// Driver 百度网盘驱动
type Driver struct {
	name         string
	refreshToken string
	clientID     string
	clientSecret string
	stateDir     string

	client *Client
}

// New 创建百度网盘驱动
func New(name, refreshToken, clientID, clientSecret, stateDir string) *Driver {
	if clientID == "" {
		clientID = DefaultClientID
	}
	if clientSecret == "" {
		clientSecret = DefaultClientSecret
	}
	return &Driver{
		name:         name,
		refreshToken: refreshToken,
		clientID:     clientID,
		clientSecret: clientSecret,
		stateDir:     stateDir,
		client:       NewClient(),
	}
}

func (d *Driver) Name() string {
	return "baidu"
}

// Init 初始化：刷新 token 或从缓存恢复
func (d *Driver) Init(ctx context.Context) error {
	if d.refreshToken == "" {
		return fmt.Errorf("百度网盘需要配置 refresh_token")
	}

	// 设置 401 自动刷新回调（运行时安全网）
	d.client.SetAuthRefreshCallback(func() error {
		slog.Info("百度网盘 token 过期，自动刷新")
		accessToken, newRefresh, expiresIn, err := d.client.refreshToken(d.refreshToken, d.clientID, d.clientSecret)
		if err != nil {
			return err
		}
		d.client.SetAccessToken(accessToken)
		if newRefresh != "" {
			d.refreshToken = newRefresh
		}
		SaveToken(d.name, d.stateDir, &TokenState{
			RefreshToken: d.refreshToken,
			AccessToken:  accessToken,
			ExpiresAt:    time.Now().Unix() + int64(expiresIn),
		})
		return nil
	})

	token, err := LoadToken(d.name, d.stateDir)
	if err == nil && token != nil {
		d.client.SetAccessToken(token.AccessToken)
		d.refreshToken = token.RefreshToken
		errno, _ := d.client.apiGetWithErrno("/nas", map[string]string{"method": "uinfo"}, nil)
		if errno == 0 {
			slog.Info("从缓存恢复百度网盘会话")
			return nil
		}
		slog.Warn("缓存 token 已失效，重新刷新")
	}

	accessToken, newRefresh, expiresIn, err := d.client.refreshToken(d.refreshToken, d.clientID, d.clientSecret)
	if err != nil {
		return fmt.Errorf("百度网盘 token 刷新失败: %w", err)
	}

	d.client.SetAccessToken(accessToken)
	if newRefresh != "" {
		d.refreshToken = newRefresh
	}

	token = &TokenState{RefreshToken: d.refreshToken, AccessToken: accessToken, ExpiresAt: time.Now().Unix() + int64(expiresIn)}
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
	dlink := dlResp.List[0].Dlink + "&access_token=" + d.client.accessToken
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
	accessToken, newRefresh, expiresIn, err := d.client.refreshToken(d.refreshToken, d.clientID, d.clientSecret)
	if err != nil {
		return err
	}
	d.client.SetAccessToken(accessToken)
	if newRefresh != "" {
		d.refreshToken = newRefresh
	}
	SaveToken(d.name, d.stateDir, &TokenState{
		RefreshToken: d.refreshToken,
		AccessToken:  accessToken,
		ExpiresAt:    time.Now().Unix() + int64(expiresIn),
	})
	return nil
}

var _ driver.Driver = (*Driver)(nil)
var _ driver.TokenManager = (*Driver)(nil)
