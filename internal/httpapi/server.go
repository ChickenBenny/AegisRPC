// Package httpapi wires the HTTP-facing routes of AegisRPC (JSON-RPC proxy,
// WebSocket proxy, and Prometheus metrics) behind a single http.Server.
//
// main() builds the business dependencies (pool, cache, handler) and hands
// them to New; route definitions and server lifecycle live here.
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
// draining is flipped to true at the very start of Shutdown so that any
// load balancer's next /healthz probe sees 503 and removes us from
// rotation before in-flight RPC requests are interrupted. The flag is
// read on every /healthz request and only ever transitions false → true,
// so atomic.Bool is sufficient (no need for a mutex).
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
func New(port int, handler *proxy.Handler, pool *upstream.Pool) *Server {
	s := &Server{}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handler.ServeHTTP(w, r)
	})
	mux.HandleFunc("/ws", proxy.ServeWS(pool))
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", s.healthz)

	s.srv = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}
	return s
}

// healthz is the /healthz probe handler. Returns 200 in normal operation;
// once Shutdown has begun it returns 503 so an upstream load balancer
// removes this instance from rotation before its in-flight requests are
// drained. Body is plain text so kubectl / curl output stays readable.
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
//
// The draining flag is flipped before http.Server.Shutdown is invoked so
// that any /healthz probe arriving on an existing keep-alive connection
// during the grace period sees 503 and triggers an LB-side removal. New
// connections are refused by the listener (http.Server.Shutdown closes it
// immediately), which would already cause LB health checks to fail — but
// the explicit 503 makes the intent visible at the protocol level rather
// than relying on TCP refused.
func (s *Server) Shutdown(timeout time.Duration) error {
	s.draining.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return s.srv.Shutdown(ctx)
}
