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
	"strings"
	"sync"
	"sync/atomic"
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

	// OAuth 授权状态（批 2 §6.4：state → 过期时间，一次性消费）
	oauthMu     sync.Mutex
	oauthStates map[string]time.Time

	// 批 2（DESIGN-2026-11-24-01 §6.5/§6.6）：刷新互斥（单飞）+ 限频 + 轮换写回
	refreshMu      sync.Mutex
	lastRefreshAt  time.Time                    // 最近一次刷新尝试时间（限频窗口）
	lastRefreshErr error                        // 最近一次刷新失败（60s 内复用，防刷新风暴）
	refreshInFlight *refreshResult              // 在途刷新结果（并发等待者复用，审核 P1-1）
	credGen       atomic.Int64                  // 当前授权代次（main 注入；轮换写回时防迟到覆盖；原子：写回回调内可改）
	rotatePersist func(ctx context.Context, expectedGen int, newRefreshToken string) error // main 注入：轮换原子写回 config

	// 健康事实（§6.10）：刷新失败但会话可用 → 黄；不可恢复失效 → 红；存储异常 → 红
	healthMu     sync.Mutex
	authDead     bool   // refresh_token 不可恢复失效（需重新授权）
	storageBad   bool   // 本地配置写回失败（存储异常，审核 P2-1）
	warnText     string // 警告文案
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
		oauthStates:  make(map[string]time.Time),
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

// SetCredentialGeneration 注入当前授权代次（main：启动时 + 管理页提交成功后同步）。
// 原子操作：可在轮换写回回调内调用（防迟到覆盖测试/回调路径）。
func (d *Driver) SetCredentialGeneration(g int) {
	d.credGen.Store(int64(g))
}

// SetRotationPersist 注入轮换写回函数（main：conf.RotateBaiduToken 原子写回 config；
// expectedGen 与当前配置代次不符时该函数应返回 nil 表示跳过）
func (d *Driver) SetRotationPersist(fn func(ctx context.Context, expectedGen int, newRefreshToken string) error) {
	d.refreshMu.Lock()
	d.rotatePersist = fn
	d.refreshMu.Unlock()
}

// refreshSingleFlight 刷新单飞 + 结果共享（§6.6.1 + 审核 P1-1/P1-A）：首个进入者执行兑换，
// 其余并发者在锁外等待其结果复用（N 并发 → token 端点仅 1 次请求）；
// 限频（§6.6.7）：失败后 60s 内复用失败结果，不打上游（成功即清零，防陈旧错误误伤）；
// 轮换（§6.5）：先写回 config（代次不符则跳过，防迟到覆盖）→ 成功才提交内存；
// 落盘失败 = 严重错误（§6.6.4/§6.10），内存不提交新值、健康事实报存储异常（红）。
func (d *Driver) refreshSingleFlight(ctx context.Context) error {
	d.refreshMu.Lock()
	// P1-D（RV-2026-11-25-05）：在途刷新优先于旧失败限频——若先判限频，
	// 旧失败记录会拦下本应加入在途新一轮的请求（A 开新轮刷新了时间戳、
	// 旧错误仍在窗口内，B 拿到旧失败结果，即使 A 最终成功也与 B 无关）
	if d.refreshInFlight != nil {
		rf := d.refreshInFlight // P1-A①：捕获本轮结果对象，醒来直接读它，不查全局
		d.refreshMu.Unlock()
		select {
		case <-rf.done:
			return rf.err // rf.err 在 close 前、锁内写入，happens-before 成立
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if d.lastRefreshErr != nil && time.Since(d.lastRefreshAt) < time.Minute {
		err := d.lastRefreshErr // 无在途刷新且失败未过 60s：复用失败结果，防刷新风暴
		d.refreshMu.Unlock()
		return err
	}
	rf := &refreshResult{done: make(chan struct{})}
	d.refreshInFlight = rf
	d.lastRefreshAt = time.Now()
	gen := int(d.credGen.Load())
	rt := d.getRefreshToken()
	d.refreshMu.Unlock()

	// 真正执行（不持 refreshMu：网络兑换与写回期间提交/代次同步可交错，
	// 靠临界区内的复检丢弃迟到结果——见 doRefresh）
	err := d.doRefresh(ctx, gen, rt)

	d.refreshMu.Lock()
	rf.err = err
	close(rf.done)
	d.refreshInFlight = nil
	if err != nil {
		d.lastRefreshErr = err
	} else {
		d.lastRefreshErr = nil // P1-A②：成功后清零，防陈旧失败误伤后续请求（误限频/白打上游）
	}
	d.refreshMu.Unlock()
	return err
}

// refreshResult 在途刷新结果（并发等待者复用）
type refreshResult struct {
	done chan struct{}
	err  error
}

// doRefresh 执行一次刷新：兑换 →（轮换则写回 config）→ 临界区（代次复检 + 内存提交）
// → 缓存落盘。
// P1-B（RV-2026-11-25-04）：「代次复检 → setRefreshToken → SetAccessToken」必须与
// CommitCredential 的内存提交同锁（refreshMu）——否则可交错成「旧刷新复检通过 →
// 用户提交新票 → 旧刷新把旧票写回内存」，运行内存与 config 不一致。
// 网络兑换与 rotatePersist 留在锁外（不破坏结果共享）；锁序 refreshMu → d.mu
// 与 CommitCredential 一致，cfgMu 仅出现在锁外的 persist 段，无环。
func (d *Driver) doRefresh(ctx context.Context, gen int, rt string) error {
	access, newRT, expiresIn, err := d.client.exchangeToken(ctx, rt, d.clientID, d.clientSecret)
	if err != nil {
		d.recordRefreshFailure(err)
		return err
	}
	d.clearRefreshFailure()

	if newRT != "" {
		persist := d.rotatePersist
		if persist != nil {
			if err := persist(ctx, gen, newRT); err != nil {
				d.recordStorageFailure()
				return err
			}
		}
	}

	d.refreshMu.Lock()
	defer d.refreshMu.Unlock()
	// 临界区内复检代次：写回期间（或之前）管理页提交了新凭据 → 丢弃本次结果，
	// 不得覆盖内存新票/新会话（防旧覆盖新，铁律 8 精神）
	if gen != int(d.credGen.Load()) {
		return nil
	}
	if newRT != "" {
		d.setRefreshToken(newRT)
	}
	d.client.SetAccessToken(access)

	// state 为纯缓存（§6.5）：写失败仅告警，access_token 丢失下次重新兑换
	if err := SaveToken(d.name, d.stateDir, &TokenState{
		V:                   1,
		AccessToken:         access,
		ExpiresAt:           time.Now().Unix() + int64(expiresIn),
		CredentialGeneration: gen,
	}); err != nil {
		slog.Warn("保存 token 缓存失败（state 为纯缓存，下次重新兑换）", "err", err)
	}
	return nil
}

// classifyBaiduRefreshErr 失效分类（§6.6.8）：不可恢复（票作废/已用过）→ 需重新授权；
// 其余（网络/临时）→ 暂败。
func classifyBaiduRefreshErr(err error) bool {
	msg := err.Error()
	for _, k := range []string{"expired_token", "has been used", "invalid_grant"} {
		if strings.Contains(msg, k) {
			return true
		}
	}
	return false
}

// recordRefreshFailure 记录刷新失败事实（供 Health 上报：红=不可恢复 / 黄=暂败）
func (d *Driver) recordRefreshFailure(err error) {
	d.healthMu.Lock()
	defer d.healthMu.Unlock()
	if classifyBaiduRefreshErr(err) {
		d.authDead = true
		d.warnText = "百度授权已失效，需要重新授权（管理页粘贴 refresh_token 或一键授权）"
	} else if !d.authDead && !d.storageBad {
		d.warnText = "百度自动刷新失败，当前会话暂可用"
	}
}

// recordStorageFailure 轮换写回 config 失败（§6.10 + 审核 P2-1：红「存储异常」，区别于暂败黄）
func (d *Driver) recordStorageFailure() {
	d.healthMu.Lock()
	defer d.healthMu.Unlock()
	d.storageBad = true
	d.warnText = "本地配置写入失败（存储异常），请检查路由器存储空间后重启重试"
}

// clearRefreshFailure 清除刷新失败事实（刷新成功 / 用户提交新凭证后）
func (d *Driver) clearRefreshFailure() {
	d.healthMu.Lock()
	d.authDead = false
	d.storageBad = false
	d.warnText = ""
	d.healthMu.Unlock()
}

// Health 健康事实（批 2 §6.10）：AuthExpired = 不可恢复失效（红）；
// StorageFailed = 本地存储异常（红）；Warning = 暂败（黄）。
func (d *Driver) Health() driver.DriverHealth {
	d.healthMu.Lock()
	defer d.healthMu.Unlock()
	h := driver.DriverHealth{}
	switch {
	case d.authDead:
		h.AuthExpired = true
		h.Warning = d.warnText
	case d.storageBad:
		h.StorageFailed = true
		h.Warning = d.warnText
	case d.warnText != "":
		h.Warning = d.warnText
	}
	return h
}

// Init 初始化：校验 OAuth 应用凭据与 refresh_token，刷新或从缓存恢复会话。
// config 为 refresh_token 唯一真值源（DESIGN-2026-11-24-01 §6.5）：state 缓存仅当
// 其 refresh_token 与 config 一致时才复用 access_token——防旧缓存覆盖新授权
// （否则管理页更新后重启会被旧缓存吞掉，D2 重启恢复验收）。
func (d *Driver) Init(ctx context.Context) error {
	// 设置 401 自动刷新回调（运行时安全网——必须在任何 early return 之前注册；
	// 批 2 起走 refreshSingleFlight：互斥 + 限频 + 轮换写回 config）
	d.client.SetAuthRefreshCallback(func() error {
		slog.Info("百度网盘 token 过期，自动刷新")
		return d.refreshSingleFlight(context.Background())
	})

	if d.clientID == "" || d.clientSecret == "" {
		return fmt.Errorf("百度网盘 OAuth 应用凭据未配置（config.json 需填写 client_id 与 client_secret，来自百度网盘开放平台应用）")
	}
	if d.getRefreshToken() == "" {
		return fmt.Errorf("百度网盘尚未授权（请在管理页粘贴 refresh_token 更新）")
	}

	token, err := LoadToken(d.name, d.stateDir)
	// 缓存复用条件（§6.5 批 2）：新格式（V==1）且缓存代次 == 当前授权代次——
	// 用户提交新凭据代次 +1 → 缓存自动失配重新兑换（D2 重启恢复）；
	// 旧格式（V==0，曾存 refresh_token）一律视为过期缓存
	if err == nil && token != nil && token.V == 1 && token.CredentialGeneration == int(d.credGen.Load()) {
		d.client.SetAccessToken(token.AccessToken)
		errno, _ := d.client.apiGetWithErrno("/nas", map[string]string{"method": "uinfo"}, nil)
		if errno == 0 {
			slog.Info("从缓存恢复百度网盘会话")
			return nil
		}
		slog.Warn("缓存 token 已失效，重新刷新")
	}

	if err := d.refreshSingleFlight(ctx); err != nil {
		return fmt.Errorf("百度网盘 token 刷新失败: %w", err)
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
		// 脱敏（审核 I1，RV-2026-11-25-02）：dlink URL 含 access_token，
		// url.Error 文本会经 webdav 进日志——与 API/token 链路同款 redactSecrets
		return nil, fmt.Errorf("%s", redactSecrets(err.Error(), d.client.getAccessToken()))
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

// RefreshToken TokenManager：后台 watcher 调用（批 2 §6.7：ctx 贯通到交换请求；
// 单飞 + 限频 + 轮换写回见 refreshSingleFlight）。
func (d *Driver) RefreshToken(ctx context.Context) error {
	return d.refreshSingleFlight(ctx)
}

var _ driver.Driver = (*Driver)(nil)
var _ driver.TokenManager = (*Driver)(nil)
var _ driver.HealthReporter = (*Driver)(nil)
