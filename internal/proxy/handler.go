package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"time"

	"github.com/ChickenBenny/AegisRPC/internal/cache"
	"github.com/ChickenBenny/AegisRPC/internal/metrics"
	"github.com/ChickenBenny/AegisRPC/internal/models"
	"github.com/ChickenBenny/AegisRPC/internal/router"
	"golang.org/x/sync/singleflight"
)

// rateLimitedError is returned by fetchFromUpstream when the upstream replies 429.
// It carries the upstream's Retry-After header value (empty string if absent) so the
// handler can forward it to the caller. It is never written to the negative cache.
type rateLimitedError struct{ retryAfter string }

func (e *rateLimitedError) Error() string { return "upstream rate limited" }

// negSentinel is stored under "neg::"+key on upstream failure. Only key
// existence is checked, so the byte content is opaque on purpose — no
// leak if a future refactor ever writes cached values back to clients.
var negSentinel = []byte{0x01}

// sfRet is the singleflight callback's return shape. Exactly one field is
// populated:
//   - result:   success — cached, then each caller rebuilds envelope with own id
//   - rpcError: RPC-level error — forwarded with caller's id, never cached
//   - rawBody:  non-JSON upstream — forwarded as-is (no id to splice)
type sfRet struct {
	result   json.RawMessage
	rpcError *models.RPCError
	rawBody  []byte
}

// Handler is an http.Handler that proxies JSON-RPC requests to upstream nodes
// with integrated caching, singleflight coalescing, and finality-aware classification.
type Handler struct {
	router     *router.Router
	cache      cache.Store
	finality   *cache.FinalityChecker
	sf         singleflight.Group
	mutableTTL time.Duration
}

func NewHandler(rtr *router.Router, c cache.Store, mutableTTL time.Duration, fc *cache.FinalityChecker) *Handler {
	return &Handler{
		router:     rtr,
		cache:      c,
		finality:   fc,
		mutableTTL: mutableTTL,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := "unknown"
	status := "ok"
	start := time.Now()
	defer func() {
		// Normalise method against the allowlist so client-supplied
		// method strings cannot blow up Prometheus label cardinality.
		// `method` itself stays raw — it is also fed to the router for
		// capability matching, which needs the real name.
		labelMethod := metrics.NormalizeMethod(method)
		metrics.RequestsTotal.WithLabelValues(labelMethod, status).Inc()
		metrics.RequestDuration.WithLabelValues(labelMethod).Observe(time.Since(start).Seconds())
	}()

	r.Body = http.MaxBytesReader(w, r.Body, 1*1024*1024)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		status = "error"
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}

	// Batch (JSON-RPC 2.0 §6) — forward as-is, uncached. Per-item caching
	// is tracked as audit #30; this minimal path unblocks SDK BatchProvider
	// users that the previous single-object Unmarshal rejected with 400.
	if isBatch(body) {
		method = "batch"
		status = "uncacheable"
		if err := h.proxyDirect(w, r, body, method); err != nil {
			status = "error"
		}
		return
	}

	var req models.RPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		status = "error"
		http.Error(w, "invalid JSON-RPC request", http.StatusBadRequest)
		return
	}
	method = req.Method

	layer := h.finality.Classify(req.Method, req.Params)

	if layer == cache.LayerUncacheable {
		status = "uncacheable"
		if err := h.proxyDirect(w, r, body, req.Method); err != nil {
			status = "error"
		}
		return
	}

	key := cache.CacheKey(req.Method, req.Params)

	if _, ok := h.cache.Get("neg::" + key); ok {
		metrics.CacheRequests.WithLabelValues("neg_hit").Inc()
		status = "neg_hit"
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}

	if cachedResult, ok := h.cache.Get(key); ok {
		metrics.CacheRequests.WithLabelValues("hit").Inc()
		status = "cache_hit"
		envelope, eerr := buildResultEnvelope(req.ID, cachedResult)
		if eerr != nil {
			// Should be unreachable — a value that survived being marshalled
			// into the cache will round-trip through json.Marshal again.
			status = "error"
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(envelope)
		return
	}

	metrics.CacheRequests.WithLabelValues("miss").Inc()
	v, err, _ := h.sf.Do(key, func() (any, error) {
		rawResp, ferr := h.fetchFromUpstream(r, body, req.Method)
		if ferr != nil {
			var rl *rateLimitedError
			if !errors.As(ferr, &rl) {
				h.cache.Set("neg::"+key, negSentinel, 1*time.Second)
			}
			return nil, ferr
		}

		// Parse so we can skip caching RPC errors (audit #1) and cache
		// only `result`, letting each caller rebuild envelope with own
		// id (audit #2).
		var parsed models.RPCResponse
		if uerr := json.Unmarshal(rawResp, &parsed); uerr != nil {
			// Non-JSON upstream — no id to splice in, forward as-is.
			return sfRet{rawBody: rawResp}, nil
		}
		if parsed.Error != nil {
			// Don't cache: transient errors must not poison the cache.
			return sfRet{rpcError: parsed.Error}, nil
		}
		if len(parsed.Result) == 0 {
			// Spec-violating upstream (no result, no error) — caching nil
			// would permanently serve partial envelopes. Forward as-is.
			return sfRet{rawBody: rawResp}, nil
		}

		ttl := time.Duration(0)
		if layer == cache.LayerMutable {
			ttl = h.mutableTTL
		}
		h.cache.Set(key, parsed.Result, ttl)
		return sfRet{result: parsed.Result}, nil
	})
	if err != nil {
		var rl *rateLimitedError
		if errors.As(err, &rl) {
			status = "rate_limited"
			if rl.retryAfter != "" {
				w.Header().Set("Retry-After", rl.retryAfter)
			}
			http.Error(w, "rate limited", http.StatusTooManyRequests)
		} else {
			status = "error"
			http.Error(w, "upstream error", http.StatusBadGateway)
		}
		return
	}

	ret := v.(sfRet)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case ret.rawBody != nil:
		w.Write(ret.rawBody)
	case ret.rpcError != nil:
		envelope, eerr := buildErrorEnvelope(req.ID, ret.rpcError)
		if eerr != nil {
			status = "error"
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Write(envelope)
	default:
		envelope, eerr := buildResultEnvelope(req.ID, ret.result)
		if eerr != nil {
			status = "error"
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Write(envelope)
	}
}

// buildResultEnvelope renders a JSON-RPC success response. id is spliced
// verbatim (json.RawMessage) so precision survives the round-trip.
func buildResultEnvelope(id, result json.RawMessage) ([]byte, error) {
	return json.Marshal(models.RPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

// buildErrorEnvelope renders a JSON-RPC error response with the caller's
// id and the upstream-reported error object.
func buildErrorEnvelope(id json.RawMessage, e *models.RPCError) ([]byte, error) {
	return json.Marshal(models.RPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   e,
	})
}

// isBatch reports whether the body starts with "[" (JSON-RPC 2.0 batch),
// skipping any leading whitespace per RFC 8259.
func isBatch(body []byte) bool {
	for _, b := range body {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '[':
			return true
		default:
			return false
		}
	}
	return false
}

// proxyDirect forwards the request to the upstream without any caching.
// It tries every capable healthy node before giving up, buffering each response so
// that headers are never written to w until a successful response is found.
// Returns an error if no upstream could serve the request.
func (h *Handler) proxyDirect(w http.ResponseWriter, r *http.Request, body []byte, method string) error {
	n := len(h.router.Nodes())
	for i := 0; i < n; i++ {
		node, err := h.router.Route(method)
		if err != nil || node == nil {
			break
		}

		outReq := r.Clone(r.Context())
		outReq.Body = io.NopCloser(bytes.NewReader(body))
		outReq.ContentLength = int64(len(body))

		proxy := httputil.NewSingleHostReverseProxy(node.URL)
		proxy.Director = func(req *http.Request) {
			req.URL.Scheme = node.URL.Scheme
			req.URL.Host = node.URL.Host
			req.Host = node.URL.Host
		}

		buf := &bufResponseWriter{header: make(http.Header), status: http.StatusOK}
		proxy.ServeHTTP(buf, outReq)
		if buf.status < 200 || buf.status >= 300 {
			slog.Warn("upstream returned bad status, trying next", "node", node.URL.Host, "status", buf.status)
			continue
		}

		for k, vs := range buf.header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(buf.status)
		w.Write(buf.body.Bytes())
		return nil
	}
	http.Error(w, "no healthy upstream available", http.StatusBadGateway)
	return errors.New("no healthy upstream available")
}

// fetchFromUpstream sends the request to an upstream node and returns the raw
// response body. It tries every capable healthy node before giving up, so
// a node that is marked healthy but fails the live request does not abort the call.
func (h *Handler) fetchFromUpstream(r *http.Request, body []byte, method string) ([]byte, error) {
	// Clone with a detached context so one caller cancelling does not abort
	// the shared singleflight call and fail all coalesced callers.
	baseReq := r.Clone(context.WithoutCancel(r.Context()))

	n := len(h.router.Nodes())
	for i := 0; i < n; i++ {
		node, err := h.router.Route(method)
		if err != nil || node == nil {
			break
		}

		outReq := baseReq.Clone(baseReq.Context())
		outReq.Body = io.NopCloser(bytes.NewReader(body))
		outReq.ContentLength = int64(len(body))

		proxy := httputil.NewSingleHostReverseProxy(node.URL)
		proxy.Director = func(req *http.Request) {
			req.URL.Scheme = node.URL.Scheme
			req.URL.Host = node.URL.Host
			req.Host = node.URL.Host
		}

		buf := &bufResponseWriter{header: make(http.Header), status: http.StatusOK}
		proxy.ServeHTTP(buf, outReq)

		if buf.status == http.StatusTooManyRequests {
			return nil, &rateLimitedError{retryAfter: buf.header.Get("Retry-After")}
		}
		if buf.status < 200 || buf.status >= 300 {
			slog.Warn("upstream returned bad status, trying next", "node", node.URL.Host, "status", buf.status)
			continue
		}
		return buf.body.Bytes(), nil
	}
	return nil, errors.New("no healthy upstream available")
}

// bufResponseWriter captures a proxied response into memory.
type bufResponseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufResponseWriter) Header() http.Header         { return b.header }
func (b *bufResponseWriter) WriteHeader(code int)        { b.status = code }
func (b *bufResponseWriter) Write(p []byte) (int, error) { return b.body.Write(p) }
func (b *bufResponseWriter) Flush()                      {} // no-op for in-memory buffer
