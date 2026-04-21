// Package metrics defines the Prometheus metric descriptors used across AegisRPC.
//
// Usage pattern: import this package for side effects (the init() registers
// all metrics with the default Prometheus registry), then call the exported
// vars directly where you want to record observations.
//
//	import _ "github.com/ChickenBenny/AegisRPC/internal/metrics"  // registration only
//	import   "github.com/ChickenBenny/AegisRPC/internal/metrics"  // + record data
package metrics

import "github.com/prometheus/client_golang/prometheus"

// ---------------------------------------------------------------------------
// RPC traffic
// ---------------------------------------------------------------------------

// RequestsTotal counts every completed HTTP RPC request.
//
// Labels:
//   - method : JSON-RPC method name (e.g. "eth_call"), or "unknown" if the
//     request could not be parsed.
//   - status : outcome of the request.
//     Possible values:
//     "cache_hit"    – served from the positive cache (no upstream call)
//     "neg_hit"      – rejected immediately by the negative cache
//     "uncacheable"  – forwarded directly, bypassing cache
//     "ok"           – singleflight fetch succeeded
//     "rate_limited" – upstream returned 429
//     "error"        – any other upstream / internal error
var RequestsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "aegis_rpc_requests_total",
		Help: "Total number of RPC requests partitioned by JSON-RPC method and result status.",
	},
	[]string{"method", "status"},
)

// RequestDuration is a Histogram of end-to-end request latency in seconds,
// partitioned by JSON-RPC method.
//
// Buckets are the Prometheus defaults:
// 5ms, 10ms, 25ms, 50ms, 100ms, 250ms, 500ms, 1s, 2.5s, 5s, 10s
// which cover the typical range of RPC calls from fast local nodes to slow
// archive queries.
var RequestDuration = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "aegis_rpc_request_duration_seconds",
		Help:    "End-to-end request latency in seconds, partitioned by JSON-RPC method.",
		Buckets: prometheus.DefBuckets,
	},
	[]string{"method"},
)

// ---------------------------------------------------------------------------
// Cache
// ---------------------------------------------------------------------------

// CacheRequests counts every cache lookup by its outcome.
//
// Labels:
//   - result : "hit"     – key found in positive cache
//     "miss"    – key not in cache; upstream was called
//     "neg_hit" – key found in negative cache (recent upstream error)
var CacheRequests = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "aegis_cache_requests_total",
		Help: "Total number of cache lookups partitioned by result (hit, miss, neg_hit).",
	},
	[]string{"result"},
)

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

func init() {
	prometheus.MustRegister(
		RequestsTotal,
		RequestDuration,
		CacheRequests,
	)
}
