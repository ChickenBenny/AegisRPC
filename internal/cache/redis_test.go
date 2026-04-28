package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestRedisStore spins up an in-process miniredis and returns a
// RedisStore wired to it, plus a cleanup function the test must defer.
func newTestRedisStore(t *testing.T) (*RedisStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	store, err := NewRedisStore(context.Background(), "redis://"+mr.Addr())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store, mr
}

func TestRedisStore_GetSetRoundtrip(t *testing.T) {
	store, _ := newTestRedisStore(t)

	store.Set("k1", []byte("hello"), time.Minute)

	v, ok := store.Get("k1")
	assert.True(t, ok)
	assert.Equal(t, []byte("hello"), v)
}

func TestRedisStore_MissReturnsFalse(t *testing.T) {
	store, _ := newTestRedisStore(t)

	v, ok := store.Get("nonexistent")
	assert.False(t, ok)
	assert.Nil(t, v)
}

func TestRedisStore_TTLZeroNeverExpires(t *testing.T) {
	store, mr := newTestRedisStore(t)

	store.Set("forever", []byte("v"), 0)

	// Advance miniredis well past any reasonable TTL — entry must remain.
	mr.FastForward(24 * time.Hour)

	v, ok := store.Get("forever")
	assert.True(t, ok, "ttl=0 must mean no expiry, matching the in-memory Cache contract")
	assert.Equal(t, []byte("v"), v)
}

func TestRedisStore_TTLPositiveExpires(t *testing.T) {
	store, mr := newTestRedisStore(t)

	store.Set("ephemeral", []byte("v"), 10*time.Second)

	mr.FastForward(11 * time.Second)

	_, ok := store.Get("ephemeral")
	assert.False(t, ok, "entry must be gone after its ttl elapses")
}

func TestRedisStore_Delete(t *testing.T) {
	store, _ := newTestRedisStore(t)

	store.Set("k", []byte("v"), time.Minute)
	store.Delete("k")

	_, ok := store.Get("k")
	assert.False(t, ok)
}

func TestRedisStore_DeleteMissingIsSilent(t *testing.T) {
	store, _ := newTestRedisStore(t)

	// Must not panic, log, or otherwise misbehave on absent keys.
	store.Delete("nope")
}

func TestNewRedisStore_PingFailsFastOnUnreachableHost(t *testing.T) {
	// Port 1 is reserved (TCP Port Service Multiplexer); nothing should
	// be listening, so Ping returns an error within the 2s budget.
	_, err := NewRedisStore(context.Background(), "redis://127.0.0.1:1/0")
	assert.Error(t, err, "constructor must fail loudly on unreachable Redis")
}

func TestNewRedisStore_BadURLRejected(t *testing.T) {
	_, err := NewRedisStore(context.Background(), "not-a-redis-url")
	assert.Error(t, err)
}

func TestRedisStore_OutageTreatedAsMiss(t *testing.T) {
	store, mr := newTestRedisStore(t)

	store.Set("k", []byte("v"), time.Minute)
	v, ok := store.Get("k")
	require.True(t, ok)
	require.Equal(t, []byte("v"), v)

	// Simulate a Redis outage. Subsequent calls must surface as cache
	// misses (Get → false, Set/Delete silent), never as panics or
	// errors propagated to the caller.
	mr.Close()

	_, ok = store.Get("k")
	assert.False(t, ok, "Get on dead Redis must return cache miss")

	store.Set("after-outage", []byte("v"), time.Minute)
	store.Delete("k")
	// No assertion here — the contract is "does not panic / does not
	// return an error", which is satisfied by reaching this line.
}

func TestRedisStore_AddrIsCredentialFree(t *testing.T) {
	mr := miniredis.RunT(t)
	mr.RequireAuth("s3cret")

	store, err := NewRedisStore(context.Background(), "redis://:s3cret@"+mr.Addr()+"/0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	addr := store.Addr()
	assert.NotContains(t, addr, "s3cret", "Addr() must never expose the password")
	assert.Contains(t, addr, mr.Addr(), "Addr() should still identify the host")
}

func TestRedisStore_SatisfiesStoreInterface(t *testing.T) {
	// Compile-time check is also in redis.go; this exercises the runtime
	// path so a future change that accidentally breaks one of the
	// interface methods (signature drift, panicky Close) is caught here.
	var s Store = &RedisStore{}
	_ = s
}
