package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ChickenBenny/AegisRPC/internal/cache"
	"github.com/ChickenBenny/AegisRPC/internal/config"
	"github.com/ChickenBenny/AegisRPC/internal/httpapi"
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

	srv := httpapi.New(cfg.Port, h, pool)

	go func() {
		log.Printf("AegisRPC started on %s (mutableTTL=%s, healthInterval=%s, probeTimeout=%s)",
			srv.Addr(), cfg.MutableTTL, cfg.HealthInterval, cfg.ProbeTimeout)
		if err := srv.Start(); err != nil {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("Shutting down...")
	if err := srv.Shutdown(15 * time.Second); err != nil {
		log.Printf("Shutdown error: %v", err)
	}
	log.Println("Stopped.")
}
