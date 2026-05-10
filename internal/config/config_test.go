package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefault(t *testing.T) {
	cfg := Default()
	assert.Equal(t, 8080, cfg.Port)
	assert.Equal(t, []string{"https://eth.llamarpc.com"}, cfg.Upstreams)
	assert.Equal(t, 12*time.Second, cfg.MutableTTL)
	assert.Equal(t, 10_000, cfg.MaxCacheEntries)
	assert.Equal(t, uint64(12), cfg.FinalityDepth)
	assert.Equal(t, 15*time.Second, cfg.HealthInterval)
	assert.Equal(t, 5*time.Second, cfg.ProbeTimeout)
	assert.Equal(t, uint64(10), cfg.LagThreshold)
	assert.Equal(t, "info", cfg.LogLevel)
	assert.Equal(t, "text", cfg.LogFormat)
	assert.Equal(t, 30*time.Second, cfg.WriteTimeout)
	assert.Equal(t, 1024, cfg.WSReplayPendingCap)
}

func TestApplyEnv(t *testing.T) {
	t.Setenv("AEGIS_PORT", "9090")
	t.Setenv("AEGIS_UPSTREAMS", "https://rpc1.example.com, https://rpc2.example.com")
	t.Setenv("AEGIS_MUTABLE_TTL", "30s")
	t.Setenv("AEGIS_MAX_CACHE_ENTRIES", "5000")
	t.Setenv("AEGIS_FINALITY_DEPTH", "24")
	t.Setenv("AEGIS_HEALTH_INTERVAL", "10s")
	t.Setenv("AEGIS_PROBE_TIMEOUT", "3s")
	t.Setenv("AEGIS_LAG_THRESHOLD", "5")

	cfg := Default()
	ApplyEnv(&cfg)

	assert.Equal(t, 9090, cfg.Port)
	assert.Equal(t, []string{"https://rpc1.example.com", "https://rpc2.example.com"}, cfg.Upstreams)
	assert.Equal(t, 30*time.Second, cfg.MutableTTL)
	assert.Equal(t, 5000, cfg.MaxCacheEntries)
	assert.Equal(t, uint64(24), cfg.FinalityDepth)
	assert.Equal(t, 10*time.Second, cfg.HealthInterval)
	assert.Equal(t, 3*time.Second, cfg.ProbeTimeout)
	assert.Equal(t, uint64(5), cfg.LagThreshold)
}

func TestApplyEnv_InvalidValuesIgnored(t *testing.T) {
	t.Setenv("AEGIS_PORT", "not-a-number")
	t.Setenv("AEGIS_MUTABLE_TTL", "bad-duration")

	cfg := Default()
	ApplyEnv(&cfg)

	assert.Equal(t, 8080, cfg.Port, "invalid AEGIS_PORT should be ignored")
	assert.Equal(t, 12*time.Second, cfg.MutableTTL, "invalid AEGIS_MUTABLE_TTL should be ignored")
}

func TestLoadFile(t *testing.T) {
	yaml := `
port: 7777
upstreams:
  - https://rpc.example.com
mutable_ttl: 20s
max_cache_entries: 2000
finality_depth: 6
health_interval: 8s
probe_timeout: 2s
lag_threshold: 3
`
	path := writeTemp(t, yaml)
	cfg := Default()
	require.NoError(t, LoadFile(path, &cfg))

	assert.Equal(t, 7777, cfg.Port)
	assert.Equal(t, []string{"https://rpc.example.com"}, cfg.Upstreams)
	assert.Equal(t, 20*time.Second, cfg.MutableTTL)
	assert.Equal(t, 2000, cfg.MaxCacheEntries)
	assert.Equal(t, uint64(6), cfg.FinalityDepth)
	assert.Equal(t, 8*time.Second, cfg.HealthInterval)
	assert.Equal(t, 2*time.Second, cfg.ProbeTimeout)
	assert.Equal(t, uint64(3), cfg.LagThreshold)
}

func TestLoadFile_PartialOverride(t *testing.T) {
	yaml := `
port: 9999
`
	path := writeTemp(t, yaml)
	cfg := Default()
	require.NoError(t, LoadFile(path, &cfg))

	assert.Equal(t, 9999, cfg.Port)
	assert.Equal(t, 12*time.Second, cfg.MutableTTL, "unset field should keep default")
}

func TestLoadFile_NotFound(t *testing.T) {
	cfg := Default()
	err := LoadFile("/nonexistent/path.yaml", &cfg)
	assert.Error(t, err)
}

func TestLoadFile_InvalidDuration(t *testing.T) {
	yaml := `mutable_ttl: "not-a-duration"`
	path := writeTemp(t, yaml)
	cfg := Default()
	assert.Error(t, LoadFile(path, &cfg))
}

// TestLoadFile_ZeroValueFields verifies that explicitly setting an integer
// field to 0 in the YAML file is not silently skipped (the pointer-based
// yamlConfig ensures zero ≠ absent).
func TestLoadFile_ZeroValueFields(t *testing.T) {
	yaml := `
max_cache_entries: 0
lag_threshold: 0
`
	path := writeTemp(t, yaml)
	cfg := Default()
	require.NoError(t, LoadFile(path, &cfg))

	assert.Equal(t, 0, cfg.MaxCacheEntries, "max_cache_entries: 0 must override the default 10000")
	assert.Equal(t, uint64(0), cfg.LagThreshold, "lag_threshold: 0 must override the default 10")
}

// ─── Validate ───────────────────────────────────────────────────────────────

func TestValidate_ValidConfig(t *testing.T) {
	cfg := Default()
	assert.NoError(t, cfg.Validate())
}

func TestValidate_EmptyUpstreams(t *testing.T) {
	cfg := Default()
	cfg.Upstreams = nil
	assert.ErrorContains(t, cfg.Validate(), "upstream")
}

func TestValidate_InvalidPort(t *testing.T) {
	cfg := Default()
	cfg.Port = 0
	assert.ErrorContains(t, cfg.Validate(), "port")

	cfg.Port = 65536
	assert.ErrorContains(t, cfg.Validate(), "port")
}

func TestValidate_ZeroMutableTTL(t *testing.T) {
	cfg := Default()
	cfg.MutableTTL = 0
	assert.ErrorContains(t, cfg.Validate(), "mutable_ttl")
}

func TestValidate_ProbeTimeoutGEHealthInterval(t *testing.T) {
	cfg := Default()
	cfg.ProbeTimeout = cfg.HealthInterval // equal → invalid
	assert.ErrorContains(t, cfg.Validate(), "probe_timeout")

	cfg.ProbeTimeout = cfg.HealthInterval + time.Second // greater → also invalid
	assert.ErrorContains(t, cfg.Validate(), "probe_timeout")
}

func TestValidate_WSReplayPendingCap(t *testing.T) {
	cfg := Default()
	cfg.WSReplayPendingCap = 0
	assert.ErrorContains(t, cfg.Validate(), "ws_replay_pending_cap")

	cfg.WSReplayPendingCap = -1
	assert.ErrorContains(t, cfg.Validate(), "ws_replay_pending_cap")

	cfg.WSReplayPendingCap = 1
	assert.NoError(t, cfg.Validate(), "any positive value is accepted")
}

func TestValidate_LogLevel(t *testing.T) {
	cfg := Default()
	for _, level := range []string{"debug", "info", "warn", "error"} {
		cfg.LogLevel = level
		assert.NoError(t, cfg.Validate(), "level %q should be accepted", level)
	}
	cfg.LogLevel = "verbose"
	assert.ErrorContains(t, cfg.Validate(), "log_level")
}

func TestValidate_WriteTimeout(t *testing.T) {
	cfg := Default()
	cfg.WriteTimeout = 0
	assert.ErrorContains(t, cfg.Validate(), "write_timeout")

	cfg.WriteTimeout = -1 * time.Second
	assert.ErrorContains(t, cfg.Validate(), "write_timeout")

	cfg.WriteTimeout = 1 * time.Millisecond
	assert.NoError(t, cfg.Validate(), "any positive duration is accepted")
}

func TestValidate_LogFormat(t *testing.T) {
	cfg := Default()
	for _, format := range []string{"text", "json"} {
		cfg.LogFormat = format
		assert.NoError(t, cfg.Validate(), "format %q should be accepted", format)
	}
	cfg.LogFormat = "yaml"
	assert.ErrorContains(t, cfg.Validate(), "log_format")
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "aegis-config-*.yaml")
	require.NoError(t, err)
	_, err = f.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return filepath.Clean(f.Name())
}
