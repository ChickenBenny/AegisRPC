package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"time"

	"github.com/ChickenBenny/AegisRPC/internal/cache"
	"github.com/ChickenBenny/AegisRPC/internal/models"
	"github.com/ChickenBenny/AegisRPC/internal/upstream"
	"golang.org/x/sync/singleflight"
)

// Handler is an http.Handler that proxies JSON-RPC requests to upstream nodes
// with integrated caching, singleflight coalescing, and finality-aware classification.
type Handler struct {
	pool       *upstream.Pool
	cache      *cache.Cache
	finality   *cache.FinalityChecker
	sf         singleflight.Group
	mutableTTL time.Duration
}

func NewHandler(pool *upstream.Pool, c *cache.Cache, mutableTTL time.Duration, fc *cache.FinalityChecker) *Handler {
	return &Handler{
		pool:       pool,
		cache:      c,
		finality:   fc,
		mutableTTL: mutableTTL,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Read body so we can inspect the method and use it multiple times.
	r.Body = http.MaxBytesReader(w, r.Body, 1*1024*1024)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}

	var req models.RPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON-RPC request", http.StatusBadRequest)
		return
	}

	layer := h.finality.Classify(req.Method, req.Params)

	// Uncacheable: bypass cache entirely.
	if layer == cache.LayerUncacheable {
		h.proxyDirect(w, r, body)
		return
	}

	key := cache.CacheKey(req.Method, req.Params)

	// Negative cache hit: upstream was recently unhealthy, fail fast.
	if _, ok := h.cache.Get("neg::" + key); ok {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}

	// Cache hit: serve immediately.
	if cached, ok := h.cache.Get(key); ok {
		w.Header().Set("Content-Type", "application/json")
		w.Write(cached)
		return
	}

	// Cache miss: use singleflight to ensure only one upstream call per key.
	v, err, _ := h.sf.Do(key, func() (any, error) {
		resp, err := h.fetchFromUpstream(r, body)
		if err != nil {
			h.cache.Set("neg::"+key, []byte(err.Error()), 1*time.Second)
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
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	result := v.([]byte)

	w.Header().Set("Content-Type", "application/json")
	w.Write(result)
}

// proxyDirect forwards the request to the upstream without any caching.
func (h *Handler) proxyDirect(w http.ResponseWriter, r *http.Request, body []byte) {
	node := h.pool.Next()
	if node == nil {
		http.Error(w, "no healthy upstream available", http.StatusBadGateway)
		return
	}
	// Restore body for the reverse proxy.
	r.Body = io.NopCloser(bytes.NewReader(body))
	proxy := httputil.NewSingleHostReverseProxy(node.URL)
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = node.URL.Scheme
		req.URL.Host = node.URL.Host
		req.Host = node.URL.Host
	}
	proxy.ServeHTTP(w, r)
}

// fetchFromUpstream sends the request to an upstream node and returns the raw
// response body. Uses a buffering ResponseWriter to capture the response.
func (h *Handler) fetchFromUpstream(r *http.Request, body []byte) ([]byte, error) {
	node := h.pool.Next()
	if node == nil {
		return nil, errors.New("no healthy upstream available")
	}

	// Clone the request with a detached context so that a single caller
	// cancelling their request does not abort the shared singleflight call
	// and fail all coalesced callers.
	outReq := r.Clone(context.WithoutCancel(r.Context()))
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
		return nil, fmt.Errorf("upstream returned %d", buf.status)
	}
	return buf.body.Bytes(), nil
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
