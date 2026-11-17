package vault

import (
	"context"
	"io"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mini-cloud/internal/model"
)

// fakeURLDriver Link 返回带签名参数的 CDN URL（模拟天翼/移动 302 型驱动）
type fakeURLDriver struct {
	fakeRaw
	baseURL   string // 测试服务器地址（httptest）
	linkCalls atomic.Int32
	expiry    string // URL 中的 Expires 值（unix 秒；空则无过期参数）
}

func (f *fakeURLDriver) Link(ctx context.Context, p string) (*model.Link, error) {
	f.linkCalls.Add(1)
	p = path.Clean("/" + p)[1:]
	if _, ok := f.files[p]; !ok {
		return nil, os.ErrNotExist
	}
	u := f.baseURL + "/" + p + "?Signature=abc"
	if f.expiry != "" {
		u += "&Expires=" + f.expiry
	}
	return &model.Link{URL: u, Header: http.Header{"User-Agent": {"Mozilla/5.0"}}}, nil
}

// TestParseURLExpiry 签名过期解析（天翼 Expires / 移动 X-Amz-Date+Expires）
func TestParseURLExpiry(t *testing.T) {
	// 天翼：unix 秒
	u, _ := url.Parse("http://x/?Signature=s&Expires=1795091763")
	exp, ok := parseURLExpiry(u)
	if !ok || exp.Unix() != 1795091763 {
		t.Errorf("天翼 Expires 解析: %v %v", exp, ok)
	}
	// 13 位毫秒
	u2, _ := url.Parse("http://x/?Expires=1795091763000")
	exp2, _ := parseURLExpiry(u2)
	if exp2.Unix() != 1795091763 {
		t.Errorf("毫秒 Expires: %v", exp2)
	}
	// 移动：X-Amz-Date + X-Amz-Expires
	u3, _ := url.Parse("http://x/?X-Amz-Date=20261114T151008Z&X-Amz-Expires=900&X-Amz-Signature=s")
	exp3, ok3 := parseURLExpiry(u3)
	want := time.Date(2026, 11, 14, 15, 25, 8, 0, time.UTC)
	if !ok3 || !exp3.Equal(want) {
		t.Errorf("移动过期解析: %v %v want %v", exp3, ok3, want)
	}
	// 无法解析
	u4, _ := url.Parse("http://x/?Signature=s")
	if _, ok4 := parseURLExpiry(u4); ok4 {
		t.Error("无过期参数应返回 ok=false")
	}
}

// rangeCDN 模拟支持 Range 的 CDN
type rangeCDN struct {
	data          []byte
	byPath        map[string][]byte // 按路径提供内容（manifest 与对象同服）
	requests      atomic.Int32
	serve200      bool // 模拟忽略 Range
	fail403       atomic.Bool
	recoverAfter1 atomic.Bool // 仅第一次 403，之后恢复正常（换签名恢复测试）
	failNext      atomic.Bool // 下一次请求失败一次后恢复（缓冲中途 403 测试）
	mismatchNext  atomic.Bool // 下一次请求 Content-Range 错位一次后恢复
	truncateNext  atomic.Bool // 下一次 206 响应短 body（Content-Range 声称未到文件尾）一次
	lastUA        atomic.Value // 最近一次请求的 User-Agent
}

func (c *rangeCDN) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reqNo := c.requests.Add(1)
	c.lastUA.Store(r.Header.Get("User-Agent"))
	if c.byPath != nil {
		if d, ok := c.byPath[r.URL.Path]; ok {
			c.data = d
		}
	}
	if c.failNext.CompareAndSwap(true, false) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if c.fail403.Load() && !(c.recoverAfter1.Load() && reqNo > 1) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	rangeHdr := r.Header.Get("Range")
	if c.serve200 || rangeHdr == "" {
		w.Write(c.data)
		return
	}
	var start, end int64
	if _, err := fmt.Sscanf(rangeHdr, "bytes=%d-%d", &start, &end); err != nil {
		http.Error(w, "bad range", http.StatusBadRequest)
		return
	}
	if start >= int64(len(c.data)) {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(c.data)))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	if end >= int64(len(c.data)) {
		end = int64(len(c.data)) - 1
	}
	if c.mismatchNext.CompareAndSwap(true, false) {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start+1, end, len(c.data)))
	} else {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(c.data)))
	}
	w.WriteHeader(http.StatusPartialContent)
	if c.truncateNext.CompareAndSwap(true, false) {
		// 传输截断：只写一半，但 Content-Range 声称完整
		w.Write(c.data[start : start+(end-start)/2])
		return
	}
	w.Write(c.data[start : end+1])
}

// newTestUpstream 构造连到 httptest CDN 的上游（驱动 Link 返回该服务器 URL）
func newTestUpstream(t *testing.T, data []byte, expiry string) (*Upstream, *rangeCDN, *fakeURLDriver) {
	t.Helper()
	cdn := &rangeCDN{data: data}
	ts := httptest.NewServer(cdn)
	t.Cleanup(ts.Close)
	drv := &fakeURLDriver{baseURL: ts.URL, expiry: expiry}
	drv.files = map[string][]byte{"backup/_mc/o/xx.bin": data}
	u, err := NewUpstream(context.Background(), drv, "backup/_mc/o/xx.bin", &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewUpstream: %v", err)
	}
	return u, cdn, drv
}

// TestUpstreamRandomRead 随机读：单块请求、内容正确、UA 透传
func TestUpstreamRandomRead(t *testing.T) {
	data := make([]byte, 3<<20) // 3 MiB
	for i := range data {
		data[i] = byte(i % 251)
	}
	u, cdn, _ := newTestUpstream(t, data, "")
	defer u.Close()

	p := make([]byte, 1000)
	n, err := u.ReadAt(p, 123456)
	if err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if n != 1000 {
		t.Fatalf("n = %d", n)
	}
	for i := range p {
		if p[i] != byte((int64(123456)+int64(i))%251) {
			t.Fatalf("内容不符 at %d", i)
		}
	}
	if cdn.requests.Load() != 1 {
		t.Errorf("随机读应只发 1 次请求，实际 %d", cdn.requests.Load())
	}
	if ua, _ := cdn.lastUA.Load().(string); ua != "Mozilla/5.0" {
		t.Errorf("UA 未透传: %q", ua)
	}
}

// TestUpstreamSequentialBuffering 顺序读：后续偏移走缓冲、不新增请求（P9 契约②）
func TestUpstreamSequentialBuffering(t *testing.T) {
	data := make([]byte, 4<<20)
	u, cdn, _ := newTestUpstream(t, data, "")
	defer u.Close()

	p := make([]byte, 1000)
	if _, err := u.ReadAt(p, 0); err != nil {
		t.Fatalf("ReadAt 0: %v", err)
	}
	if _, err := u.ReadAt(p, 1000); err != nil {
		t.Fatalf("ReadAt 1000: %v", err)
	}
	if _, err := u.ReadAt(p, 2000); err != nil {
		t.Fatalf("ReadAt 2000: %v", err)
	}
	// 三次顺序读只应发 1 次（大段缓冲）
	if cdn.requests.Load() != 1 {
		t.Errorf("顺序读应只发 1 次请求，实际 %d", cdn.requests.Load())
	}
}

// TestUpstreamBackwardSeekInvalidates 反向 Seek：缓冲失效、重新请求（P9 契约③）
func TestUpstreamBackwardSeekInvalidates(t *testing.T) {
	data := make([]byte, 4<<20)
	u, cdn, _ := newTestUpstream(t, data, "")
	defer u.Close()

	p := make([]byte, 1000)
	if _, err := u.ReadAt(p, 2<<20); err != nil {
		t.Fatalf("ReadAt 2M: %v", err)
	}
	reqs := cdn.requests.Load()
	if _, err := u.ReadAt(p, 100); err != nil { // 反向：远小于缓冲起点
		t.Fatalf("ReadAt 100: %v", err)
	}
	if cdn.requests.Load() == reqs {
		t.Error("反向 Seek 应重新请求（缓冲已失效）")
	}
}

// TestUpstream403Refresh 403 → 换签名重试一次（feasibility §5.4）
func TestUpstream403Refresh(t *testing.T) {
	data := make([]byte, 1<<20)
	u, cdn, _ := newTestUpstream(t, data, "")
	defer u.Close()

	cdn.fail403.Store(true)
	p := make([]byte, 1000)
	if _, err := u.ReadAt(p, 0); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("应报 403 错误，got %v", err)
	}
	if cdn.requests.Load() != 2 {
		t.Errorf("403 后应重试 1 次（共 2 次请求），实际 %d", cdn.requests.Load())
	}
}

// TestUpstream403ThenRecover 403 一次后恢复：第二次（换签名后）成功
func TestUpstream403ThenRecover(t *testing.T) {
	data := make([]byte, 1<<20)
	for i := range data {
		data[i] = byte(i % 7)
	}
	u, cdn, _ := newTestUpstream(t, data, "")
	defer u.Close()

	cdn.fail403.Store(true)
	cdn.recoverAfter1.Store(true) // 第一次 403，之后正常
	p := make([]byte, 100)
	n, err := u.ReadAt(p, 500)
	if err != nil || n != 100 {
		t.Fatalf("403 后应换签名恢复: n=%d err=%v", n, err)
	}
	if p[0] != data[500] {
		t.Error("恢复后内容不符")
	}
	if cdn.requests.Load() != 2 {
		t.Errorf("应恰好 2 次请求（403 + 重试成功），实际 %d", cdn.requests.Load())
	}
}

// TestUpstream200IgnoreRange 200 分级（I10）：小偏移丢弃后继续；大偏移直接失败
func TestUpstream200IgnoreRange(t *testing.T) {
	data := make([]byte, 1<<20)
	u, cdn, _ := newTestUpstream(t, data, "")
	defer u.Close()
	cdn.serve200 = true

	// 小偏移（≤ 2 MiB）：丢弃到偏移后返回内容
	p := make([]byte, 100)
	n, err := u.ReadAt(p, 5000)
	if err != nil || n != 100 {
		t.Fatalf("小偏移丢弃读取: n=%d err=%v", n, err)
	}
	if p[0] != data[5000] {
		t.Error("丢弃后内容不符")
	}

	// 大偏移：直接失败（ErrRangeNotSupported → webdav 502）
	big := make([]byte, 100)
	if _, err := u.ReadAt(big, 10<<20); err == nil {
		t.Error("大偏移 200 应失败")
	}
}

// TestUpstream416 I2：off 超过文件长度时服务端返回 416（bytes */N）——
// 按 io.ReaderAt 语义应返回 (0, io.EOF)，而非报错
func TestUpstream416(t *testing.T) {
	data := make([]byte, 1000)
	u, _, _ := newTestUpstream(t, data, "")
	defer u.Close()
	p := make([]byte, 100)
	n, err := u.ReadAt(p, 5000)
	if err != io.EOF || n != 0 {
		t.Errorf("越界读应返回 (0, io.EOF)，got n=%d err=%v", n, err)
	}
}

// TestUpstreamExpiryRefresh 签名临近过期时主动刷新（Expires 参数驱动）
func TestUpstreamExpiryRefresh(t *testing.T) {
	data := make([]byte, 1000)
	// 过期时间在未来 30 秒内 → 触发主动刷新（-60s 阈值）
	u, _, drv := newTestUpstream(t, data, fmt.Sprintf("%d", time.Now().Add(30*time.Second).Unix()))
	defer u.Close()

	// 首次构造后 ensureFreshURL 会因临期而刷新
	if err := u.ensureFreshURL(context.Background()); err != nil {
		t.Fatalf("ensureFreshURL: %v", err)
	}
	if drv.linkCalls.Load() < 2 {
		t.Errorf("临期签名应主动刷新（Link 至少 2 次），实际 %d", drv.linkCalls.Load())
	}
}

// TestUpstreamChunkCrossingWindow U1：以真实块长（1 MiB+16）连续读 8 块，
// 断言每块完整、内容正确——跨窗口（4 MiB）读取不得短读误报「截断」
func TestUpstreamChunkCrossingWindow(t *testing.T) {
	chunkLen := DefaultChunkSize + TagSize // 1,048,592
	data := make([]byte, 10<<20)           // 10 MiB
	for i := range data {
		data[i] = byte(i % 251)
	}
	u, cdn, _ := newTestUpstream(t, data, "")
	defer u.Close()

	for i := 0; i < 8; i++ {
		off := int64(i) * int64(chunkLen)
		p := make([]byte, chunkLen)
		n, err := u.ReadAt(p, off)
		if err != nil && err != io.EOF {
			t.Fatalf("块 %d 读取失败: %v", i, err)
		}
		if n != chunkLen {
			t.Fatalf("块 %d 短读: %d/%d（U1 回归）", i, n, chunkLen)
		}
		for j := range p {
			if p[j] != byte((off+int64(j))%251) {
				t.Fatalf("块 %d 内容不符 at %d", i, j)
			}
		}
	}
	// 8 块 ≈ 2 个 4 MiB 窗口 + 64 字节错位（每 4 块边界跨一次）→ 3 次请求
	if reqs := cdn.requests.Load(); reqs != 3 {
		t.Errorf("请求次数 = %d, want 3（窗口 4 MiB，跨窗口续拉）", reqs)
	}
}

// TestUpstreamJumpThenSequential U2：随机跳跃后预读不失效——后续顺序读仍走缓冲
func TestUpstreamJumpThenSequential(t *testing.T) {
	chunkLen := DefaultChunkSize + TagSize
	data := make([]byte, 56<<20) // 56 MiB（覆盖 50 MiB 跳跃）
	for i := range data {
		data[i] = byte(i % 199)
	}
	u, cdn, _ := newTestUpstream(t, data, "")
	defer u.Close()

	// 建立第一个缓冲
	p := make([]byte, 1000)
	if _, err := u.ReadAt(p, 2<<20); err != nil {
		t.Fatalf("ReadAt 2M: %v", err)
	}
	// 跳跃到 50 MiB（随机单块 → 替换缓冲，U2 修复）
	if _, err := u.ReadAt(p, 50<<20); err != nil {
		t.Fatalf("ReadAt 50M: %v", err)
	}
	before := cdn.requests.Load()

	// 连续读 3 块：应从新缓冲顺序延续（每窗口一次请求）
	off := int64(50<<20) + int64(chunkLen)
	for i := 0; i < 3; i++ {
		q := make([]byte, chunkLen)
		n, err := u.ReadAt(q, off)
		if err != nil && err != io.EOF {
			t.Fatalf("块 %d: %v", i, err)
		}
		if n != chunkLen {
			t.Fatalf("块 %d 短读: %d/%d", i, n, chunkLen)
		}
		if q[0] != byte(off%199) {
			t.Fatalf("块 %d 内容不符", i)
		}
		off += int64(chunkLen)
	}
	// 跳跃后 3 个连续块只应触发 1 次窗口请求（缓冲已重建）
	if reqs := cdn.requests.Load() - before; reqs != 1 {
		t.Errorf("跳跃后连续读请求数 = %d, want 1（U2：预读应已恢复）", reqs)
	}
}

// TestUpstream403MidBuffer P4①：403 发生在顺序缓冲连续读中途——换签名后恢复
func TestUpstream403MidBuffer(t *testing.T) {
	data := make([]byte, 10<<20)
	for i := range data {
		data[i] = byte(i % 131)
	}
	u, cdn, _ := newTestUpstream(t, data, "")
	defer u.Close()

	// 建立缓冲（首读）
	p := make([]byte, 1000)
	if _, err := u.ReadAt(p, 0); err != nil {
		t.Fatalf("首读: %v", err)
	}
	// 下一次请求 403 一次
	cdn.failNext.Store(true)
	// 读缓冲之外的块：触发新请求 → 403 → 换签名重试 → 成功
	q := make([]byte, 1000)
	before := cdn.requests.Load()
	if _, err := u.ReadAt(q, 6<<20); err != nil {
		t.Fatalf("缓冲中途 403 应恢复: %v", err)
	}
	if q[0] != byte((int64(6<<20))%131) {
		t.Error("恢复后内容不符")
	}
	if delta := cdn.requests.Load() - before; delta != 2 {
		t.Errorf("403 后应恰好重试 1 次（2 个请求），实际 %d", delta)
	}
}

// TestUpstreamContentRangeMismatch P4③：Content-Range 与请求区间不符 → 换签名重试
func TestUpstreamContentRangeMismatch(t *testing.T) {
	data := make([]byte, 1<<20)
	for i := range data {
		data[i] = byte(i % 97)
	}
	u, cdn, _ := newTestUpstream(t, data, "")
	defer u.Close()

	cdn.mismatchNext.Store(true) // 第一次响应 Content-Range 错位
	p := make([]byte, 500)
	before := cdn.requests.Load()
	n, err := u.ReadAt(p, 1000)
	if err != nil || n != 500 {
		t.Fatalf("Content-Range 不符应换签名恢复: n=%d err=%v", n, err)
	}
	if p[0] != byte(1000%97) {
		t.Error("恢复后内容不符")
	}
	if delta := cdn.requests.Load() - before; delta != 2 {
		t.Errorf("应恰好 2 次请求（错位 + 换签名重试），实际 %d", delta)
	}
}

// TestUpstreamTruncatedBodyRetries I1：206 短 body 且未到文件尾 = 传输截断 → 换签名重试成功
func TestUpstreamTruncatedBodyRetries(t *testing.T) {
	data := make([]byte, 1<<20)
	for i := range data {
		data[i] = byte(i % 61)
	}
	u, cdn, _ := newTestUpstream(t, data, "")
	defer u.Close()

	cdn.truncateNext.Store(true) // 第一次响应截断
	p := make([]byte, 1000)
	before := cdn.requests.Load()
	n, err := u.ReadAt(p, 5000)
	if err != nil {
		t.Fatalf("截断应换签名重试成功: %v", err)
	}
	if n != 1000 || p[0] != byte(5000%61) {
		t.Errorf("重试后内容不符: n=%d", n)
	}
	if delta := cdn.requests.Load() - before; delta != 2 {
		t.Errorf("应恰好 2 次请求（截断 + 换签名重试），实际 %d", delta)
	}
}
