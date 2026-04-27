package cache

import "time"

// Store is the abstract key-value backing store used by the proxy handler.
// Two implementations are provided:
//
//   - Cache (cache.go)      : in-process LRU with TTL, default for single-instance deployments.
//   - RedisStore (redis.go) : remote shared store, opt-in for multi-replica deployments
//     where every replica should observe the same cached responses.
//
// A ttl of 0 means "never expire" — used for immutable responses such as
// finalized block bodies and transaction receipts.
type Store interface {
	Get(key string) ([]byte, bool)
	Set(key string, value []byte, ttl time.Duration)
	Delete(key string)
}

// Compile-time assertion that *Cache satisfies Store. If anyone changes the
// Cache method set incompatibly, the build breaks here instead of at the
// call site.
var _ Store = (*Cache)(nil)
