package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChickenBenny/AegisRPC/internal/cache"
	"github.com/ChickenBenny/AegisRPC/internal/capability"
	"github.com/ChickenBenny/AegisRPC/internal/router"
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
	rtr := router.New(pool)
	return NewHandler(rtr, cache.NewCache(context.Background(), time.Minute), mutableTTL, fc)
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

// rpcRequestWithID is like rpcRequest but lets the caller specify the raw
// JSON form of the id field. Used by id-preservation tests where the exact
// shape of "id" matters (number vs string vs decimal vs big integer).
func rpcRequestWithID(method, params, idJSON string) *http.Request {
	body := `{"jsonrpc":"2.0","id":` + idJSON + `,"method":"` + method + `","params":` + params + `}`
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// ---------------------------------------------------------------------------
// audit #3 — JSON-RPC batch requests are detected and forwarded as-is.
// ---------------------------------------------------------------------------

func TestHandler_Batch_ForwardedToUpstream(t *testing.T) {
	var receivedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(`[{"jsonrpc":"2.0","id":1,"result":"0x1"},{"jsonrpc":"2.0","id":2,"result":"0x2"}]`))
	}))
	defer srv.Close()

	h := newHandler(t, srv.URL, 5*time.Second)

	batchBody := `[{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]},{"jsonrpc":"2.0","id":2,"method":"eth_chainId","params":[]}]`
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(batchBody))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "batch request must not be rejected with 400")
	assert.Equal(t, batchBody, string(receivedBody), "batch body must reach upstream byte-exact")
	assert.Contains(t, rec.Body.String(), `"id":1`)
	assert.Contains(t, rec.Body.String(), `"id":2`)
}

func TestHandler_Batch_IsUncached(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Write([]byte(`[{"jsonrpc":"2.0","id":1,"result":"0x1"}]`))
	}))
	defer srv.Close()

	h := newHandler(t, srv.URL, 5*time.Second)
	batchBody := `[{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}]`

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(batchBody))
		req.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}

	assert.Equal(t, int32(3), callCount.Load(),
		"batch path is intentionally uncached (audit #30) — every call must hit upstream")
}

func TestIsBatch(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"plain object", `{"id":1}`, false},
		{"plain array", `[{"id":1}]`, true},
		{"leading spaces before array", `   [{"id":1}]`, true},
		{"leading newlines before array", "\n\n[{}]", true},
		{"leading whitespace mix before object", "  \t\n{}", false},
		{"empty body", "", false},
		{"only whitespace", "   ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isBatch([]byte(tc.body)))
		})
	}
}

// ---------------------------------------------------------------------------
// audit #1 — RPC-level error responses must not be cached.
// ---------------------------------------------------------------------------

func TestHandler_RPCError_NotCached(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		// HTTP 200 + RPC error envelope. Pre-fix this got permanently
		// cached at LayerImmutable, poisoning every future caller.
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"execution reverted"}}`))
	}))
	defer srv.Close()

	h := newHandler(t, srv.URL, 5*time.Second)

	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, rpcRequest("eth_getTransactionByHash", `["0xabc"]`))
		require.Equal(t, http.StatusOK, rec.Code, "iteration %d", i)
		assert.Contains(t, rec.Body.String(), "execution reverted", "iteration %d: error must be forwarded", i)
	}

	assert.Equal(t, int32(3), callCount.Load(),
		"RPC error responses must not be cached; each call should hit upstream")
}

// TestHandler_MalformedUpstream_NotCached: a spec-violating upstream
// returning valid JSON without result or error must not poison the
// cache. Each call must hit upstream (PR #20 review).
func TestHandler_MalformedUpstream_NotCached(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		// Valid envelope shape but neither result nor error (spec violation).
		w.Write([]byte(`{"jsonrpc":"2.0","id":1}`))
	}))
	defer srv.Close()

	h := newHandler(t, srv.URL, 5*time.Second)

	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, rpcRequest("eth_getTransactionByHash", `["0xabc"]`))
		require.Equal(t, http.StatusOK, rec.Code, "iteration %d", i)
	}

	assert.Equal(t, int32(3), callCount.Load(),
		"malformed upstream responses must not be cached as nil result")
}

// TestHandler_RPCError_DataPreserved: error.data must round-trip
// byte-exact (PR #20 review). Pre-fix, interface{} rounded
// 9007199254740993 to ...992 via float64.
func TestHandler_RPCError_DataPreserved(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"x","data":9007199254740993}}`))
	}))
	defer srv.Close()

	h := newHandler(t, srv.URL, 5*time.Second)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, rpcRequest("eth_call", `[]`))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "9007199254740993",
		"error.data must be preserved byte-exact (no float64 rounding)")
	assert.NotContains(t, rec.Body.String(), "9007199254740992",
		"the float64-rounded form must not appear")
}

// ---------------------------------------------------------------------------
// audit #2 — request id must round-trip byte-exact, even across cache hits
// and across concurrent coalesced callers.
// ---------------------------------------------------------------------------

func TestHandler_CacheHit_PreservesCallerID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","id":999,"result":"0xdeadbeef"}`))
	}))
	defer srv.Close()

	h := newHandler(t, srv.URL, 5*time.Second)

	// Prime the cache with a request whose id is 1.
	h.ServeHTTP(httptest.NewRecorder(), rpcRequestWithID("eth_getTransactionByHash", `["0xabc"]`, "1"))

	// Second caller uses a totally different id; the cached result must be
	// returned but with this id, not the original "1" or upstream's "999".
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, rpcRequestWithID("eth_getTransactionByHash", `["0xabc"]`, "42"))
	assert.Contains(t, rec.Body.String(), `"id":42`)
	assert.NotContains(t, rec.Body.String(), `"id":1`)
	assert.NotContains(t, rec.Body.String(), `"id":999`)
}

func TestHandler_IDPreservation_AcrossTypes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
	}))
	defer srv.Close()

	cases := []struct {
		name string
		id   string
	}{
		{"integer", "42"},
		{"string", `"abc-def"`},
		{"big-integer-beyond-float64", "9007199254740993"},
		{"decimal", "1.5"},
		{"null", "null"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHandler(t, srv.URL, 5*time.Second)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, rpcRequestWithID("eth_blockNumber", `[]`, tc.id))
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Contains(t, rec.Body.String(), `"id":`+tc.id,
				"id %q must be byte-exact preserved (no float64 round-trip, no quote drift)", tc.id)
		})
	}
}

func TestHandler_ConcurrentCoalescedCallers_GetOwnIDs(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		time.Sleep(50 * time.Millisecond) // hold long enough for concurrent coalescing
		w.Write([]byte(`{"jsonrpc":"2.0","id":777,"result":"0x100"}`))
	}))
	defer srv.Close()

	h := newHandler(t, srv.URL, 5*time.Second)

	const N = 5
	results := make([]*httptest.ResponseRecorder, N)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			id := []byte{'1', '0', '0', '0' + byte(i)} // ids 1000..1004
			req := rpcRequestWithID("eth_blockNumber", `[]`, string(id))
			h.ServeHTTP(rec, req)
			results[i] = rec
		}()
	}
	wg.Wait()

	require.Equal(t, int32(1), callCount.Load(), "callers must be coalesced into one upstream call")
	for i, rec := range results {
		expectedID := `"id":100` + string('0'+byte(i))
		assert.Contains(t, rec.Body.String(), expectedID,
			"caller %d expected its own id %s, got body %q", i, expectedID, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// audit #16 — negative cache stores a sentinel, never the raw error string.
// ---------------------------------------------------------------------------

func TestHandler_NegativeCache_UsesSentinel(t *testing.T) {
	// Point at an unreachable port so fetchFromUpstream errors out.
	h := newHandler(t, "http://127.0.0.1:1", 5*time.Second)

	// Trigger one upstream attempt — it will fail and write the negative
	// cache entry.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, rpcRequest("eth_getTransactionByHash", `["0xabc"]`))
	require.Equal(t, http.StatusBadGateway, rec.Code)

	// Pre-fix this would contain the raw error string ("dial tcp ...
	// connection refused"); the sentinel is opaque.
	key := "neg::" + cache.CacheKey("eth_getTransactionByHash",
		rpcParams(`["0xabc"]`))
	val, ok := h.cache.Get(key)
	require.True(t, ok, "negative cache entry must exist after upstream failure")
	// Compare against a literal, not negSentinel — that would silently
	// pass if a bug mutated the underlying slice.
	assert.Equal(t, []byte{0x01}, val,
		"negative cache must store the opaque sentinel, not the upstream error message")
}

func rpcParams(s string) []byte {
	return []byte(s)
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
