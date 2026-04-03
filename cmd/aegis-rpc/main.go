package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ChickenBenny/AegisRPC/internal/cache"
	"github.com/ChickenBenny/AegisRPC/internal/proxy"
	"github.com/ChickenBenny/AegisRPC/internal/upstream"
)

func main() {
	// 1. Parse command line flags
	port := flag.Int("port", 8080, "The port to listen on")
	upstreams := flag.String("upstreams", "https://eth.llamarpc.com", "Comma-separated list of upstream RPC URLs")
	mutableTTL     := flag.Duration("mutable-ttl", 12*time.Second, "TTL for mutable cached responses (e.g. eth_blockNumber)")
	maxCacheEntries := flag.Int("max-cache-entries", 10_000, "LRU cap for the response cache (0 = unlimited)")
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

	// 5. Build cache + handler (Phase 4)
	c := cache.NewCache(ctx, 5*time.Minute, *maxCacheEntries)
	h := proxy.NewHandler(pool, c, *mutableTTL)

	// 6. Set up the mux — enforce POST-only at the edge
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.ServeHTTP(w, r)
	})

	// 7. Start server
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", *port),
		Handler: mux,
	}

	go func() {
		log.Printf("AegisRPC started on :%d (mutableTTL=%s)", *port, *mutableTTL)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	// 8. Block until signal received
	<-ctx.Done()
	log.Println("Shutting down...")

	// 9. Give in-flight requests up to 15s to finish
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("Shutdown error: %v", err)
	}

	log.Println("Stopped.")
}
