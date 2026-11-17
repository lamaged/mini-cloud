package vault

import (
	"context"
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
//   ① 预读/缓冲窗口 8 MiB；② 相邻 ReadAt 偏移差 ≤ 窗口视为顺序（命中缓冲不请求）；
//   ③ 反向 Seek/跨块跳跃/403 重签后缓冲立即失效；④ 缓冲内存计入 max_streams 账（每流 ≤ 窗口）。
// 403 刷新（feasibility §5.4）：失效旧 URL → 重新 Link → 重试一次 → 仍失败报错（webdav 映射 502）。
// ctx 传导（I6）：使用 Link 时的请求 ctx（baseCtx），客户端断开即取消上游请求。
// ============================================================

const (
	// StreamWindow 顺序读缓冲窗口（P9：4–8 MiB，取 4 MiB 下限——
	// 每流内存 = 窗口 4 MiB + 分块缓存 2 MiB = 6 MiB，4 路并发封顶 24 MiB）
	StreamWindow = 4 << 20
	// MaxDiscard 上游忽略 Range 时允许的流式丢弃上限（I10：≤ 2 MiB）
	MaxDiscard = 2 << 20
	// URL 缓存 TTL 上限（feasibility §5.4：min(10min, expires-now-60s)）
	MaxURLTTL = 10 * time.Minute
)

// ErrUpstream 上游异常（webdav 映射 502）
type ErrUpstream struct{ msg string }

func (e *ErrUpstream) Error() string { return "vault upstream: " + e.msg }

// ErrRangeNotSupported 上游不支持 Range（I10：200 分级策略耗尽）
var ErrRangeNotSupported = &ErrUpstream{msg: "上游不支持 Range"}

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

	mu         sync.Mutex // 保护 URL 状态/刷新单飞/缓冲
	linkURL    string
	linkHdr    http.Header
	expiresAt  time.Time
	refreshing bool

	// 顺序读缓冲（P9 契约①②③）
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

// refreshURL 重新 Link 获取签名 URL（单飞保护由调用方保证）
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
		// 无法解析：按保守 TTL（10 分钟上限，与 CachedReader 链接缓存一致）
		exp = time.Now().Add(MaxURLTTL)
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

// ensureFreshURL 签名临近过期时刷新（单飞：并发共用同一次刷新，I6）
func (u *Upstream) ensureFreshURL(ctx context.Context) error {
	u.mu.Lock()
	if time.Now().Before(u.expiresAt.Add(-60 * time.Second)) {
		u.mu.Unlock()
		return nil
	}
	if u.refreshing {
		for u.refreshing {
			u.mu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
			u.mu.Lock()
		}
		u.mu.Unlock()
		return nil
	}
	u.refreshing = true
	u.mu.Unlock()

	err := u.refreshURL(ctx)

	u.mu.Lock()
	u.refreshing = false
	u.mu.Unlock()
	if err != nil {
		return fmt.Errorf("vault upstream: 刷新签名失败: %w", err)
	}
	slog.Info("vault upstream: 签名已刷新", "obj", u.objPath, "ttl", u.urlTTL())
	return nil
}

// ReadAt 随机读取密文（契约②：偏移差 ≤ 窗口视为顺序走缓冲；否则单块请求）
func (u *Upstream) ReadAt(p []byte, off int64) (int, error) {
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return 0, fmt.Errorf("vault upstream: 已关闭")
	}
	// 顺序命中缓冲
	if len(u.buf) > 0 && off >= u.bufStart && off < u.bufEnd {
		rel := off - u.bufStart
		n := copy(p, u.buf[rel:])
		u.mu.Unlock()
		if n == 0 {
			return 0, io.EOF
		}
		return n, nil
	}
	// 顺序判定（P9 契约②）：无缓冲（首读）或请求起点紧接缓冲之后视为顺序 → 拉大段
	sequential := len(u.buf) == 0 || (off >= u.bufEnd && off-u.bufEnd <= StreamWindow)
	u.mu.Unlock()

	if sequential {
		return u.fetchRange(p, off, StreamWindow, true)
	}
	return u.fetchRange(p, off, len(p), false)
}

// fetchRange 发 Range 请求；allowBuffer=true 时把拉到的数据写入顺序缓冲。
// 失败（403/网络/Content-Range 不符）→ 强制刷新签名重试一次（feasibility §5.4）。
func (u *Upstream) fetchRange(p []byte, off int64, length int, allowBuffer bool) (int, error) {
	ctx := u.baseCtx
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if err := u.ensureFreshURL(ctx); err != nil {
			return 0, err
		}
		n, err := u.doRange(ctx, p, off, length, allowBuffer)
		if err == nil {
			return n, nil
		}
		lastErr = err
		if errorsIsRangeNotSupported(err) {
			return 0, err // I10：换签名仍 200 → 直接失败，不再无谓重试
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

func errorsIsRangeNotSupported(err error) bool {
	return err == ErrRangeNotSupported
}

// doRange 单次 Range 请求（I10 异常分支表）
func (u *Upstream) doRange(ctx context.Context, p []byte, off int64, length int, allowBuffer bool) (int, error) {
	u.mu.Lock()
	rawURL := u.linkURL
	hdr := u.linkHdr
	u.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+int64(length)-1))
	if hdr != nil {
		for k, vs := range hdr {
			for _, v := range vs {
				req.Header.Set(k, v) // 天翼需 UA=Mozilla/5.0（来自 Link 的 Header）
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
		if allowBuffer {
			u.mu.Lock()
			u.buf = append(u.buf[:0], data...)
			u.bufStart = off
			u.bufEnd = off + int64(len(data))
			u.mu.Unlock()
		}
		n := copy(p, data)
		if n < len(p) {
			return n, io.EOF
		}
		return n, nil

	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		return 0, fmt.Errorf("vault upstream: 416 区间无效")

	case resp.StatusCode == http.StatusForbidden:
		return 0, fmt.Errorf("vault upstream: 签名过期（403）")

	case resp.StatusCode == http.StatusOK:
		// I10 分级：换签名由 fetchRange 重试；本次请求先尝试「丢弃到偏移」（仅 ≤ MaxDiscard）
		if off > MaxDiscard {
			return 0, ErrRangeNotSupported
		}
		if _, err := io.CopyN(io.Discard, resp.Body, off); err != nil {
			return 0, fmt.Errorf("vault upstream: 200 丢弃失败: %w", err)
		}
		n, err := io.ReadFull(resp.Body, p)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return 0, fmt.Errorf("vault upstream: 200 读取失败: %w", err)
		}
		return n, nil

	default:
		return 0, fmt.Errorf("vault upstream: HTTP %d", resp.StatusCode)
	}
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
