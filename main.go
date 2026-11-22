package main

import (
	"context"
	"crypto/subtle"
	"encoding/xml"
	"flag"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	rtdebug "runtime/debug"
	"strings"
	"sync"
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

// mountedDriver 已挂载的云盘驱动信息
type mountedDriver struct {
	Name   string        // 驱动名称（类型名，如 "mobile"）
	Prefix string        // URL 前缀，如 "/mobile"
	Drv    driver.Driver // 驱动实例（原始驱动）

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

	// 创建所有驱动
	ctx := context.Background()
	mounts, err := createAllDrivers(ctx, cfg)
	if err != nil {
		slog.Error("创建驱动失败", "err", err)
		os.Exit(1)
	}

	// 管理页：收集支持 CredentialUpdater 的挂载（断言原始驱动，不是包装层）
	// 挂载告警双通道（design §5.2 + 铁律 7）：驱动告警（HealthReporter 对原始驱动断言）
	// + Vault 告警（具体方法 Warning()，在装配循环里注入）——两者合并展示。
	// 全部挂载都入管理页：凭证挂载带更新表单，其余为「状态观察」卡（无表单）。
	// （真机发现：天翼无 CredentialUpdater 曾导致其 vault 告警无处显示）
	vaultWarn := make(map[string]func() string)
	var adminMounts []admin.Mount
	for i := range mounts {
		m := &mounts[i]
		am := admin.Mount{Name: m.Name, Prefix: m.Prefix}
		if up, ok := m.Drv.(driver.CredentialUpdater); ok {
			am.FieldsFunc = up.CredentialFields // 动态标签（剩余有效期/获取指引），渲染时现取
			am.Update = up
		}
		am.Warning = func() string {
			var parts []string
			if hr, ok := m.Drv.(driver.HealthReporter); ok {
				if w := hr.HealthWarning(); w != "" {
					parts = append(parts, w)
				}
			}
			if vw, ok := vaultWarn[m.Name]; ok {
				if w := vw(); w != "" {
					parts = append(parts, w)
				}
			}
			return strings.Join(parts, "；")
		}
		adminMounts = append(adminMounts, am)
	}

	// 配置写回（两阶段语义：验证通过才写；互斥保护避免并发写）
	var cfgMu sync.Mutex
	adminHandler := admin.New(adminMounts, func(ctx context.Context, mountName string, values map[string]string) (func() error, error) {
		cfgMu.Lock()
		defer cfgMu.Unlock()
		// 严格读取 + 以磁盘当前内容为准（尊重运行期间的手工修改）；写盘成功后才更新内存副本
		fresh, err := conf.WriteCredential(*configPath, mountName, values)
		if err != nil {
			return nil, err
		}
		cfg.Clouds = fresh.Clouds // 写盘成功后才更新内存副本
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

	// 管理页（仅当存在支持在线更新的挂载时注册）
	if len(adminMounts) > 0 {
		mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
			if !checkAuth(r, serverUser, serverPass) {
				w.Header().Set("WWW-Authenticate", `Basic realm="mini-cloud"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			adminHandler.Page(w, r)
		})
		mux.HandleFunc("POST /admin/update", func(w http.ResponseWriter, r *http.Request) {
			if !checkAuth(r, serverUser, serverPass) {
				w.Header().Set("WWW-Authenticate", `Basic realm="mini-cloud"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			adminHandler.Update(w, r)
		})
	}

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
		// GET → 浏览器导航页
		if r.Method == "GET" || r.Method == "HEAD" {
			serveRootIndex(w, mounts, len(adminMounts) > 0)
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

	// 每个云盘注册到 /<prefix>/
	var davHandlers []*webdav.Handler
	for i := range mounts {
		m := &mounts[i]
		cached := fs.NewCachedReader(fs.NewRetryReader(m.Drv))
		var drv webdav.Reader = cached
		if vaultCfg != nil && vaultSupported(m.Drv) {
			v := vault.New(cached, m.Drv, vaultKey, *vaultCfg)
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
		davHandlers = append(davHandlers, davHandler)
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

	// 凭证生效后：清空该挂载的目录/链接缓存，避免旧账号数据残留（30s 目录 / 10min 链接）
	adminHandler.OnCommitted = func(mountName string) {
		for i := range mounts {
			if mounts[i].Name != mountName {
				continue
			}
			if inv, ok := davHandlers[i].Driver.(interface{ InvalidateAll() }); ok {
				inv.InvalidateAll()
			}
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

// createAllDrivers 根据配置创建所有云盘驱动
func createAllDrivers(ctx context.Context, cfg *conf.Config) ([]mountedDriver, error) {
	var mounts []mountedDriver

	for _, cc := range cfg.Clouds {
		drv, err := createDriver(&cc)
		if err != nil {
			return nil, fmt.Errorf("创建 %s 驱动失败: %w", cc.MountName(), err)
		}

		if err := drv.Init(ctx); err != nil {
			return nil, fmt.Errorf("初始化 %s 驱动失败: %w", cc.MountName(), err)
		}
		slog.Info("驱动就绪", "name", drv.Name(), "type", cc.MountName())

		// 启动后台 Token Watcher
		auth.StartWatcher(ctx, drv, 5*time.Minute)

		mounts = append(mounts, mountedDriver{
			Name:   cc.MountName(),
			Prefix: "/" + cc.MountName(),
			Drv:    drv,
		})
	}

	return mounts, nil
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

// displayName 返回云盘在导航/索引页的显示名。
// 若驱动报告健康警告（如移动云盘 authorization 过期）或 vault 层告警
// （manifest 缺失/损坏），则用警告文本覆盖显示名。
func displayName(m mountedDriver) string {
	label := cloudDisplayNames[m.Name]
	if label == "" {
		label = m.Name
	}
	if hr, ok := m.Drv.(driver.HealthReporter); ok {
		if w := hr.HealthWarning(); w != "" {
			return label + "：" + w // 挂载名 + 告警（如「移动云盘：令牌已过期，请更新令牌」）
		}
	}
	return label
}

// serveRootPropfind 为根路径生成 WebDAV PROPFIND 响应（列出所有云盘作为虚拟目录）
func serveRootPropfind(w http.ResponseWriter, r *http.Request, mounts []mountedDriver) {
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

func serveRootIndex(w http.ResponseWriter, mounts []mountedDriver, hasAdmin bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Mini Cloud WebDAV</title>
<style>
body{font-family:sans-serif;max-width:600px;margin:40px auto;padding:0 20px}
h1{font-size:22px;border-bottom:2px solid #0366d6;padding-bottom:10px}
.card{display:block;padding:16px 20px;margin:12px 0;border:1px solid #ddd;border-radius:8px;text-decoration:none;color:#333}
.card:hover{background:#f6f8fa;border-color:#0366d6}
.card .name{font-size:18px;font-weight:bold}
.card .desc{color:#666;margin-top:4px;font-size:14px}
.admin{display:inline-block;margin-top:8px}
</style></head><body>
<h1>☁️ Mini Cloud WebDAV</h1>
<p>已挂载 %d 个云盘：</p>
`, len(mounts))

	for _, m := range mounts {
		label := displayName(m)
		href := webdav.EncodePathSegments(m.Prefix) + "/"
		fmt.Fprintf(w, `<a class="card" href="%s">
<div class="name">%s</div>
<div class="desc">%s</div>
</a>`, html.EscapeString(href), html.EscapeString(label), html.EscapeString(href))
	}

	if hasAdmin {
		fmt.Fprint(w, `<p><a class="admin" href="/admin">管理</a></p>`)
	}

	fmt.Fprint(w, "</body></html>")
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
