package cache

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

type cacheEntry struct {
	value []byte
	ttl   time.Time
}

type Cache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
}

func NewCache(ctx context.Context, cleanupInterval time.Duration) *Cache {
	c := &Cache{
		entries: make(map[string]cacheEntry),
	}
	go c.cleanupLoop(ctx, cleanupInterval)
	return c
}

func (c *Cache) cleanupLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.deleteExpired()
		case <-ctx.Done():
			return
		}
	}
}

func (c *Cache) deleteExpired() {
	now := time.Now()
	c.mu.Lock()
	for k, e := range c.entries {
		if !e.ttl.IsZero() && e.ttl.Before(now) {
			delete(c.entries, k)
		}
	}
	c.mu.Unlock()
}

func CacheKey(method string, params []byte) string {
	var v any
	if err := json.Unmarshal(params, &v); err != nil {
		return method + ":" + string(params)
	}
	canonical, _ := json.Marshal(v)
	return method + ":" + string(canonical)
}

func (c *Cache) Get(key string) ([]byte, bool) {
	c.mu.RLock()
	entry, exists := c.entries[key]
	c.mu.RUnlock()

	if !exists {
		return nil, false
	}
	if !entry.ttl.IsZero() && entry.ttl.Before(time.Now()) {
		c.Delete(key)
		return nil, false
	}
	return entry.value, true
}

func (c *Cache) Set(key string, value []byte, ttl time.Duration) {
	var expiry time.Time
	if ttl > 0 {
		expiry = time.Now().Add(ttl)
	}

	c.mu.Lock()
	c.entries[key] = cacheEntry{value: value, ttl: expiry}
	c.mu.Unlock()
}

func (c *Cache) Delete(key string) {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}

func (c *Cache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}
