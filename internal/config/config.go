package config

import (
	"flag"
	"fmt"
	"log"
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
}

// Default returns a Config populated with production-ready defaults.
func Default() Config {
	return Config{
		Port:            8080,
		Upstreams:       []string{"https://eth.llamarpc.com"},
		MutableTTL:      12 * time.Second,
		MaxCacheEntries: 10_000,
		FinalityDepth:   12,
		HealthInterval:  15 * time.Second,
		ProbeTimeout:    5 * time.Second,
		LagThreshold:    10,
	}
}

// yamlConfig mirrors Config but uses strings for duration fields to support "15s" syntax.
type yamlConfig struct {
	Port            int      `yaml:"port"`
	Upstreams       []string `yaml:"upstreams"`
	MutableTTL      string   `yaml:"mutable_ttl"`
	MaxCacheEntries int      `yaml:"max_cache_entries"`
	FinalityDepth   uint64   `yaml:"finality_depth"`
	HealthInterval  string   `yaml:"health_interval"`
	ProbeTimeout    string   `yaml:"probe_timeout"`
	LagThreshold    uint64   `yaml:"lag_threshold"`
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

	if yc.Port != 0 {
		cfg.Port = yc.Port
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
	if yc.MaxCacheEntries != 0 {
		cfg.MaxCacheEntries = yc.MaxCacheEntries
	}
	if yc.FinalityDepth != 0 {
		cfg.FinalityDepth = yc.FinalityDepth
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
	if yc.LagThreshold != 0 {
		cfg.LagThreshold = yc.LagThreshold
	}

	return nil
}

// ApplyEnv reads AEGIS_* environment variables and overrides fields in cfg.
// Invalid values are silently ignored so a typo in one var doesn't kill the server.
func ApplyEnv(cfg *Config) {
	if v := os.Getenv("AEGIS_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Port = n
		}
	}
	if v := os.Getenv("AEGIS_UPSTREAMS"); v != "" {
		urls := splitTrimmed(v, ",")
		if len(urls) > 0 {
			cfg.Upstreams = urls
		}
	}
	if v := os.Getenv("AEGIS_MUTABLE_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.MutableTTL = d
		}
	}
	if v := os.Getenv("AEGIS_MAX_CACHE_ENTRIES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxCacheEntries = n
		}
	}
	if v := os.Getenv("AEGIS_FINALITY_DEPTH"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			cfg.FinalityDepth = n
		}
	}
	if v := os.Getenv("AEGIS_HEALTH_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.HealthInterval = d
		}
	}
	if v := os.Getenv("AEGIS_PROBE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.ProbeTimeout = d
		}
	}
	if v := os.Getenv("AEGIS_LAG_THRESHOLD"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			cfg.LagThreshold = n
		}
	}
}

// Parse builds the final Config by layering all sources in priority order:
//
//	CLI flags  >  ENV variables  >  YAML config file  >  built-in defaults
//
// Call Parse() once at program startup; it calls flag.Parse() internally.
func Parse() (Config, error) {
	// -- flag declarations --------------------------------------------------
	configFile    := flag.String("config", "", "Path to YAML config file (env AEGIS_CONFIG)")
	portFlag      := flag.Int("port", 8080, "Port to listen on (env AEGIS_PORT)")
	upstreamsFlag := flag.String("upstreams", "https://eth.llamarpc.com", "Comma-separated upstream RPC URLs (env AEGIS_UPSTREAMS)")
	mutableTTL    := flag.Duration("mutable-ttl", 12*time.Second, "TTL for mutable cached responses (env AEGIS_MUTABLE_TTL)")
	maxCache      := flag.Int("max-cache-entries", 10_000, "LRU cap for the response cache, 0=unlimited (env AEGIS_MAX_CACHE_ENTRIES)")
	finality      := flag.Uint64("finality-depth", 12, "Blocks required to consider a block finalized (env AEGIS_FINALITY_DEPTH)")
	healthInt     := flag.Duration("health-interval", 15*time.Second, "Health check polling interval (env AEGIS_HEALTH_INTERVAL)")
	probeTo       := flag.Duration("probe-timeout", 5*time.Second, "Per-node health probe HTTP timeout (env AEGIS_PROBE_TIMEOUT)")
	lagThresh     := flag.Uint64("lag-threshold", 10, "Max blocks a node may lag before marked unhealthy (env AEGIS_LAG_THRESHOLD)")
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
		log.Printf("Loaded config from %s", cfgPath)
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
		}
	})

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
