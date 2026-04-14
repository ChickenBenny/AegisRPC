package cache

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ─── FinalityChecker.IsFinalized ──────────────────────────────────────────────
//
// Logic: head - blockNum >= depth → finalized
//
//	head=100, depth=12 → safe boundary = 88
//	block 88: 100-88=12 >= 12 → finalized ✓
//	block 89: 100-89=11 < 12  → NOT finalized ✗
func TestFinalityChecker_IsFinalized_Standard(t *testing.T) {
	fc := NewFinalityChecker(12)
	fc.SetHead(100)

	assert.True(t, fc.IsFinalized(0), "genesis block should always be finalized")
	assert.True(t, fc.IsFinalized(87), "block 87 is 13 deep: finalized")
	assert.True(t, fc.IsFinalized(88), "block 88 is exactly depth deep: finalized")
	assert.False(t, fc.IsFinalized(89), "block 89 is only 11 deep: NOT finalized")
	assert.False(t, fc.IsFinalized(100), "current head is NOT finalized")
}

func TestFinalityChecker_IsFinalized_YoungChain(t *testing.T) {
	// Chain just started: height < depth, so no block is considered finalized.
	fc := NewFinalityChecker(12)
	fc.SetHead(5)

	assert.False(t, fc.IsFinalized(0), "head=5 < depth=12: even block 0 is not finalized")
}

func TestFinalityChecker_IsFinalized_ExactDepthBoundary(t *testing.T) {
	// head == depth: only block 0 is exactly at the finality boundary.
	fc := NewFinalityChecker(12)
	fc.SetHead(12)

	assert.True(t, fc.IsFinalized(0), "block 0 with head=12 depth=12: 12-0=12 >= 12")
	assert.False(t, fc.IsFinalized(1), "block 1 with head=12 depth=12: 12-1=11 < 12")
}

func TestFinalityChecker_IsFinalized_ZeroHead(t *testing.T) {
	// Initial state: head=0, no block is considered finalized.
	fc := NewFinalityChecker(12)
	// SetHead not called; head defaults to 0.

	assert.False(t, fc.IsFinalized(0), "head=0: even block 0 is not finalized when chain hasn't started")
}

func TestFinalityChecker_SetHead_UpdatesFinality(t *testing.T) {
	fc := NewFinalityChecker(12)
	fc.SetHead(10) // Chain is too young; block 50 doesn't even exist.

	assert.False(t, fc.IsFinalized(0), "head=10, depth=12: not finalized yet")

	fc.SetHead(100) // Chain has grown.
	assert.True(t, fc.IsFinalized(0), "after SetHead(100): block 0 should be finalized")
	assert.True(t, fc.IsFinalized(50), "after SetHead(100): block 50 should be finalized")
}

func TestFinalityChecker_Head_ReturnsCurrentHead(t *testing.T) {
	fc := NewFinalityChecker(12)
	assert.Equal(t, uint64(0), fc.Head(), "initial head should be 0")

	fc.SetHead(42)
	assert.Equal(t, uint64(42), fc.Head())

	fc.SetHead(100)
	assert.Equal(t, uint64(100), fc.Head())
}

// ─── FinalityChecker.Classify — eth_getBlockByNumber ─────────────────────────
//
// eth_getBlockByNumber was previously LayerUncacheable (default case in Classify).
// FinalityChecker.Classify should classify it dynamically based on block height.
func TestFinalityChecker_Classify_BlockByNumber_FinalizedHex(t *testing.T) {
	fc := NewFinalityChecker(12)
	fc.SetHead(100)

	// block 0x50 = 80, head=100, 100-80=20 >= 12 → finalized
	params := json.RawMessage(`["0x50", false]`)
	assert.Equal(t, LayerImmutable, fc.Classify("eth_getBlockByNumber", params))
}

func TestFinalityChecker_Classify_BlockByNumber_RecentHex(t *testing.T) {
	fc := NewFinalityChecker(12)
	fc.SetHead(100)

	// block 0x62 = 98, head=100, 100-98=2 < 12 → NOT finalized
	params := json.RawMessage(`["0x62", false]`)
	assert.Equal(t, LayerMutable, fc.Classify("eth_getBlockByNumber", params))
}

func TestFinalityChecker_Classify_BlockByNumber_TagLatest(t *testing.T) {
	fc := NewFinalityChecker(12)
	fc.SetHead(100)

	params := json.RawMessage(`["latest", false]`)
	assert.Equal(t, LayerMutable, fc.Classify("eth_getBlockByNumber", params))
}

func TestFinalityChecker_Classify_BlockByNumber_TagPending(t *testing.T) {
	fc := NewFinalityChecker(12)
	fc.SetHead(100)

	params := json.RawMessage(`["pending", false]`)
	assert.Equal(t, LayerMutable, fc.Classify("eth_getBlockByNumber", params))
}

func TestFinalityChecker_Classify_BlockByNumber_TagSafe(t *testing.T) {
	// "safe" is Ethereum's soft-finality tag; we conservatively treat it as Mutable.
	fc := NewFinalityChecker(12)
	fc.SetHead(100)

	params := json.RawMessage(`["safe", false]`)
	assert.Equal(t, LayerMutable, fc.Classify("eth_getBlockByNumber", params))
}

func TestFinalityChecker_Classify_BlockByNumber_TagEarliest(t *testing.T) {
	// "earliest" = block 0 (genesis), which can never be re-orged → Immutable.
	fc := NewFinalityChecker(12)
	fc.SetHead(100)

	params := json.RawMessage(`["earliest", false]`)
	assert.Equal(t, LayerImmutable, fc.Classify("eth_getBlockByNumber", params))
}

func TestFinalityChecker_Classify_BlockByNumber_TagFinalized(t *testing.T) {
	// "finalized" = the node's own confirmed-final block → Immutable.
	fc := NewFinalityChecker(12)
	fc.SetHead(100)

	params := json.RawMessage(`["finalized", false]`)
	assert.Equal(t, LayerImmutable, fc.Classify("eth_getBlockByNumber", params))
}

func TestFinalityChecker_Classify_BlockByNumber_YoungChain_AlwaysMutable(t *testing.T) {
	// head=5 < depth=12: even an old block is not finalized.
	fc := NewFinalityChecker(12)
	fc.SetHead(5)

	params := json.RawMessage(`["0x02", false]`) // block 2
	assert.Equal(t, LayerMutable, fc.Classify("eth_getBlockByNumber", params))
}

func TestFinalityChecker_Classify_BlockByNumber_MalformedParams_FallsBackToMutable(t *testing.T) {
	// On parse failure, fall back conservatively to Mutable.
	fc := NewFinalityChecker(12)
	fc.SetHead(100)

	params := json.RawMessage(`"not an array"`)
	assert.Equal(t, LayerMutable, fc.Classify("eth_getBlockByNumber", params))
}

// ─── FinalityChecker.Classify — mutable methods with block param ──────────────
//
// These methods were previously always LayerMutable.
// With finality awareness, if a finalized block is specified,
// they can be upgraded to LayerImmutable (cached permanently).
func TestFinalityChecker_Classify_GetBalance_FinalizedBlock(t *testing.T) {
	fc := NewFinalityChecker(12)
	fc.SetHead(100)

	// eth_getBalance(address, blockParam) — blockParam at index 1
	// block 0x50 = 80 → finalized
	params := json.RawMessage(`["0xd3cda913deb6f279967d0f8aa74a1cf9b14f4d54", "0x50"]`)
	assert.Equal(t, LayerImmutable, fc.Classify("eth_getBalance", params))
}

func TestFinalityChecker_Classify_GetBalance_RecentBlock(t *testing.T) {
	fc := NewFinalityChecker(12)
	fc.SetHead(100)

	// block 0x61 = 97 → NOT finalized
	params := json.RawMessage(`["0xd3cda913deb6f279967d0f8aa74a1cf9b14f4d54", "0x61"]`)
	assert.Equal(t, LayerMutable, fc.Classify("eth_getBalance", params))
}

func TestFinalityChecker_Classify_GetBalance_TagLatest(t *testing.T) {
	fc := NewFinalityChecker(12)
	fc.SetHead(100)

	params := json.RawMessage(`["0xd3cda913deb6f279967d0f8aa74a1cf9b14f4d54", "latest"]`)
	assert.Equal(t, LayerMutable, fc.Classify("eth_getBalance", params))
}

func TestFinalityChecker_Classify_Call_FinalizedBlock(t *testing.T) {
	fc := NewFinalityChecker(12)
	fc.SetHead(100)

	// eth_call(callObj, blockParam) — blockParam at index 1
	params := json.RawMessage(`[{"to":"0xabc","data":"0x"}, "0x50"]`)
	assert.Equal(t, LayerImmutable, fc.Classify("eth_call", params))
}

func TestFinalityChecker_Classify_Call_RecentBlock(t *testing.T) {
	fc := NewFinalityChecker(12)
	fc.SetHead(100)

	params := json.RawMessage(`[{"to":"0xabc","data":"0x"}, "0x61"]`)
	assert.Equal(t, LayerMutable, fc.Classify("eth_call", params))
}

func TestFinalityChecker_Classify_EstimateGas_FinalizedBlock(t *testing.T) {
	fc := NewFinalityChecker(12)
	fc.SetHead(100)

	params := json.RawMessage(`[{"to":"0xabc","data":"0x"}, "0x50"]`)
	assert.Equal(t, LayerImmutable, fc.Classify("eth_estimateGas", params))
}

// ─── FinalityChecker.Classify — unchanged behaviors ───────────────────────────
//
// Hash-based queries: a hash uniquely identifies a block/tx and is re-org-proof → still Immutable.
// Uncacheable methods: unchanged.
// Mutable methods with no block param: unchanged.
func TestFinalityChecker_Classify_HashBasedMethods_AlwaysImmutable(t *testing.T) {
	fc := NewFinalityChecker(12)
	fc.SetHead(100)

	cases := []struct {
		method string
		params string
	}{
		{"eth_getTransactionByHash", `["0xabc123"]`},
		{"eth_getTransactionReceipt", `["0xabc123"]`},
		{"eth_getBlockByHash", `["0xabc123", false]`},
		{"eth_getUncleByBlockHashAndIndex", `["0xabc123", "0x0"]`},
	}

	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			assert.Equal(t, LayerImmutable,
				fc.Classify(tc.method, json.RawMessage(tc.params)),
				"hash-based methods must remain immutable",
			)
		})
	}
}

func TestFinalityChecker_Classify_UncacheableMethods_Unchanged(t *testing.T) {
	fc := NewFinalityChecker(12)
	fc.SetHead(100)

	cases := []struct {
		method string
		params string
	}{
		{"eth_sendRawTransaction", `["0xdeadbeef"]`},
		{"eth_sendTransaction", `[{}]`},
		{"debug_traceTransaction", `["0xabc"]`},
		{"trace_call", `[{}, ["vmTrace"]]`},
		{"eth_subscribe", `["newHeads"]`},
	}

	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			assert.Equal(t, LayerUncacheable,
				fc.Classify(tc.method, json.RawMessage(tc.params)),
			)
		})
	}
}

func TestFinalityChecker_Classify_NoBlockParamMutable_Unchanged(t *testing.T) {
	// eth_blockNumber and eth_gasPrice have no block param → remain Mutable.
	fc := NewFinalityChecker(12)
	fc.SetHead(100)

	assert.Equal(t, LayerMutable, fc.Classify("eth_blockNumber", json.RawMessage(`[]`)))
	assert.Equal(t, LayerMutable, fc.Classify("eth_gasPrice", json.RawMessage(`[]`)))
}
