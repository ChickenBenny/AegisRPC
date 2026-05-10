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
// Method label normalisation
// ---------------------------------------------------------------------------

// knownMethods is the allowlist NormalizeMethod uses to bound the
// `method` Prometheus label cardinality — without it, client-supplied
// strings could mint unlimited series.
//
// TODO(phase 7): when multi-chain support lands, this should become
// knownMethodsByChain and the metric labels should grow a "chain"
// dimension. Today's list covers ETH + EVM-compatible L2.
var knownMethods = map[string]struct{}{
	// eth_* — bulk of dApp / wallet traffic
	"eth_blockNumber":                   {},
	"eth_chainId":                       {},
	"eth_call":                          {},
	"eth_createAccessList":              {},
	"eth_estimateGas":                   {},
	"eth_feeHistory":                    {},
	"eth_gasPrice":                      {},
	"eth_getBalance":                    {},
	"eth_getBlockByHash":                {},
	"eth_getBlockByNumber":              {},
	"eth_getBlockTransactionCountByHash":   {},
	"eth_getBlockTransactionCountByNumber": {},
	"eth_getCode":                       {},
	"eth_getLogs":                       {},
	"eth_getProof":                      {},
	"eth_getStorageAt":                  {},
	"eth_getTransactionByHash":          {},
	"eth_getTransactionCount":           {},
	"eth_getTransactionReceipt":         {},
	"eth_getUncleByBlockHashAndIndex":   {},
	"eth_getUncleByBlockNumberAndIndex": {},
	"eth_maxPriorityFeePerGas":          {},
	"eth_sendRawTransaction":            {},
	"eth_subscribe":                     {},
	"eth_unsubscribe":                   {},
	"eth_syncing":                       {},

	// eth_* filter API — used by polling clients (ethers.js, web3.py)
	"eth_newFilter":                  {},
	"eth_newBlockFilter":             {},
	"eth_newPendingTransactionFilter": {},
	"eth_getFilterChanges":           {},
	"eth_getFilterLogs":              {},
	"eth_uninstallFilter":            {},

	// net_* / web3_* — node introspection
	"net_version":        {},
	"net_listening":      {},
	"net_peerCount":      {},
	"web3_clientVersion": {},
	"web3_sha3":          {},

	// debug_* / trace_* — archive-node diagnostics
	"debug_traceTransaction":      {},
	"debug_traceCall":             {},
	"debug_traceBlock":            {},
	"debug_traceBlockByHash":      {},
	"debug_traceBlockByNumber":    {},
	"trace_block":                 {},
	"trace_transaction":           {},
	"trace_call":                  {},
	"trace_callMany":              {},
	"trace_filter":                {},
	"trace_get":                   {},

	// "unknown" is the handler's parse-failure sentinel — kept separate
	// from "other" so dashboards distinguish bad-JSON from bad-method.
	"unknown": {},

	// "batch" — JSON-RPC batch requests (uncached proxyDirect today).
	// Per-item-aware metrics arrive with per-item caching (audit #30).
	"batch": {},
}

// NormalizeMethod returns method if it appears in the known-methods
// allowlist, otherwise "other". Callers should pass the result — not the
// raw method string — to WithLabelValues so cardinality stays bounded.
func NormalizeMethod(method string) string {
	if _, ok := knownMethods[method]; ok {
		return method
	}
	return "other"
}

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
