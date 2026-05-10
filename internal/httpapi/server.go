// Package httpapi wires the HTTP-facing routes of AegisRPC (JSON-RPC proxy,
// WebSocket proxy, and Prometheus metrics) behind a single http.Server.
package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/ChickenBenny/AegisRPC/internal/proxy"
	"github.com/ChickenBenny/AegisRPC/internal/upstream"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server owns the http.Server and its mux. Construct with New, then call
// Start in a goroutine and Shutdown on termination.
//
// draining is flipped to true at the start of Shutdown so that /healthz
// returns 503 before in-flight requests are interrupted.
type Server struct {
	srv      *http.Server
	draining atomic.Bool
}

// New builds the route table and returns a ready-to-start Server.
//
//	/         POST  → JSON-RPC handler
//	/ws       GET   → WebSocket proxy (upgrade)
//	/metrics  GET   → Prometheus scrape endpoint
//	/healthz  GET   → readiness probe; 200 normally, 503 once Shutdown begins
//
// writeTimeout sets http.Server.WriteTimeout. Archive deployments serving
// wide-range eth_getLogs / debug_trace* should pass a generous value
// (120s+); the default 30s is safe for wallet/dApp traffic.
//
// wsReplayPendingCap caps how many upstream frames each WS session may
// buffer during reconnect / subscription replay (see audit #5).
// wsAllowedOrigins is the WS upgrade origin allowlist; empty = allow all
// (audit #15).
func New(port int, writeTimeout time.Duration, wsReplayPendingCap int, wsAllowedOrigins []string, handler *proxy.Handler, pool *upstream.Pool) *Server {
	s := &Server{}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handler.ServeHTTP(w, r)
	})
	mux.HandleFunc("/ws", proxy.ServeWS(pool, wsReplayPendingCap, wsAllowedOrigins))
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", s.healthz)

	s.srv = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
		// Slowloris defence: ReadHeaderTimeout caps the wait for the
		// request header's \r\n\r\n; the others bound the rest of the
		// request lifecycle. WebSocket-safe — gorilla hijacks the conn
		// on Upgrade and manages its own deadlines afterwards.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       120 * time.Second,
	}
	return s
}

// healthz returns 200 normally and 503 once Shutdown has begun.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if s.draining.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("draining\n"))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// Addr returns the listen address (e.g. ":8080") for logging.
func (s *Server) Addr() string { return s.srv.Addr }

// Start blocks on ListenAndServe. Returns nil on graceful shutdown
// (http.ErrServerClosed is treated as success).
func (s *Server) Start() error {
	if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Shutdown stops the server, waiting up to `timeout` for in-flight requests.
// The draining flag is set first so that any /healthz probe on a keep-alive
// connection sees 503 and triggers load-balancer removal.
func (s *Server) Shutdown(timeout time.Duration) error {
	s.draining.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return s.srv.Shutdown(ctx)
}
