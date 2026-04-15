package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

var healthCheckPayload = []byte(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`)

// StartHealthChecks polls every node in the pool on the given interval.
// Nodes lagging more than lagThreshold blocks behind the best node are marked unhealthy.
// The optional onHeadUpdate callbacks are invoked after each round with the best observed block height.
func (p *Pool) StartHealthChecks(ctx context.Context, interval time.Duration, lagThreshold uint64, onHeadUpdate ...func(uint64)) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				var wg sync.WaitGroup
				for _, node := range p.nodes {
					wg.Add(1)
					go func(n *Upstream) {
						defer wg.Done()
						checkNode(n)
					}(node)
				}
				wg.Wait()
				bestHeight := p.markLaggingNodes(lagThreshold)
				for _, cb := range onHeadUpdate {
					cb(bestHeight)
				}
			}
		}
	}()
}

func parseBlockNumber(hexStr string) (uint64, error) {
	if len(hexStr) < 2 || hexStr[:2] != "0x" {
		return 0, fmt.Errorf("block number must be hex string starting with 0x")
	}

	num, err := strconv.ParseUint(hexStr[2:], 16, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid block number: %w", err)
	}

	return num, nil
}

// wsToHTTP converts a WebSocket URL (ws:// or wss://) to its HTTP equivalent
// so that http.DefaultClient can be used for health-check POST requests.
func wsToHTTP(u string) string {
	switch {
	case strings.HasPrefix(u, "wss://"):
		return "https://" + u[6:]
	case strings.HasPrefix(u, "ws://"):
		return "http://" + u[5:]
	}
	return u
}

func checkNode(node *Upstream) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wsToHTTP(node.URL.String()),
		bytes.NewReader(healthCheckPayload))
	if err != nil {
		log.Printf("[health] %s failed to build request: %v", node.URL.Host, err)
		node.SetHealthy(false)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Host = node.URL.Host

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[health] %s unreachable: %v", node.URL.Host, err)
		node.SetHealthy(false)
		return
	}
	defer resp.Body.Close()

	// 429 means the health-check itself was rate-limited; the node is reachable
	// but busy. Mark it healthy so callers can still try this round.
	if resp.StatusCode == http.StatusTooManyRequests {
		log.Printf("[health] %s rate-limited (429), marking healthy", node.URL.Host)
		node.SetHealthy(true)
		return
	}
	if resp.StatusCode != http.StatusOK {
		log.Printf("[health] %s HTTP %d", node.URL.Host, resp.StatusCode)
		node.SetHealthy(false)
		return
	}

	const maxResponseSize = 1 * 1024 * 1024 // 1 MB
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		log.Printf("[health] %s failed to read response: %v", node.URL.Host, err)
		node.SetHealthy(false)
		return
	}

	blockHeight, err := validateRPCResponse(body)
	if err != nil {
		log.Printf("[health] %s RPC error: %v", node.URL.Host, err)
		node.SetHealthy(false)
		return
	}

	node.SetHealthy(true)
	node.SetBlockHeight(blockHeight)
	log.Printf("[health] %s OK", node.URL.Host)
}

func validateRPCResponse(body []byte) (uint64, error) {
	var rpcResp struct {
		Error  *struct{ Message string } `json:"error"`
		Result string                    `json:"result"`
	}
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return 0, fmt.Errorf("invalid JSON: %w", err)
	}
	if rpcResp.Error != nil {
		return 0, fmt.Errorf("rpc error: %s", rpcResp.Error.Message)
	}
	if rpcResp.Result == "" {
		return 0, fmt.Errorf("empty result")
	}
	blockHeight, err := parseBlockNumber(rpcResp.Result)
	if err != nil {
		return 0, fmt.Errorf("invalid block number: %w", err)
	}
	return blockHeight, nil
}
