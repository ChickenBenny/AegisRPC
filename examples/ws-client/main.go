// ws-client is a lightweight prototype that demonstrates AegisRPC's WebSocket
// proxy endpoint in action.
//
// What it does:
//  1. Connects to the proxy's /ws endpoint.
//  2. Sends eth_subscribe newHeads → prints every new block header.
//  3. Concurrently calls eth_blockNumber once per second and prints the result.
//  4. Gracefully unsubscribes and disconnects on Ctrl-C.
//
// Usage:
//
//	# Terminal 1 – start the proxy against a real or local upstream
//	go run ./cmd/aegis-rpc -upstreams wss://eth.llamarpc.com -port 8080
//
//	# Terminal 2 – run this client
//	go run ./examples/ws-client -addr localhost:8080
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// ── JSON-RPC helpers ──────────────────────────────────────────────────────────

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type rpcResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

type rpcNotification struct {
	Method string `json:"method"`
	Params struct {
		Subscription string          `json:"subscription"`
		Result       json.RawMessage `json:"result"`
	} `json:"params"`
}

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	addr := flag.String("addr", "localhost:8080", "AegisRPC proxy address (host:port)")
	flag.Parse()

	url := "ws://" + *addr + "/ws"
	log.Printf("Connecting to %s ...", url)

	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		log.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	log.Println("Connected.")

	// ── 1. Subscribe to new block headers ────────────────────────────────────
	subReq := rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "eth_subscribe",
		Params:  []string{"newHeads"},
	}
	if err := conn.WriteJSON(subReq); err != nil {
		log.Fatalf("eth_subscribe: %v", err)
	}

	// Read the subscribe response to get the subscription ID.
	var subResp rpcResponse
	if err := conn.ReadJSON(&subResp); err != nil {
		log.Fatalf("read subscribe response: %v", err)
	}
	var subID string
	if err := json.Unmarshal(subResp.Result, &subID); err != nil {
		log.Fatalf("parse sub ID: %v (response: %s)", err, subResp.Result)
	}
	log.Printf("[+] Subscribed to newHeads  subID=%s", subID)

	// ── 2. Ticker: eth_blockNumber once per second ────────────────────────────
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	reqID := 2

	go func() {
		for range ticker.C {
			req := rpcRequest{
				JSONRPC: "2.0",
				ID:      reqID,
				Method:  "eth_blockNumber",
				Params:  []any{},
			}
			reqID++
			if err := conn.WriteJSON(req); err != nil {
				log.Printf("[poll] write eth_blockNumber: %v", err)
				return
			}
		}
	}()

	// ── 3. Signal handler: unsubscribe on Ctrl-C ──────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-quit
		log.Println("\nCtrl-C — unsubscribing and closing...")
		unsubReq := rpcRequest{
			JSONRPC: "2.0",
			ID:      999,
			Method:  "eth_unsubscribe",
			Params:  []string{subID},
		}
		conn.WriteJSON(unsubReq)
		time.Sleep(300 * time.Millisecond)
		conn.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "bye"),
		)
		os.Exit(0)
	}()

	// ── 4. Main read loop ─────────────────────────────────────────────────────
	log.Println("Listening for messages (Ctrl-C to quit)...")
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			log.Printf("connection closed: %v", err)
			return
		}

		// Detect whether this is a subscription notification or a normal response.
		var probe struct {
			Method string `json:"method"`
		}
		json.Unmarshal(raw, &probe)

		if probe.Method == "eth_subscription" {
			var notif rpcNotification
			if err := json.Unmarshal(raw, &notif); err != nil {
				log.Printf("[notif] parse error: %v  raw=%s", err, raw)
				continue
			}
			// Extract just the block number from the header for a compact log line.
			var header struct {
				Number string `json:"number"`
				Hash   string `json:"hash"`
			}
			json.Unmarshal(notif.Params.Result, &header)
			fmt.Printf("[newHead]  block=%s  hash=%s\n", header.Number, header.Hash)
		} else {
			// Regular response (e.g. eth_blockNumber, eth_unsubscribe).
			var resp rpcResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				log.Printf("[resp] parse error: %v  raw=%s", err, raw)
				continue
			}
			if len(resp.Error) > 0 && string(resp.Error) != "null" {
				log.Printf("[resp] id=%d error=%s", resp.ID, resp.Error)
			} else {
				fmt.Printf("[resp]     id=%-3d result=%s\n", resp.ID, resp.Result)
			}
		}
	}
}
