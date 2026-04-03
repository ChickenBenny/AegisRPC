package cache

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Cache.Get / Set ──────────────────────────────────────────────────────

func TestCache_SetAndGet(t *testing.T) {
	c := NewCache(context.Background(), time.Minute)
	c.Set("key1", []byte(`{"result":"0x1"}`), 0)

	val, ok := c.Get("key1")
	require.True(t, ok)
	assert.Equal(t, []byte(`{"result":"0x1"}`), val)
}

func TestCache_Get_MissingKey(t *testing.T) {
	c := NewCache(context.Background(), time.Minute)
	_, ok := c.Get("nonexistent")
	assert.False(t, ok)
}

func TestCache_Set_OverwritesExisting(t *testing.T) {
	c := NewCache(context.Background(), time.Minute)
	c.Set("key1", []byte(`"old"`), 0)
	c.Set("key1", []byte(`"new"`), 0)

	val, ok := c.Get("key1")
	require.True(t, ok)
	assert.Equal(t, []byte(`"new"`), val)
}

// ─── TTL ─────────────────────────────────────────────────────────────────

func TestCache_TTL_EntryExpires(t *testing.T) {
	c := NewCache(context.Background(), time.Minute)
	c.Set("ttl-key", []byte(`"value"`), 50*time.Millisecond)

	// should be present immediately
	_, ok := c.Get("ttl-key")
	require.True(t, ok)

	time.Sleep(80 * time.Millisecond)

	// should be gone after TTL
	_, ok = c.Get("ttl-key")
	assert.False(t, ok, "entry should have expired")
}

func TestCache_TTL_Zero_NeverExpires(t *testing.T) {
	c := NewCache(context.Background(), time.Minute)
	c.Set("forever", []byte(`"immortal"`), 0)

	time.Sleep(50 * time.Millisecond)

	_, ok := c.Get("forever")
	assert.True(t, ok, "entry with ttl=0 should never expire")
}

// ─── Delete ───────────────────────────────────────────────────────────────

func TestCache_Delete(t *testing.T) {
	c := NewCache(context.Background(), time.Minute)
	c.Set("del-key", []byte(`"bye"`), 0)
	c.Delete("del-key")

	_, ok := c.Get("del-key")
	assert.False(t, ok)
}

func TestCache_Delete_NonExistent_NoError(t *testing.T) {
	c := NewCache(context.Background(), time.Minute)
	// should not panic
	c.Delete("ghost")
}

// ─── Size ─────────────────────────────────────────────────────────────────

func TestCache_Size(t *testing.T) {
	c := NewCache(context.Background(), time.Minute)
	assert.Equal(t, 0, c.Size())

	c.Set("a", []byte("1"), 0)
	c.Set("b", []byte("2"), 0)
	assert.Equal(t, 2, c.Size())

	c.Delete("a")
	assert.Equal(t, 1, c.Size())
}

// ─── LRU eviction ────────────────────────────────────────────────────────

func TestCache_LRU_EvictsWhenAtCap(t *testing.T) {
	c := NewCache(context.Background(), time.Minute, 3)
	c.Set("a", []byte("1"), 0)
	c.Set("b", []byte("2"), 0)
	c.Set("c", []byte("3"), 0)
	assert.Equal(t, 3, c.Size())

	// Adding a fourth entry should evict "a" (LRU).
	c.Set("d", []byte("4"), 0)
	assert.Equal(t, 3, c.Size())
	_, ok := c.Get("a")
	assert.False(t, ok, "LRU entry should have been evicted")
}

func TestCache_LRU_GetPromotesEntry(t *testing.T) {
	c := NewCache(context.Background(), time.Minute, 3)
	c.Set("a", []byte("1"), 0)
	c.Set("b", []byte("2"), 0)
	c.Set("c", []byte("3"), 0)

	// Touch "a" so it becomes most-recently-used; "b" is now LRU.
	c.Get("a")
	c.Set("d", []byte("4"), 0)

	_, aOk := c.Get("a")
	_, bOk := c.Get("b")
	assert.True(t, aOk, "recently accessed entry should survive eviction")
	assert.False(t, bOk, "LRU entry should have been evicted")
}

func TestCache_LRU_ZeroCap_Unlimited(t *testing.T) {
	c := NewCache(context.Background(), time.Minute, 0)
	for i := range 200 {
		c.Set(string(rune('a'+i%26))+string(rune(i)), []byte("v"), 0)
	}
	// No eviction limit — size should be at least 100.
	assert.GreaterOrEqual(t, c.Size(), 100)
}

// ─── CacheKey ─────────────────────────────────────────────────────────────

func TestCacheKey_SameMethodAndParams(t *testing.T) {
	k1 := CacheKey("eth_blockNumber", []byte(`[]`))
	k2 := CacheKey("eth_blockNumber", []byte(`[]`))
	assert.Equal(t, k1, k2)
}

func TestCacheKey_DifferentMethods(t *testing.T) {
	k1 := CacheKey("eth_blockNumber", []byte(`[]`))
	k2 := CacheKey("eth_gasPrice", []byte(`[]`))
	assert.NotEqual(t, k1, k2)
}

func TestCacheKey_DifferentParams(t *testing.T) {
	k1 := CacheKey("eth_getBalance", []byte(`["0xabc","latest"]`))
	k2 := CacheKey("eth_getBalance", []byte(`["0xdef","latest"]`))
	assert.NotEqual(t, k1, k2)
}

func TestCacheKey_NormalizesWhitespace(t *testing.T) {
	k1 := CacheKey("eth_getBalance", []byte(`["0xabc","latest"]`))
	k2 := CacheKey("eth_getBalance", []byte(`[ "0xabc",  "latest" ]`))
	assert.Equal(t, k1, k2, "semantically identical params should produce the same key")
}
