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
	"time"

	"github.com/ChickenBenny/AegisRPC/internal/proxy"
	"github.com/ChickenBenny/AegisRPC/internal/upstream"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server owns the http.Server and its mux. Construct with New, then call
// Start in a goroutine and Shutdown on termination.
type Server struct {
	srv *http.Server
}

// New builds the route table and returns a ready-to-start Server.
//
//	/         POST  → JSON-RPC handler
//	/ws       GET   → WebSocket proxy (upgrade)
//	/metrics  GET   → Prometheus scrape endpoint
//	/healthz  GET   → liveness / readiness probe (always fast, no side effects)
func New(port int, handler *proxy.Handler, pool *upstream.Pool) *Server {
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
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	return &Server{
		srv: &http.Server{
			Addr:    fmt.Sprintf(":%d", port),
			Handler: mux,
		},
	}
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
func (s *Server) Shutdown(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return s.srv.Shutdown(ctx)
}
