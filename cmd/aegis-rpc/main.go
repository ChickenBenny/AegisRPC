package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ChickenBenny/AegisRPC/internal/cache"
	"github.com/ChickenBenny/AegisRPC/internal/config"
	"github.com/ChickenBenny/AegisRPC/internal/proxy"
	"github.com/ChickenBenny/AegisRPC/internal/router"
	"github.com/ChickenBenny/AegisRPC/internal/upstream"
)

func main() {
	cfg, err := config.Parse()
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}

	pool, err := upstream.NewPool(cfg.Upstreams)
	if err != nil {
		log.Fatalf("Failed to create upstream pool: %v", err)
	}
	log.Printf("Loaded %d upstream(s)", len(cfg.Upstreams))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fc := cache.NewFinalityChecker(cfg.FinalityDepth)
	pool.StartHealthChecks(ctx, cfg.HealthInterval, cfg.LagThreshold, cfg.ProbeTimeout, fc.SetHead)
	log.Printf("Finality depth: %d blocks", cfg.FinalityDepth)

	c := cache.NewCache(ctx, 5*time.Minute, cfg.MaxCacheEntries)
	rtr := router.New(pool)
	h := proxy.NewHandler(rtr, c, cfg.MutableTTL, fc)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.ServeHTTP(w, r)
	})
	mux.HandleFunc("/ws", proxy.ServeWS(pool))

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: mux,
	}

	go func() {
		log.Printf("AegisRPC started on :%d (mutableTTL=%s, healthInterval=%s, probeTimeout=%s)",
			cfg.Port, cfg.MutableTTL, cfg.HealthInterval, cfg.ProbeTimeout)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("Shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("Shutdown error: %v", err)
	}
	log.Println("Stopped.")
}
