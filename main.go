package main

import (
	"context"
	"crypto/subtle"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mini-cloud/drivers/baidu"
	"mini-cloud/drivers/local"
	"mini-cloud/drivers/mobile"
	"mini-cloud/drivers/tianyi"
	"mini-cloud/internal/auth"
	"mini-cloud/internal/conf"
	"mini-cloud/internal/driver"
	"mini-cloud/internal/fs"
	"mini-cloud/internal/webdav"
)

const version = "v0.5"

// mountedDriver 已挂载的云盘驱动信息
type mountedDriver struct {
	Name   string        // 驱动名称（类型名，如 "mobile"）
	Prefix string        // URL 前缀，如 "/mobile"
	Drv    driver.Driver // 驱动实例
}

func main() {
	configPath := flag.String("config", "config.json", "配置文件路径")
	debug := flag.Bool("debug", false, "启用调试日志（含请求追踪）")
	quiet := flag.Bool("quiet", false, "静默模式（仅错误日志）")
	flag.Parse()

	// 日志级别：quiet > debug > 默认(WARN)
	logLevel := slog.LevelWarn
	if *debug {
		logLevel = slog.LevelDebug
	} else if !*quiet {
		logLevel = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})))

	// 加载配置
	cfg, err := conf.Load(*configPath)
	if err != nil {
		slog.Error("加载配置失败", "err", err)
		os.Exit(1)
	}
	slog.Info("配置加载完成", "listen", cfg.Server.Listen, "clouds", len(cfg.Clouds))

	// 创建所有驱动
	ctx := context.Background()
	mounts, err := createAllDrivers(ctx, cfg)
	if err != nil {
		slog.Error("创建驱动失败", "err", err)
		os.Exit(1)
	}

	// HTTP 路由
	mux := http.NewServeMux()

	// 根路径：GET 返回导航页，WebDAV 方法返回索引
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if !checkAuth(r, cfg.Server.Username, cfg.Server.Password) {
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
			serveRootIndex(w, mounts)
			return
		}
		// PROPFIND → 返回云盘列表（兼容 WebDAV 客户端）
		if r.Method == "PROPFIND" {
			serveRootPropfind(w, r, mounts)
			return
		}
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	})

	// 每个云盘注册到 /<prefix>/
	for i := range mounts {
		m := &mounts[i]
		davHandler := &webdav.Handler{
			Driver: fs.NewCachedReader(fs.NewRetryReader(m.Drv)),
			Prefix: m.Prefix,
		}
		// 注册 /<prefix>/ 及其子路径
		mux.HandleFunc(m.Prefix+"/", func(w http.ResponseWriter, r *http.Request) {
			if !checkAuth(r, cfg.Server.Username, cfg.Server.Password) {
				w.Header().Set("WWW-Authenticate", `Basic realm="mini-cloud"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			davHandler.ServeHTTP(w, r)
		})
		slog.Info("挂载云盘", "name", m.Name, "prefix", m.Prefix)
	}

	// 启动服务
	server := &http.Server{
		Addr:    cfg.Server.Listen,
		Handler: withLogging(mux),
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
</D:response>`, m.Prefix, m.Name)
	}

	fmt.Fprint(w, "\n</D:multistatus>")
}

func serveRootIndex(w http.ResponseWriter, mounts []mountedDriver) {
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
</style></head><body>
<h1>☁️ Mini Cloud WebDAV</h1>
<p>已挂载 %d 个云盘：</p>
`, len(mounts))

	// 云盘中文名称映射
	names := map[string]string{
		"local":  "本地文件",
		"tianyi": "天翼云盘",
		"mobile": "移动云盘",
			"baidu":  "百度网盘",
	}

	for _, m := range mounts {
		label := names[m.Name]
		if label == "" {
			label = m.Name
		}
		fmt.Fprintf(w, `<a class="card" href="%s/">
<div class="name">%s</div>
<div class="desc">%s</div>
</a>`, m.Prefix, label, m.Prefix+"/")
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

