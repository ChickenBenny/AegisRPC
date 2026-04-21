// local-demo wires up a complete end-to-end pipeline entirely in-process:
//
//	fake upstream  ──►  AegisRPC proxy (/ws)  ──►  WS client
//
// No internet access or external node is required.
//
// Fake upstream behaviour:
//   - Accepts eth_subscribe / eth_unsubscribe / eth_blockNumber.
//   - Pushes a fake "newHeads" notification every 2 seconds.
//   - Simulates a node crash after --crash-after seconds (default: 10).
//     The proxy auto-reconnects to the second upstream instance and the
//     client never notices (except the subscription ID is remapped).
//
// Usage:
//
//	go run ./examples/local-demo
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ChickenBenny/AegisRPC/internal/proxy"
	"github.com/ChickenBenny/AegisRPC/internal/upstream"
	"github.com/gorilla/websocket"
)

// ── shared upgrader ──────────────────────────────────────────────────────────

var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

// ── fake upstream ─────────────────────────────────────────────────────────────

// fakeUpstream is a minimal WebSocket JSON-RPC server that supports
// eth_subscribe(newHeads), eth_unsubscribe, and eth_blockNumber.
type fakeUpstream struct {
	name       string
	blockNum   *atomic.Uint64 // shared block counter across all instances
	crashAfter time.Duration  // 0 = never crash
}

func (f *fakeUpstream) handler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	subID := fmt.Sprintf("0x%016x", rand.Uint64())
	subActive := make(chan struct{})
	subClosed := false

	// goroutine: push newHead notifications while subscribed
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-subActive:
				return
			case <-ticker.C:
				num := f.blockNum.Add(1)
				notif := map[string]any{
					"jsonrpc": "2.0",
					"method":  "eth_subscription",
					"params": map[string]any{
						"subscription": subID,
						"result": map[string]any{
							"number": fmt.Sprintf("0x%x", num),
							"hash":   fmt.Sprintf("0xdeadbeef%08x", num),
						},
					},
				}
				if err := conn.WriteJSON(notif); err != nil {
					return
				}
				log.Printf("[%s] pushed newHead block=0x%x", f.name, num)
			}
		}
	}()

	// optional crash timer
	if f.crashAfter > 0 {
		go func() {
			time.Sleep(f.crashAfter)
			log.Printf("[%s] *** simulating crash — closing upstream connection ***", f.name)
			conn.Close()
		}()
	}

	// request loop
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			continue
		}

		switch req.Method {
		case "eth_subscribe":
			conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": subID})
			log.Printf("[%s] client subscribed  subID=%s", f.name, subID)

		case "eth_unsubscribe":
			if !subClosed {
				close(subActive)
				subClosed = true
			}
			conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": true})
			log.Printf("[%s] client unsubscribed", f.name)

		case "eth_blockNumber":
			num := f.blockNum.Load()
			conn.WriteJSON(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  fmt.Sprintf("0x%x", num),
			})
		}
	}
}

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	proxyPort := flag.Int("proxy-port", 9191, "Port the AegisRPC proxy listens on")
	crashAfter := flag.Duration("crash-after", 10*time.Second, "Crash first upstream after this duration (0 = never)")
	flag.Parse()

	// shared block counter so both upstream instances count together
	var blockNum atomic.Uint64
	blockNum.Store(0x1234567)

	// ── start two fake upstream instances ────────────────────────────────────
	// upstream-A: will crash after crashAfter to trigger failover
	upA := &fakeUpstream{name: "upstream-A", blockNum: &blockNum, crashAfter: *crashAfter}
	srvA := httptest.NewServer(http.HandlerFunc(upA.handler))
	defer srvA.Close()

	// upstream-B: stable, takes over after A crashes
	upB := &fakeUpstream{name: "upstream-B", blockNum: &blockNum}
	srvB := httptest.NewServer(http.HandlerFunc(upB.handler))
	defer srvB.Close()

	// httptest servers have http:// URLs; upstream.Pool dials them as ws://
	urlA := srvA.URL // http://127.0.0.1:PORT
	urlB := srvB.URL
	log.Printf("upstream-A: %s (crashes in %s)", urlA, *crashAfter)
	log.Printf("upstream-B: %s (stable)", urlB)

	// ── build upstream pool ──────────────────────────────────────────────────
	pool, err := upstream.NewPool([]string{urlA, urlB})
	if err != nil {
		log.Fatalf("pool: %v", err)
	}
	for _, n := range pool.Nodes() {
		n.SetHealthy(true) // mark both healthy up front (no real health check needed)
	}

	// ── start AegisRPC proxy ─────────────────────────────────────────────────
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", proxy.ServeWS(pool))
	// also expose a plain HTTP ping so curl can confirm the server is up
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("pong\n"))
	})

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", *proxyPort),
		Handler: mux,
	}
	go func() {
		log.Printf("AegisRPC proxy listening on ws://localhost:%d/ws", *proxyPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("proxy: %v", err)
		}
	}()
	time.Sleep(100 * time.Millisecond) // brief pause to let the server bind

	// ── connect a demo WS client ─────────────────────────────────────────────
	proxyURL := fmt.Sprintf("ws://localhost:%d/ws", *proxyPort)
	log.Printf("Client connecting to %s ...", proxyURL)

	conn, _, err := websocket.DefaultDialer.Dial(proxyURL, nil)
	if err != nil {
		log.Fatalf("client dial: %v", err)
	}
	defer conn.Close()
	log.Println("Client connected.")

	// subscribe to newHeads
	subReq := map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"method": "eth_subscribe", "params": []string{"newHeads"},
	}
	if err := conn.WriteJSON(subReq); err != nil {
		log.Fatalf("eth_subscribe: %v", err)
	}

	var subResp struct {
		ID     int    `json:"id"`
		Result string `json:"result"`
	}
	if err := conn.ReadJSON(&subResp); err != nil {
		log.Fatalf("read subscribe response: %v", err)
	}
	clientSubID := subResp.Result
	log.Printf("Client subscribed  clientSubID=%s", clientSubID)

	// poll eth_blockNumber every 5 seconds
	reqID := 2
	pollTick := time.NewTicker(5 * time.Second)
	defer pollTick.Stop()
	go func() {
		for range pollTick.C {
			conn.WriteJSON(map[string]any{
				"jsonrpc": "2.0", "id": reqID,
				"method": "eth_blockNumber", "params": []any{},
			})
			reqID++
		}
	}()

	// graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-quit
		fmt.Println()
		log.Println("Shutting down...")
		conn.WriteJSON(map[string]any{
			"jsonrpc": "2.0", "id": 999,
			"method": "eth_unsubscribe", "params": []string{clientSubID},
		})
		time.Sleep(200 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
		os.Exit(0)
	}()

	// ── read loop ─────────────────────────────────────────────────────────────
	fmt.Println()
	fmt.Println("══════════════════════════════════════════")
	fmt.Printf("  Client sub ID: %s\n", clientSubID)
	fmt.Println("  Waiting for newHeads… (Ctrl-C to quit)")
	fmt.Println("══════════════════════════════════════════")
	fmt.Println()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if strings.Contains(err.Error(), "use of closed") {
				return
			}
			log.Printf("read error: %v", err)
			return
		}

		var probe struct {
			Method string `json:"method"`
		}
		json.Unmarshal(raw, &probe)

		if probe.Method == "eth_subscription" {
			var notif struct {
				Params struct {
					Subscription string `json:"subscription"`
					Result       struct {
						Number string `json:"number"`
						Hash   string `json:"hash"`
					} `json:"result"`
				} `json:"params"`
			}
			json.Unmarshal(raw, &notif)
			p := notif.Params

			// Verify the proxy preserved the client's original sub ID.
			idMatch := "✓"
			if p.Subscription != clientSubID {
				idMatch = fmt.Sprintf("✗ MISMATCH got=%s", p.Subscription)
			}
			fmt.Printf("[newHead]  block=%-12s  hash=%s  subID=%s\n",
				p.Result.Number, p.Result.Hash, idMatch)

		} else {
			// Regular response
			var resp struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			json.Unmarshal(raw, &resp)
			if len(resp.Error) > 0 && string(resp.Error) != "null" {
				fmt.Printf("[resp]     id=%-3d  error=%s\n", resp.ID, resp.Error)
			} else {
				fmt.Printf("[resp]     id=%-3d  result=%s\n", resp.ID, resp.Result)
			}
		}
	}
}
