package cache

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
)

// FinalityChecker classifies block identifiers into cache layers based on
// the current chain head and a configurable finality depth. SetHead is
// invoked from the health-check goroutine while Classify / IsFinalized run
// on every request handler, so head reads and writes are synchronised
// through mu.
type FinalityChecker struct {
	mu    sync.RWMutex
	depth uint64
	head  uint64
}

func NewFinalityChecker(depth uint64) *FinalityChecker {
	return &FinalityChecker{depth: depth}
}

func (fc *FinalityChecker) Head() uint64 {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	return fc.head
}

func (fc *FinalityChecker) SetHead(blockNum uint64) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.head = blockNum
}

// IsFinalized reports whether blockNum has accumulated at least fc.depth
// confirmations on top of it. The guard against uint64 underflow is the
// head >= blockNum check.
func (fc *FinalityChecker) IsFinalized(blockNum uint64) bool {
	fc.mu.RLock()
	head := fc.head
	fc.mu.RUnlock()
	// depth is set once at construction and never mutated; no lock needed.
	return head >= blockNum && head-blockNum >= fc.depth
}

// parseHexBlock decodes a "0x…"-prefixed hex string into a uint64.
func parseHexBlock(s string) (uint64, bool) {
	if !strings.HasPrefix(s, "0x") && !strings.HasPrefix(s, "0X") {
		return 0, false
	}
	n, err := strconv.ParseUint(s[2:], 16, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// blockTagLayer maps named block tags to a Layer.
// Returns (layer, true) when the tag is recognized.
func blockTagLayer(tag string) (Layer, bool) {
	switch tag {
	case "latest", "pending", "safe":
		return LayerMutable, true
	case "earliest", "finalized":
		return LayerImmutable, true
	}
	return 0, false
}

// blockParamLayer converts a single block-parameter value (tag or hex string)
// to a Layer. Falls back to LayerMutable on unrecognized input.
func (fc *FinalityChecker) blockParamLayer(raw string) Layer {
	if layer, ok := blockTagLayer(raw); ok {
		return layer
	}
	if n, ok := parseHexBlock(raw); ok {
		if fc.IsFinalized(n) {
			return LayerImmutable
		}
		return LayerMutable
	}
	return LayerMutable
}

// extractStringParam unmarshals params as a JSON array and returns the element
// at idx as a string. Returns ("", false) on any error.
func extractStringParam(params json.RawMessage, idx int) (string, bool) {
	var arr []json.RawMessage
	if err := json.Unmarshal(params, &arr); err != nil || len(arr) <= idx {
		return "", false
	}
	var s string
	if err := json.Unmarshal(arr[idx], &s); err != nil {
		return "", false
	}
	return s, true
}

// Classify returns the caching layer for a JSON-RPC method call, taking
// on-chain finality into account. It extends the static Classify in layer.go
// with dynamic block-height checks.
func (fc *FinalityChecker) Classify(method string, params json.RawMessage) Layer {
	switch method {

	// ── Hash-addressed: content-unique, re-org-proof ─────────────────────────
	case "eth_getTransactionByHash",
		"eth_getTransactionReceipt",
		"eth_getBlockByHash",
		"eth_getUncleByBlockHashAndIndex":
		return LayerImmutable

	// ── Dynamic: block identity lives in params[0] ───────────────────────────
	case "eth_getBlockByNumber":
		tag, ok := extractStringParam(params, 0)
		if !ok {
			return LayerMutable
		}
		return fc.blockParamLayer(tag)

	// ── State queries: block param lives in params[1] ────────────────────────
	case "eth_getBalance", "eth_call", "eth_estimateGas":
		tag, ok := extractStringParam(params, 1)
		if !ok {
			return LayerMutable
		}
		return fc.blockParamLayer(tag)

	// ── No block param: always live data ─────────────────────────────────────
	case "eth_blockNumber", "eth_gasPrice", "eth_feeHistory":
		return LayerMutable

	default:
		return LayerUncacheable
	}
}
