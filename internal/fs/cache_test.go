package fs

import "testing"

func TestStopIdempotent(t *testing.T) {
	c := NewCachedReader(nil)
	c.Stop()
	c.Stop() // 重复 Stop 不得 panic（I7/P4）
}

func TestInvalidateAll(t *testing.T) {
	c := NewCachedReader(nil)
	defer c.Stop()

	c.mu.Lock()
	c.dirCache["/a"] = &cacheEntry{}
	c.linkCache["/b.txt"] = &linkCacheEntry{}
	c.mu.Unlock()

	c.InvalidateAll()

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.dirCache) != 0 || len(c.linkCache) != 0 {
		t.Errorf("InvalidateAll should clear both caches: dirs=%d links=%d", len(c.dirCache), len(c.linkCache))
	}
}
