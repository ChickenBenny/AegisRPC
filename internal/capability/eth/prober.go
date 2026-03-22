package eth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ChickenBenny/AegisRPC/internal/capability"
)

type EthProber struct {
	client *http.Client
}

func NewEthProber() *EthProber {
	return &EthProber{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (p *EthProber) Probe(ctx context.Context, nodeURL string) (capability.Capability, error) {
	caps := capability.CapBasic

	historical, err := p.probeHistorical(ctx, nodeURL)
	if err != nil {
		return 0, err
	}
	if historical {
		caps |= capability.CapHistorical
	}

	debug, err := p.probeDebug(ctx, nodeURL)
	if err != nil {
		return 0, err
	}
	if debug {
		caps |= capability.CapDebug
	}

	trace, err := p.probeTrace(ctx, nodeURL)
	if err != nil {
		return 0, err
	}
	if trace {
		caps |= capability.CapTrace
	}

	return caps, nil
}

func (p *EthProber) sendRPCRequest(ctx context.Context, nodeURL string, method string, params []interface{}) ([]byte, error) {
	payload, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, nodeURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	const maxProbeResponseSize = 1 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeResponseSize))
	if err != nil {
		return nil, err
	}

	return body, nil
}

func (p *EthProber) probeHistorical(ctx context.Context, nodeURL string) (bool, error) {
	body, err := p.sendRPCRequest(ctx, nodeURL, "eth_getBalance", []interface{}{
		"0xd3CdA913deB6f4967b2Ef3aa68f5A843FcAa641",
		"0x1",
	})
	if err != nil {
		return false, err
	}

	var resp struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return false, err
	}

	if resp.Error == nil {
		return true, nil
	}

	// Only "missing trie node" confirms a pruned (non-archive) node.
	// Any other error is ambiguous — surface it to the caller.
	if strings.Contains(resp.Error.Message, "missing trie node") {
		return false, nil
	}

	return false, fmt.Errorf("probeHistorical: unexpected RPC error: %s", resp.Error.Message)
}

// probeDebug checks if the node supports the debug_* namespace (Geth).
func (p *EthProber) probeDebug(ctx context.Context, nodeURL string) (bool, error) {
	body, err := p.sendRPCRequest(ctx, nodeURL, "debug_traceBlockByNumber", []interface{}{
		"latest", map[string]interface{}{},
	})
	if err != nil {
		return false, err
	}

	var resp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return false, err
	}

	// Only supported if the node returns a result with no error.
	return resp.Error == nil, nil
}

// probeTrace checks if the node supports the trace_* namespace (Erigon / Nethermind).
func (p *EthProber) probeTrace(ctx context.Context, nodeURL string) (bool, error) {
	body, err := p.sendRPCRequest(ctx, nodeURL, "trace_block", []interface{}{"latest"})
	if err != nil {
		return false, err
	}

	var resp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return false, err
	}

	return resp.Error == nil, nil
}
