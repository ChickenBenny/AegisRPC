package eth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ChickenBenny/AegisRPC/internal/capability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEthProber_BasicOnly(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch callCount {
		case 1: // historical → missing trie node (full/pruned node)
			w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32000,"message":"missing trie node"},"id":1}`))
		default: // debug/trace → method not found (-32601)
			w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32601,"message":"method not found"},"id":1}`))
		}
	}))
	defer server.Close()

	prober := NewEthProber()
	caps, err := prober.Probe(context.Background(), server.URL)

	require.NoError(t, err)
	assert.True(t, caps.Has(capability.CapBasic))
	assert.False(t, caps.Has(capability.CapHistorical))
	assert.False(t, caps.Has(capability.CapDebug))
	assert.False(t, caps.Has(capability.CapTrace))
}

// One namespace returning an ambiguous error (rate limit, auth, transient 5xx)
// must NOT poison the result of the other namespaces — audit #10.
func TestEthProber_AmbiguousDebugStillReportsHistorical(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch callCount {
		case 1: // historical → archive
			w.Write([]byte(`{"jsonrpc":"2.0","result":"0x1","id":1}`))
		case 2: // debug → rate limited (NOT -32601)
			w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32005,"message":"rate limit exceeded"},"id":1}`))
		default: // trace → method not found
			w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32601,"message":"method not found"},"id":1}`))
		}
	}))
	defer server.Close()

	prober := NewEthProber()
	caps, err := prober.Probe(context.Background(), server.URL)

	require.NoError(t, err)
	assert.True(t, caps.Has(capability.CapBasic))
	assert.True(t, caps.Has(capability.CapHistorical), "historical confirmed before debug failed")
	assert.False(t, caps.Has(capability.CapDebug), "debug ambiguous → not added")
	assert.False(t, caps.Has(capability.CapTrace))
}

// Every namespace returns ambiguous → caps fall back to CapBasic, not zero.
func TestEthProber_AllAmbiguous(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32000,"message":"some generic error"},"id":1}`))
	}))
	defer server.Close()

	prober := NewEthProber()
	caps, err := prober.Probe(context.Background(), server.URL)

	require.NoError(t, err)
	assert.True(t, caps.Has(capability.CapBasic), "basic is unconditional")
	assert.False(t, caps.Has(capability.CapHistorical))
	assert.False(t, caps.Has(capability.CapDebug))
	assert.False(t, caps.Has(capability.CapTrace))
}

func TestEthProber_ArchiveNode(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch callCount {
		case 1: // historical → success
			w.Write([]byte(`{"jsonrpc":"2.0","result":"0x1","id":1}`))
		default: // debug/trace → method not found
			w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32601,"message":"method not found"},"id":1}`))
		}
	}))
	defer server.Close()

	prober := NewEthProber()
	caps, err := prober.Probe(context.Background(), server.URL)

	require.NoError(t, err)
	assert.True(t, caps.Has(capability.CapBasic))
	assert.True(t, caps.Has(capability.CapHistorical))
	assert.False(t, caps.Has(capability.CapDebug))
}

// -32602 (invalid params) means the method exists and dispatched, just hated
// the empty params — confirms namespace is enabled. Audit #11.
func TestEthProber_InvalidParamsMeansNamespaceEnabled(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch callCount {
		case 1: // historical → archive
			w.Write([]byte(`{"jsonrpc":"2.0","result":"0x1","id":1}`))
		case 2: // debug → invalid params (method exists)
			w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32602,"message":"missing value for required argument 0"},"id":1}`))
		default: // trace → invalid params (method exists)
			w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32602,"message":"missing required argument 0"},"id":1}`))
		}
	}))
	defer server.Close()

	prober := NewEthProber()
	caps, err := prober.Probe(context.Background(), server.URL)

	require.NoError(t, err)
	assert.True(t, caps.Has(capability.CapBasic))
	assert.True(t, caps.Has(capability.CapHistorical))
	assert.True(t, caps.Has(capability.CapDebug), "-32602 should be read as namespace enabled")
	assert.True(t, caps.Has(capability.CapTrace), "-32602 should be read as namespace enabled")
}

// Caller's ctx deadline must bound the probe — audit #27. Pre-fix, an
// http.Client.Timeout of 10s would silently cap a longer caller context.
// Inverted here: a 100ms caller ctx must complete in <1s regardless of
// any internal client default.
func TestEthProber_RespectsContextDeadline(t *testing.T) {
	// Handler sleeps a bounded amount then 500s. With the prober's ctx set
	// to 100ms, every probe must give up well before the handler returns.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(800 * time.Millisecond):
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	prober := NewEthProber()
	start := time.Now()
	caps, err := prober.Probe(ctx, server.URL)
	elapsed := time.Since(start)

	require.NoError(t, err, "Probe never returns error for transport failures (degrades to probeUnknown)")
	assert.True(t, caps.Has(capability.CapBasic), "basic is unconditional")
	assert.False(t, caps.Has(capability.CapHistorical))
	// Pre-#27 the http.Client.Timeout would have been 10s — this assertion
	// would have failed had the client.Timeout still capped the deadline.
	assert.Less(t, elapsed, 1*time.Second, "ctx deadline must bound probe duration")
}

func TestEthProber_FullDebugNode(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch callCount {
		case 1: // historical → missing trie node (full node)
			w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32000,"message":"missing trie node"},"id":1}`))
		case 2: // debug → success
			w.Write([]byte(`{"jsonrpc":"2.0","result":{},"id":1}`))
		default: // trace → method not found
			w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32601,"message":"method not found"},"id":1}`))
		}
	}))
	defer server.Close()

	prober := NewEthProber()
	caps, err := prober.Probe(context.Background(), server.URL)

	require.NoError(t, err)
	assert.True(t, caps.Has(capability.CapBasic))
	assert.False(t, caps.Has(capability.CapHistorical))
	assert.True(t, caps.Has(capability.CapDebug))
	assert.False(t, caps.Has(capability.CapTrace))
}
