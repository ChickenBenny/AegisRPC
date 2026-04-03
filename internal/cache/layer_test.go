package cache

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassify_ImmutableMethods(t *testing.T) {
	immutable := []string{
		"eth_getTransactionByHash",
		"eth_getTransactionReceipt",
		"eth_getBlockByHash",
		"eth_getUncleByBlockHashAndIndex",
	}
	for _, method := range immutable {
		assert.Equal(t, LayerImmutable, Classify(method), "expected Immutable for %s", method)
	}
}

func TestClassify_MutableMethods(t *testing.T) {
	mutable := []string{
		"eth_blockNumber",
		"eth_gasPrice",
		"eth_getBalance",
		"eth_call",
		"eth_estimateGas",
		"eth_feeHistory",
	}
	for _, method := range mutable {
		assert.Equal(t, LayerMutable, Classify(method), "expected Mutable for %s", method)
	}
}

func TestClassify_UncacheableMethods(t *testing.T) {
	uncacheable := []string{
		"eth_sendRawTransaction",
		"eth_sendTransaction",
		"eth_sign",
		"net_version", // 不值得 cache
		"debug_traceTransaction",
		"trace_block",
	}
	for _, method := range uncacheable {
		assert.Equal(t, LayerUncacheable, Classify(method), "expected Uncacheable for %s", method)
	}
}

func TestClassify_UnknownMethod_IsUncacheable(t *testing.T) {
	assert.Equal(t, LayerUncacheable, Classify("some_unknownMethod"))
}
