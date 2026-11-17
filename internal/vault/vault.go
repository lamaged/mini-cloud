package vault

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"mini-cloud/internal/driver"
	"mini-cloud/internal/model"
)

// Config Vault 配置（全局一套，按挂载生效；docs/design.md §4.3）
type Config struct {
	Paths      []string // 根目录名列表（无前导斜杠，如 ["backup"]；首段匹配、大小写敏感）
	MaxStreams int      // 全局并发解密流上限（0 = 默认 4）
}

// Vault 包装层：拦截 vault 路径（虚拟树 + 解密），其余路径原样委托 cached。
// 断言约定（铁律 7）：Vault 不得实现 TokenManager/HealthReporter/CredentialUpdater。
type Vault struct {
	cached driver.Reader // 非 vault 路径：List/Link 委托（302 等行为不变）
	raw    driver.Driver // vault 路径 Link 用原始驱动（Phase 0=local；Phase 1 自管 URL 表）
	master []byte
	cfg    Config
	sem    *streamSem

	mu       sync.RWMutex
	roots    map[string]*root // vault 根名 → 虚拟树
	stopOnce sync.Once
}

// root 一个 vault 根的虚拟树（manifest 唯一来源）
type root struct {
	name     string
	warning  string
	entries  map[string]Entry       // 虚拟文件路径（"/电影/a.mkv"）→ 条目
	dirs     map[string]bool        // 虚拟目录集合（含 "/"）
	children map[string][]model.Obj // 虚拟目录 → 直接子项（loadRoot 一次性构建并排序，P15）
}

// streamSem 全局解密流信号量（I6：max_streams，超限 503 语义由调用方决定）
type streamSem struct {
	mu    sync.Mutex
	avail int
	max   int
}

func (s *streamSem) acquire(ctx context.Context, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		select {
		case <-ctx.Done():
			return false // P16：客户端断开不再傻等
		default:
		}
		s.mu.Lock()
		if s.avail > 0 {
			s.avail--
			s.mu.Unlock()
			return true
		}
		s.mu.Unlock()
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (s *streamSem) release() {
	s.mu.Lock()
	s.avail++
	s.mu.Unlock()
}

// acquireTimeout 信号量获取超时（测试可缩短）
var acquireTimeout = 5 * time.Second

// httpClient 云盘取数层 HTTP 客户端（测试可替换）。
// 超时 90s（P4）：4 MiB 窗口在 1 Mbps 弱网约需 32s，30s 总超时必然误杀；90s 给足余量。
var httpClient = &http.Client{Timeout: 90 * time.Second}

// New 创建 Vault 包装层（签名以本实现为准：vault.New(cached, raw, master, cfg)）
func New(cached driver.Reader, raw driver.Driver, master []byte, cfg Config) *Vault {
	if cfg.MaxStreams <= 0 {
		cfg.MaxStreams = 4
	}
	return &Vault{
		cached: cached,
		raw:    raw,
		master: master,
		cfg:    cfg,
		roots:  make(map[string]*root),
		sem:    &streamSem{avail: cfg.MaxStreams, max: cfg.MaxStreams},
	}
}

// Name 返回原始驱动名（类型名；断言必须针对原始驱动）
func (v *Vault) Name() string { return v.raw.Name() }

// Init 扫描挂载根：命中 cfg.Paths 的目录即 vault 根，加载其 manifest 建虚拟树。
// manifest 缺失/损坏 → 该根为空目录 + 告警日志，不崩溃（design §4.3）。
func (v *Vault) Init(ctx context.Context) error {
	objs, err := v.raw.List(ctx, "/")
	if err != nil {
		return fmt.Errorf("vault: 扫描挂载根失败: %w", err)
	}
	for i := range objs {
		o := &objs[i]
		if !o.IsDir || !v.isVaultRoot(o.Name) {
			continue
		}
		v.loadRoot(ctx, o.Name)
	}
	if len(v.roots) == 0 {
		slog.Info("vault: 未发现 vault 根目录，挂载按普通模式工作", "paths", v.cfg.Paths)
	}
	return nil
}

func (v *Vault) isVaultRoot(name string) bool {
	for _, p := range v.cfg.Paths {
		if p == name {
			return true
		}
	}
	return false
}

// loadRoot 加载单个 vault 根（manifest 唯一真值源）。
// Phase 1：URL 型驱动（天翼/移动）的 manifest 也经云盘取数层读取。
func (v *Vault) loadRoot(ctx context.Context, name string) {
	r := &root{name: name, entries: make(map[string]Entry), dirs: map[string]bool{"/": true}, children: make(map[string][]model.Obj)}
	manifestPath := path.Join(name, "_mc", "manifest.mcf")

	link, err := v.raw.Link(ctx, manifestPath)
	if err != nil {
		r.warning = fmt.Sprintf("vault 根 %s：manifest 缺失或不可读，虚拟视图为空目录", name)
		slog.Warn("vault: "+r.warning, "err", err)
		r.buildChildren() // 空虚拟树：List 返回空列表而非 not found
		v.mu.Lock()
		v.roots[name] = r
		v.mu.Unlock()
		return
	}

	var src io.ReadCloser
	switch {
	case link.Reader != nil:
		// 铁律 2（P11）：加密不支持百度——代理流非 ReaderAt 的驱动不建虚拟树，
		// 挂载完全普通模式（密文目录按普通目录可见）
		if _, ok := link.Reader.(io.ReaderAt); !ok {
			link.Reader.Close()
			slog.Info("vault: 该驱动不支持 Vault（仅天翼/移动），跳过该挂载的 vault 根", "root", name)
			return
		}
		src = link.Reader
	case link.URL != "":
		// Phase 1 接线：URL 型驱动经取数层读 manifest（Range 请求）
		up, err := NewUpstreamWithLink(ctx, v.raw, manifestPath, link, httpClient)
		if err != nil {
			r.warning = fmt.Sprintf("vault 根 %s：manifest 读取失败，虚拟视图为空目录", name)
			slog.Warn("vault: "+r.warning, "err", err)
			r.buildChildren()
			v.mu.Lock()
			v.roots[name] = r
			v.mu.Unlock()
			return
		}
		src = &rangeReadCloser{src: up}
	default:
		// 既无数据流也无 URL：无法读取，不建根（挂载按普通模式工作）
		slog.Info("vault: 驱动无法提供对象内容，跳过该挂载的 vault 根", "root", name)
		return
	}
	defer src.Close()

	m, err := LoadManifest(src, ManifestKey(v.master))
	if err != nil {
		r.warning = fmt.Sprintf("vault 根 %s：manifest 解密失败，虚拟视图为空目录", name)
		slog.Warn("vault: "+r.warning, "err", err)
		r.buildChildren() // 空虚拟树：List 返回空列表而非 not found
		v.mu.Lock()
		v.roots[name] = r
		v.mu.Unlock()
		return
	}

	for _, e := range m.Files {
		if !strings.HasPrefix(path.Clean(e.Obj), "_mc/o/") {
			// P18：manifest 异常条目不得把任意路径交给驱动
			slog.Warn("vault: 跳过对象路径非法的条目", "path", e.Path, "obj", e.Obj)
			continue
		}
		r.entries[e.Path] = e
		for d := path.Dir(e.Path); ; d = path.Dir(d) {
			r.dirs[d] = true
			if d == "/" {
				break
			}
		}
	}
	for _, d := range m.Dirs {
		r.dirs[d] = true
	}
	r.buildChildren()
	slog.Info("vault: 虚拟树就绪", "root", name, "files", len(m.Files))
	v.mu.Lock()
	v.roots[name] = r
	v.mu.Unlock()
}

// etagFor 虚拟文件 ETag（I3：PROPFIND 与 GET 同一字符串、同一来源）
func etagFor(e Entry) string {
	return fmt.Sprintf(`"%s-%d"`, e.FileID, e.Size)
}

// buildChildren 构建目录 → 子项索引（P15：List 不再全表扫描）
func (r *root) buildChildren() {
	for d := range r.dirs {
		r.children[d] = r.children[d][:0]
	}
	for p, e := range r.entries {
		parent := path.Dir(p)
		if r.dirs[parent] {
			r.children[parent] = append(r.children[parent], model.Obj{
				Name:        e.Name,
				Size:        e.Size,
				ModTime:     time.Unix(e.MTime, 0),
				IsDir:       false,
				ETag:        etagFor(e),
				ContentType: e.MIME,
			})
		}
	}
	for d := range r.dirs {
		if d == "/" {
			continue
		}
		parent := path.Dir(d)
		if r.dirs[parent] {
			r.children[parent] = append(r.children[parent], model.Obj{Name: path.Base(d), IsDir: true})
		}
	}
	for d := range r.children {
		sort.Slice(r.children[d], func(i, j int) bool { return r.children[d][i].Name < r.children[d][j].Name })
	}
}

// findRoot 判定挂载内路径是否命中 vault 根；返回 (根, vault 内相对路径, 是否命中)
func (v *Vault) findRoot(reqPath string) (*root, string, bool) {
	segs := strings.Split(strings.TrimPrefix(reqPath, "/"), "/")
	if len(segs) == 0 || segs[0] == "" {
		return nil, "", false
	}
	v.mu.RLock()
	r, ok := v.roots[segs[0]]
	v.mu.RUnlock()
	if !ok {
		return nil, "", false
	}
	inner := "/" + strings.Join(segs[1:], "/")
	return r, inner, true
}

// isMcPath 是否指向密文对象区（_mc 及以下；I4：PROPFIND 隐藏、GET 403）
func isMcPath(inner string) bool {
	return inner == "/_mc" || strings.HasPrefix(inner, "/_mc/")
}

// List：vault 路径返回虚拟树（manifest 唯一来源，_mc 绝不出现）；其余委托 cached
func (v *Vault) List(ctx context.Context, reqPath string) ([]model.Obj, error) {
	r, inner, hit := v.findRoot(reqPath)
	if !hit {
		return v.cached.List(ctx, reqPath)
	}
	if isMcPath(inner) {
		return nil, driver.ErrForbidden
	}
	objs, ok := r.children[inner]
	if !ok {
		return nil, fmt.Errorf("not found: %s", reqPath)
	}
	return append([]model.Obj(nil), objs...), nil // P22：返回副本，防调用方污染索引
}

// Link：vault 文件 → 解密数据流（随机访问 + Range 支持）；_mc → 403；其余委托 cached
func (v *Vault) Link(ctx context.Context, reqPath string) (*model.Link, error) {
	r, inner, hit := v.findRoot(reqPath)
	if !hit {
		return v.cached.Link(ctx, reqPath)
	}
	if isMcPath(inner) {
		return nil, driver.ErrForbidden // 绝不回落 302 交出密文（I4）
	}
	e, ok := r.entries[inner]
	if !ok {
		return nil, fmt.Errorf("not found: %s", reqPath)
	}

	objPath := path.Join(r.name, e.Obj) // 挂载内对象路径（"backup/_mc/o/..."）

	// U3/P21：全局信号量在 raw.Link（云盘 API 调用）之前获取——超限拒绝时不白开上游连接
	if !v.sem.acquire(ctx, acquireTimeout) {
		return nil, driver.ErrTooManyStreams
	}
	release := true
	defer func() {
		if release {
			v.sem.release()
		}
	}()

	fileID, err := DecodeFileID(e.FileID)
	if err != nil {
		return nil, err
	}
	headerSHA, err := hex.DecodeString(e.HeaderSHA256)
	if err != nil {
		return nil, fmt.Errorf("vault: header_sha256 解析失败: %w", err)
	}

	link, err := v.raw.Link(ctx, objPath)
	if err != nil {
		return nil, fmt.Errorf("vault: 获取对象失败: %w", err)
	}

	var src RangeSource
	if link.Reader != nil {
		// 流式驱动（local / 百度代理）：Reader 需支持 io.ReaderAt
		ra, ok := link.Reader.(io.ReaderAt)
		if !ok {
			// I3：百度代理流不支持随机读取且已退出支持目标（PM 拍板 2026-11-17）→ 502
			link.Reader.Close()
			return nil, fmt.Errorf("vault: 该驱动不在支持范围（Vault 仅支持天翼/移动）: %w", driver.ErrUpstream)
		}
		src = &readAtCloser{r: ra, c: link.Reader}
	} else if link.URL != "" {
		// URL 型驱动（天翼/移动 302 模式）：走云盘取数层（Phase 1 接线）
		up, err := NewUpstream(ctx, v.raw, objPath, httpClient)
		if err != nil {
			return nil, err
		}
		src = up
	} else {
		return nil, fmt.Errorf("vault: 上游既无数据流也无 URL: %s", objPath)
	}

	dr, err := OpenDecryptReader(src, v.master, fileID, headerSHA)
	if err != nil {
		src.Close()
		return nil, err
	}

	// I3 对象绑定强校验（Phase 1）：文件内嵌 metadata.p 必须与请求虚拟路径一致（防跨对象替换）；
	// P3：空路径一律拒绝（fail-closed，不因老对象/异常对象绕过）
	if p := dr.OriginalPath(); p != inner {
		dr.Close()
		return nil, fmt.Errorf("vault: 对象绑定校验失败：内嵌路径 %q 与请求路径 %q 不一致", p, inner)
	}

	release = false // 信号量交由 semDecryptReader.Close 释放

	hdr := http.Header{}
	if e.MIME != "" {
		hdr.Set("Content-Type", e.MIME)
	}
	hdr.Set("ETag", etagFor(e)) // I3：与 PROPFIND 同源（I5：空 MIME 时不得对 nil map 赋值）
	return &model.Link{
		Reader: &semDecryptReader{DecryptReader: dr, sem: v.sem},
		Header: hdr,
	}, nil
}

// Stop 幂等下传（所有权链：main → Handler.Close → Vault.Stop → CachedReader.Stop）
func (v *Vault) Stop() {
	v.stopOnce.Do(func() {
		if s, ok := v.cached.(interface{ Stop() }); ok {
			s.Stop()
		}
	})
}

// Warning 该挂载所有 vault 根的健康提示（manifest 缺失/损坏等）。
// 当前仅日志与测试使用；Phase 1 接入 /admin 展示（TODO 技术债）。
func (v *Vault) Warning() string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	var parts []string
	for _, r := range v.roots {
		if r.warning != "" {
			parts = append(parts, r.warning)
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "；")
}

// rangeReadCloser 适配 RangeSource → io.ReadCloser（manifest 顺序读取用）。
// 依赖外层限长（LoadManifest 的 LimitReader 32 MiB+1）：本适配器自身不做尺寸保护（P6）。
type rangeReadCloser struct {
	src RangeSource
	off int64
}

func (r *rangeReadCloser) Read(p []byte) (int, error) {
	n, err := r.src.ReadAt(p, r.off)
	r.off += int64(n)
	return n, err
}
func (r *rangeReadCloser) Close() error { return r.src.Close() }

// readAtCloser 适配 io.ReaderAt + io.Closer
type readAtCloser struct {
	r io.ReaderAt
	c io.Closer
}

func (a *readAtCloser) ReadAt(p []byte, off int64) (int, error) { return a.r.ReadAt(p, off) }
func (a *readAtCloser) Close() error                            { return a.c.Close() }

// semDecryptReader 关闭时释放全局信号量（P16：重复 Close 幂等，只释放一次）
type semDecryptReader struct {
	*DecryptReader
	sem      *streamSem
	closeOnce sync.Once
}

func (r *semDecryptReader) Close() error {
	var err error
	r.closeOnce.Do(func() {
		err = r.DecryptReader.Close()
		r.sem.release()
	})
	return err
}

var _ driver.Driver = (*Vault)(nil)
