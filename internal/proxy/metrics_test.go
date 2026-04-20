package proxy

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ChickenBenny/AegisRPC/internal/cache"
	"github.com/ChickenBenny/AegisRPC/internal/capability"
	"github.com/ChickenBenny/AegisRPC/internal/metrics"
	"github.com/ChickenBenny/AegisRPC/internal/router"
	"github.com/ChickenBenny/AegisRPC/internal/upstream"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newHandlerForMetrics builds a fresh Handler with its own cache/finality instances.
// Using a dedicated constructor avoids sharing cache state with other tests.
func newHandlerForMetrics(t *testing.T, upstreamURL string) *Handler {
	t.Helper()
	pool, err := upstream.NewPool([]string{upstreamURL})
	require.NoError(t, err)
	pool.Nodes()[0].SetCapabilities(capability.CapBasic)
	fc := cache.NewFinalityChecker(12)
	rtr := router.New(pool)
	return NewHandler(rtr, cache.NewCache(context.Background(), time.Minute), 5*time.Second, fc)
}

// rpcPost builds a JSON-RPC POST request with the given method and params.
func rpcPost(method, params string) *http.Request {
	body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + params + `}`
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// ---------------------------------------------------------------------------
// TestMetrics_CacheHit verifies that a positive cache hit increments the
// "hit" label on CacheRequests and records the "cache_hit" status in
// RequestsTotal.
//
// Note: Prometheus counters are process-global and accumulate across tests.
// We capture values BEFORE the test and compare the DELTA to avoid
// test-ordering problems.
// ---------------------------------------------------------------------------
func TestMetrics_CacheHit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0xdeadbeef"}`))
	}))
	defer srv.Close()

	h := newHandlerForMetrics(t, srv.URL)

	// ─── warm the cache with a first request ────────────────────────────────
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, rpcPost("eth_getTransactionByHash", `["0xabc"]`))
	require.Equal(t, http.StatusOK, rec.Code)

	// Capture baseline values after warm-up.
	// (The first call was a cache miss; we want to measure the second call.)
	beforeHit := testutil.ToFloat64(metrics.CacheRequests.WithLabelValues("hit"))
	beforeTotal := testutil.ToFloat64(metrics.RequestsTotal.WithLabelValues("eth_getTransactionByHash", "cache_hit"))

	// ─── second request: should be a cache hit ───────────────────────────────
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, rpcPost("eth_getTransactionByHash", `["0xabc"]`))
	require.Equal(t, http.StatusOK, rec2.Code)

	assert.Equal(t, 1.0, testutil.ToFloat64(metrics.CacheRequests.WithLabelValues("hit"))-beforeHit,
		"CacheRequests{result=hit} should increment by 1 on a cache hit")
	assert.Equal(t, 1.0, testutil.ToFloat64(metrics.RequestsTotal.WithLabelValues("eth_getTransactionByHash", "cache_hit"))-beforeTotal,
		"RequestsTotal{status=cache_hit} should increment by 1")
}

// ---------------------------------------------------------------------------
// TestMetrics_CacheMiss verifies that a cache miss increments the "miss"
// label and records "ok" status in RequestsTotal.
// ---------------------------------------------------------------------------
func TestMetrics_CacheMiss(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1234"}`))
	}))
	defer srv.Close()

	h := newHandlerForMetrics(t, srv.URL)

	beforeMiss := testutil.ToFloat64(metrics.CacheRequests.WithLabelValues("miss"))
	beforeOK := testutil.ToFloat64(metrics.RequestsTotal.WithLabelValues("eth_getTransactionByHash", "ok"))

	rec := httptest.NewRecorder()
	// Use a unique param to guarantee this is a cache miss.
	h.ServeHTTP(rec, rpcPost("eth_getTransactionByHash", `["0xunique_for_miss_test"]`))
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Equal(t, 1.0, testutil.ToFloat64(metrics.CacheRequests.WithLabelValues("miss"))-beforeMiss,
		"CacheRequests{result=miss} should increment by 1 on a cache miss")
	assert.Equal(t, 1.0, testutil.ToFloat64(metrics.RequestsTotal.WithLabelValues("eth_getTransactionByHash", "ok"))-beforeOK,
		"RequestsTotal{status=ok} should increment by 1 on a successful upstream call")
}

// ---------------------------------------------------------------------------
// TestMetrics_RequestDuration verifies that the latency histogram is
// observed for every request.
//
// testutil.ToFloat64 panics on Histograms (non-gauge/counter); we use
// prometheus.DefaultGatherer directly to read the histogram's sample_count.
// ---------------------------------------------------------------------------
func TestMetrics_RequestDuration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
	}))
	defer srv.Close()

	h := newHandlerForMetrics(t, srv.URL)

	// Helper: gather the current sample_count for
	// aegis_rpc_request_duration_seconds{method="eth_blockNumber"}.
	observationCount := func() uint64 {
		families, err := prometheus.DefaultGatherer.Gather()
		require.NoError(t, err)
		for _, mf := range families {
			if mf.GetName() != "aegis_rpc_request_duration_seconds" {
				continue
			}
			for _, m := range mf.GetMetric() {
				for _, lp := range m.GetLabel() {
					if lp.GetName() == "method" && lp.GetValue() == "eth_blockNumber" {
						return m.GetHistogram().GetSampleCount()
					}
				}
			}
		}
		return 0
	}

	before := observationCount()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, rpcPost("eth_blockNumber", `[]`))
	require.Equal(t, http.StatusOK, rec.Code)

	after := observationCount()
	assert.Equal(t, uint64(1), after-before,
		"RequestDuration should record exactly one observation per request")
}
