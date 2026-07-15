package fs

import (
	"context"
	"sync"
	"time"

	"mini-cloud/internal/model"
)

// Reader 只读驱动接口（同 webdav.Reader）
type Reader interface {
	List(ctx context.Context, path string) ([]model.Obj, error)
	Link(ctx context.Context, path string) (*model.Link, error)
}

// 缓存容量限制（嵌入式设备友好）
const (
	maxDirEntries  = 500
	maxLinkEntries = 1000
	cleanupInterval = 5 * time.Minute
)

// ============================================================
// 缓存条目（含访问时间用于 LRU 淘汰）
// ============================================================

type cacheEntry struct {
	data       []model.Obj
	expiresAt  time.Time
	lastAccess time.Time
}

type linkCacheEntry struct {
	link       *model.Link
	expiresAt  time.Time
	lastAccess time.Time
}

// ============================================================
// CachedReader 带缓存上限的 Reader
// ============================================================

type CachedReader struct {
	inner     Reader
	mu        sync.Mutex
	dirCache  map[string]*cacheEntry
	linkCache map[string]*linkCacheEntry

	dirTTL  time.Duration
	linkTTL time.Duration

	stopCleanup chan struct{}
}

// NewCachedReader 创建缓存包装器，自动启动定期清理
func NewCachedReader(inner Reader) *CachedReader {
	c := &CachedReader{
		inner:       inner,
		dirCache:    make(map[string]*cacheEntry),
		linkCache:   make(map[string]*linkCacheEntry),
		dirTTL:      30 * time.Second,
		linkTTL:     10 * time.Minute,
		stopCleanup: make(chan struct{}),
	}
	go c.periodicCleanup()
	return c
}

// periodicCleanup 定期清理过期条目
func (c *CachedReader) periodicCleanup() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.removeExpired()
		case <-c.stopCleanup:
			return
		}
	}
}

// removeExpired 清除所有过期条目
func (c *CachedReader) removeExpired() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, v := range c.dirCache {
		if now.After(v.expiresAt) {
			delete(c.dirCache, k)
		}
	}
	for k, v := range c.linkCache {
		if now.After(v.expiresAt) {
			delete(c.linkCache, k)
		}
	}
}

// List 带缓存的目录列表
func (c *CachedReader) List(ctx context.Context, path string) ([]model.Obj, error) {
	c.mu.Lock()
	entry, ok := c.dirCache[path]
	if ok && time.Now().Before(entry.expiresAt) {
		entry.lastAccess = time.Now()
		c.mu.Unlock()
		return entry.data, nil
	}
	c.mu.Unlock()

	// 缓存未命中，调用底层驱动
	objs, err := c.inner.List(ctx, path)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// 检查是否需要淘汰
	if len(c.dirCache) >= maxDirEntries {
		c.evictOldestDirLocked()
	}

	c.dirCache[path] = &cacheEntry{
		data:       objs,
		expiresAt:  time.Now().Add(c.dirTTL),
		lastAccess: time.Now(),
	}

	return objs, nil
}

// Link 带缓存的文件链接（只缓存 URL 类型）
func (c *CachedReader) Link(ctx context.Context, path string) (*model.Link, error) {
	c.mu.Lock()
	entry, ok := c.linkCache[path]
	if ok && time.Now().Before(entry.expiresAt) {
		entry.lastAccess = time.Now()
		c.mu.Unlock()
		return entry.link, nil
	}
	c.mu.Unlock()

	link, err := c.inner.Link(ctx, path)
	if err != nil {
		return nil, err
	}

	// 只缓存 URL 类型的 Link（不缓存数据流）
	if link.URL != "" {
		c.mu.Lock()
		if len(c.linkCache) >= maxLinkEntries {
			c.evictOldestLinkLocked()
		}
		c.linkCache[path] = &linkCacheEntry{
			link:       link,
			expiresAt:  time.Now().Add(c.linkTTL),
			lastAccess: time.Now(),
		}
		c.mu.Unlock()
	}

	return link, nil
}

// evictOldestDirLocked 淘汰最旧的目录缓存条目（调用方需持有锁）
func (c *CachedReader) evictOldestDirLocked() {
	// 优先淘汰已过期且最久未访问的
	var oldestKey string
	var oldestTime time.Time
	first := true

	for k, v := range c.dirCache {
		if first || v.lastAccess.Before(oldestTime) {
			oldestKey = k
			oldestTime = v.lastAccess
			first = false
		}
	}
	if oldestKey != "" {
		delete(c.dirCache, oldestKey)
	}
}

func (c *CachedReader) evictOldestLinkLocked() {
	var oldestKey string
	var oldestTime time.Time
	first := true

	for k, v := range c.linkCache {
		if first || v.lastAccess.Before(oldestTime) {
			oldestKey = k
			oldestTime = v.lastAccess
			first = false
		}
	}
	if oldestKey != "" {
		delete(c.linkCache, oldestKey)
	}
}

// ============================================================
// 对外接口
// ============================================================

// Stop 停止后台清理
func (c *CachedReader) Stop() {
	close(c.stopCleanup)
}

// InvalidateDir 使指定目录缓存失效
func (c *CachedReader) InvalidateDir(path string) {
	c.mu.Lock()
	delete(c.dirCache, path)
	c.mu.Unlock()
}

// InvalidateLink 使指定文件链接缓存失效
func (c *CachedReader) InvalidateLink(path string) {
	c.mu.Lock()
	delete(c.linkCache, path)
	c.mu.Unlock()
}

// Stats 返回缓存统计（调试用）
func (c *CachedReader) Stats() (dirs int, links int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.dirCache), len(c.linkCache)
}
