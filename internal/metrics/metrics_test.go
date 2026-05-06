package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeMethod_KnownMethod_ReturnsAsIs(t *testing.T) {
	// Spot-check a few entries from each namespace in the allowlist; an
	// exhaustive check would mostly assert that the literal map equals
	// itself, which is not informative.
	for _, m := range []string{
		"eth_call",
		"eth_blockNumber",
		"eth_sendRawTransaction",
		"net_version",
		"web3_clientVersion",
		"trace_block",
	} {
		assert.Equal(t, m, NormalizeMethod(m), "known method %q should pass through", m)
	}
}

func TestNormalizeMethod_UnknownMethod_ReturnsOther(t *testing.T) {
	// A mix of inputs an attacker might use: random hex, fake namespaces,
	// almost-valid spellings, mixed case. All must collapse to "other".
	for _, m := range []string{
		"random_garbage_method",
		"eth_nonexistent",        // namespace is right, name is not
		"Eth_Call",               // case must match — Prometheus labels are case-sensitive
		"0xdeadbeef",             // hex blob
		"sendTransaction",        // Solana-style; not eth_*
		"' OR 1=1 --",            // SQLi probe in method field
	} {
		assert.Equal(t, "other", NormalizeMethod(m), "unknown method %q should normalize to other", m)
	}
}

func TestNormalizeMethod_EmptyString_ReturnsOther(t *testing.T) {
	// handler.go initialises method to "unknown" if the body cannot be
	// parsed. An empty string can also reach NormalizeMethod if some
	// future caller forgets to default it; cardinality protection must
	// hold either way.
	assert.Equal(t, "other", NormalizeMethod(""))
}
