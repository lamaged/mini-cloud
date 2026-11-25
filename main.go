package main

import (
	"context"
	"crypto/subtle"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	rtdebug "runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"mini-cloud/drivers/baidu"
	"mini-cloud/drivers/local"
	"mini-cloud/drivers/mobile"
	"mini-cloud/drivers/tianyi"
	"mini-cloud/internal/admin"
	"mini-cloud/internal/auth"
	"mini-cloud/internal/conf"
	"mini-cloud/internal/driver"
	"mini-cloud/internal/fs"
	"mini-cloud/internal/vault"
	"mini-cloud/internal/webdav"
)

const version = "v1.2.0-beta" // Phase 2 完成（真机验收全部通过并合入 master；≥1 个月观察后升正式版 v1.2.0）

// MountRuntime 挂载运行时状态（DESIGN-2026-11-24-01 §4.2）：
// 状态表 = 全部配置挂载（首页/管理页渲染源）；数据路由仅注册 Available 挂载。
type MountRuntime struct {
	Config    conf.CloudConfig
	Name      string // 挂载名（URL 前缀）
	Prefix    string // URL 前缀，如 "/<name>"
	RawDriver driver.Driver // 创建成功即保留；Init 失败也保留（/admin 凭证更新入口）；创建失败为 nil
	InitError error         // 创建或初始化失败原因（RawDriver == nil 即创建失败）
	Available bool          // Init 成功且 WebDAV 路由已注册

	mu            sync.RWMutex // 以下状态字段并发保护（探测 goroutine 写、HTTP 页读）
	status        mountStatus
	statusMsg     string
	probeFails    int
	probeMsg      string
	lastCheckAt   time.Time
	lastSuccessAt time.Time
	updatedAt     time.Time

	probing atomic.Bool // 同挂载单飞（探测进行中标记）
}

// mountStatus 挂载状态（应用层统一合成；DESIGN-2026-11-24-01 §5.2）
type mountStatus string

const (
	statusHealthy     mountStatus = "healthy"     // 正常（黑）
	statusWarning     mountStatus = "warning"     // 警告（黄）
	statusUnavailable mountStatus = "unavailable" // 失效（红）
)

// warnExpiryThreshold 凭据临近过期阈值（DESIGN-2026-11-24-01 §5.3：剩余 <7 天转黄）
const warnExpiryThreshold = 7 * 24 * time.Hour

// synthesizeStatus 状态合成唯一规则（§5.2）：初始化结果 + 探测结果 + 驱动健康事实。
// AuthExpired 优先级最高——一次 List 成功不得覆盖已确认失效；
// ExpiresAt 临近过期（<7 天）转黄，由事实驱动——探测成功也不会把黄洗回黑。
func synthesizeStatus(initErr error, dh driver.DriverHealth, probeFails int, probeMsg string) (mountStatus, string) {
	return synthesizeStatusAt(time.Now(), initErr, dh, probeFails, probeMsg)
}

// synthesizeStatusAt 同上，now 注入版（7 天临界值测试用，避免墙钟误差）
func synthesizeStatusAt(now time.Time, initErr error, dh driver.DriverHealth, probeFails int, probeMsg string) (mountStatus, string) {
	if initErr != nil {
		return statusUnavailable, sanitizeErr(initErr.Error())
	}
	if dh.AuthExpired {
		msg := dh.Warning
		if msg == "" {
			msg = "凭据已失效"
		}
		return statusUnavailable, msg
	}
	if dh.StorageFailed {
		msg := dh.Warning
		if msg == "" {
			msg = "本地存储异常"
		}
		return statusUnavailable, msg
	}
	if probeFails >= 2 {
		msg := probeMsg
		if msg == "" {
			msg = "健康检查连续失败"
		}
		return statusUnavailable, msg
	}
	if dh.ExpiresAt != nil && dh.ExpiresAt.Sub(now) < warnExpiryThreshold {
		return statusWarning, fmt.Sprintf("凭据将在 %d 天后过期，请尽快更新", expiryDaysLeftAt(now, *dh.ExpiresAt))
	}
	if dh.Warning != "" {
		return statusWarning, dh.Warning
	}
	return statusHealthy, ""
}

// expiryDaysLeft 剩余天数（向上取整；不足 1 天按 1 天计）
func expiryDaysLeft(t time.Time) int {
	return expiryDaysLeftAt(time.Now(), t)
}

func expiryDaysLeftAt(now, t time.Time) int {
	d := t.Sub(now)
	if d <= 0 {
		return 0
	}
	days := int((d + 24*time.Hour - 1) / (24 * time.Hour))
	if days < 1 {
		days = 1
	}
	return days
}

// sanitizeErr 错误信息脱敏限长（≤200 字符）
func sanitizeErr(s string) string {
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// health 读取驱动健康事实（断言原始驱动，铁律 7）
func (m *MountRuntime) health() driver.DriverHealth {
	if m.RawDriver == nil {
		return driver.DriverHealth{}
	}
	if hr, ok := m.RawDriver.(driver.HealthReporter); ok {
		return hr.Health()
	}
	return driver.DriverHealth{}
}

// applyInitResult 初始化后按统一合成规则写入状态快照
func (m *MountRuntime) applyInitResult(initErr error) {
	m.InitError = initErr
	m.mu.Lock()
	st, msg := synthesizeStatus(initErr, m.health(), 0, "")
	m.status = st
	m.statusMsg = msg
	m.updatedAt = time.Now()
	m.mu.Unlock()
}

// applyProbeResult 探测完成后一次性替换快照（探测判据 §5.3：
// err==nil 但驱动有失效事实时合成规则仍判红/黄，不得因 List 无错判健康）。
// 错误只存分类短语（classifyProbeErr）——原始错误文本可能携带凭据
// （百度 url.Error 含 access_token，安全审核 RV-2026-11-24-06），且分类更可读。
func (m *MountRuntime) applyProbeResult(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastCheckAt = time.Now()
	if err != nil {
		m.probeFails++
		m.probeMsg = classifyProbeErr(err)
	} else {
		m.probeFails = 0
		m.probeMsg = ""
		m.lastSuccessAt = time.Now()
	}
	st, msg := synthesizeStatus(m.InitError, m.health(), m.probeFails, m.probeMsg)
	m.status = st
	m.statusMsg = msg
	m.updatedAt = time.Now()
}

// classifyProbeErr 探测错误 → 固定分类短语（design-v1.3.0 §7 错误分类）。
// 不保留原始错误文本：上游错误文本可能携带凭据（安全审核 RV-2026-11-24-06）。
func classifyProbeErr(err error) string {
	var ne net.Error
	var ue *url.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		return "健康检查超时"
	case errors.As(err, &ue) || errors.As(err, &ne):
		return "网络不可达"
	case errors.Is(err, driver.ErrUpstream):
		return "上游云盘不可用"
	case errors.Is(err, os.ErrNotExist):
		return "根目录不存在"
	default:
		return "上游服务异常"
	}
}

// refreshSnapshotFromFacts 按当前事实重算状态快照（凭证提交成功后调用，PM 拍板 2026-11-25）：
// 不触碰探测计数与检查时间——移动贴票成功后事实即干净 → 状态卡当场转正常（旧行为即立即生效）；
// Init 失败的挂载仍由 InitError 判红 → 「已保存，重启后生效」语义不变（D3）。
func (m *MountRuntime) refreshSnapshotFromFacts() {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, msg := synthesizeStatus(m.InitError, m.health(), m.probeFails, m.probeMsg)
	m.status = st
	m.statusMsg = msg
	m.updatedAt = time.Now()
}

// snapshot 读取当前状态快照（HTTP 页只读）
func (m *MountRuntime) snapshot() (mountStatus, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status, m.statusMsg
}

// checkInfo 读取最近检查/成功时间（管理页展示）
func (m *MountRuntime) checkInfo() (lastCheckAt, lastSuccessAt time.Time) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastCheckAt, m.lastSuccessAt
}

func main() {
	configPath := flag.String("config", "config.json", "配置文件路径")
	debug := flag.Bool("debug", false, "启用调试日志（含请求追踪）")
	quiet := flag.Bool("quiet", false, "静默模式（仅警告和错误日志）")
	flag.Parse()

	// 日志级别：debug > 默认(INFO) > quiet(WARN)
	logLevel := slog.LevelWarn
	if *debug {
		logLevel = slog.LevelDebug
	} else if !*quiet {
		logLevel = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})))

	// D（RV-2026-11-18-01/02 + 外部审核 #4 修正 2026-11-20）：内存软上限——堆接近
	// 64 MiB 时 GC 提前介入，防止低内存路由器上堆失控触发内核 OOM（PM 原则①）。
	// 前移到启动扫描（vault.Init/manifest 解析）**之前**：启动期扫描同样受控；
	// 配合 manifest 解析全局并发限 2（vault 内部），大清单场景下以 GC 提前介入
	// 换取不 OOM。3 并发解密流 live ≈ 18 MiB + 基线，正常播放不触发。
	rtdebug.SetMemoryLimit(64 << 20)

	// G（RV-2026-11-18-01/02）：-debug 下每分钟打一次内存观测（压测定位内存问题用；P5 由 5 分钟加密）
	if *debug {
		go memStatsLogger()
	}

	// 加载配置
	cfg, err := conf.Load(*configPath)
	if err != nil {
		slog.Error("加载配置失败", "err", err)
		os.Exit(1)
	}
	slog.Info("配置加载完成", "listen", cfg.Server.Listen, "clouds", len(cfg.Clouds))

	// 服务端认证信息固定快照：管理页写回只改 clouds 段，认证信息不参与运行时共享可变状态
	serverUser, serverPass := cfg.Server.Username, cfg.Server.Password

	// 配置写回互斥（管理页提交与百度轮换写回共用同一把锁，§6.5「比较代次→写回」互斥范围）
	var cfgMu sync.Mutex

	// 创建所有驱动（单挂载故障隔离：失败只记录状态，不再整体退出；
	// inject 在 Init 之前给百度驱动注入轮换写回与授权代次——Init 期可能发生轮换）
	ctx := context.Background()
	mounts := createAllDrivers(ctx, cfg, func(drv driver.Driver, cc *conf.CloudConfig) error {
		bd, ok := drv.(*baidu.Driver)
		if !ok {
			return nil
		}
		mount := cc.MountName()
		bd.SetCredentialGeneration(cc.CredentialGeneration)
		bd.SetRotationPersist(func(ctx context.Context, expectedGen int, rt string) error {
			cfgMu.Lock()
			defer cfgMu.Unlock()
			fresh, err := conf.RotateBaiduToken(*configPath, mount, expectedGen, rt)
			if err != nil {
				return err
			}
			if fresh == nil {
				return nil // 代次不符：迟到的轮换结果，跳过（防旧覆盖新）
			}
			cfg.Clouds = fresh.Clouds // 写盘成功后才更新内存副本
			return nil
		})
		return nil
	})

	// 健康探测（v1.3.0 批 1）：每挂载 5 分钟，绕过缓存直调 raw.List("/")
	go startHealthProbe(ctx, mounts)

	// 管理页：收集全部配置挂载（含失败挂载的状态卡；凭证更新入口仅对
	// 「创建成功且实现 CredentialUpdater」的挂载提供，D1）。断言一律对原始驱动（铁律 7）。
	// 状态经统一合成规则渲染（红黄黑 + 原因 + 最近检查时间）；Vault 告警随渲染时合并。
	// hasUpdater：是否存在支持在线更新的挂载——只有它为真才注册 /admin 路由、
	// 显示首页「管理」链接（审核 P2：状态卡不是注册管理页的理由）。
	vaultWarn := make(map[string]func() string)
	adminMounts := make([]admin.Mount, 0, len(mounts))
	hasUpdater := false
	for _, m := range mounts {
		am := admin.Mount{Name: m.Name, Prefix: m.Prefix, Available: m.Available}
		if m.RawDriver != nil {
			if up, ok := m.RawDriver.(driver.CredentialUpdater); ok {
				am.FieldsFunc = up.CredentialFields // 动态标签（剩余有效期/获取指引），渲染时现取
				am.Update = up
				hasUpdater = true
			}
			if oa, ok := m.RawDriver.(driver.OAuthAuthorizer); ok {
				am.OAuth = oa // 一键授权（批 2 §6.4；Init 失败的挂载同样保留入口，D1）
			}
		}
		am.Status = func() admin.MountStatus {
			st, msg := m.snapshot()
			v := admin.MountStatus{Label: "正常", Color: "ok", Detail: msg}
			switch st {
			case statusWarning:
				v.Label, v.Color = "警告", "warning"
			case statusUnavailable:
				v.Label, v.Color = "不可用", "error"
			}
			if vw, ok := vaultWarn[m.Name]; ok {
				if w := vw(); w != "" {
					if v.Detail != "" {
						v.Detail += "；"
					}
					v.Detail += w
				}
			}
			if lc, ls := m.checkInfo(); !lc.IsZero() {
				v.LastCheckAt = lc.Format("2006-01-02 15:04:05")
				if !ls.IsZero() {
					v.LastCheckAt += "（成功）"
				}
			}
			return v
		}
		adminMounts = append(adminMounts, am)
	}

	// 配置写回（两阶段语义：验证通过才写；互斥保护避免并发写——cfgMu 见上，管理页与轮换共用）
	adminHandler := admin.New(adminMounts, func(ctx context.Context, mountName string, values map[string]string) (func() error, error) {
		cfgMu.Lock()
		defer cfgMu.Unlock()
		// 严格读取 + 以磁盘当前内容为准（尊重运行期间的手工修改）；写盘成功后才更新内存副本
		fresh, err := conf.WriteCredential(*configPath, mountName, values)
		if err != nil {
			return nil, err
		}
		cfg.Clouds = fresh.Clouds // 写盘成功后才更新内存副本
		// 百度：在 CommitCredential 之前同步授权代次（§6.5：让在途轮换的迟到结果
		// 在写回复检时代次不符被丢弃，防旧覆盖新）
		for _, m := range mounts {
			if m.Name != mountName {
				continue
			}
			if bd, ok := m.RawDriver.(*baidu.Driver); ok {
				for i := range fresh.Clouds {
					if fresh.Clouds[i].MountName() == mountName {
						bd.SetCredentialGeneration(fresh.Clouds[i].CredentialGeneration)
						break
					}
				}
			}
			break
		}
		return func() error {
			// 提交失败回滚（I6，PM 拍板方案①）：用 .bak 还原配置并同步内存副本。
			// 失败必须如实返回（I8）：不得假装已还原。
			if err := conf.RestoreBackup(*configPath); err != nil {
				return fmt.Errorf("还原配置失败: %w", err)
			}
			cfgMu.Lock()
			defer cfgMu.Unlock()
			restored, err := conf.LoadStrict(*configPath)
			if err != nil {
				return fmt.Errorf("重读配置失败: %w", err)
			}
			cfg.Clouds = restored.Clouds
			return nil
		}, nil
	})

	// HTTP 路由
	mux := http.NewServeMux()

	// 管理页（仅当存在支持在线更新的挂载时注册——状态卡不是注册理由，审核 P2）
	registerAdminRoutes(mux, hasUpdater, serverUser, serverPass, adminHandler)

	// 根路径：GET 返回导航页，WebDAV 方法返回索引
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if !checkAuth(r, serverUser, serverPass) {
			w.Header().Set("WWW-Authenticate", `Basic realm="mini-cloud"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		// OPTIONS 返回 DAV 头
		if r.Method == "OPTIONS" {
			w.Header().Set("Allow", "OPTIONS, GET, HEAD, PROPFIND")
			w.Header().Set("DAV", "1, 2")
			w.Header().Set("MS-Author-Via", "DAV")
			w.WriteHeader(http.StatusOK)
			return
		}
		// GET → 浏览器导航页（状态表 = 全部配置挂载）
		if r.Method == "GET" || r.Method == "HEAD" {
			serveRootIndex(w, mounts, hasUpdater)
			return
		}
		// PROPFIND → 返回云盘列表（兼容 WebDAV 客户端）
		if r.Method == "PROPFIND" {
			serveRootPropfind(w, r, mounts)
			return
		}
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	})

	// Vault 全局配置（PM 拍板：全局一套，按挂载生效；docs/design.md §4.1/§4.3）
	var vaultKey []byte
	var vaultCfg *vault.Config
	if cfg.Vault != nil && cfg.Vault.Enabled {
		vaultKey, err = os.ReadFile(cfg.Vault.KeyFile)
		if err != nil {
			slog.Error("读取 vault 密钥失败", "key_file", cfg.Vault.KeyFile, "err", err)
			os.Exit(1)
		}
		if len(vaultKey) != 32 {
			slog.Error("vault 密钥长度必须为 32 字节", "len", len(vaultKey))
			os.Exit(1)
		}
		if len(cfg.Vault.Paths) == 0 {
			slog.Error("vault.paths 不能为空")
			os.Exit(1)
		}
		vaultCfg = &vault.Config{Paths: cfg.Vault.Paths, MaxStreams: cfg.Vault.MaxStreams}
		// R2-1（外部审核 2026-11-20）：并发额度进程级共享——装配前设置一次
		//（默认 3，PM 拍板；配置天花板 4 已由 conf 校验）
		vault.SetGlobalStreamLimit(cfg.Vault.MaxStreams)
	}

	// 每个可用云盘注册到 /<prefix>/（数据路由仅注册 Available 挂载，§4.2）
	davHandlers := make(map[string]*webdav.Handler)
	for _, m := range mounts {
		if !m.Available || m.RawDriver == nil {
			continue
		}
		cached := fs.NewCachedReader(fs.NewRetryReader(m.RawDriver))
		var drv webdav.Reader = cached
		if vaultCfg != nil && vaultSupported(m.RawDriver) {
			v := vault.New(cached, m.RawDriver, vaultKey, *vaultCfg)
			vaultWarn[m.Name] = v.Warning // 双通道：vault 告警注入管理页（§5.2）
			if err := v.Init(ctx); err != nil {
				slog.Warn("vault 初始化失败，该挂载按普通模式工作", "mount", m.Name, "err", err)
			} else {
				drv = v
			}
		}
		davHandler := &webdav.Handler{
			Driver: drv,
			Prefix: m.Prefix,
		}
		davHandlers[m.Name] = davHandler
		// 注册 /<prefix>/ 及其子路径
		mux.HandleFunc(m.Prefix+"/", func(w http.ResponseWriter, r *http.Request) {
			if !checkAuth(r, serverUser, serverPass) {
				w.Header().Set("WWW-Authenticate", `Basic realm="mini-cloud"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			davHandler.ServeHTTP(w, r)
		})
		slog.Info("挂载云盘", "name", m.Name, "prefix", m.Prefix)
	}

	// 管理页「重启服务」（§6.12）：原地重启自身——syscall.Exec 替换进程镜像，
	// PID 不变、日志 fd 继承、init.d 的 PID_FILE/RUN_FLAG 继续有效
	adminHandler.Restart = func() error {
		exe, err := os.Executable()
		if err != nil {
			exe = "/proc/self/exe"
		}
		return syscall.Exec(exe, os.Args, os.Environ())
	}

	// 凭证生效后：清空该挂载的目录/链接缓存，避免旧账号数据残留（30s 目录 / 10min 链接）；
	// 按当前事实重算状态卡（PM 拍板 2026-11-25；授权代次已在写回闭包内同步）
	adminHandler.OnCommitted = func(mountName string) {
		if h, ok := davHandlers[mountName]; ok {
			if inv, ok := h.Driver.(interface{ InvalidateAll() }); ok {
				inv.InvalidateAll()
			}
		}
		for _, m := range mounts {
			if m.Name != mountName {
				continue
			}
			m.refreshSnapshotFromFacts()
			return
		}
	}

	// 启动服务
	server := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           withLogging(mux),
		ReadHeaderTimeout: 10 * time.Second, // 防慢连接长期占用（I7 附带，全站收益）
	}

	// 优雅关闭
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		slog.Info("收到信号，正在关闭...", "signal", sig)
		server.Close()
	}()

	slog.Info("Mini Cloud WebDAV Gateway 启动", "version", version, "listen", cfg.Server.Listen)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("服务异常退出", "err", err)
		os.Exit(1)
	}

	// 服务停止后清理资源（缓存后台 goroutine 等）
	for _, h := range davHandlers {
		h.Close()
	}
	slog.Info("服务已停止")
}

// ============================================================
// 驱动创建
// ============================================================

// createAllDrivers 根据配置创建所有云盘驱动（v1.3.0 批 1：单挂载故障隔离）——
// 创建/初始化失败只记录该挂载状态，不再整体返回错误；至少一个成功即启动服务；
// 全部失败仍启动诊断首页（Q1 拍板）。失败挂载保留 RawDriver（创建成功时）进 /admin。
// inject 在 Init 之前按挂载注入依赖（百度：轮换写回与授权代次，§6.5）；注入失败按挂载失败处理。
func createAllDrivers(ctx context.Context, cfg *conf.Config, inject func(driver.Driver, *conf.CloudConfig) error) []*MountRuntime {
	mounts := make([]*MountRuntime, 0, len(cfg.Clouds))
	for i := range cfg.Clouds {
		cc := &cfg.Clouds[i]
		m := &MountRuntime{
			Config: *cc,
			Name:   cc.MountName(),
			Prefix: "/" + cc.MountName(),
		}

		drv, err := createDriver(cc)
		if err != nil {
			m.applyInitResult(err)
			slog.Error("创建驱动失败", "mount", m.Name, "err", err)
			mounts = append(mounts, m)
			continue
		}
		m.RawDriver = drv

		if inject != nil {
			if err := inject(drv, cc); err != nil {
				m.applyInitResult(err)
				slog.Error("注入驱动依赖失败", "mount", m.Name, "err", err)
				mounts = append(mounts, m)
				continue
			}
		}

		if err := drv.Init(ctx); err != nil {
			m.applyInitResult(err)
			slog.Error("初始化驱动失败", "mount", m.Name, "err", err)
			mounts = append(mounts, m)
			continue
		}
		m.Available = true
		m.applyInitResult(nil)
		slog.Info("驱动就绪", "name", drv.Name(), "mount", m.Name)

		// 启动后台 Token Watcher
		auth.StartWatcher(ctx, drv, 5*time.Minute)

		mounts = append(mounts, m)
	}

	avail := 0
	for _, m := range mounts {
		if m.Available {
			avail++
		}
	}
	slog.Info("挂载初始化完成", "configured", len(mounts), "available", avail)
	return mounts
}

// createDriver 根据单个云盘配置创建驱动
func createDriver(cc *conf.CloudConfig) (driver.Driver, error) {
	switch cc.Type {
	case "local":
		return local.New("./data")
	case "tianyi":
		return tianyi.New(cc.MountName(), cc.Username, cc.Password, cc.FamilyID, "./state"), nil
	case "mobile":
		return mobile.New(cc.MountName(), cc.Authorization, "./state"), nil
	case "baidu":
		return baidu.New(cc.MountName(), cc.RefreshToken, cc.ClientID, cc.ClientSecret, "./state"), nil
	default:
		return nil, fmt.Errorf("不支持的驱动类型: %s", cc.Type)
	}
}

// ============================================================
// 认证
// ============================================================

// registerAdminRoutes 注册管理页路由——仅当存在支持在线更新的挂载（hasUpdater）时；
// 无更新能力时不注册（/admin 返回 404），首页「管理」链接同条件（审核 P2 + 复核补充测试）。
func registerAdminRoutes(mux *http.ServeMux, hasUpdater bool, serverUser, serverPass string, h *admin.Handler) {
	if !hasUpdater {
		return
	}
	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(r, serverUser, serverPass) {
			w.Header().Set("WWW-Authenticate", `Basic realm="mini-cloud"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		h.Page(w, r)
	})
	mux.HandleFunc("POST /admin/update", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(r, serverUser, serverPass) {
			w.Header().Set("WWW-Authenticate", `Basic realm="mini-cloud"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		h.Update(w, r)
	})
	// 一键授权（批 2 §6.4）：仅当存在 OAuthAuthorizer 挂载时注册
	mux.HandleFunc("POST /admin/oauth/start", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(r, serverUser, serverPass) {
			w.Header().Set("WWW-Authenticate", `Basic realm="mini-cloud"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		h.OAuthStart(w, r)
	})
	mux.HandleFunc("POST /admin/oauth/finish", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(r, serverUser, serverPass) {
			w.Header().Set("WWW-Authenticate", `Basic realm="mini-cloud"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		h.OAuthFinish(w, r)
	})
	// 重启服务（批 2 §6.12）
	mux.HandleFunc("POST /admin/restart", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(r, serverUser, serverPass) {
			w.Header().Set("WWW-Authenticate", `Basic realm="mini-cloud"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		h.RestartService(w, r)
	})
}

func checkAuth(r *http.Request, username, password string) bool {
	u, p, ok := r.BasicAuth()
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(u), []byte(username)) == 1 &&
		subtle.ConstantTimeCompare([]byte(p), []byte(password)) == 1
}

// ============================================================
// 根索引页
// ============================================================

// cloudDisplayNames 驱动类型到中文显示名的映射
var cloudDisplayNames = map[string]string{
	"local":  "本地文件",
	"tianyi": "天翼云盘",
	"mobile": "移动云盘",
	"baidu":  "百度网盘",
}

// displayName 返回云盘在导航/索引页的显示名（纯挂载名，PM 拍板 2026-11-24：
// 首页与根 PROPFIND 一律不再拼接告警文本，状态只经首页颜色 + /admin 表达）
func displayName(m *MountRuntime) string {
	label := cloudDisplayNames[m.Name]
	if label == "" {
		label = m.Name
	}
	return label
}

// serveRootPropfind 为根路径生成 WebDAV PROPFIND 响应（列出所有云盘作为虚拟目录）
func serveRootPropfind(w http.ResponseWriter, r *http.Request, mounts []*MountRuntime) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)

	fmt.Fprint(w, `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
<D:response>
<D:href>/</D:href>
<D:propstat>
<D:prop>
<D:displayname>/</D:displayname>
<D:resourcetype><D:collection/></D:resourcetype>
<D:getcontenttype>httpd/unix-directory</D:getcontenttype>
</D:prop>
<D:status>HTTP/1.1 200 OK</D:status>
</D:propstat>
</D:response>`)

	for _, m := range mounts {
		fmt.Fprintf(w, `
<D:response>
<D:href>%s/</D:href>
<D:propstat>
<D:prop>
<D:displayname>%s</D:displayname>
<D:resourcetype><D:collection/></D:resourcetype>
<D:getcontenttype>httpd/unix-directory</D:getcontenttype>
</D:prop>
<D:status>HTTP/1.1 200 OK</D:status>
</D:propstat>
</D:response>`, xmlText(webdav.EncodePathSegments(m.Prefix)), xmlText(displayName(m)))
	}

	fmt.Fprint(w, "\n</D:multistatus>")
}

// xmlText 转义 XML 文本内容（挂载名含 & 等字符时防止产出非法 XML）
func xmlText(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func serveRootIndex(w http.ResponseWriter, mounts []*MountRuntime, hasAdmin bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Mini Cloud WebDAV</title>
<style>
body{font-family:sans-serif;max-width:600px;margin:40px auto;padding:0 20px}
h1{font-size:22px;border-bottom:2px solid #0366d6;padding-bottom:10px}
.card{display:block;padding:16px 20px;margin:12px 0;border:1px solid #ddd;border-radius:8px;text-decoration:none;color:#333}
.card:hover{background:#f6f8fa;border-color:#0366d6}
.card.nolink{cursor:default}
.card.nolink:hover{background:inherit;border-color:#ddd}
.card .name{font-size:18px;font-weight:bold}
.card .name.ok{color:#333}
.card .name.warning{color:#b26a00}
.card .name.error{color:#b00020}
.admin{display:inline-block;margin-top:8px}
</style></head><body>
<h1>☁️ Mini Cloud WebDAV</h1>
<p>已配置 %d 个云盘：</p>
`, len(mounts))

	hasProblem := false
	for _, m := range mounts {
		st, _ := m.snapshot()
		if st != statusHealthy {
			hasProblem = true
		}
		color := "ok"
		switch st {
		case statusWarning:
			color = "warning"
		case statusUnavailable:
			color = "error"
		}
		label := displayName(m)
		if st == statusUnavailable {
			// 红卡不可点击（PM 拍板 2026-11-25）：失败挂载无数据路由，点击必 404
			fmt.Fprintf(w, `<div class="card nolink">
<div class="name %s">%s</div>
</div>`, color, html.EscapeString(label))
			continue
		}
		href := webdav.EncodePathSegments(m.Prefix) + "/"
		fmt.Fprintf(w, `<a class="card" href="%s">
<div class="name %s">%s</div>
</a>`, html.EscapeString(href), color, html.EscapeString(label))
	}

	// PM 拍板 2026-11-24：全部正常时首页不显示「管理」链接，只有出问题时才显示
	if hasAdmin && hasProblem {
		fmt.Fprint(w, `<p><a class="admin" href="/admin">管理</a></p>`)
	}

	fmt.Fprint(w, "</body></html>")
}

// ============================================================
// 健康探测（v1.3.0 批 1，DESIGN-2026-11-24-01 §5.3）
// ============================================================

// startHealthProbe 每挂载独立健康探测：启动后立即一次 + 每 5 分钟；
// 绕过 CachedReader 直调 raw.List("/")（探测结果不更新缓存）；
// 同挂载单飞、独立 10s 超时；Init 失败的挂载不探测（状态由 InitError 决定，重启恢复）。
func startHealthProbe(ctx context.Context, mounts []*MountRuntime) {
	probeOne := func(m *MountRuntime) {
		if m.RawDriver == nil || m.InitError != nil || !m.probing.CompareAndSwap(false, true) {
			return
		}
		defer m.probing.Store(false)
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_, err := m.RawDriver.List(pctx, "/")
		m.applyProbeResult(err)
	}

	for _, m := range mounts {
		probeOne(m) // 启动后立即一次
	}

	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, m := range mounts {
				go probeOne(m)
			}
		}
	}
}

// ============================================================
// 请求日志中间件
// ============================================================

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &responseLogger{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		slog.Debug("请求", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr, "status", rw.status, "bytes", rw.size)
	})
}

type responseLogger struct {
	http.ResponseWriter
	status int
	size   int64
}

func (r *responseLogger) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseLogger) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.size += int64(n)
	return n, err
}

// vaultSupported 铁律 2（加密仅支持天翼/移动）：不支持的驱动（百度等）不装配 Vault——
// 避免对无意义的路径做容器扫描，产生「容器扫描失败」类告警噪声（真机发现 2026-11-19）。
// 按驱动名（drivers/*.go 的 Name() 返回值：tianyi/mobile/baidu/local）允许列表判定；
// local 保留（Phase 0 开发/测试路径）。
func vaultSupported(d driver.Driver) bool {
	switch d.Name() {
	case "tianyi", "mobile", "local":
		return true
	}
	return false
}

// memStatsLogger -debug 模式下的内存观测（G：HeapInuse/HeapSys/NumGC/goroutine 数，每分钟一次）
func memStatsLogger() {
	t := time.NewTicker(1 * time.Minute)
	defer t.Stop()
	var m runtime.MemStats
	for range t.C {
		runtime.ReadMemStats(&m)
		slog.Debug("memstats", "heap_inuse_mib", m.HeapInuse>>20,
			"heap_sys_mib", m.HeapSys>>20, "num_gc", m.NumGC, "goroutines", runtime.NumGoroutine())
	}
}
