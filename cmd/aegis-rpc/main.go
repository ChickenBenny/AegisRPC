package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/ChickenBenny/AegisRPC/internal/models"
)

func main() {
	// 1. Parse command line flags for configuration
	port := flag.Int("port", 8080, "The port to listen on")
	upstreamURL := flag.String("upstream", "https://eth.llamarpc.com", "The upstream RPC provider URL")
	flag.Parse()

	// 2. Parse the upstream URL
	target, err := url.Parse(*upstreamURL)
	if err != nil {
		log.Fatalf("Failed to parse upstream URL: %v", err)
	}

	// 3. Initialize the standard Reverse Proxy
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host
	}

	// 4. Set up the handler
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Read the body for inspection
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read request body", http.StatusInternalServerError)
			return
		}

		// Restore the body for the proxy to read
		r.Body = io.NopCloser(bytes.NewBuffer(body))

		// Parse the RPC request
		var rpcReq models.RPCRequest
		if err := json.Unmarshal(body, &rpcReq); err != nil {
			log.Printf("Non-RPC request received: %v", err)
		} else {
			log.Printf("RPC Method: %s (ID: %v)", rpcReq.Method, rpcReq.ID)
		}

		proxy.ServeHTTP(w, r)
	})

	// 5. Start the server
	addr := fmt.Sprintf(":%d", *port)
	log.Printf("AegisRPC started on %s, proxying to %s", addr, *upstreamURL)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
