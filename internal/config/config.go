package config

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all tunable parameters for AegisRPC.
type Config struct {
	Port            int
	Upstreams       []string
	MutableTTL      time.Duration
	MaxCacheEntries int
	FinalityDepth   uint64
	HealthInterval  time.Duration
	ProbeTimeout    time.Duration
	LagThreshold    uint64
	// CacheBackend selects the cache implementation: "memory" (default,
	// per-process LRU) or "redis" (shared across replicas).
	CacheBackend string
	// RedisURL is the connection string used when CacheBackend="redis",
	// e.g. redis://localhost:6379/0 or redis://:password@host:6379/0.
	RedisURL string
	// LogLevel filters slog output. One of "debug", "info", "warn", "error".
	// Lower-level events are dropped; e.g. "info" hides debug-level probe
	// successes but keeps state transitions and errors.
	LogLevel string
	// LogFormat selects the slog handler: "text" for human-readable
	// key=value output or "json" for structured logs suitable for
	// shipping to ELK / Loki / Datadog.
	LogFormat string
	// WriteTimeout caps how long the HTTP server may take to write a
	// full response. The default 30s is safe for wallet/dApp traffic;
	// archive deployments serving wide-range eth_getLogs or debug_trace*
	// queries may need to raise this to 120s+ to avoid mid-response
	// connection resets.
	WriteTimeout time.Duration
	// WSReplayPendingCap bounds upstream frames buffered per WS session
	// during reconnect / subscription replay. 1024 fits wallet/dApp
	// traffic; indexer-heavy deployments may want to raise this.
	WSReplayPendingCap int
	// WSAllowedOrigins is the exact-match origin allowlist for /ws upgrade
	// requests. Empty (default) = allow-all for backward compat; any
	// non-empty value activates strict checking.
	WSAllowedOrigins []string
}

// Default returns a Config populated with production-ready defaults.
func Default() Config {
	return Config{
		Port:               8080,
		Upstreams:          []string{"https://eth.llamarpc.com"},
		MutableTTL:         12 * time.Second,
		MaxCacheEntries:    10_000,
		FinalityDepth:      12,
		HealthInterval:     15 * time.Second,
		ProbeTimeout:       5 * time.Second,
		LagThreshold:       10,
		CacheBackend:       "memory",
		RedisURL:           "",
		LogLevel:           "info",
		LogFormat:          "text",
		WriteTimeout:       30 * time.Second,
		WSReplayPendingCap: 1024,
		WSAllowedOrigins:   nil, // empty = allow all (backward compat)
	}
}

// yamlConfig mirrors Config but:
//   - Uses strings for duration fields to support "15s" syntax.
//   - Uses pointers for integer/uint64 fields so that an explicit "0" in the
//     YAML file can be distinguished from "field not present". Without pointers
//     the zero-value check (if yc.MaxCacheEntries != 0) would silently skip a
//     legitimate max_cache_entries: 0 (unlimited) setting.
type yamlConfig struct {
	Port               *int     `yaml:"port"`
	Upstreams          []string `yaml:"upstreams"`
	MutableTTL         string   `yaml:"mutable_ttl"`
	MaxCacheEntries    *int     `yaml:"max_cache_entries"`
	FinalityDepth      *uint64  `yaml:"finality_depth"`
	HealthInterval     string   `yaml:"health_interval"`
	ProbeTimeout       string   `yaml:"probe_timeout"`
	LagThreshold       *uint64  `yaml:"lag_threshold"`
	CacheBackend       string   `yaml:"cache_backend"`
	RedisURL           string   `yaml:"redis_url"`
	LogLevel           string   `yaml:"log_level"`
	LogFormat          string   `yaml:"log_format"`
	WriteTimeout       string   `yaml:"write_timeout"`
	WSReplayPendingCap *int     `yaml:"ws_replay_pending_cap"`
	WSAllowedOrigins   []string `yaml:"ws_allowed_origins"`
}

// LoadFile reads a YAML config file and merges non-zero values into cfg.
// Only fields present in the file override the existing cfg values.
func LoadFile(path string, cfg *Config) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open config file: %w", err)
	}
	defer f.Close()

	var yc yamlConfig
	if err := yaml.NewDecoder(f).Decode(&yc); err != nil {
		return fmt.Errorf("parse config file: %w", err)
	}

	if yc.Port != nil {
		cfg.Port = *yc.Port
	}
	if len(yc.Upstreams) > 0 {
		cfg.Upstreams = yc.Upstreams
	}
	if yc.MutableTTL != "" {
		d, err := time.ParseDuration(yc.MutableTTL)
		if err != nil {
			return fmt.Errorf("invalid mutable_ttl %q: %w", yc.MutableTTL, err)
		}
		cfg.MutableTTL = d
	}
	if yc.MaxCacheEntries != nil {
		cfg.MaxCacheEntries = *yc.MaxCacheEntries
	}
	if yc.FinalityDepth != nil {
		cfg.FinalityDepth = *yc.FinalityDepth
	}
	if yc.HealthInterval != "" {
		d, err := time.ParseDuration(yc.HealthInterval)
		if err != nil {
			return fmt.Errorf("invalid health_interval %q: %w", yc.HealthInterval, err)
		}
		cfg.HealthInterval = d
	}
	if yc.ProbeTimeout != "" {
		d, err := time.ParseDuration(yc.ProbeTimeout)
		if err != nil {
			return fmt.Errorf("invalid probe_timeout %q: %w", yc.ProbeTimeout, err)
		}
		cfg.ProbeTimeout = d
	}
	if yc.LagThreshold != nil {
		cfg.LagThreshold = *yc.LagThreshold
	}
	if yc.CacheBackend != "" {
		cfg.CacheBackend = yc.CacheBackend
	}
	if yc.RedisURL != "" {
		cfg.RedisURL = yc.RedisURL
	}
	if yc.LogLevel != "" {
		cfg.LogLevel = yc.LogLevel
	}
	if yc.LogFormat != "" {
		cfg.LogFormat = yc.LogFormat
	}
	if yc.WriteTimeout != "" {
		d, err := time.ParseDuration(yc.WriteTimeout)
		if err != nil {
			return fmt.Errorf("invalid write_timeout %q: %w", yc.WriteTimeout, err)
		}
		cfg.WriteTimeout = d
	}
	if yc.WSReplayPendingCap != nil {
		cfg.WSReplayPendingCap = *yc.WSReplayPendingCap
	}
	if yc.WSAllowedOrigins != nil {
		cfg.WSAllowedOrigins = yc.WSAllowedOrigins
	}

	return nil
}

// ApplyEnv reads AEGIS_* environment variables and overrides fields in cfg.
// Invalid values emit a [config] warning and are ignored, so that a single
// typo does not prevent the server from starting.
func ApplyEnv(cfg *Config) {
	envInt("AEGIS_PORT", func(v int) { cfg.Port = v })
	envStringSlice("AEGIS_UPSTREAMS", ",", func(v []string) { cfg.Upstreams = v })
	envDuration("AEGIS_MUTABLE_TTL", func(v time.Duration) { cfg.MutableTTL = v })
	envInt("AEGIS_MAX_CACHE_ENTRIES", func(v int) { cfg.MaxCacheEntries = v })
	envUint64("AEGIS_FINALITY_DEPTH", func(v uint64) { cfg.FinalityDepth = v })
	envDuration("AEGIS_HEALTH_INTERVAL", func(v time.Duration) { cfg.HealthInterval = v })
	envDuration("AEGIS_PROBE_TIMEOUT", func(v time.Duration) { cfg.ProbeTimeout = v })
	envUint64("AEGIS_LAG_THRESHOLD", func(v uint64) { cfg.LagThreshold = v })
	envString("AEGIS_CACHE_BACKEND", func(v string) { cfg.CacheBackend = v })
	envString("AEGIS_REDIS_URL", func(v string) { cfg.RedisURL = v })
	envString("AEGIS_LOG_LEVEL", func(v string) { cfg.LogLevel = v })
	envString("AEGIS_LOG_FORMAT", func(v string) { cfg.LogFormat = v })
	envDuration("AEGIS_WRITE_TIMEOUT", func(v time.Duration) { cfg.WriteTimeout = v })
	envInt("AEGIS_WS_REPLAY_PENDING_CAP", func(v int) { cfg.WSReplayPendingCap = v })
	envStringSlice("AEGIS_WS_ALLOWED_ORIGINS", ",", func(v []string) { cfg.WSAllowedOrigins = v })
}

// envInt reads an environment variable as a base-10 integer.
// If the value is empty the setter is not called; if it is present but malformed
// a warning is logged and the setter is still skipped so the existing field
// value (default or YAML-loaded) is preserved.
func envInt(name string, set func(int)) {
	v := os.Getenv(name)
	if v == "" {
		return
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		slog.Warn("ignoring invalid config env var", "key", name, "value", v, "expected", "integer")
		return
	}
	set(n)
}

func envUint64(name string, set func(uint64)) {
	v := os.Getenv(name)
	if v == "" {
		return
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		slog.Warn("ignoring invalid config env var", "key", name, "value", v, "expected", "uint64")
		return
	}
	set(n)
}

func envDuration(name string, set func(time.Duration)) {
	v := os.Getenv(name)
	if v == "" {
		return
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		slog.Warn("ignoring invalid config env var", "key", name, "value", v, "expected", "duration")
		return
	}
	set(d)
}

func envString(name string, set func(string)) {
	if v := os.Getenv(name); v != "" {
		set(v)
	}
}

// envStringSlice splits a separator-delimited env var. An entirely-empty result
// (e.g. AEGIS_UPSTREAMS=",,,") is treated as malformed and ignored with a warning.
func envStringSlice(name, sep string, set func([]string)) {
	v := os.Getenv(name)
	if v == "" {
		return
	}
	parts := splitTrimmed(v, sep)
	if len(parts) == 0 {
		slog.Warn("ignoring invalid config env var", "key", name, "value", v, "expected", "non-empty list")
		return
	}
	set(parts)
}

// Validate checks that the Config is internally consistent and safe to use.
// Call it after Parse() or whenever a Config is constructed manually.
func (c Config) Validate() error {
	if len(c.Upstreams) == 0 {
		return fmt.Errorf("at least one upstream URL is required")
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port %d is out of valid range [1, 65535]", c.Port)
	}
	if c.MutableTTL <= 0 {
		return fmt.Errorf("mutable_ttl must be positive, got %s", c.MutableTTL)
	}
	if c.ProbeTimeout <= 0 {
		return fmt.Errorf("probe_timeout must be positive, got %s", c.ProbeTimeout)
	}
	if c.HealthInterval <= 0 {
		return fmt.Errorf("health_interval must be positive, got %s", c.HealthInterval)
	}
	if c.ProbeTimeout >= c.HealthInterval {
		return fmt.Errorf("probe_timeout (%s) must be less than health_interval (%s)", c.ProbeTimeout, c.HealthInterval)
	}
	switch strings.ToLower(strings.TrimSpace(c.CacheBackend)) {
	case "memory":
		// no extra constraints
	case "redis":
		if c.RedisURL == "" {
			return fmt.Errorf("cache_backend=redis requires redis_url to be set (env AEGIS_REDIS_URL)")
		}
	default:
		return fmt.Errorf("cache_backend %q is not valid (allowed: memory, redis)", c.CacheBackend)
	}
	switch strings.ToLower(strings.TrimSpace(c.LogLevel)) {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log_level %q is not valid (allowed: debug, info, warn, error)", c.LogLevel)
	}
	switch strings.ToLower(strings.TrimSpace(c.LogFormat)) {
	case "text", "json":
	default:
		return fmt.Errorf("log_format %q is not valid (allowed: text, json)", c.LogFormat)
	}
	if c.WriteTimeout <= 0 {
		return fmt.Errorf("write_timeout must be positive, got %s", c.WriteTimeout)
	}
	if c.WSReplayPendingCap <= 0 {
		return fmt.Errorf("ws_replay_pending_cap must be positive, got %d", c.WSReplayPendingCap)
	}
	return nil
}

// Parse builds the final Config by layering all sources in priority order:
//
//	CLI flags  >  ENV variables  >  YAML config file  >  built-in defaults
//
// Call Parse() once at program startup; it calls flag.Parse() internally.
func Parse() (Config, error) {
	// -- flag declarations --------------------------------------------------
	configFile := flag.String("config", "", "Path to YAML config file (env AEGIS_CONFIG)")
	portFlag := flag.Int("port", 8080, "Port to listen on (env AEGIS_PORT)")
	upstreamsFlag := flag.String("upstreams", "https://eth.llamarpc.com", "Comma-separated upstream RPC URLs (env AEGIS_UPSTREAMS)")
	mutableTTL := flag.Duration("mutable-ttl", 12*time.Second, "TTL for mutable cached responses (env AEGIS_MUTABLE_TTL)")
	maxCache := flag.Int("max-cache-entries", 10_000, "LRU cap for the response cache, 0=unlimited (env AEGIS_MAX_CACHE_ENTRIES)")
	finality := flag.Uint64("finality-depth", 12, "Blocks required to consider a block finalized (env AEGIS_FINALITY_DEPTH)")
	healthInt := flag.Duration("health-interval", 15*time.Second, "Health check polling interval (env AEGIS_HEALTH_INTERVAL)")
	probeTo := flag.Duration("probe-timeout", 5*time.Second, "Per-node health probe HTTP timeout (env AEGIS_PROBE_TIMEOUT)")
	lagThresh := flag.Uint64("lag-threshold", 10, "Max blocks a node may lag before marked unhealthy (env AEGIS_LAG_THRESHOLD)")
	cacheBackend := flag.String("cache-backend", "memory", "Cache backend: memory or redis (env AEGIS_CACHE_BACKEND)")
	redisURL := flag.String("redis-url", "", "Redis connection URL when cache-backend=redis (env AEGIS_REDIS_URL)")
	logLevel := flag.String("log-level", "info", "Log level: debug, info, warn, error (env AEGIS_LOG_LEVEL)")
	logFormat := flag.String("log-format", "text", "Log format: text or json (env AEGIS_LOG_FORMAT)")
	writeTimeout := flag.Duration("write-timeout", 30*time.Second, "Maximum response write duration; raise for archive RPC (env AEGIS_WRITE_TIMEOUT)")
	wsReplayCap := flag.Int("ws-replay-pending-cap", 1024, "Max upstream frames buffered during WS subscription replay (env AEGIS_WS_REPLAY_PENDING_CAP)")
	wsAllowedOrigins := flag.String("ws-allowed-origins", "", "Comma-separated WS origin allowlist; empty = allow-all (env AEGIS_WS_ALLOWED_ORIGINS)")
	flag.Parse()

	// -- layer 1: defaults --------------------------------------------------
	cfg := Default()

	// -- layer 2: YAML file -------------------------------------------------
	cfgPath := *configFile
	if cfgPath == "" {
		cfgPath = os.Getenv("AEGIS_CONFIG")
	}
	if cfgPath != "" {
		if err := LoadFile(cfgPath, &cfg); err != nil {
			return cfg, fmt.Errorf("config file: %w", err)
		}
		slog.Info("loaded config file", "path", cfgPath)
	}

	// -- layer 3: ENV -------------------------------------------------------
	ApplyEnv(&cfg)

	// -- layer 4: explicit CLI flags ----------------------------------------
	// flag.Visit only walks flags the caller actually passed on the command
	// line, so a flag left at its default value never overrides ENV or the file.
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "port":
			cfg.Port = *portFlag
		case "upstreams":
			cfg.Upstreams = splitTrimmed(*upstreamsFlag, ",")
		case "mutable-ttl":
			cfg.MutableTTL = *mutableTTL
		case "max-cache-entries":
			cfg.MaxCacheEntries = *maxCache
		case "finality-depth":
			cfg.FinalityDepth = *finality
		case "health-interval":
			cfg.HealthInterval = *healthInt
		case "probe-timeout":
			cfg.ProbeTimeout = *probeTo
		case "lag-threshold":
			cfg.LagThreshold = *lagThresh
		case "cache-backend":
			cfg.CacheBackend = *cacheBackend
		case "redis-url":
			cfg.RedisURL = *redisURL
		case "log-level":
			cfg.LogLevel = *logLevel
		case "log-format":
			cfg.LogFormat = *logFormat
		case "write-timeout":
			cfg.WriteTimeout = *writeTimeout
		case "ws-replay-pending-cap":
			cfg.WSReplayPendingCap = *wsReplayCap
		case "ws-allowed-origins":
			cfg.WSAllowedOrigins = splitTrimmed(*wsAllowedOrigins, ",")
		}
	})

	cfg.CacheBackend = strings.ToLower(strings.TrimSpace(cfg.CacheBackend))
	cfg.LogLevel = strings.ToLower(strings.TrimSpace(cfg.LogLevel))
	cfg.LogFormat = strings.ToLower(strings.TrimSpace(cfg.LogFormat))

	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

func splitTrimmed(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
