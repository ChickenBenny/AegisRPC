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
	"strings"
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
	urls := strings.Split(*upstreams, ",")
	pool, err := upstream.NewPool(urls)
	if err != nil {
		log.Fatalf("Failed to create upstream pool: %v", err)
	}
	log.Printf("Loaded %d upstream(s)", len(urls))

	// Start background health checks every 15 seconds
	pool.StartHealthChecks(context.Background(), 15*time.Second)

	// 3. Set up the handler
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Pick a healthy upstream
		node := pool.Next()
		if node == nil {
			http.Error(w, "No healthy upstream available", http.StatusBadGateway)
			return
		}

		// Read body for inspection
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read request body", http.StatusInternalServerError)
			return
		}
		r.Body = io.NopCloser(bytes.NewBuffer(body))

		// Parse and log the RPC method
		var rpcReq models.RPCRequest
		if err := json.Unmarshal(body, &rpcReq); err != nil {
			log.Printf("Non-RPC request received: %v", err)
		} else {
			log.Printf("[%s] RPC Method: %s (ID: %v)", node.URL.Host, rpcReq.Method, rpcReq.ID)
		}

		// Proxy to selected node
		target := node.URL
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.Director = func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
		}
		proxy.ServeHTTP(w, r)
	})

	// 4. Start the server
	addr := fmt.Sprintf(":%d", *port)
	log.Printf("AegisRPC started on %s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
