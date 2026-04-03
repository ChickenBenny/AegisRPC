package cache

import (
	"container/list"
	"context"
	"encoding/json"
	"sync"
	"time"
)

type entry struct {
	key    string
	value  []byte
	expiry time.Time // zero means never expires
}

// Cache is an in-memory store with TTL support and optional LRU eviction.
// When maxEntries > 0 the least-recently-used entry is evicted once the cap
// is reached, bounding memory usage even for immutable (TTL=0) entries.
type Cache struct {
	mu         sync.Mutex
	items      map[string]*list.Element
	lru        *list.List // front = most recently used
	maxEntries int        // 0 = unlimited
}

// NewCache creates a Cache that periodically removes expired entries.
// maxEntries > 0 enables LRU eviction when the cap is reached.
func NewCache(ctx context.Context, cleanupInterval time.Duration, maxEntries ...int) *Cache {
	cap := 0
	if len(maxEntries) > 0 {
		cap = maxEntries[0]
	}
	c := &Cache{
		items:      make(map[string]*list.Element),
		lru:        list.New(),
		maxEntries: cap,
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
	for _, el := range c.items {
		e := el.Value.(*entry)
		if !e.expiry.IsZero() && e.expiry.Before(now) {
			c.removeElement(el)
		}
	}
	c.mu.Unlock()
}

func CacheKey(method string, params []byte) string {
	var v any
	if err := json.Unmarshal(params, &v); err != nil {
		return method + ":" + string(params)
	}
	// Note: if params is a JSON object, Go's map[string]interface{} does not
	// guarantee key order, so two semantically identical objects may produce
	// different keys. RPC params are almost always arrays, so this is rarely
	// an issue in practice.
	canonical, _ := json.Marshal(v)
	return method + ":" + string(canonical)
}

func (c *Cache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	e := el.Value.(*entry)
	if !e.expiry.IsZero() && e.expiry.Before(time.Now()) {
		c.removeElement(el)
		return nil, false
	}
	c.lru.MoveToFront(el)
	return e.value, true
}

func (c *Cache) Set(key string, value []byte, ttl time.Duration) {
	var expiry time.Time
	if ttl > 0 {
		expiry = time.Now().Add(ttl)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		// Update existing entry and mark it as recently used.
		e := el.Value.(*entry)
		e.value = value
		e.expiry = expiry
		c.lru.MoveToFront(el)
		return
	}

	// Evict the least-recently-used entry when at capacity.
	if c.maxEntries > 0 && c.lru.Len() >= c.maxEntries {
		c.evictLRU()
	}

	el := c.lru.PushFront(&entry{key: key, value: value, expiry: expiry})
	c.items[key] = el
}

func (c *Cache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.removeElement(el)
	}
}

func (c *Cache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Len()
}

// evictLRU removes the least-recently-used entry. Must be called with mu held.
func (c *Cache) evictLRU() {
	el := c.lru.Back()
	if el != nil {
		c.removeElement(el)
	}
}

// removeElement removes a list element and its map entry. Must be called with mu held.
func (c *Cache) removeElement(el *list.Element) {
	c.lru.Remove(el)
	delete(c.items, el.Value.(*entry).key)
}
