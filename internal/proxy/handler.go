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
	// --- Observability setup ---
	// `method` and `status` are updated throughout this function.
	// The deferred closure reads their final values when the function returns,
	// so we only need one recording point no matter how many return paths exist.
	method := "unknown"
	status := "ok"
	start := time.Now()
	defer func() {
		metrics.RequestsTotal.WithLabelValues(method, status).Inc()
		metrics.RequestDuration.WithLabelValues(method).Observe(time.Since(start).Seconds())
	}()

	// Read body so we can inspect the method and use it multiple times.
	r.Body = http.MaxBytesReader(w, r.Body, 1*1024*1024)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		status = "error"
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}

	var req models.RPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		status = "error"
		http.Error(w, "invalid JSON-RPC request", http.StatusBadRequest)
		return
	}
	method = req.Method // now we know the real method name

	layer := h.finality.Classify(req.Method, req.Params)

	// Uncacheable: bypass cache entirely.
	if layer == cache.LayerUncacheable {
		status = "uncacheable"
		if err := h.proxyDirect(w, r, body, req.Method); err != nil {
			status = "error"
		}
		return
	}

	key := cache.CacheKey(req.Method, req.Params)

	// Negative cache hit: upstream was recently unhealthy, fail fast.
	if _, ok := h.cache.Get("neg::" + key); ok {
		metrics.CacheRequests.WithLabelValues("neg_hit").Inc()
		status = "neg_hit"
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}

	// Cache hit: serve immediately.
	if cached, ok := h.cache.Get(key); ok {
		metrics.CacheRequests.WithLabelValues("hit").Inc()
		status = "cache_hit"
		w.Header().Set("Content-Type", "application/json")
		w.Write(cached)
		return
	}

	// Cache miss: we will call the upstream via singleflight.
	metrics.CacheRequests.WithLabelValues("miss").Inc()

	// Use singleflight to ensure only one upstream call per key.
	v, err, _ := h.sf.Do(key, func() (any, error) {
		resp, err := h.fetchFromUpstream(r, body, req.Method)
		if err != nil {
			var rl *rateLimitedError
			if !errors.As(err, &rl) {
				h.cache.Set("neg::"+key, []byte(err.Error()), 1*time.Second)
			}
			return nil, err
		}
		// Store result in cache with appropriate TTL.
		ttl := time.Duration(0) // Immutable: never expires
		if layer == cache.LayerMutable {
			ttl = h.mutableTTL
		}
		h.cache.Set(key, resp, ttl)
		return resp, nil
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
	result := v.([]byte)

	w.Header().Set("Content-Type", "application/json")
	w.Write(result)
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

		// Successful response — flush buffer to the real ResponseWriter.
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
	// Clone once with a detached context so a single caller cancelling their
	// request does not abort the shared singleflight call and fail all
	// coalesced callers.
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
