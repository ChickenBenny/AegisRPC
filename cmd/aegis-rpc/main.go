package main

import (
	"context"
	"log/slog"
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
		// Logger is not configured yet; use stderr directly with a stable prefix
		// so config-failure errors look the same regardless of LogFormat.
		slog.Error("config error", "err", err)
		os.Exit(1)
	}
	setupLogger(cfg)

	pool, err := upstream.NewPool(cfg.Upstreams)
	if err != nil {
		slog.Error("failed to create upstream pool", "err", err)
		os.Exit(1)
	}
	slog.Info("upstreams loaded", "count", len(cfg.Upstreams))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fc := cache.NewFinalityChecker(cfg.FinalityDepth)
	healthDone := pool.StartHealthChecks(ctx, cfg.HealthInterval, cfg.LagThreshold, cfg.ProbeTimeout, fc.SetHead)
	slog.Info("finality checker initialised", "depth_blocks", cfg.FinalityDepth)

	store, err := buildCacheStore(ctx, cfg)
	if err != nil {
		slog.Error("cache backend error", "err", err)
		os.Exit(1)
	}
	defer func() {
		if err := store.Close(); err != nil {
			slog.Warn("cache close error", "err", err)
		}
	}()
	rtr := router.New(pool)
	h := proxy.NewHandler(rtr, store, cfg.MutableTTL, fc)

	srv := httpapi.New(cfg.Port, h, pool)

	go func() {
		slog.Info("server started",
			"addr", srv.Addr(),
			"mutable_ttl", cfg.MutableTTL,
			"health_interval", cfg.HealthInterval,
			"probe_timeout", cfg.ProbeTimeout)
		if err := srv.Start(); err != nil {
			slog.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
	if err := srv.Shutdown(15 * time.Second); err != nil {
		slog.Warn("server shutdown error", "err", err)
	}
	// Wait for the health-check goroutine to exit before letting deferred
	// store.Close() run; otherwise an in-flight probe could outlive the
	// resources it depends on. The deadline is a safety net for the rare
	// case where ctx cancellation does not propagate promptly through the
	// HTTP stack (e.g. DNS resolution or TLS handshake on some platforms);
	// in steady state the channel is already closed when we get here.
	healthDeadline := cfg.ProbeTimeout + time.Second
	select {
	case <-healthDone:
	case <-time.After(healthDeadline):
		slog.Warn("health-check goroutine did not exit within deadline",
			"deadline", healthDeadline)
	}
	slog.Info("stopped")
}

// setupLogger installs the application-wide slog default handler. Called once
// after Parse() so that LogLevel/LogFormat from config (validated against an
// allowlist) shape every subsequent log line. Output goes to stderr so that
// stdout stays free for any future structured RPC output.
func setupLogger(cfg config.Config) {
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default: // "info"
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if cfg.LogFormat == "json" {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(handler))
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
		slog.Info("cache backend selected", "backend", "redis", "addr", store.Addr())
		return store, nil
	default:
		slog.Info("cache backend selected", "backend", "memory", "max_entries", cfg.MaxCacheEntries)
		return cache.NewCache(ctx, 5*time.Minute, cfg.MaxCacheEntries), nil
	}
}
