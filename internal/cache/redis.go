package cache

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisOpTimeout bounds every individual Redis command on the proxy hot
// path. The PING in NewRedisStore uses a longer 2s budget — startup is
// allowed to be slower than a request-time call, where 200 ms is the
// hard ceiling we are willing to add to a single RPC request.
const redisOpTimeout = 200 * time.Millisecond

// RedisStore is a Store backed by a remote Redis instance. It is opt-in:
// the process picks it via AEGIS_CACHE_BACKEND=redis and supplies a
// connection string in AEGIS_REDIS_URL.
//
// Errors are not propagated to callers: a Redis blip degrades the proxy
// to a no-cache mode rather than failing requests. To avoid log floods
// when Redis is down, only the first error after a healthy period is
// logged, and a single recovery line is logged when calls start
// succeeding again.
type RedisStore struct {
	client *redis.Client
	parent context.Context
	addr   string // host:port, never carries credentials
	db     int

	// degraded toggles 0→1 on the first failed op and 1→0 on the first
	// success after that. Both transitions emit exactly one log line.
	degraded atomic.Int32
}

// NewRedisStore parses the Redis URL, dials once to verify connectivity,
// and returns a usable Store. parent should be the proxy lifecycle
// context — Get/Set/Delete derive child contexts from it so in-flight
// calls cancel cleanly on shutdown instead of waiting out the full
// per-op timeout.
func NewRedisStore(parent context.Context, redisURL string) (*RedisStore, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(opts)

	pingCtx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, err
	}
	return &RedisStore{
		client: client,
		parent: parent,
		addr:   opts.Addr,
		db:     opts.DB,
	}, nil
}

// Addr returns a credential-free description of the connection
// (host:port db=N) suitable for startup logs.
func (r *RedisStore) Addr() string {
	return fmt.Sprintf("%s db=%d", r.addr, r.db)
}

// Get returns the value for key. Any error — including the redis.Nil
// sentinel for a missing key, a network blip, or a timeout — is
// treated as a cache miss.
func (r *RedisStore) Get(key string) ([]byte, bool) {
	ctx, cancel := context.WithTimeout(r.parent, redisOpTimeout)
	defer cancel()

	v, err := r.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			r.markHealthy()
			return nil, false
		}
		r.markDegraded("GET", err)
		return nil, false
	}
	r.markHealthy()
	return v, true
}

// Set writes key=value with the given ttl. ttl=0 means "no expiry" —
// matching the in-memory Cache semantics, used for immutable responses.
func (r *RedisStore) Set(key string, value []byte, ttl time.Duration) {
	ctx, cancel := context.WithTimeout(r.parent, redisOpTimeout)
	defer cancel()

	if err := r.client.Set(ctx, key, value, ttl).Err(); err != nil {
		r.markDegraded("SET", err)
		return
	}
	r.markHealthy()
}

// Delete removes key. Missing keys are not an error.
func (r *RedisStore) Delete(key string) {
	ctx, cancel := context.WithTimeout(r.parent, redisOpTimeout)
	defer cancel()

	if err := r.client.Del(ctx, key).Err(); err != nil {
		r.markDegraded("DEL", err)
		return
	}
	r.markHealthy()
}

// Close releases the underlying connection pool. Safe to call multiple
// times — the underlying redis client is idempotent on Close.
func (r *RedisStore) Close() error {
	return r.client.Close()
}

// markDegraded records the first failure of a healthy → unhealthy
// transition. Subsequent failures while already degraded are silent so
// that a Redis outage does not flood logs at the proxy's request rate.
func (r *RedisStore) markDegraded(op string, err error) {
	if r.degraded.CompareAndSwap(0, 1) {
		log.Printf("[redis] %s failed, switching to no-cache mode: %v", op, err)
	}
}

// markHealthy records a recovery (unhealthy → healthy). Logged once
// per recovery; no-ops on the steady-state happy path.
func (r *RedisStore) markHealthy() {
	if r.degraded.CompareAndSwap(1, 0) {
		log.Printf("[redis] backend recovered")
	}
}

// Compile-time assertion: keep RedisStore in lockstep with Store.
var _ Store = (*RedisStore)(nil)
