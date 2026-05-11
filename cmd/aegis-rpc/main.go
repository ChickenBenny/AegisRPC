package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ChickenBenny/AegisRPC/internal/cache"
	ethprober "github.com/ChickenBenny/AegisRPC/internal/capability/eth"
	"github.com/ChickenBenny/AegisRPC/internal/config"
	"github.com/ChickenBenny/AegisRPC/internal/httpapi"
	"github.com/ChickenBenny/AegisRPC/internal/proxy"
	"github.com/ChickenBenny/AegisRPC/internal/router"
	"github.com/ChickenBenny/AegisRPC/internal/upstream"
)

func main() {
	// Configure the logger from env vars + CLI flags before config.Parse()
	// runs so that any slog emissions from the parser itself (config-file
	// load notifications, env-var validation warnings) already match the
	// user's chosen format. Without this, the first few lines would always
	// be in text format — fatal for AEGIS_LOG_FORMAT=json log pipelines
	// that expect every line to be parseable JSON.
	//
	// YAML is intentionally not consulted here; partial-parsing the YAML
	// file before flag.Parse has run is fragile, and operators driving
	// log config from YAML alone is the rare case (env / flag are far
	// more common for deployment-time logging settings).
	setupLoggerFromEarly()

	cfg, err := config.Parse()
	if err != nil {
		slog.Error("config error", "err", err)
		os.Exit(1)
	}
	// Re-apply with the fully resolved cfg so YAML overrides — and any
	// normalisation Validate has performed — take effect.
	setupLogger(cfg)

	pool, err := upstream.NewPool(cfg.Upstreams)
	if err != nil {
		slog.Error("failed to create upstream pool", "err", err)
		os.Exit(1)
	}
	slog.Info("upstreams loaded", "count", len(cfg.Upstreams))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Bounded capability probe — runs every upstream concurrently with a
	// wall-clock cap of 2× ProbeTimeout. Probe results OR-merge into URL
	// annotations (annotation is a floor); failures or deadline overflow
	// leave that node at its pre-probe caps. We block startup on this so
	// the routing surface is stable when the listener opens.
	probeCtx, probeCancel := context.WithTimeout(ctx, 2*cfg.ProbeTimeout)
	pool.ProbeCapabilities(probeCtx, ethprober.NewEthProber())
	probeCancel()

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

	srv := httpapi.New(cfg.Port, cfg.WriteTimeout, cfg.WSReplayPendingCap, cfg.WSAllowedOrigins, h, pool)

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

// setupLogger installs the application-wide slog default handler. Called twice:
// once early via setupLoggerFromEnv() so config parsing emits in the right
// format, then again after Parse() so YAML/flag overrides take effect. Output
// goes to stderr so stdout stays free for any future structured RPC output.
//
// Inputs are normalized defensively (ToLower) so this function is safe to
// call with raw user values — e.g. before config.Validate has had a chance
// to reject typos. Unknown values fall through to text/info, mirroring the
// stdlib slog defaults.
func setupLogger(cfg config.Config) {
	var level slog.Level
	switch strings.ToLower(strings.TrimSpace(cfg.LogLevel)) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default: // "info" or anything unknown — Validate will reject typos later
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if strings.ToLower(strings.TrimSpace(cfg.LogFormat)) == "json" {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(handler))
}

// setupLoggerFromEarly configures the logger using AEGIS_LOG_LEVEL /
// AEGIS_LOG_FORMAT env vars and the corresponding --log-level / --log-format
// CLI flags, falling back to defaults when unset. Used as the first step of
// main() so subsequent slog emissions during config.Parse() respect the
// operator's chosen format/level. Any YAML override is reapplied via
// setupLogger(cfg) after Parse() completes.
//
// CLI flags are read by directly walking os.Args via peekFlag, not by
// calling flag.Parse() — config.Parse later does its own flag.Parse and
// declaring flags twice on the global flag set would panic. The duplication
// is bounded to two well-known flags whose names are an industry convention
// (--log-level / --log-format), so this is acceptable.
func setupLoggerFromEarly() {
	cfg := config.Default()

	// env layer — applied first so flag layer can override
	if v := os.Getenv("AEGIS_LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if v := os.Getenv("AEGIS_LOG_FORMAT"); v != "" {
		cfg.LogFormat = v
	}

	// flag layer — overrides env when present, matching config.Parse precedence
	if v := peekFlag("log-level"); v != "" {
		cfg.LogLevel = v
	}
	if v := peekFlag("log-format"); v != "" {
		cfg.LogFormat = v
	}

	setupLogger(cfg)
}

// peekFlag scans os.Args for a flag in any of the four standard forms:
//
//	--name value     -name value
//	--name=value     -name=value
//
// and returns the value, or "" if the flag is absent. This is intentionally
// a minimal implementation; it does not validate that the flag is one of the
// program's declared flags (config.Parse handles full flag parsing later).
// Only used for log-level / log-format pre-parsing so that early-startup
// slog emissions match the operator's chosen format.
func peekFlag(name string) string {
	long, short := "--"+name, "-"+name
	longEq, shortEq := long+"=", short+"="
	args := os.Args[1:]
	for i, a := range args {
		switch {
		case strings.HasPrefix(a, longEq):
			return a[len(longEq):]
		case strings.HasPrefix(a, shortEq):
			return a[len(shortEq):]
		case a == long || a == short:
			if i+1 < len(args) {
				return args[i+1]
			}
		}
	}
	return ""
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
