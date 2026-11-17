package vault

import (
	"context"
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
	requests      atomic.Int32
	serve200      bool // 模拟忽略 Range
	fail403       atomic.Bool
	recoverAfter1 atomic.Bool // 仅第一次 403，之后恢复正常（换签名恢复测试）
	lastUA        atomic.Value // 最近一次请求的 User-Agent
}

func (c *rangeCDN) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reqNo := c.requests.Add(1)
	c.lastUA.Store(r.Header.Get("User-Agent"))
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
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(c.data)))
	w.WriteHeader(http.StatusPartialContent)
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

// TestUpstream416 区间无效
func TestUpstream416(t *testing.T) {
	data := make([]byte, 1000)
	u, _, _ := newTestUpstream(t, data, "")
	defer u.Close()
	p := make([]byte, 100)
	if _, err := u.ReadAt(p, 5000); err == nil || !strings.Contains(err.Error(), "416") {
		t.Errorf("越界应报 416，got %v", err)
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
