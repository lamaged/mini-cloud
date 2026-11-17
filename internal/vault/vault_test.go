package vault

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path"
	"sort"
	"strings"
	"testing"
	"time"

	"mini-cloud/internal/driver"
	"mini-cloud/internal/model"
)

// fakeRaw 内存假云盘：模拟 local 驱动（Link 返回 seekable 数据流）
type fakeRaw struct {
	files      map[string][]byte // 挂载内路径 → 内容
	urlOnly    bool              // 模拟天翼/移动：Link 只返回 URL（302 模式）
	urlOnlyURL string            // urlOnly 时返回的 URL（默认不可达域名；测试可指向拒绝端口加速）
	streamOnly bool              // 模拟百度：Reader 不可 seek（P14）
}

// onlyReader 只有 Read 没有 ReadAt（模拟百度代理流）
type onlyReader struct{ r io.Reader }

func (o *onlyReader) Read(p []byte) (int, error) { return o.r.Read(p) }

// fakeRaw 字段补充（P14）


func (f *fakeRaw) Name() string                          { return "local" }
func (f *fakeRaw) Init(ctx context.Context) error        { return nil }
func (f *fakeRaw) List(ctx context.Context, p string) ([]model.Obj, error) {
	p = path.Clean("/" + p)
	prefix := p
	if prefix != "/" {
		prefix += "/"
	}
	var objs []model.Obj
	seen := map[string]bool{}
	for k := range f.files {
		if path.Dir("/"+k) == p {
			name := path.Base(k)
			if !seen[name] {
				objs = append(objs, model.Obj{Name: name, Size: int64(len(f.files[k])), IsDir: false})
				seen[name] = true
			}
		}
	}
	_ = prefix
	// 目录：从文件路径推断（含显式目录条目）
	dirSeen := map[string]bool{}
	for k := range f.files {
		d := path.Dir("/" + k)
		for ; d != "/" && !dirSeen[d]; d = path.Dir(d) {
			dirSeen[d] = true
		}
	}
	for d := range dirSeen {
		if path.Dir(d) == p {
			objs = append(objs, model.Obj{Name: path.Base(d), IsDir: true})
		}
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].Name < objs[j].Name })
	return objs, nil
}
func (f *fakeRaw) Link(ctx context.Context, p string) (*model.Link, error) {
	// 与真实驱动一致：路径为挂载内相对形式（无前导斜杠）
	p = path.Clean("/" + p)
	p = p[1:]
	data, ok := f.files[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	if f.urlOnly {
		base := f.urlOnlyURL
		if base == "" {
			base = "https://cdn.example.com"
		}
		return &model.Link{URL: base + "/" + p}, nil // 302 模式：无数据流
	}
	if f.streamOnly {
		// 百度代理流：只有 Read 无 ReadAt（P14）
		return &model.Link{Reader: io.NopCloser(&onlyReader{r: bytes.NewReader(data)})}, nil
	}
	return &model.Link{Reader: &memReader{Reader: bytes.NewReader(data)}}, nil
}

type memReader struct {
	*bytes.Reader
}

func (m *memReader) Close() error { return nil }

// buildTestVault 构造一个带 vault 根（backup）的假云盘 + 明文样本
func buildTestVault(t *testing.T, withManifest bool) (*fakeRaw, []byte, []byte) {
	t.Helper()
	master := bytes.Repeat([]byte{0x5A}, 32)
	plain := []byte("你好，加密视频内容 hello vault!")
	fileID := [16]byte{7, 7, 7, 7}
	objID := EncodeFileID(fileID)

	meta := &Metadata{V: 1, P: "/电影/a.mkv", N: "a.mkv", S: int64(len(plain)),
		M: 1712345678, T: "video/x-matroska"}
	var objBuf bytes.Buffer
	h, shaHex, err := EncryptObject(&objBuf, master, bytes.NewReader(plain), int64(len(plain)), fileID, meta)
	if err != nil {
		t.Fatalf("EncryptObject: %v", err)
	}

	f := &fakeRaw{files: map[string][]byte{
		"backup/" + ObjectPath(objID): objBuf.Bytes(),
		"普通视频.mkv":                    []byte("normal-video-data"),
	}}

	if withManifest {
		m := &Manifest{V: 1,
			Files: []Entry{{
				Path: "/电影/a.mkv", Name: "a.mkv", Size: int64(len(plain)), MTime: 1712345678,
				MIME: "video/x-matroska", FileID: objID, Obj: ObjectPath(objID),
				HeaderSHA256: hex.EncodeToString(h.HeaderHash()), SHA256: shaHex,
			}},
			Dirs: []string{"/电影"},
		}
		var mf bytes.Buffer
		if err := SaveManifest(&mf, ManifestKey(master), m); err != nil {
			t.Fatalf("SaveManifest: %v", err)
		}
		f.files["backup/_mc/manifest.mcf"] = mf.Bytes()
	}
	return f, master, plain
}

func newTestVault(t *testing.T, f *fakeRaw, master []byte, maxStreams int) *Vault {
	t.Helper()
	v := New(f, f, master, Config{Paths: []string{"backup"}, MaxStreams: maxStreams})
	if err := v.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return v
}

func TestVaultListVirtual(t *testing.T) {
	f, master, _ := buildTestVault(t, true)
	v := newTestVault(t, f, master, 4)
	defer v.Stop()

	// vault 根：虚拟树 = 原始文件名，绝不出现 _mc
	objs, err := v.List(context.Background(), "/backup")
	if err != nil {
		t.Fatalf("List /backup: %v", err)
	}
	if len(objs) != 1 || objs[0].Name != "电影" || !objs[0].IsDir {
		t.Errorf("vault 根虚拟列表不符: %+v", objs)
	}
	objs2, err := v.List(context.Background(), "/backup/电影")
	if err != nil {
		t.Fatalf("List /backup/电影: %v", err)
	}
	if len(objs2) != 1 || objs2[0].Name != "a.mkv" || objs2[0].IsDir {
		t.Errorf("虚拟文件列表不符: %+v", objs2)
	}
	// D3：ETag 与 ContentType 唯一来源
	if objs2[0].ETag == "" || objs2[0].ContentType != "video/x-matroska" {
		t.Errorf("ETag/ContentType 未填充: %+v", objs2[0])
	}
	// _mc 屏蔽：PROPFIND/List 403
	if _, err := v.List(context.Background(), "/backup/_mc"); !errors.Is(err, driver.ErrForbidden) {
		t.Errorf("_mc List 应 403，got %v", err)
	}
}

func TestVaultLinkPlaintextAndForbidden(t *testing.T) {
	f, master, plain := buildTestVault(t, true)
	v := newTestVault(t, f, master, 4)
	defer v.Stop()

	// 虚拟文件 → 明文
	link, err := v.Link(context.Background(), "/backup/电影/a.mkv")
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	got, err := io.ReadAll(link.Reader)
	link.Reader.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Error("解密内容与明文不符")
	}
	if link.Header.Get("Content-Type") != "video/x-matroska" {
		t.Errorf("Content-Type = %q", link.Header.Get("Content-Type"))
	}

	// _mc 密文对象：403，绝不返回密文
	if _, err := v.Link(context.Background(), "/backup/_mc/manifest.mcf"); !errors.Is(err, driver.ErrForbidden) {
		t.Errorf("_mc Link 应 403，got %v", err)
	}
	if _, err := v.Link(context.Background(), "/backup/_mc/o/x.bin"); !errors.Is(err, driver.ErrForbidden) {
		t.Errorf("对象 Link 应 403，got %v", err)
	}
	// 虚拟树中不存在的路径：not found
	if _, err := v.Link(context.Background(), "/backup/不存在.mkv"); err == nil {
		t.Error("虚拟树外路径应 not found")
	}
}

func TestVaultNonVaultDelegates(t *testing.T) {
	f, master, _ := buildTestVault(t, true)
	v := newTestVault(t, f, master, 4)
	defer v.Stop()

	// 非 vault 路径：原样委托（302 行为不变）
	link, err := v.Link(context.Background(), "/普通视频.mkv")
	if err != nil {
		t.Fatalf("Link 普通: %v", err)
	}
	got, _ := io.ReadAll(link.Reader)
	link.Reader.Close()
	if string(got) != "normal-video-data" {
		t.Error("非 vault 路径未正确委托")
	}
}

func TestVaultMissingManifestEmptyDir(t *testing.T) {
	f, master, _ := buildTestVault(t, false) // backup 目录存在但无 manifest
	v := newTestVault(t, f, master, 4)
	defer v.Stop()

	objs, err := v.List(context.Background(), "/backup")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objs) != 0 {
		t.Errorf("manifest 缺失应显示空目录: %+v", objs)
	}
	if v.Warning() == "" {
		t.Error("manifest 缺失应有健康提示")
	}
}

// TestVaultURLOnlyDriverUnreadableManifest I1 演进（Phase 1 语义）：URL 型驱动已支持
// （manifest 经取数层读取）；但取数失败（如假域名不可达）时——空目录 + 告警，不崩溃
func TestVaultURLOnlyDriverUnreadableManifest(t *testing.T) {
	f, master, _ := buildTestVault(t, true)
	f.urlOnly = true
	f.urlOnlyURL = "http://127.0.0.1:1" // 本地拒绝端口：连接立即失败，加速测试
	v := newTestVault(t, f, master, 4)
	defer v.Stop()

	objs, err := v.List(context.Background(), "/backup")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objs) != 0 {
		t.Errorf("manifest 不可读应显示空目录: %+v", objs)
	}
	if v.Warning() == "" {
		t.Error("manifest 不可读应有告警")
	}
}

// buildURLVault 构造 URL 型驱动的完整 vault（httptest CDN 同时服务 manifest 与对象）
func buildURLVault(t *testing.T, maxStreams int) (*Vault, *fakeURLDriver, *rangeCDN, []byte, string) {
	t.Helper()
	master := bytes.Repeat([]byte{0x6B}, 32)
	plain := []byte("Phase1 云盘播放端到端内容 hello tianyi!")
	fileID := [16]byte{3, 3, 3, 3}
	objID := EncodeFileID(fileID)

	meta := &Metadata{V: 1, P: "/电影/a.mkv", N: "a.mkv", S: int64(len(plain)), M: 1712345678, T: "video/x-matroska"}
	var objBuf bytes.Buffer
	h, shaHex, err := EncryptObject(&objBuf, master, bytes.NewReader(plain), int64(len(plain)), fileID, meta)
	if err != nil {
		t.Fatal(err)
	}
	m := &Manifest{V: 1, Files: []Entry{{
		Path: "/电影/a.mkv", Name: "a.mkv", Size: int64(len(plain)), MTime: 1712345678,
		MIME: "video/x-matroska", FileID: objID, Obj: ObjectPath(objID),
		HeaderSHA256: hex.EncodeToString(h.HeaderHash()), SHA256: shaHex,
	}}, Dirs: []string{"/电影"}}
	var mf bytes.Buffer
	if err := SaveManifest(&mf, ManifestKey(master), m); err != nil {
		t.Fatal(err)
	}

	objKey := "backup/" + ObjectPath(objID)
	cdn := &rangeCDN{byPath: map[string][]byte{
		"/backup/_mc/manifest.mcf": mf.Bytes(),
		"/" + objKey:              objBuf.Bytes(),
	}}
	ts := httptest.NewServer(cdn)
	t.Cleanup(ts.Close)
	drv := &fakeURLDriver{baseURL: ts.URL}
	drv.files = map[string][]byte{"backup/_mc/manifest.mcf": mf.Bytes(), objKey: objBuf.Bytes()}
	v := New(drv, drv, master, Config{Paths: []string{"backup"}, MaxStreams: maxStreams})
	if err := v.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return v, drv, cdn, plain, objID
}

// TestVaultLinkViaUpstream Phase 1 核心集成：URL 型驱动全链路
// （manifest 经取数层 → 虚拟列表 → 解密数据流经云盘 Range）
func TestVaultLinkViaUpstream(t *testing.T) {
	v, _, _, plain, _ := buildURLVault(t, 4)
	defer v.Stop()

	objs, err := v.List(context.Background(), "/backup/电影")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objs) != 1 || objs[0].Name != "a.mkv" {
		t.Fatalf("虚拟列表不符: %+v", objs)
	}
	link, err := v.Link(context.Background(), "/backup/电影/a.mkv")
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	got, err := io.ReadAll(link.Reader)
	link.Reader.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("云盘取数解密内容与明文不符")
	}
}

// TestVaultSemaphoreBeforeLink U3：信号量在 raw.Link（云盘 API）之前获取——
// 超限拒绝时不得多调用一次 Link
func TestVaultSemaphoreBeforeLink(t *testing.T) {
	v, drv, _, _, _ := buildURLVault(t, 1)
	defer v.Stop()

	old := acquireTimeout
	acquireTimeout = 100 * time.Millisecond
	defer func() { acquireTimeout = old }()

	link1, err := v.Link(context.Background(), "/backup/电影/a.mkv")
	if err != nil {
		t.Fatalf("Link 1: %v", err)
	}
	baseline := drv.linkCalls.Load()
	if _, err := v.Link(context.Background(), "/backup/电影/a.mkv"); !errors.Is(err, driver.ErrTooManyStreams) {
		link1.Reader.Close()
		t.Fatalf("第二路应被信号量拒绝，got %v", err)
	}
	if drv.linkCalls.Load() != baseline {
		t.Errorf("信号量拒绝后不应再调用云盘 Link：before=%d after=%d", baseline, drv.linkCalls.Load())
	}
	link1.Reader.Close()
}

// TestVaultBindingMismatch I3：对象内嵌 metadata.p 与请求路径不一致 → 拒绝（防跨对象替换）
func TestVaultBindingMismatch(t *testing.T) {
	master := bytes.Repeat([]byte{0x6B}, 32)
	plain := []byte("stolen content")
	fileID := [16]byte{4, 4, 4, 4}
	objID := EncodeFileID(fileID)

	// 对象内嵌路径是 /别的目录/b.mkv，但 manifest 把它挂到 /电影/a.mkv
	meta := &Metadata{V: 1, P: "/别的目录/b.mkv", N: "b.mkv", S: int64(len(plain)), M: 1}
	var objBuf bytes.Buffer
	h, shaHex, err := EncryptObject(&objBuf, master, bytes.NewReader(plain), int64(len(plain)), fileID, meta)
	if err != nil {
		t.Fatal(err)
	}
	m := &Manifest{V: 1, Files: []Entry{{
		Path: "/电影/a.mkv", Name: "a.mkv", Size: int64(len(plain)), MTime: 1,
		MIME: "video/x-matroska", FileID: objID, Obj: ObjectPath(objID),
		HeaderSHA256: hex.EncodeToString(h.HeaderHash()), SHA256: shaHex,
	}}}
	var mf bytes.Buffer
	if err := SaveManifest(&mf, ManifestKey(master), m); err != nil {
		t.Fatal(err)
	}
	objKey := "backup/" + ObjectPath(objID)
	cdn := &rangeCDN{byPath: map[string][]byte{
		"/backup/_mc/manifest.mcf": mf.Bytes(),
		"/" + objKey:              objBuf.Bytes(),
	}}
	ts := httptest.NewServer(cdn)
	t.Cleanup(ts.Close)
	drv := &fakeURLDriver{baseURL: ts.URL}
	drv.files = map[string][]byte{"backup/_mc/manifest.mcf": mf.Bytes(), objKey: objBuf.Bytes()}
	v := New(drv, drv, master, Config{Paths: []string{"backup"}})
	if err := v.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer v.Stop()

	if _, err := v.Link(context.Background(), "/backup/电影/a.mkv"); err == nil ||
		!strings.Contains(err.Error(), "绑定校验失败") {
		t.Errorf("路径绑定不一致必须被拒绝，got %v", err)
	}
}

// TestVaultETagSameSource I3：GET 响应 ETag 与 PROPFIND 列表同源
func TestVaultETagSameSource(t *testing.T) {
	f, master, _ := buildTestVault(t, true)
	v := newTestVault(t, f, master, 4)
	defer v.Stop()

	objs, err := v.List(context.Background(), "/backup/电影")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	link, err := v.Link(context.Background(), "/backup/电影/a.mkv")
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	link.Reader.Close()
	if objs[0].ETag == "" || link.Header.Get("ETag") != objs[0].ETag {
		t.Errorf("ETag 不同源: PROPFIND=%q GET=%q", objs[0].ETag, link.Header.Get("ETag"))
	}
}

// TestVaultLinkEmptyMIME I5：manifest 条目 MIME 为空（无扩展名/未知类型文件）时
// Link 不得 panic、响应不设 Content-Type、ETag 照常
func TestVaultLinkEmptyMIME(t *testing.T) {
	master := bytes.Repeat([]byte{0x5A}, 32)
	plain := []byte("no-extension file")
	fileID := [16]byte{9, 9, 9, 9}
	objID := EncodeFileID(fileID)

	meta := &Metadata{V: 1, P: "/资料/无扩展名", N: "无扩展名", S: int64(len(plain))}
	var objBuf bytes.Buffer
	h, shaHex, err := EncryptObject(&objBuf, master, bytes.NewReader(plain), int64(len(plain)), fileID, meta)
	if err != nil {
		t.Fatalf("EncryptObject: %v", err)
	}
	m := &Manifest{V: 1, Files: []Entry{{
		Path: "/资料/无扩展名", Name: "无扩展名", Size: int64(len(plain)), MTime: 1,
		MIME: "", FileID: objID, Obj: ObjectPath(objID),
		HeaderSHA256: hex.EncodeToString(h.HeaderHash()), SHA256: shaHex,
	}}}
	var mf bytes.Buffer
	if err := SaveManifest(&mf, ManifestKey(master), m); err != nil {
		t.Fatal(err)
	}
	f := &fakeRaw{files: map[string][]byte{
		"backup/" + ObjectPath(objID): objBuf.Bytes(),
		"backup/_mc/manifest.mcf":     mf.Bytes(),
	}}
	v := newTestVault(t, f, master, 4)
	defer v.Stop()

	link, err := v.Link(context.Background(), "/backup/资料/无扩展名")
	if err != nil {
		t.Fatalf("Link（空 MIME）: %v", err)
	}
	got, err := io.ReadAll(link.Reader)
	link.Reader.Close()
	if err != nil || !bytes.Equal(got, plain) {
		t.Errorf("明文不符: %v", err)
	}
	if ct := link.Header.Get("Content-Type"); ct != "" {
		t.Errorf("空 MIME 不应设置 Content-Type，got %q", ct)
	}
	if et := link.Header.Get("ETag"); et == "" {
		t.Error("ETag 必须照常设置")
	}
}

// TestVaultStreamLimitDoubleClose P16：重复 Close 幂等（信号量只释放一次）
func TestVaultStreamLimitDoubleClose(t *testing.T) {
	f, master, _ := buildTestVault(t, true)
	v := New(f, f, master, Config{Paths: []string{"backup"}, MaxStreams: 1})
	if err := v.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer v.Stop()

	old := acquireTimeout
	acquireTimeout = 100 * time.Millisecond
	defer func() { acquireTimeout = old }()

	link1, err := v.Link(context.Background(), "/backup/电影/a.mkv")
	if err != nil {
		t.Fatalf("Link 1: %v", err)
	}
	link1.Reader.Close()
	link1.Reader.Close() // 重复关闭：不得重复释放导致信号量超发
	v.sem.mu.Lock()
	if v.sem.avail > v.sem.max {
		t.Errorf("双 Close 重复释放：avail=%d > max=%d", v.sem.avail, v.sem.max)
	}
	v.sem.mu.Unlock()
}

func TestVaultStreamLimit(t *testing.T) {
	f, master, _ := buildTestVault(t, true)
	v := New(f, f, master, Config{Paths: []string{"backup"}, MaxStreams: 1})
	if err := v.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer v.Stop()

	old := acquireTimeout
	acquireTimeout = 100 * time.Millisecond
	defer func() { acquireTimeout = old }()

	link1, err := v.Link(context.Background(), "/backup/电影/a.mkv")
	if err != nil {
		t.Fatalf("Link 1: %v", err)
	}
	// 第 2 路超上限：必须被拒绝（不排队）
	if _, err := v.Link(context.Background(), "/backup/电影/a.mkv"); err == nil {
		link1.Reader.Close()
		t.Fatal("max_streams=1 时第二路应被拒绝")
	}
	// 关闭第一路后重试应成功
	link1.Reader.Close()
	link2, err2 := v.Link(context.Background(), "/backup/电影/a.mkv")
	if err2 != nil {
		t.Fatalf("关闭后 Link 2: %v", err2)
	}
	link2.Reader.Close()
}

// TestVaultStreamOnlyDriverSkipped P14：不可 seek 的流式驱动（百度）完全不建 vault 根——
// 物理视图原样委托、无告警
func TestVaultStreamOnlyDriverSkipped(t *testing.T) {
	f, master, _ := buildTestVault(t, true)
	f.streamOnly = true
	v := newTestVault(t, f, master, 4)
	defer v.Stop()

	// backup 不被虚拟树接管：List 委托 cached（物理视图，含 _mc 目录）
	objs, err := v.List(context.Background(), "/backup")
	if err != nil {
		t.Fatalf("List /backup: %v", err)
	}
	found := false
	for _, o := range objs {
		if o.Name == "_mc" && o.IsDir {
			found = true
		}
	}
	if !found {
		t.Errorf("流式驱动应保持物理视图（含 _mc），实际: %+v", objs)
	}
	if v.Warning() != "" {
		t.Errorf("跳过的驱动不应产生告警，got %q", v.Warning())
	}
}
