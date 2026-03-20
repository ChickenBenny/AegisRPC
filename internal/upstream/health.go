package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
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
				for _, node := range p.nodes {
					go checkNode(node)
				}
			}
		}
	}()
}

func checkNode(node *Upstream) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, node.URL.String(),
		bytes.NewReader(healthCheckPayload))
	req.Header.Set("Content-Type", "application/json")
	req.Host = node.URL.Host

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[health] %s unreachable: %v", node.URL.Host, err)
		node.SetHealthy(false)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var rpcResp struct {
		Error *struct{ Message string } `json:"error"`
	}
	if err := json.Unmarshal(body, &rpcResp); err != nil || rpcResp.Error != nil {
		log.Printf("[health] %s returned RPC error", node.URL.Host)
		node.SetHealthy(false)
		return
	}

	node.SetHealthy(true)
	log.Printf("[health] %s OK", node.URL.Host)
}
