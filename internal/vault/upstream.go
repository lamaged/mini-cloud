package vault

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"mini-cloud/internal/driver"
)

// ============================================================
// 云盘上游取数层（Phase 1）
// 契约（P9/design §4.1）：
//   ① 预读/缓冲窗口 4 MiB；② 相邻 ReadAt 偏移差 ≤ 窗口视为顺序（命中缓冲不请求）；
//   ③ 反向 Seek/跨块跳跃/换签名后缓冲立即失效（单块读取也写入缓冲，保证后续顺序读延续）；
//   ④ 缓冲内存计入 max_streams 账（每流 = 窗口 4 MiB + 解密块 2 MiB = 6 MiB）。
// 403 刷新（feasibility §5.4）：失效旧 URL → 重新 Link → 重试一次 → 仍失败报错（webdav 映射 502）。
// ctx 传导（I6）：使用 Link 时的请求 ctx（baseCtx），客户端断开即取消上游请求。
//
// 串行契约（U4 方案 a）：Upstream 非并发安全——DecryptReader 由单 goroutine 使用
// （http.ServeContent 契约），并发只发生在不同实例之间；因此无需单飞锁，
// 所有共享状态写入均在 mu 内完成。
// ============================================================

const (
	// StreamWindow 顺序读缓冲窗口（P9：4–8 MiB，取 4 MiB）
	StreamWindow = 4 << 20
	// MaxDiscard 上游忽略 Range 时允许的流式丢弃上限（I10：≤ 2 MiB）
	MaxDiscard = 2 << 20
	// MaxURLTTL URL 缓存 TTL 上限（feasibility §5.4：min(10min, expires-now-60s)）
	MaxURLTTL = 10 * time.Minute
)

// ErrUpstream 上游异常（webdav 映射 502）
type ErrUpstream struct{ msg string }

func (e *ErrUpstream) Error() string { return "vault upstream: " + e.msg }

// ErrRangeNotSupported 上游不支持 Range（I10：两次 200 后且偏移超限）
var ErrRangeNotSupported = &ErrUpstream{msg: "上游不支持 Range"}

// errRangeInvalid 416：区间无效（P2：不触发换签名）
var errRangeInvalid = errors.New("vault upstream: 416 区间无效")

// errIgnoredRange 上游忽略 Range（200；触发换签名重试，两次后按 I10 分级）
var errIgnoredRange = errors.New("vault upstream: 上游忽略 Range（200）")

// parseURLExpiry 从签名 URL 解析绝对过期时间（feasibility §5.1；不写死 TTL，以查询参数为准）：
//   - 天翼：Expires=<unix 秒>（13 位毫秒除以 1000）
//   - 移动：X-Amz-Date + X-Amz-Expires（相对秒）
func parseURLExpiry(u *url.URL) (time.Time, bool) {
	q := u.Query()
	if v := q.Get("Expires"); v != "" {
		if sec, err := strconv.ParseInt(v, 10, 64); err == nil {
			if sec > 1e12 {
				sec /= 1000
			}
			return time.Unix(sec, 0), true
		}
	}
	if dateStr := q.Get("X-Amz-Date"); dateStr != "" {
		if expiresStr := q.Get("X-Amz-Expires"); expiresStr != "" {
			if date, err := time.Parse("20060102T150405Z", dateStr); err == nil {
				if rel, err := strconv.Atoi(expiresStr); err == nil {
					return date.Add(time.Duration(rel) * time.Second), true
				}
			}
		}
	}
	return time.Time{}, false
}

// Upstream 云盘对象的随机读取上游（URL 自管 + 顺序缓冲 + 403 刷新）
type Upstream struct {
	drv     driver.Driver // raw：对象路径的 Link（URL）
	objPath string        // 挂载内对象路径（backup/_mc/o/...）
	httpc   *http.Client
	baseCtx context.Context // Link 时的请求 ctx：客户端断开即取消上游请求

	mu        sync.Mutex // 保护全部共享状态（U4：写入均在锁内）
	linkURL   string
	linkHdr   http.Header
	expiresAt time.Time

	// 顺序读缓冲（P9 契约①②③；U2：单块读取也写入缓冲）
	buf      []byte
	bufStart int64
	bufEnd   int64

	closed bool
}

// NewUpstream 获取对象的上游（raw.Link 取签名 URL）。
// 信号量必须由调用方在 raw.Link 之前获取（P21）。
func NewUpstream(ctx context.Context, drv driver.Driver, objPath string, httpc *http.Client) (*Upstream, error) {
	u := &Upstream{drv: drv, objPath: objPath, httpc: httpc, baseCtx: ctx}
	if err := u.refreshURL(ctx); err != nil {
		return nil, err
	}
	return u, nil
}

// refreshURL 重新 Link 获取签名 URL（调用方需持锁）
func (u *Upstream) refreshURL(ctx context.Context) error {
	link, err := u.drv.Link(ctx, u.objPath)
	if err != nil {
		return fmt.Errorf("vault upstream: 获取签名失败: %w", err)
	}
	if link.URL == "" {
		return fmt.Errorf("vault upstream: 驱动未返回签名 URL（%s）", u.objPath)
	}
	parsed, err := url.Parse(link.URL)
	if err != nil {
		return fmt.Errorf("vault upstream: 签名 URL 解析失败: %w", err)
	}
	exp, ok := parseURLExpiry(parsed)
	if !ok {
		exp = time.Now().Add(MaxURLTTL) // 无法解析：保守 TTL
	}
	u.linkURL = link.URL
	u.linkHdr = link.Header
	u.expiresAt = exp
	u.buf = nil // 签名变了：旧缓冲作废（P9 契约③）
	u.bufStart, u.bufEnd = 0, 0
	return nil
}

// urlTTL 剩余有效缓存时间（feasibility §5.4）
func (u *Upstream) urlTTL() time.Duration {
	remain := time.Until(u.expiresAt) - 60*time.Second
	if remain > MaxURLTTL {
		remain = MaxURLTTL
	}
	if remain < time.Second {
		return time.Second
	}
	return remain
}

// ensureFreshURL 签名临近过期时刷新（调用方串行，U4a）
func (u *Upstream) ensureFreshURL(ctx context.Context) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if time.Now().Before(u.expiresAt.Add(-60 * time.Second)) {
		return nil
	}
	if err := u.refreshURL(ctx); err != nil {
		return fmt.Errorf("vault upstream: 刷新签名失败: %w", err)
	}
	slog.Info("vault upstream: 签名已刷新", "obj", u.objPath, "ttl", u.urlTTL())
	return nil
}

// ReadAt 随机读取密文。
// U1 修复：跨窗口读取内部循环补齐——缓冲耗尽时按顺序续拉，直到填满 p 或到达 EOF；
// 短读必须带 io.EOF（io.ReaderAt 契约）。
func (u *Upstream) ReadAt(p []byte, off int64) (int, error) {
	total := 0
	for total < len(p) {
		n, err := u.readOnce(p[total:], off+int64(total))
		total += n
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.EOF
		}
	}
	return total, nil
}

// readOnce 单次读取：完整覆盖走缓冲；否则顺序大段或随机单块
func (u *Upstream) readOnce(p []byte, off int64) (int, error) {
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return 0, fmt.Errorf("vault upstream: 已关闭")
	}
	// U1：仅当缓冲完整覆盖 p 才直接返回（避免跨窗口短读）
	if len(u.buf) > 0 && off >= u.bufStart && off+int64(len(p)) <= u.bufEnd {
		rel := off - u.bufStart
		n := copy(p, u.buf[rel:])
		u.mu.Unlock()
		return n, nil
	}
	// 顺序判定（P9 契约②；U2：单块也写缓冲后，随机读会替换缓冲，后续读自然延续）
	sequential := len(u.buf) == 0 || (off >= u.bufStart && off <= u.bufEnd+StreamWindow)
	u.mu.Unlock()

	if sequential {
		return u.fetchRange(p, off, StreamWindow)
	}
	return u.fetchRange(p, off, len(p))
}

// fetchRange 发 Range 请求；失败（403/网络/Content-Range 不符/200）→ 换签名重试一次（P1 对齐 I10）。
// 416 直接返回不换签名（P2）。
func (u *Upstream) fetchRange(p []byte, off int64, length int) (int, error) {
	ctx := u.baseCtx
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if err := u.ensureFreshURL(ctx); err != nil {
			return 0, err
		}
		n, err := u.doRange(ctx, p, off, length)
		if err == nil {
			return n, nil
		}
		lastErr = err
		if errors.Is(err, errRangeInvalid) {
			return 0, err // 416：偏移越界，换签名无意义
		}
		if errors.Is(err, errIgnoredRange) && attempt == 1 {
			// I10 分级：两次 200（换签名仍无效）——≤2 MiB 丢弃后继续，否则 502
			if off > MaxDiscard {
				return 0, ErrRangeNotSupported
			}
			return u.discardServe(ctx, p, off)
		}
		if attempt == 1 {
			break
		}
		u.mu.Lock()
		u.buf = nil
		u.bufStart, u.bufEnd = 0, 0
		refErr := u.refreshURL(ctx)
		u.mu.Unlock()
		if refErr != nil {
			return 0, lastErr
		}
		slog.Warn("vault upstream: 上游异常，已换签名重试", "obj", u.objPath, "err", err)
	}
	return 0, lastErr
}

// doRange 单次 Range 请求（I10 异常分支表）
func (u *Upstream) doRange(ctx context.Context, p []byte, off int64, length int) (int, error) {
	u.mu.Lock()
	rawURL := u.linkURL
	hdr := u.linkHdr
	u.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+int64(length)-1))
	// P3：仅透传白名单请求头（天翼需 UA；响应语义头如 Content-Type 不得带进请求）
	if hdr != nil {
		for _, k := range []string{"User-Agent", "Referer"} {
			if v := hdr.Get(k); v != "" {
				req.Header.Set(k, v)
			}
		}
	}

	resp, err := u.httpc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("vault upstream: 请求失败: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusPartialContent:
		// 校验 Content-Range 与请求区间一致（I10：不符 → 失效重试）
		cr := resp.Header.Get("Content-Range")
		wantStart := fmt.Sprintf("%d-", off)
		if !strings.HasPrefix(cr, "bytes "+wantStart) {
			return 0, fmt.Errorf("vault upstream: Content-Range 与请求区间不符: %q", cr)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, int64(length)+1))
		if err != nil {
			return 0, fmt.Errorf("vault upstream: 读取失败: %w", err)
		}
		// U2：无论顺序/随机都写入缓冲（随机读替换旧缓冲，后续顺序读自然延续）
		u.mu.Lock()
		u.buf = append(u.buf[:0], data...)
		u.bufStart = off
		u.bufEnd = off + int64(len(data))
		u.mu.Unlock()
		n := copy(p, data)
		if n < len(p) {
			return n, io.EOF
		}
		return n, nil

	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		return 0, errRangeInvalid

	case resp.StatusCode == http.StatusForbidden:
		return 0, fmt.Errorf("vault upstream: 签名过期（403）")

	case resp.StatusCode == http.StatusOK:
		return 0, errIgnoredRange

	default:
		return 0, fmt.Errorf("vault upstream: HTTP %d", resp.StatusCode)
	}
}

// discardServe 200 分级中的「丢弃到偏移」路径（I10）。
// 注（P5）：此路径读完即弃 body，放弃 keep-alive 连接复用（可接受取舍）。
func (u *Upstream) discardServe(ctx context.Context, p []byte, off int64) (int, error) {
	u.mu.Lock()
	rawURL := u.linkURL
	hdr := u.linkHdr
	u.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return 0, err
	}
	if hdr != nil {
		for _, k := range []string{"User-Agent", "Referer"} {
			if v := hdr.Get(k); v != "" {
				req.Header.Set(k, v)
			}
		}
	}
	resp, err := u.httpc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("vault upstream: 丢弃请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("vault upstream: 丢弃请求状态 %d", resp.StatusCode)
	}
	if _, err := io.CopyN(io.Discard, resp.Body, off); err != nil {
		return 0, fmt.Errorf("vault upstream: 丢弃失败: %w", err)
	}
	n, err := io.ReadFull(resp.Body, p)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return 0, fmt.Errorf("vault upstream: 读取失败: %w", err)
	}
	return n, nil
}

// Close 关闭上游（丢弃缓冲）
func (u *Upstream) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.closed = true
	u.buf = nil
	u.bufStart, u.bufEnd = 0, 0
	return nil
}

var _ RangeSource = (*Upstream)(nil)
