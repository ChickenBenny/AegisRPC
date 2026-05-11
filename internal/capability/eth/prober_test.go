package eth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

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
