package cache

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisOpTimeout bounds every individual Redis command. The proxy's hot path
// must never block on a slow or hung Redis instance — if Redis cannot answer
// within this budget, treat it as a cache miss and let the upstream call run.
const redisOpTimeout = 200 * time.Millisecond

// RedisStore is a Store backed by a remote Redis instance. It is opt-in: the
// process picks it via AEGIS_CACHE_BACKEND=redis and supplies a connection
// string in AEGIS_REDIS_URL. Errors from Redis are logged but never
// propagated, so a Redis outage degrades the proxy to a no-cache mode rather
// than failing requests.
type RedisStore struct {
	client *redis.Client
}

// NewRedisStore parses the Redis URL, dials once to verify connectivity, and
// returns a usable Store. The eager PING is intentional: a misconfigured
// AEGIS_REDIS_URL should fail loudly at startup, not silently produce cache
// misses for the lifetime of the process.
func NewRedisStore(ctx context.Context, redisURL string) (*RedisStore, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(opts)

	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, err
	}
	return &RedisStore{client: client}, nil
}

// Get returns the value for key. Any error — including the redis.Nil sentinel
// for a missing key, a network blip, or a timeout — is treated as a cache
// miss. Real errors are logged so the operator can spot a degraded backend.
func (r *RedisStore) Get(key string) ([]byte, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
	defer cancel()

	v, err := r.client.Get(ctx, key).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			log.Printf("[redis] GET %q failed: %v", key, err)
		}
		return nil, false
	}
	return v, true
}

// Set writes key=value with the given ttl. A ttl of 0 means "no expiry" —
// matching the in-memory Cache semantics, used for immutable responses.
func (r *RedisStore) Set(key string, value []byte, ttl time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
	defer cancel()

	if err := r.client.Set(ctx, key, value, ttl).Err(); err != nil {
		log.Printf("[redis] SET %q failed: %v", key, err)
	}
}

// Delete removes key. Missing keys are not an error.
func (r *RedisStore) Delete(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
	defer cancel()

	if err := r.client.Del(ctx, key).Err(); err != nil {
		log.Printf("[redis] DEL %q failed: %v", key, err)
	}
}

// Close releases the underlying connection pool.
func (r *RedisStore) Close() error {
	return r.client.Close()
}

// Compile-time assertion: keep RedisStore in lockstep with the Store contract.
var _ Store = (*RedisStore)(nil)
