package eth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ChickenBenny/AegisRPC/internal/capability"
)

type EthProber struct {
	client *http.Client
}

// NewEthProber returns a prober that defers deadline management to the
// caller's context. Audit #27: a hardcoded http.Client.Timeout fought with
// the caller-supplied context (a 30s probe ctx was getting silently
// truncated to 10s) and made the bounded-deadline guarantee in
// Pool.ProbeCapabilities unreliable.
func NewEthProber() *EthProber {
	return &EthProber{client: &http.Client{}}
}

// probeResult is the per-namespace outcome of a single capability check.
// "unknown" lets one namespace fail (rate limit, auth, transient 5xx) without
// poisoning the result for the rest — audit #10.
type probeResult int

const (
	probeUnknown probeResult = iota // ambiguous error; treat as not-supported but keep probing siblings
	probeNo                         // confirmed unsupported
	probeYes                        // confirmed supported
)

// Probe returns the union of capabilities confirmed by per-namespace probes.
// Per-namespace failures degrade to probeUnknown locally and are logged; they
// no longer drop sibling results. The error return is reserved for catastrophic
// setup failures (e.g. malformed URL) where no probe could even start.
func (p *EthProber) Probe(ctx context.Context, nodeURL string) (capability.Capability, error) {
	caps := capability.CapBasic

	if p.probeHistorical(ctx, nodeURL) == probeYes {
		caps |= capability.CapHistorical
	}
	if p.probeDebug(ctx, nodeURL) == probeYes {
		caps |= capability.CapDebug
	}
	if p.probeTrace(ctx, nodeURL) == probeYes {
		caps |= capability.CapTrace
	}
	return caps, nil
}

func (p *EthProber) sendRPCRequest(ctx context.Context, nodeURL string, method string, params []interface{}) ([]byte, error) {
	payload, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal RPC request: %w", err)
	}

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

func (p *EthProber) probeHistorical(ctx context.Context, nodeURL string) probeResult {
	// Zero address at block 1: widely available on all mainnet-compatible chains,
	// guaranteed to have a trie entry on archive nodes but absent on pruned nodes.
	body, err := p.sendRPCRequest(ctx, nodeURL, "eth_getBalance", []interface{}{
		"0x0000000000000000000000000000000000000000",
		"0x1",
	})
	if err != nil {
		slog.Warn("capability probe transport error", "namespace", "historical", "node", nodeURL, "err", err)
		return probeUnknown
	}

	var resp struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		slog.Warn("capability probe parse error", "namespace", "historical", "node", nodeURL, "err", err)
		return probeUnknown
	}

	if resp.Error == nil {
		return probeYes
	}
	if strings.Contains(resp.Error.Message, "missing trie node") {
		return probeNo
	}
	// Ambiguous (rate limit, auth, 5xx surfaced as RPC error). Treat as unknown
	// so siblings can still record their results — audit #10.
	slog.Warn("capability probe ambiguous", "namespace", "historical", "node", nodeURL, "msg", resp.Error.Message)
	return probeUnknown
}

// probeNamespace checks if a method is registered on the upstream by calling
// it with intentionally empty params. JSON-RPC servers resolve the method
// name *before* unmarshalling params, so the only two outcomes we care about
// are -32601 (method not found → namespace disabled) and -32602 (invalid
// params → method exists, namespace enabled). Anything else (rate limit,
// auth, transient 5xx) is ambiguous.
//
// This avoids the audit #11 footgun of probing with debug_traceBlockByNumber
// ("latest", {}), which actually traces every transaction in the latest block
// and is reliably rate-limited on public RPC providers.
func (p *EthProber) probeNamespace(ctx context.Context, nodeURL, namespace, method string) probeResult {
	body, err := p.sendRPCRequest(ctx, nodeURL, method, []interface{}{})
	if err != nil {
		slog.Warn("capability probe transport error", "namespace", namespace, "node", nodeURL, "err", err)
		return probeUnknown
	}

	var resp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		slog.Warn("capability probe parse error", "namespace", namespace, "node", nodeURL, "err", err)
		return probeUnknown
	}

	if resp.Error == nil {
		// Some servers tolerate empty params and return a successful result
		// (e.g. trace_block defaulting to latest). Still a positive signal.
		return probeYes
	}
	switch resp.Error.Code {
	case -32601:
		return probeNo
	case -32602, -32600:
		// Invalid params / invalid request — method dispatched, framework
		// rejected our payload. Namespace is enabled.
		return probeYes
	}
	slog.Warn("capability probe ambiguous", "namespace", namespace, "node", nodeURL, "code", resp.Error.Code, "msg", resp.Error.Message)
	return probeUnknown
}

// probeDebug checks if the node supports the debug_* namespace (Geth).
func (p *EthProber) probeDebug(ctx context.Context, nodeURL string) probeResult {
	return p.probeNamespace(ctx, nodeURL, "debug", "debug_traceTransaction")
}

// probeTrace checks if the node supports the trace_* namespace (Erigon / Nethermind).
func (p *EthProber) probeTrace(ctx context.Context, nodeURL string) probeResult {
	return p.probeNamespace(ctx, nodeURL, "trace", "trace_transaction")
}
