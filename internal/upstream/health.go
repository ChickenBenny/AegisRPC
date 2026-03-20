package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

var healthCheckPayload = []byte(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`)

// StartHealthChecks polls every node in the pool on the given interval.
// Marks a node unhealthy if the request fails or returns an RPC error.
func (p *Pool) StartHealthChecks(ctx context.Context, interval time.Duration) {
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
			}
		}
	}()
}

func checkNode(node *Upstream) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, node.URL.String(),
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

	if resp.StatusCode != http.StatusOK {
		log.Printf("[health] %s HTTP %d", node.URL.Host, resp.StatusCode)
		node.SetHealthy(false)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[health] %s failed to read response: %v", node.URL.Host, err)
		node.SetHealthy(false)
		return
	}

	if err := validateRPCResponse(body); err != nil {
		log.Printf("[health] %s RPC error: %v", node.URL.Host, err)
		node.SetHealthy(false)
		return
	}

	node.SetHealthy(true)
	log.Printf("[health] %s OK", node.URL.Host)
}

func validateRPCResponse(body []byte) error {
	var rpcResp struct {
		Error *struct{ Message string } `json:"error"`
	}
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if rpcResp.Error != nil {
		return fmt.Errorf("rpc error: %s", rpcResp.Error.Message)
	}
	return nil
}
