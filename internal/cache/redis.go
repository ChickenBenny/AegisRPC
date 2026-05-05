package cache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisOpTimeout bounds every individual Redis command on the proxy hot path.
// Startup uses a longer 2s budget; request-time calls are capped at 200ms.
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

	// degraded toggles 0→1 on the first failure and 1→0 on recovery;
	// each transition emits exactly one log line.
	degraded atomic.Int32
}

// NewRedisStore parses the Redis URL, dials once to verify connectivity,
// and returns a usable Store. parent should be the proxy lifecycle context.
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

// Get returns the value for key; any error (including cache miss) is treated as a miss.
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

// Close releases the underlying connection pool.
func (r *RedisStore) Close() error {
	return r.client.Close()
}

// markDegraded logs the first failure of a healthy → unhealthy transition;
// subsequent failures while already degraded are silent.
func (r *RedisStore) markDegraded(op string, err error) {
	if r.degraded.CompareAndSwap(0, 1) {
		slog.Warn("redis op failed, switching to no-cache mode", "op", op, "err", err)
	}
}

// markHealthy logs recovery (unhealthy → healthy) and is a no-op in steady state.
func (r *RedisStore) markHealthy() {
	if r.degraded.CompareAndSwap(1, 0) {
		slog.Info("redis backend recovered")
	}
}

// Compile-time assertion: keep RedisStore in lockstep with Store.
var _ Store = (*RedisStore)(nil)
