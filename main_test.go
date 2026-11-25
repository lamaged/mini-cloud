package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"mini-cloud/drivers/baidu"
	"mini-cloud/drivers/local"
	"mini-cloud/drivers/mobile"
	"mini-cloud/drivers/tianyi"
	"mini-cloud/internal/admin"
	"mini-cloud/internal/conf"
	"mini-cloud/internal/driver"
	"mini-cloud/internal/model"
)

// mkMount 构造测试挂载并写入初始状态
func mkMount(name, prefix string, st mountStatus) *MountRuntime {
	m := &MountRuntime{Name: name, Prefix: prefix}
	m.applyInitResult(nil)
	m.mu.Lock()
	m.status = st
	m.mu.Unlock()
	return m
}

func TestXMLTextEscapes(t *testing.T) {
	got := xmlText(`a&b<c>"d"`)
	want := "a&amp;b&lt;c&gt;&#34;d&#34;"
	if got != want {
		t.Errorf("xmlText = %q, want %q", got, want)
	}
}

func TestServeRootPropfindEscapesXML(t *testing.T) {
	mounts := []*MountRuntime{mkMount("mobile", "/移&动", statusHealthy)}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PROPFIND", "/", nil)
	serveRootPropfind(rec, req, mounts)

	body := rec.Body.String()
	if strings.Contains(body, "移&动") {
		t.Errorf("prefix not XML-escaped: %s", body)
	}
	// 前缀先百分号编码（移 → %E7%A7%BB；& 在 URL 路径中合法、无需编码），XML 层再转义 &
	if !strings.Contains(body, "%E7%A7%BB&amp;%E5%8A%A8") {
		t.Errorf("expected percent-encoded+XML-escaped prefix, got: %s", body)
	}
}

// TestServeRootIndexEscapesHTML：挂载名/前缀转义 + 全正常时（PM 拍板 2026-11-24）
// 首页不显示「管理」链接。
func TestServeRootIndexEscapesHTML(t *testing.T) {
	mounts := []*MountRuntime{mkMount("mobile", "/<script>", statusHealthy)}
	rec := httptest.NewRecorder()
	serveRootIndex(rec, mounts, true)

	body := rec.Body.String()
	if strings.Contains(body, "/<script>") {
		t.Errorf("prefix not escaped: %s", body)
	}
	// 前缀百分号编码后无 < > 字符，HTML 层无需再转义
	if !strings.Contains(body, "%3Cscript%3E") {
		t.Errorf("expected percent-encoded prefix in HTML, got: %s", body)
	}
	if strings.Contains(body, `href="/admin"`) {
		t.Errorf("admin link must be hidden when all mounts healthy: %s", body)
	}
}

// TestServeRootIndexAdminLinkOnProblem：有异常挂载时「管理」链接出现
func TestServeRootIndexAdminLinkOnProblem(t *testing.T) {
	mounts := []*MountRuntime{
		mkMount("mobile", "/mobile", statusHealthy),
		mkMount("baidu", "/baidu", statusUnavailable),
	}
	rec := httptest.NewRecorder()
	serveRootIndex(rec, mounts, true)
	if !strings.Contains(rec.Body.String(), `href="/admin"`) {
		t.Errorf("admin link missing when a mount has problem: %s", rec.Body.String())
	}
}

func TestServeRootIndexNoAdminLink(t *testing.T) {
	mounts := []*MountRuntime{mkMount("mobile", "/mobile", statusUnavailable)}
	rec := httptest.NewRecorder()
	serveRootIndex(rec, mounts, false)

	if strings.Contains(rec.Body.String(), "/admin") {
		t.Error("admin link should be absent when hasAdmin=false")
	}
}

// TestServeRootIndexColors 状态颜色落在挂载名文字上（黑/黄/红，无灰色档）
func TestServeRootIndexColors(t *testing.T) {
	mounts := []*MountRuntime{
		mkMount("mobile", "/mobile", statusHealthy),
		mkMount("tianyi", "/tianyi", statusWarning),
		mkMount("baidu", "/baidu", statusUnavailable),
	}
	rec := httptest.NewRecorder()
	serveRootIndex(rec, mounts, true)

	body := rec.Body.String()
	for _, want := range []string{`class="name ok"`, `class="name warning"`, `class="name error"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing color class %q: %s", want, body)
		}
	}
	// 红卡不可点击（PM 拍板 2026-11-25）：不可用挂载渲染为不可点的 div，不带链接
	if !strings.Contains(body, `<div class="card nolink">`) {
		t.Errorf("unavailable mount should render as non-clickable div: %s", body)
	}
	if strings.Contains(body, `href="/baidu/"`) {
		t.Errorf("unavailable mount must not be a link: %s", body)
	}
	if !strings.Contains(body, `href="/mobile/"`) || !strings.Contains(body, `href="/tianyi/"`) {
		t.Errorf("healthy/warning mounts should remain links: %s", body)
	}
	// 全部失败（A04）也要能看到每个挂载：名字仍在页面中
	if !strings.Contains(body, "已配置 3 个云盘") {
		t.Errorf("expected configured count, got: %s", body)
	}
}

// TestSynthesizeStatus 状态合成唯一规则（§5.2）：初始化失败红 > AuthExpired 红 >
// 探测连续 2 次失败红 > 警告黄 > 正常黑；一次探测成功不得覆盖已确认失效。
func TestSynthesizeStatus(t *testing.T) {
	expired := driver.DriverHealth{AuthExpired: true, Warning: "令牌已过期，请更新令牌"}
	warn := driver.DriverHealth{Warning: "云主机发现失败，服务受限"}

	tests := []struct {
		name       string
		initErr    error
		dh         driver.DriverHealth
		probeFails int
		want       mountStatus
	}{
		{"初始化失败", errAny, driver.DriverHealth{}, 0, statusUnavailable},
		{"凭据已失效优先级最高", nil, expired, 0, statusUnavailable},
		{"凭据已失效不被探测成功覆盖", nil, expired, 1, statusUnavailable},
		{"探测连续两次失败", nil, driver.DriverHealth{}, 2, statusUnavailable},
		{"探测单次失败不转红", nil, driver.DriverHealth{}, 1, statusHealthy},
		{"警告", nil, warn, 0, statusWarning},
		{"正常", nil, driver.DriverHealth{}, 0, statusHealthy},
	}
	for _, tt := range tests {
		got, _ := synthesizeStatus(tt.initErr, tt.dh, tt.probeFails, "")
		if got != tt.want {
			t.Errorf("%s: synthesizeStatus = %q, want %q", tt.name, got, tt.want)
		}
	}
}

var errAny = &testErr{}

type testErr struct{}

func (e *testErr) Error() string { return "测试错误" }

// TestCreateAllDriversIsolation A（§4）：单挂载创建/初始化失败不影响其他挂载；
// 状态表含全部配置挂载；失败的挂载保留 RawDriver（创建成功时）。
func TestCreateAllDriversIsolation(t *testing.T) {
	cfg := &conf.Config{Clouds: []conf.CloudConfig{
		{Name: "本地", Type: "local"},
		{Name: "百度", Type: "baidu", RefreshToken: ""}, // Init 失败：尚未授权
		{Name: "坏类型", Type: "nonexistent"},           // 创建失败
	}}
	mounts := createAllDrivers(t.Context(), cfg)

	if len(mounts) != 3 {
		t.Fatalf("状态表应含全部配置挂载：got %d, want 3", len(mounts))
	}
	var avail int
	for _, m := range mounts {
		if m.Available {
			avail++
		}
		st, _ := m.snapshot()
		if !m.Available && st != statusUnavailable {
			t.Errorf("失败挂载 %s 状态 = %q, want unavailable", m.Name, st)
		}
		if m.InitError == nil && !m.Available {
			t.Errorf("失败挂载 %s 应有 InitError", m.Name)
		}
	}
	if avail != 1 {
		t.Errorf("available = %d, want 1", avail)
	}
	// 百度 Init 失败但创建成功 → RawDriver 保留（/admin 更新入口，D1）
	for _, m := range mounts {
		if m.Name == "百度" && m.RawDriver == nil {
			t.Error("Init 失败的百度挂载应保留 RawDriver")
		}
		if m.Name == "坏类型" && m.RawDriver != nil {
			t.Error("创建失败的挂载 RawDriver 应为 nil")
		}
	}
}

// TestAllDriversFailedStillServesDiagnostic Q1 拍板：全部失败时诊断首页仍渲染全部挂载
func TestAllDriversFailedStillServesDiagnostic(t *testing.T) {
	mounts := []*MountRuntime{
		mkMount("baidu", "/百度", statusUnavailable),
		mkMount("mobile", "/移动云盘", statusUnavailable),
	}
	rec := httptest.NewRecorder()
	serveRootIndex(rec, mounts, true)
	body := rec.Body.String()
	if !strings.Contains(body, "已配置 2 个云盘") {
		t.Errorf("diagnostic page should show configured mounts, got: %s", body)
	}
	if !strings.Contains(body, "移动云盘") || !strings.Contains(body, "百度网盘") {
		t.Errorf("diagnostic page should show every failed mount, got: %s", body)
	}
}

// TestVaultSupported 铁律 2：加密仅装配天翼/移动。
// P1（RV-2026-11-19-07）：对**真实驱动实例**断言——锁死「Name() 返回值 ↔ 允许列表」
// 不变量（回归教训：曾手写名单与 Name() 实际值不符导致全云盘加密失效）。
func TestVaultSupported(t *testing.T) {
	tests := []struct {
		name string
		drv  driver.Driver
		want bool
	}{
		{"tianyi 个人", tianyi.New("t", "u", "p", "", t.TempDir()), true},
		{"tianyi 家庭", tianyi.New("t", "u", "p", "family-1", t.TempDir()), true},
		{"mobile", mobile.New("m", "auth", t.TempDir()), true},
		{"local", func() driver.Driver {
			d, err := local.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			return d
		}(), true},
		{"baidu", baidu.New("b", "rt", "cid", "cs", t.TempDir()), false},
	}
	for _, tt := range tests {
		if got := vaultSupported(tt.drv); got != tt.want {
			t.Errorf("vaultSupported(%s) = %v, want %v（驱动 Name()=%q）", tt.name, got, tt.want, tt.drv.Name())
		}
	}
}

// TestMountRuntimeProbeUpdate 探测结果快照更新（I3 并发安全 + 探测判据 I2）
func TestMountRuntimeProbeUpdate(t *testing.T) {
	m := mkMount("mobile", "/mobile", statusHealthy)
	m.applyProbeResult(nil)
	if m.lastCheckAt.IsZero() {
		t.Error("探测成功应更新 lastCheckAt")
	}
	if !m.lastSuccessAt.After(time.Time{}) {
		t.Error("探测成功应更新 lastSuccessAt")
	}
	st, _ := m.snapshot()
	if st != statusHealthy {
		t.Errorf("status = %q, want healthy", st)
	}

	// 连续 2 次失败 → 红；一次成功恢复
	m.applyProbeResult(errAny)
	m.applyProbeResult(errAny)
	if st, _ := m.snapshot(); st != statusUnavailable {
		t.Errorf("after 2 probe failures status = %q, want unavailable", st)
	}
	m.applyProbeResult(nil)
	if st, _ := m.snapshot(); st != statusHealthy {
		t.Errorf("after recovery status = %q, want healthy", st)
	}
}

// TestSynthesizeExpiryWarning P1（代码审核）：ExpiresAt 临近过期（<7 天）转黄；
// AuthExpired 优先于临近过期；探测成功不得把临近过期洗回黑。
func TestSynthesizeExpiryWarning(t *testing.T) {
	at := func(d time.Duration) *time.Time { t := time.Now().Add(d); return &t }
	tests := []struct {
		name string
		dh   driver.DriverHealth
		want mountStatus
	}{
		{"剩 8 天不警告", driver.DriverHealth{ExpiresAt: at(8 * 24 * time.Hour)}, statusHealthy},
		{"剩 6 天转黄", driver.DriverHealth{ExpiresAt: at(6*24*time.Hour + time.Hour)}, statusWarning},
		{"剩 2 小时转黄", driver.DriverHealth{ExpiresAt: at(2 * time.Hour)}, statusWarning},
		{"已过期转红", driver.DriverHealth{ExpiresAt: at(-time.Hour), AuthExpired: true, Warning: "令牌已过期"}, statusUnavailable},
		{"过期事实优先于临近", driver.DriverHealth{ExpiresAt: at(-time.Hour), AuthExpired: true}, statusUnavailable},
	}
	for _, tt := range tests {
		got, msg := synthesizeStatus(nil, tt.dh, 0, "")
		if got != tt.want {
			t.Errorf("%s: got %q (%s), want %q", tt.name, got, msg, tt.want)
		}
	}
}

// fakeHealthDriver 实现 Driver + HealthReporter 的最小测试驱动
type fakeHealthDriver struct{ dh driver.DriverHealth }

func (f *fakeHealthDriver) Name() string                            { return "fake" }
func (f *fakeHealthDriver) Init(ctx context.Context) error          { return nil }
func (f *fakeHealthDriver) List(ctx context.Context, p string) ([]model.Obj, error) {
	return nil, nil
}
func (f *fakeHealthDriver) Link(ctx context.Context, p string) (*model.Link, error) {
	return nil, nil
}
func (f *fakeHealthDriver) Health() driver.DriverHealth { return f.dh }

// TestProbeSuccessKeepsExpiryWarning 探测成功不得把临近过期洗回黑（审核 P1）
func TestProbeSuccessKeepsExpiryWarning(t *testing.T) {
	exp := time.Now().Add(6 * 24 * time.Hour)
	m := &MountRuntime{
		Name:      "移动云盘",
		Prefix:    "/移动云盘",
		RawDriver: &fakeHealthDriver{dh: driver.DriverHealth{ExpiresAt: &exp}},
		Available: true,
	}
	m.applyInitResult(nil)
	if st, _ := m.snapshot(); st != statusWarning {
		t.Fatalf("初始化后应为警告: %q", st)
	}
	m.applyProbeResult(nil) // 探测成功
	if st, _ := m.snapshot(); st != statusWarning {
		t.Errorf("探测成功不得把临近过期洗回黑: %q", st)
	}
	m.applyProbeResult(nil)
	if st, _ := m.snapshot(); st != statusWarning {
		t.Errorf("多次探测成功仍应保持警告: %q", st)
	}
}

// TestSynthesizeExpiryBoundary 7 天临界值（复核补充建议）：契约是严格 <7 天才黄——
// 恰好 7 天仍黑，防止将来误改成 <=7 天。用注入 now 的 synthesizeStatusAt 消除墙钟误差。
func TestSynthesizeExpiryBoundary(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		exp  time.Time
		want mountStatus
	}{
		{"恰好 7 天", now.Add(7 * 24 * time.Hour), statusHealthy},
		{"7 天少 1 秒", now.Add(7*24*time.Hour - time.Second), statusWarning},
		{"7 天多 1 秒", now.Add(7*24*time.Hour + time.Second), statusHealthy},
	}
	for _, c := range cases {
		got, _ := synthesizeStatusAt(now, nil, driver.DriverHealth{ExpiresAt: &c.exp}, 0, "")
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// TestRegisterAdminRoutesGate 复核补充建议：无在线更新能力的部署不得注册 /admin（404）；
// 有更新能力时注册（未认证 401）。首页链接条件已由 TestServeRootIndexNoAdminLink 覆盖。
func TestRegisterAdminRoutesGate(t *testing.T) {
	noAuth := func(mux *http.ServeMux) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/admin", nil))
		return rec
	}

	muxNone := http.NewServeMux()
	registerAdminRoutes(muxNone, false, "u", "p", admin.New(nil, nil))
	if rec := noAuth(muxNone); rec.Code != http.StatusNotFound {
		t.Errorf("无更新能力时 /admin 应 404，got %d", rec.Code)
	}

	muxYes := http.NewServeMux()
	registerAdminRoutes(muxYes, true, "u", "p", admin.New(nil, nil))
	if rec := noAuth(muxYes); rec.Code != http.StatusUnauthorized {
		t.Errorf("有更新能力时 /admin 应注册（未认证 401），got %d", rec.Code)
	}
}

// TestProbeErrorClassifiedNoSecret 安全审核 RV-2026-11-24-06：
// 探测错误只存分类短语，原始文本（含 url.Error 中的 access_token）不得进入状态；
// 恢复后 probeMsg 清空。
func TestProbeErrorClassifiedNoSecret(t *testing.T) {
	m := mkMount("baidu", "/baidu", statusHealthy)
	ue := &url.Error{Op: "Get",
		URL: "https://pan.baidu.com/rest/2.0/xpan/file?access_token=SECRET&method=list",
		Err: errors.New("dial tcp: connection refused")}

	m.applyProbeResult(ue)
	m.applyProbeResult(ue)
	st, msg := m.snapshot()
	if st != statusUnavailable {
		t.Fatalf("连续两次探测失败应转红, got %q", st)
	}
	if strings.Contains(msg, "SECRET") {
		t.Errorf("状态消息不得含凭据: %q", msg)
	}
	if msg != "网络不可达" {
		t.Errorf("应为分类短语「网络不可达」, got %q", msg)
	}

	// 恢复后：转正常且消息清空
	m.applyProbeResult(nil)
	if st, msg := m.snapshot(); st != statusHealthy || msg != "" {
		t.Errorf("恢复后应正常且无消息: %q %q", st, msg)
	}
}

// TestClassifyProbeErr 探测错误分类（超时/网络/上游哨兵/兜底）
func TestClassifyProbeErr(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{context.DeadlineExceeded, "健康检查超时"},
		{&url.Error{Op: "Get", URL: "https://x/", Err: errors.New("refused")}, "网络不可达"},
		{fmt.Errorf("%w: 上游失败", driver.ErrUpstream), "上游云盘不可用"},
		{fmt.Errorf("百度网盘 errno=2"), "上游服务异常"},
	}
	for _, c := range cases {
		if got := classifyProbeErr(c.err); got != c.want {
			t.Errorf("classifyProbeErr(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

// TestRefreshSnapshotAfterCommit PM 拍板 2026-11-25：贴票成功后状态卡按当前事实重算——
// 移动（警告事实）当场转正常；Init 失败的挂载保持红色（D3 重启语义不变）。
func TestRefreshSnapshotAfterCommit(t *testing.T) {
	// 移动：受限警告事实 → 黄；提交后事实干净 → 当场转黑
	m := &MountRuntime{
		Name:      "移动云盘",
		Prefix:    "/移动云盘",
		RawDriver: &fakeHealthDriver{dh: driver.DriverHealth{Warning: "云主机发现失败，服务受限"}},
		Available: true,
	}
	m.applyInitResult(nil)
	if st, _ := m.snapshot(); st != statusWarning {
		t.Fatalf("初始应为警告, got %q", st)
	}
	exp := time.Now().Add(30 * 24 * time.Hour)
	m.RawDriver.(*fakeHealthDriver).dh = driver.DriverHealth{ExpiresAt: &exp} // 提交成功 → 事实干净
	m.refreshSnapshotFromFacts()
	if st, msg := m.snapshot(); st != statusHealthy || msg != "" {
		t.Errorf("贴票后应当场转正常且无消息, got %q %q", st, msg)
	}

	// Init 失败的挂载：重算后仍红（D3 不变——更新成功只代表已保存，重启才恢复）
	bad := &MountRuntime{Name: "百度", Prefix: "/百度",
		InitError: errors.New("百度网盘尚未授权（请在管理页粘贴 refresh_token 更新）")}
	bad.applyInitResult(bad.InitError)
	bad.refreshSnapshotFromFacts()
	if st, _ := bad.snapshot(); st != statusUnavailable {
		t.Errorf("Init 失败的挂载重算后仍应红, got %q", st)
	}
}
