package proxy

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChickenBenny/AegisRPC/internal/cache"
	"github.com/ChickenBenny/AegisRPC/internal/capability"
	"github.com/ChickenBenny/AegisRPC/internal/upstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newHandler builds a Handler pointed at a single upstream URL.
// mutableTTL controls how long mutable results are cached.
func newHandler(t *testing.T, upstreamURL string, mutableTTL time.Duration) *Handler {
	t.Helper()
	pool, err := upstream.NewPool([]string{upstreamURL})
	require.NoError(t, err)
	pool.Nodes()[0].SetCapabilities(capability.CapBasic)
	fc := cache.NewFinalityChecker(12)
	return NewHandler(pool, cache.NewCache(context.Background(), time.Minute), mutableTTL, fc)
}

// rpcRequest builds a raw JSON-RPC POST request.
func rpcRequest(method, params string) *http.Request {
	body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + params + `}`
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// ---------------------------------------------------------------------------
// Uncacheable — every call must reach the upstream
// ---------------------------------------------------------------------------

func TestHandler_Uncacheable_AlwaysHitsUpstream(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
	}))
	defer srv.Close()

	h := newHandler(t, srv.URL, 5*time.Second)

	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, rpcRequest("eth_sendRawTransaction", `["0xdeadbeef"]`))
		require.Equal(t, http.StatusOK, rec.Code)
	}

	assert.Equal(t, int32(3), callCount.Load(), "uncacheable: every request must hit upstream")
}

// ---------------------------------------------------------------------------
// Immutable — upstream called once, subsequent calls served from cache
// ---------------------------------------------------------------------------

func TestHandler_Immutable_SecondCallHitsCache(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"hash":"0xabc"}}`))
	}))
	defer srv.Close()

	h := newHandler(t, srv.URL, 5*time.Second)

	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, rpcRequest("eth_getTransactionByHash", `["0xabc123"]`))
	require.Equal(t, http.StatusOK, rec1.Code)

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, rpcRequest("eth_getTransactionByHash", `["0xabc123"]`))
	require.Equal(t, http.StatusOK, rec2.Code)

	assert.Equal(t, int32(1), callCount.Load(), "immutable: upstream should only be called once")
	assert.Equal(t, rec1.Body.Bytes(), rec2.Body.Bytes(), "cached response must match original")
}

func TestHandler_Immutable_DifferentParams_BothHitUpstream(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"ok"}`))
	}))
	defer srv.Close()

	h := newHandler(t, srv.URL, 5*time.Second)

	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, rpcRequest("eth_getTransactionByHash", `["0xAAA"]`))

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, rpcRequest("eth_getTransactionByHash", `["0xBBB"]`))

	assert.Equal(t, int32(2), callCount.Load(), "different params = different cache keys")
}

// ---------------------------------------------------------------------------
// Mutable — SingleFlight coalesces concurrent requests; TTL controls expiry
// ---------------------------------------------------------------------------

func TestHandler_Mutable_ConcurrentRequests_Coalesced(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		time.Sleep(20 * time.Millisecond) // simulate network latency
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x100"}`))
	}))
	defer srv.Close()

	h := newHandler(t, srv.URL, 5*time.Second)

	const N = 10
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, rpcRequest("eth_blockNumber", `[]`))
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), callCount.Load(), "mutable: concurrent requests must be coalesced")
}

func TestHandler_Mutable_CacheExpires_HitsUpstreamAgain(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x100"}`))
	}))
	defer srv.Close()

	h := newHandler(t, srv.URL, 50*time.Millisecond) // very short TTL for test

	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, rpcRequest("eth_blockNumber", `[]`))
	assert.Equal(t, int32(1), callCount.Load())

	time.Sleep(80 * time.Millisecond) // wait for TTL to expire

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, rpcRequest("eth_blockNumber", `[]`))
	assert.Equal(t, int32(2), callCount.Load(), "mutable: must hit upstream again after TTL expires")
}
