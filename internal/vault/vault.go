package vault

import (
	"os"
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

// Config Vault 配置（全局一套，按挂载生效；docs/design.md §4.3/§5.1）
type Config struct {
	Paths      []string // 容器根名列表（无前导斜杠，如 ["backup"]；首段匹配、大小写敏感；容器下子目录为独立加密单元）
	MaxStreams int      // 全局并发解密流上限（0 = 默认 3，PM 拍板）
}

const (
	// scanTTL 容器扫描周期（懒刷新：TTL 到期后的首次 List 触发，§5.1）
	scanTTL = 5 * time.Minute
	// maxSubDirs 容器直接子目录数上限（超过跳过扫描并告警，防异常结构拖垮）
	maxSubDirs = 32
	// maxHotManifest 热刷新清单解压后大小上限（超过跳过热刷新、重启生效——AX6 瞬时双份峰值守卫）
	maxHotManifest = 8 << 20

	// maxNestedCheck 深层 _mc 检查的子目录数上限（P7）
	maxNestedCheck = 8
)

// manifestReadTimeout 扫描期单目录读取超时（I3：区别于播放流的 90s）。
// var 以便测试缩短（P2-3 等待取消测试需要）
var manifestReadTimeout = 5 * time.Second

// Vault 包装层：拦截 vault 路径（虚拟树 + 解密），其余路径原样委托 cached。
// 断言约定（铁律 7）：Vault 不得实现 TokenManager/HealthReporter/CredentialUpdater。
type Vault struct {
	cached driver.Reader // 非 vault 路径：List/Link 委托（302 等行为不变）
	raw    driver.Driver // vault 路径 Link 用原始驱动（Phase 0=local；Phase 1 自管 URL 表）
	master []byte
	cfg    Config
	sem    *streamSem

	mu         sync.RWMutex
	containers map[string]*container // 容器根名 → 容器状态（Phase 2 两级：容器 → 子根）
	disabled   bool                  // 驱动不支持 Vault（代理流）：全部路径普通模式（铁律 2）
	stopOnce   sync.Once
}

// container 一个 vault 容器根（cfg.Paths 条目；其直接子目录为独立加密单元，§5.1）
type container struct {
	name      string
	refreshMu sync.Mutex       // 扫描串行化（跨触发去重，TryLock）
	lastScan  time.Time        // 上次扫描时间（v.mu 下访问）
	warning   string           // 容器级告警（保留名/扫描失败/子目录过多/深层 _mc）
	roots     map[string]*root // 子根名 → 虚拟树（v.mu 下访问/交换）
}

// root 一个子根的虚拟树（manifest 唯一来源）
type root struct {
	name        string
	objBase     string                // 对象路径前缀（容器/子根，如 "backup/dir1"，Link 拼接用）
	warning     string                // 子根告警（manifest 不可读/损坏等，接 /admin §5.2）
	manifestSum [32]byte              // 解密后清单内容摘要（I4：变更检测，热刷新比对）
	entries     map[string]Entry      // 虚拟文件路径（"/电影/a.mkv"）→ 条目
	dirs        map[string]bool       // 虚拟目录集合（含 "/"）
	children    map[string][]model.Obj // 虚拟目录 → 直接子项（构建时排序，P15）
}

// streamSem 全局解密流信号量（I6：max_streams，超限 503 语义由调用方决定）
type streamSem struct {
	mu    sync.Mutex
	avail int
	max   int
}

// 进程级共享额度（R2-1，外部审核 2026-11-20）：跨挂载统一账本——
// 每挂载独立额度会使内存账失效（3 挂载 × 3 流 = 9 路解密 + 6 路清单解析）。
// 真正的内存护栏 = 进程级解密流上限 + 进程级 manifest 解析并发 2 + 16 MiB 清单上限，
// 64 MiB 软上限只是最后一道 GC 兜底。
var (
	globalStreamSem   = &streamSem{avail: 3, max: 3}
	globalManifestSem = make(chan struct{}, 2) // manifest 解析全局并发上限（#4）
)

// SetGlobalStreamLimit 设置进程级并发解密流上限（main 在装配前按配置调用一次）。
// n<=0 → 默认 3（PM 拍板 2026-11-20：全局默认 3，不能再高；配置天花板 4 由 conf 校验）。
func SetGlobalStreamLimit(n int) {
	globalStreamSem.mu.Lock()
	if n <= 0 {
		n = 3
	}
	globalStreamSem.avail, globalStreamSem.max = n, n
	globalStreamSem.mu.Unlock()
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
		// PM 拍板 2026-11-18：2–3 路并发为极限（对应 2 播放器 + 1 下载场景），
		// 默认 3；config 可调高（每流 ≈ 6 MiB，调高前先核算路由器空闲内存）
		cfg.MaxStreams = 3
	}
	// sem 指向进程级共享信号量（R2-1：跨挂载统一额度；上限由 main 经
	// SetGlobalStreamLimit 在装配前设置一次，cfg.MaxStreams 不再按挂载独立生效）
	return &Vault{
		cached:     cached,
		raw:        raw,
		master:     master,
		cfg:        cfg,
		containers: make(map[string]*container),
		sem:        globalStreamSem,
	}
}

// Name 返回原始驱动名（类型名；断言必须针对原始驱动）
func (v *Vault) Name() string { return v.raw.Name() }

// Init 装配容器根并做一次同步初始扫描（启动即用）。
// 扫描失败不崩溃：容器/子根记告警，对应路径回落普通模式（design §4.3/§5.1）。
func (v *Vault) Init(ctx context.Context) error {
	if len(v.cfg.Paths) == 0 {
		return nil
	}
	for _, p := range v.cfg.Paths {
		v.mu.Lock()
		if v.containers[p] == nil {
			v.containers[p] = &container{name: p, roots: make(map[string]*root)}
		}
		v.mu.Unlock()
	}
	for _, p := range v.cfg.Paths {
		v.scan(ctx, p, true)
	}
	total := 0
	v.mu.RLock()
	for _, c := range v.containers {
		total += len(c.roots)
	}
	v.mu.RUnlock()
	if total == 0 {
		slog.Info("vault: 未发现 vault 根目录，挂载按普通模式工作", "paths", v.cfg.Paths)
	}
	return nil
}

// scan 扫描容器（sync=true 同步初始扫描；否则为懒刷新）。
// refreshMu 串行化：并发触发只有一个执行（TryLock 失败即返回，§5.1 去重）。
func (v *Vault) scan(ctx context.Context, name string, initial bool) {
	v.mu.RLock()
	c := v.containers[name]
	v.mu.RUnlock()
	if c == nil {
		return
	}
	if !c.refreshMu.TryLock() {
		return // 已有扫描在跑
	}
	defer c.refreshMu.Unlock()

	// P9（RV-2026-11-19-03）：懒刷新用后台 ctx——客户端断开不取消扫描；
	// 仅受每目录 manifestReadTimeout 短超时约束。
	bg := ctx
	if !initial {
		bg = context.Background()
	}
	v.mu.Lock()
	c.lastScan = time.Now()
	v.mu.Unlock()

	lctx, cancel := context.WithTimeout(bg, manifestReadTimeout) // P1：容器级 List 同样 ≤5s
	objs, err := v.raw.List(lctx, "/"+name)
	cancel()
	if err != nil {
		// P2（RV-2026-11-19-07）注：此分支**有意**粘滞（直接 setContainerWarning、
		// 不经本轮 cwarns 重置）——容器 List 失败时无法验证问题是否已修复，
		// 保留旧告警比清空更安全；下一次成功的扫描会经 defer 的 cwarns 机制自然清除。
		v.setContainerWarning(name, fmt.Sprintf("容器扫描失败（%s）", err))
		return
	}
	// 容器告警为「本轮状态」：无新问题即清空（防旧警告残留，真机发现 2026-11-19）
	var cwMu sync.Mutex
	var cwarns []string
	cwarn := func(msg string) {
		cwMu.Lock()
		cwarns = append(cwarns, msg)
		cwMu.Unlock()
	}
	defer func() {
		v.mu.Lock()
		if c := v.containers[name]; c != nil {
			c.warning = strings.Join(cwarns, "；")
		}
		v.mu.Unlock()
	}()
	var dirs []string
	for i := range objs {
		if objs[i].IsDir {
			dirs = append(dirs, objs[i].Name)
		}
	}
	if len(dirs) > maxSubDirs {
		cwarn(fmt.Sprintf("容器下子目录过多（%d > %d），跳过扫描", len(dirs), maxSubDirs))
		return
	}
	v.pruneRoots(name, dirs)
	// P2（RV-2026-11-19-04）：子目录并发扫描——初始同步扫描最坏 N×5s 阻塞启动，
	// 并发后受各目录 5s 超时约束、总时长 ≈ 单目录（上限 32 目录守卫）
	var wg sync.WaitGroup
	for _, d := range dirs {
		if strings.HasPrefix(d, "_") {
			// 保留名（旧结构 _mc 等）：屏蔽 + 告警（§5.1，防旧结构密文泄露）
			cwarn(fmt.Sprintf("发现保留名目录 %q（旧结构？已屏蔽，请迁移到 <名字>/_mc）", d))
			continue
		}
		if v.isDisabled() {
			break // P3：驱动能力不足（铁律 2）已禁用整挂载，停止无谓的网络扫描
		}
		wg.Add(1)
		go func(d string) {
			defer wg.Done()
			if w := v.refreshRoot(bg, name, d); w != "" {
				cwarn(w)
			}
		}(d)
	}
	wg.Wait()
}

// isDisabled 驱动不支持 Vault（代理流）标记
func (v *Vault) isDisabled() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.disabled
}

// refreshRoot 扫描单个子目录（加密单元）：判定 _mc 存在 → 读清单 → 摘要比对 → 原子重建。
// 失败隔离（I3）：单目录失败记告警、跳过，绝不拖累整体；下周期重试。
// refreshRoot 扫描单个子目录；返回容器级告警（"" 表示无；供 scan 累积为本轮状态）
func (v *Vault) refreshRoot(ctx context.Context, cont, dir string) string {
	if v.isDisabled() {
		return "" // P3：禁用后不再做无谓的网络调用
	}
	mctx, cancel := context.WithTimeout(ctx, manifestReadTimeout)
	defer cancel()

	// 判定：子目录里是否有 _mc（先 List 一层，避免把「无 _mc」误判为单元）
	objs, err := v.raw.List(mctx, "/"+cont+"/"+dir)
	if err != nil {
		return fmt.Sprintf("子目录 %s 读取失败，跳过本轮（%s）", dir, err)
	}
	hasMc := false
	var subdirs []string
	for i := range objs {
		if !objs[i].IsDir {
			continue
		}
		if objs[i].Name == "_mc" {
			hasMc = true
		} else {
			subdirs = append(subdirs, objs[i].Name)
		}
	}
	if !hasMc {
		// 非加密目录：摘除旧树（若有）；P7 深层 _mc 检查（发现即返回容器告警）
		v.removeRoot(cont, dir)
		return v.checkNestedMc(mctx, cont, dir, subdirs)
	}

	manifestPath := path.Join(cont, dir, "_mc", "manifest.mcf")
	// #4/R2-1：manifest 读取+解析全程持**进程级**信号量（读取/解密/解压/JSON
	// 多层瞬时内存叠加受控；跨挂载共享，3 挂载时仍只有 2 路解析）。
	// P2-3（外部审核 2026-11-20）：等待额度时响应超时/取消，不傻等
	select {
	case globalManifestSem <- struct{}{}:
		defer func() { <-globalManifestSem }()
	case <-mctx.Done():
		return "" // 扫描超时/取消：不阻塞在额度等待上
	}
	link, err := v.raw.Link(mctx, manifestPath)
	if err != nil {
		// 网络/对象缺失（上传中间态）：有旧树保留旧树，无旧树建空树
		//（保证「含 _mc 的目录必有子根」→ _mc 恒受 403 屏蔽，不回落普通模式）
		v.mu.RLock()
		_, exists := v.containers[cont].roots[dir]
		v.mu.RUnlock()
		if !exists {
			v.setRootTree(cont, dir, emptyRoot(cont, dir))
		}
		v.setRootWarning(cont, dir, fmt.Sprintf("manifest 不可读，%s", condStr(exists, "保留旧树，下周期重试", "虚拟视图为空目录")))
		return ""
	}

	var src io.ReadCloser
	switch {
	case link.Reader != nil:
		// 铁律 2（P11）：加密不支持百度——代理流非 ReaderAt 的驱动禁用整挂载 Vault
		if _, ok := link.Reader.(io.ReaderAt); !ok {
			link.Reader.Close()
			v.disableVault()
			return ""
		}
		src = link.Reader
	case link.URL != "":
		up, err := NewUpstreamWithLink(mctx, v.raw, manifestPath, link, httpClient)
		if err != nil {
			v.mu.RLock()
			_, exists := v.containers[cont].roots[dir]
			v.mu.RUnlock()
			if !exists {
				v.setRootTree(cont, dir, emptyRoot(cont, dir))
			}
			v.setRootWarning(cont, dir, fmt.Sprintf("manifest 读取失败，%s", condStr(exists, "保留旧树，下周期重试", "虚拟视图为空目录")))
			return ""
		}
		src = &rangeReadCloser{src: up}
	default:
		// 既无数据流也无 URL：驱动无法提供对象内容 → 禁用整挂载（同 Phase 1）
		v.disableVault()
		return ""
	}
	defer src.Close()

	m, sum, rawSize, err := LoadManifestSum(src, ManifestKey(v.master))
	if err != nil {
		// 清单损坏/校验失败：子根保留、虚拟树为空 + 警告（密文仍 403 屏蔽，§5.1）
		v.setRootTree(cont, dir, emptyRoot(cont, dir))
		v.setRootWarning(cont, dir, fmt.Sprintf("manifest 解密失败，虚拟视图为空目录（%s）", err))
		return ""
	}
	// #3（外部审核 2026-11-20）：虚拟路径规范校验（fail-closed）——
	// 相对路径等不规范条目会使 buildRoot 的 path.Dir 循环不收敛（死循环）
	if perr := validateManifestPaths(m); perr != nil {
		v.setRootTree(cont, dir, emptyRoot(cont, dir))
		v.setRootWarning(cont, dir, fmt.Sprintf("manifest 含不规范路径，拒绝加载（%s）", perr))
		return ""
	}

	// 大清单守卫（热刷新）：已有树且清单过大 → 跳过本轮（重启生效；首建不限制）
	v.mu.RLock()
	old, exists := v.containers[cont].roots[dir]
	v.mu.RUnlock()
	if exists && rawSize > maxHotManifest {
		v.setRootWarning(cont, dir, fmt.Sprintf("manifest 过大（%d MiB > %d MiB），跳过热刷新，重启生效", rawSize>>20, maxHotManifest>>20))
		return ""
	}
	if exists && old.manifestSum == sum {
		// #11（外部审核 2026-11-20）：读取成功即先清旧告警（如网络瞬断产生的
		// 「manifest 不可读」），再判断摘要——否则旧告警残留
		v.clearRootWarning(cont, dir)
		return "" // I4：内容未变化，零成本跳过
	}

	nr := buildRoot(cont, dir, m, sum)
	v.setRootTree(cont, dir, nr)
	v.invalidateCache()
	slog.Info("vault: 子根虚拟树就绪", "container", cont, "root", dir, "files", len(m.Files))
	return ""
}

// condStr 条件文案（告警措辞用）
func condStr(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

// validateManifestPaths 校验清单全部虚拟路径规范（#3）：任一不合格即拒绝加载
func validateManifestPaths(m *Manifest) error {
	for i := range m.Files {
		if err := ValidateVirtualPath(m.Files[i].Path); err != nil {
			return fmt.Errorf("条目 %d：%w", i, err)
		}
	}
	for i := range m.Dirs {
		if err := ValidateVirtualPath(m.Dirs[i]); err != nil {
			return fmt.Errorf("目录 %d：%w", i, err)
		}
	}
	return nil
}

// buildRoot 从 manifest 构建子根虚拟树（锁外构建，调用方 setRootTree 原子交换）
func buildRoot(cont, dir string, m *Manifest, sum [32]byte) *root {
	r := &root{name: dir, objBase: path.Join(cont, dir), manifestSum: sum,
		entries: make(map[string]Entry), dirs: map[string]bool{"/": true}, children: make(map[string][]model.Obj)}
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
	return r
}

// emptyRoot 空子根（含 _mc 的目录清单不可读/损坏时的占位：虚拟树为空、_mc 恒 403）
func emptyRoot(cont, dir string) *root {
	r := &root{name: dir, objBase: path.Join(cont, dir),
		entries: make(map[string]Entry), dirs: map[string]bool{"/": true}, children: make(map[string][]model.Obj)}
	r.buildChildren()
	return r
}

// setRootTree 原子设置子根虚拟树（v.mu 内交换；调用方持新树引用）
func (v *Vault) setRootTree(cont, dir string, r *root) {
	v.mu.Lock()
	if c := v.containers[cont]; c != nil {
		c.roots[dir] = r
	}
	v.mu.Unlock()
}

// removeRoot 摘除子根（云盘上已消失或变为非加密目录）
func (v *Vault) removeRoot(cont, dir string) {
	v.mu.Lock()
	c := v.containers[cont]
	_, existed := false, false
	if c != nil {
		_, existed = c.roots[dir]
		delete(c.roots, dir)
	}
	v.mu.Unlock()
	if existed {
		slog.Info("vault: 子根已摘除", "container", cont, "root", dir)
		v.invalidateCache()
	}
}

// pruneRoots 摘除容器扫描中已消失的子根
func (v *Vault) pruneRoots(cont string, present []string) {
	set := make(map[string]bool, len(present))
	for _, d := range present {
		set[d] = true
	}
	v.mu.Lock()
	c := v.containers[cont]
	removed := false
	if c != nil {
		for name := range c.roots {
			if !set[name] {
				delete(c.roots, name)
				removed = true
			}
		}
	}
	v.mu.Unlock()
	if removed {
		slog.Info("vault: 子根已摘除", "container", cont)
		v.invalidateCache()
	}
}

// checkNestedMc P7（RV-2026-11-19-03）：非加密子目录的更深层若出现 _mc，
// 不会被识别为加密单元（密文将按普通文件可枚举）——检测一层并告警。
func (v *Vault) checkNestedMc(ctx context.Context, cont, dir string, subdirs []string) string {
	if len(subdirs) == 0 || len(subdirs) > maxNestedCheck {
		return ""
	}
	for _, sd := range subdirs {
		objs, err := v.raw.List(ctx, "/"+cont+"/"+dir+"/"+sd)
		if err != nil {
			continue
		}
		for i := range objs {
			if objs[i].IsDir && objs[i].Name == "_mc" {
				return fmt.Sprintf("子目录 %s/%s 内发现更深层 _mc：仅识别第二层单元，该目录未找到（404）", dir, sd)
			}
		}
	}
	return ""
}

// disableVault 驱动不支持 Vault（代理流非 ReaderAt，铁律 2）——清空容器，
// 全部路径回落普通模式（与 Phase 1 行为一致）
func (v *Vault) disableVault() {
	v.mu.Lock()
	v.containers = map[string]*container{}
	v.disabled = true
	v.mu.Unlock()
	slog.Info("vault: 该驱动不支持 Vault（仅天翼/移动），跳过该挂载的 vault 装配")
}

// maybeScan 懒刷新入口（§5.1）：请求路径首段为容器名时，TTL 到期则触发一次后台扫描
func (v *Vault) maybeScan(reqPath string) {
	segs := strings.Split(strings.TrimPrefix(reqPath, "/"), "/")
	if len(segs) == 0 || segs[0] == "" {
		return
	}
	v.mu.RLock()
	c := v.containers[segs[0]]
	due := c != nil && time.Since(c.lastScan) >= scanTTL
	v.mu.RUnlock()
	if !due {
		return
	}
	go v.scan(context.Background(), segs[0], false)
}

// clearRootWarning 清除子根告警（读取成功即清，防旧告警残留，#11）
func (v *Vault) clearRootWarning(cont, dir string) {
	v.mu.Lock()
	if c := v.containers[cont]; c != nil {
		if r := c.roots[dir]; r != nil {
			r.warning = ""
		}
	}
	v.mu.Unlock()
}

// setContainerWarning / setRootWarning 记录告警（接 /admin 展示，§5.2）
func (v *Vault) setContainerWarning(cont, msg string) {
	v.mu.Lock()
	if c := v.containers[cont]; c != nil {
		c.warning = msg
	}
	v.mu.Unlock()
}

func (v *Vault) setRootWarning(cont, dir, msg string) {
	v.mu.Lock()
	if c := v.containers[cont]; c != nil {
		if r := c.roots[dir]; r != nil {
			r.warning = msg
		}
	}
	v.mu.Unlock()
}

// invalidateCache 树变化后清空目录/链接缓存（30s 目录 / 10min 链接缓存里的
// 旧 fileID 签名 URL 必须失效，§5.1）
func (v *Vault) invalidateCache() {
	if inv, ok := v.cached.(interface{ InvalidateAll() }); ok {
		inv.InvalidateAll()
	}
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

// findRoot 两级判定（Phase 2，§5.1）：容器名（首段）→ 子根名（第二段）；
// 返回 (子根, 子根内相对路径, 是否命中)。容器本身（无第二段）不命中 → 普通列表。
func (v *Vault) findRoot(reqPath string) (*root, string, bool) {
	segs := strings.Split(strings.TrimPrefix(reqPath, "/"), "/")
	if len(segs) < 2 || segs[0] == "" || segs[1] == "" {
		return nil, "", false
	}
	v.mu.RLock()
	c, ok := v.containers[segs[0]]
	var r *root
	if ok {
		r = c.roots[segs[1]]
	}
	v.mu.RUnlock()
	if r == nil {
		return nil, "", false
	}
	inner := "/" + strings.Join(segs[2:], "/")
	return r, inner, true
}

// isMcPath 是否指向密文对象区（_mc 及以下；I4：PROPFIND 隐藏、GET 403）
func isMcPath(inner string) bool {
	return inner == "/_mc" || strings.HasPrefix(inner, "/_mc/")
}

// List：容器 = 加密专区（PM 拍板 2026-11-19：非加密单元一律不显示 + 403）——
// 容器列表由当前子根合成（仅加密单元可见）；子根内为虚拟树（_mc 绝不出现）；
// 其余路径委托 cached。
// 懒刷新：容器路径的首次 List（TTL 到期后）触发后台扫描（§5.1）。
func (v *Vault) List(ctx context.Context, reqPath string) ([]model.Obj, error) {
	v.maybeScan(reqPath)
	if hit, isContainer := v.classify(reqPath); hit {
		if isContainer {
			return v.containerListing(reqPath)
		}
		// 容器下但非加密单元（stray 文件/无 _mc 目录/放错层级的密文/保留名）：
		// 程序不认识 → 一律「未找到」（PM 口径：不是主动藏，是不认识即不管；
		// 密文同样拿不到，P7 枚举风险根治）
		return nil, notFoundErr(reqPath)
	}
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

// classify 容器路径分类：返回 (是否容器区路径, 是否容器本身)。
// 首段为容器名且第二段非子根 → (true, false)（非单元路径，List 403）；
// 首段为容器名且无第二段 → (true, true)（容器本身，合成列表）。
func (v *Vault) classify(reqPath string) (bool, bool) {
	segs := strings.Split(strings.TrimPrefix(reqPath, "/"), "/")
	if len(segs) == 0 || segs[0] == "" {
		return false, false
	}
	v.mu.RLock()
	c, ok := v.containers[segs[0]]
	isRoot := false
	if ok && len(segs) >= 2 && segs[1] != "" {
		_, isRoot = c.roots[segs[1]]
	}
	v.mu.RUnlock()
	if !ok {
		return false, false
	}
	if len(segs) < 2 || segs[1] == "" {
		return true, true // 容器本身
	}
	return !isRoot, false
}

// notFoundErr 「未找到」错误（包装 os.ErrNotExist → webdav 映射 404；
// 语义：程序不认识该路径，并非「存在但禁止」）
func notFoundErr(reqPath string) error {
	return fmt.Errorf("not found: %s: %w", reqPath, os.ErrNotExist)
}

// containerListing 容器列表：仅当前加密单元（子根）可见，排序返回副本
func (v *Vault) containerListing(reqPath string) ([]model.Obj, error) {
	segs := strings.Split(strings.TrimPrefix(reqPath, "/"), "/")
	v.mu.RLock()
	c := v.containers[segs[0]]
	var objs []model.Obj
	if c != nil {
		for name := range c.roots {
			objs = append(objs, model.Obj{Name: name, IsDir: true})
		}
	}
	v.mu.RUnlock()
	sort.Slice(objs, func(i, j int) bool { return objs[i].Name < objs[j].Name })
	return objs, nil
}

// Link：vault 文件 → 解密数据流（随机访问 + Range 支持）；_mc → 403；其余委托 cached
func (v *Vault) Link(ctx context.Context, reqPath string) (*model.Link, error) {
	if hit, _ := v.classify(reqPath); hit {
		// 容器本身或容器下非加密单元：一律「未找到」（不认识即不管；_mc 的 403 屏蔽不受影响）
		return nil, notFoundErr(reqPath)
	}
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

	objPath := path.Join(r.objBase, e.Obj) // 挂载内对象路径（"backup/dir1/_mc/o/..."）

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
	if err != nil || len(headerSHA) != 32 {
		// #8：解码失败/长度异常 → fail-closed（跳过校验 = 放行可能被篡改的对象）
		return nil, fmt.Errorf("vault: header_sha256 非法（解码失败或长度 %d ≠ 32）", len(headerSHA))
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
		// URL 型驱动（天翼/移动 302 模式）：走云盘取数层。
		// #12（外部审核 2026-11-20）：复用上方已获取的 link（NewUpstreamWithLink），
		// 避免每次开播重复调用一次云盘 API
		up, err := NewUpstreamWithLink(ctx, v.raw, objPath, link, httpClient)
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

// Warning 该挂载 vault 的健康提示（容器/子根告警聚合；Phase 2 §5.2 接 /admin 展示）。
// 双通道约定（铁律 7）：驱动告警（HealthReporter）由 main 对原始驱动收集，
// 本方法只含 vault 自身告警——两者由 main 合并展示。
func (v *Vault) Warning() string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	var parts []string
	for _, c := range v.containers {
		if c.warning != "" {
			parts = append(parts, c.warning)
		}
		for _, r := range c.roots {
			if r.warning != "" {
				parts = append(parts, r.warning)
			}
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
