package web

// cache.go — bounded in-memory thumbnail cache.
// Evicts an arbitrary entry when at capacity to prevent unbounded memory growth
// in long-running deployments with large SD cards.

import "sync"

const thumbCacheMaxSize = 512

type boundedCache struct {
	mu      sync.Mutex
	entries map[string][]byte
}

func newBoundedCache() *boundedCache {
	return &boundedCache{entries: make(map[string][]byte)}
}

func (c *boundedCache) Load(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.entries[key]
	return v, ok
}

func (c *boundedCache) Store(key string, value []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists && len(c.entries) >= thumbCacheMaxSize {
		// Evict an arbitrary entry to stay within the size limit.
		for k := range c.entries {
			delete(c.entries, k)
			break
		}
	}
	c.entries[key] = value
}
