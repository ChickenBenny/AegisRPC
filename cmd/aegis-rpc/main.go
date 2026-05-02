package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ChickenBenny/AegisRPC/internal/cache"
	"github.com/ChickenBenny/AegisRPC/internal/config"
	"github.com/ChickenBenny/AegisRPC/internal/httpapi"
	"github.com/ChickenBenny/AegisRPC/internal/proxy"
	"github.com/ChickenBenny/AegisRPC/internal/router"
	"github.com/ChickenBenny/AegisRPC/internal/upstream"
)

func main() {
	cfg, err := config.Parse()
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}

	pool, err := upstream.NewPool(cfg.Upstreams)
	if err != nil {
		log.Fatalf("Failed to create upstream pool: %v", err)
	}
	log.Printf("Loaded %d upstream(s)", len(cfg.Upstreams))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fc := cache.NewFinalityChecker(cfg.FinalityDepth)
	healthDone := pool.StartHealthChecks(ctx, cfg.HealthInterval, cfg.LagThreshold, cfg.ProbeTimeout, fc.SetHead)
	log.Printf("Finality depth: %d blocks", cfg.FinalityDepth)

	store, err := buildCacheStore(ctx, cfg)
	if err != nil {
		log.Fatalf("Cache backend error: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			log.Printf("Cache close error: %v", err)
		}
	}()
	rtr := router.New(pool)
	h := proxy.NewHandler(rtr, store, cfg.MutableTTL, fc)

	srv := httpapi.New(cfg.Port, h, pool)

	go func() {
		log.Printf("AegisRPC started on %s (mutableTTL=%s, healthInterval=%s, probeTimeout=%s)",
			srv.Addr(), cfg.MutableTTL, cfg.HealthInterval, cfg.ProbeTimeout)
		if err := srv.Start(); err != nil {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("Shutting down...")
	if err := srv.Shutdown(15 * time.Second); err != nil {
		log.Printf("Shutdown error: %v", err)
	}
	// Wait for the health-check goroutine to exit before letting deferred
	// store.Close() run; otherwise an in-flight probe could outlive the
	// resources it depends on.
	<-healthDone
	log.Println("Stopped.")
}

// buildCacheStore wires the cache backend chosen in config. The default
// in-memory LRU has zero external dependencies; switching to redis is opt-in
// for users who run multiple AegisRPC replicas and want a shared hit-rate.
func buildCacheStore(ctx context.Context, cfg config.Config) (cache.Store, error) {
	switch cfg.CacheBackend {
	case "redis":
		store, err := cache.NewRedisStore(ctx, cfg.RedisURL)
		if err != nil {
			return nil, err
		}
		// Log the redacted address from the parsed URL — never the raw
		// AEGIS_REDIS_URL value, which may contain a password.
		log.Printf("Cache backend: redis (%s)", store.Addr())
		return store, nil
	default:
		log.Printf("Cache backend: memory (max %d entries)", cfg.MaxCacheEntries)
		return cache.NewCache(ctx, 5*time.Minute, cfg.MaxCacheEntries), nil
	}
}
