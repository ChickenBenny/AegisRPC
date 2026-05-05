package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

var healthCheckPayload = []byte(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`)

// StartHealthChecks polls every node in the pool on the given interval.
// Nodes lagging more than lagThreshold blocks behind the best node are marked unhealthy.
// probeTimeout caps each individual health-check HTTP request.
// The optional onHeadUpdate callbacks are invoked after each round with the best observed block height.
//
// Returns a channel that is closed once the goroutine has exited (after ctx is
// cancelled and any in-flight probes finish). Callers that need to coordinate
// shutdown — typically main() after srv.Shutdown — should block on it before
// tearing down shared resources like the cache backend.
func (p *Pool) StartHealthChecks(ctx context.Context, interval time.Duration, lagThreshold uint64, probeTimeout time.Duration, onHeadUpdate ...func(uint64)) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
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
						checkNode(ctx, n, probeTimeout)
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
	return done
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

// checkNode performs one health probe against a single upstream.
// ctx is wrapped with timeout so that cancelling the parent aborts in-flight
// HTTP requests immediately rather than waiting out the full probe timeout.
// Logging is transition-only: state changes log at Info/Warn; steady-state at Debug.
func checkNode(parent context.Context, node *Upstream, timeout time.Duration) {
	wasHealthy := node.IsHealthy()

	healthy, blockHeight, reason := probeNode(parent, node, timeout)

	node.SetHealthy(healthy)
	if blockHeight > 0 {
		node.SetBlockHeight(blockHeight)
	}

	host := node.URL.Host
	switch {
	case wasHealthy && !healthy:
		slog.Warn("upstream degraded", "node", host, "reason", reason)
	case !wasHealthy && healthy:
		slog.Info("upstream recovered", "node", host, "block", blockHeight)
	case healthy:
		if reason == "" {
			slog.Debug("upstream ok", "node", host, "block", blockHeight)
		} else {
			slog.Debug("upstream ok", "node", host, "block", blockHeight, "reason", reason)
		}
	default:
		slog.Debug("upstream still degraded", "node", host, "reason", reason)
	}
}

// probeNode performs the actual HTTP probe and returns the resulting node
// state. Splitting this out keeps checkNode focused on the transition-logging
// concern; the two have orthogonal responsibilities.
//
// Return values:
//   - healthy: whether the probe should mark the node available
//   - blockHeight: parsed block number, or 0 when no fresh height was observed
//     (errors, or rate-limit responses that prove only reachability)
//   - reason: short human-readable cause. "" for a clean OK, "rate_limited"
//     for 429, otherwise an error description used in transition log lines
func probeNode(parent context.Context, node *Upstream, timeout time.Duration) (healthy bool, blockHeight uint64, reason string) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wsToHTTP(node.URL.String()),
		bytes.NewReader(healthCheckPayload))
	if err != nil {
		return false, 0, fmt.Sprintf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Host = node.URL.Host

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, 0, fmt.Sprintf("unreachable: %v", err)
	}
	defer resp.Body.Close()

	// 429 means the probe itself was rate-limited; the node is reachable
	// but busy. Mark it healthy so callers can still try this round, but
	// do not update block height (we did not get one).
	if resp.StatusCode == http.StatusTooManyRequests {
		return true, 0, "rate_limited"
	}
	if resp.StatusCode != http.StatusOK {
		return false, 0, fmt.Sprintf("http_%d", resp.StatusCode)
	}

	const maxResponseSize = 1 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return false, 0, fmt.Sprintf("read body: %v", err)
	}

	height, err := validateRPCResponse(body)
	if err != nil {
		return false, 0, fmt.Sprintf("rpc error: %v", err)
	}
	return true, height, ""
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
