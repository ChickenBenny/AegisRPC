package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ChickenBenny/AegisRPC/internal/models"
	"github.com/ChickenBenny/AegisRPC/internal/upstream"
)

func main() {
	// 1. Parse command line flags
	port := flag.Int("port", 8080, "The port to listen on")
	upstreams := flag.String("upstreams", "https://eth.llamarpc.com", "Comma-separated list of upstream RPC URLs")
	flag.Parse()

	// 2. Build upstream pool
	rawURLs := strings.Split(*upstreams, ",")
	urls := make([]string, 0, len(rawURLs))
	for _, u := range rawURLs {
		if trimmed := strings.TrimSpace(u); trimmed != "" {
			urls = append(urls, trimmed)
		}
	}
	pool, err := upstream.NewPool(urls)
	if err != nil {
		log.Fatalf("Failed to create upstream pool: %v", err)
	}
	log.Printf("Loaded %d upstream(s)", len(urls))

	// 3. Context that cancels on SIGTERM or SIGINT
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 4. Start background health checks
	pool.StartHealthChecks(ctx, 15*time.Second, 10)

	// 5. Set up the handler
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		node := pool.Next()
		if node == nil {
			http.Error(w, "No healthy upstream available", http.StatusBadGateway)
			return
		}

		// Read body for inspection (limit to 1MB to prevent OOM)
		r.Body = http.MaxBytesReader(w, r.Body, 1*1024*1024)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Request too large", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = io.NopCloser(bytes.NewBuffer(body))

		var rpcReq models.RPCRequest
		if err := json.Unmarshal(body, &rpcReq); err != nil {
			log.Printf("Non-RPC request received: %v", err)
		} else {
			log.Printf("[%s] RPC Method: %s (ID: %v)", node.URL.Host, rpcReq.Method, rpcReq.ID)
		}

		target := node.URL
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.Director = func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
		}
		proxy.ServeHTTP(w, r)
	})

	// 6. Start server
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", *port),
		Handler: mux,
	}

	go func() {
		log.Printf("AegisRPC started on :%d", *port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	// 7. Block until signal received
	<-ctx.Done()
	log.Println("Shutting down...")

	// 8. Give in-flight requests up to 15s to finish
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("Shutdown error: %v", err)
	}

	log.Println("Stopped.")
}
