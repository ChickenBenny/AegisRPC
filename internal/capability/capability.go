package capability

// Capability represents what an RPC node is able to serve.
// Using a bitmask allows nodes to hold multiple capabilities simultaneously.
//
//	node.capabilities = CapBasic | CapArchive  →  full + historical queries
type Capability uint16

const (
	CapBasic      Capability = 1 << iota // Standard RPC (eth_blockNumber, eth_getBalance, ...)
	CapHistorical                         // Historical state queries (Archive on EVM, Geyser on Solana, ...)
	CapDebug                              // debug_* namespace (Geth)
	CapTrace                              // trace_* namespace (Erigon / Nethermind)
)

// String returns a human-readable list of capabilities.
func (c Capability) String() string {
	if c == 0 {
		return "none"
	}
	names := []struct {
		cap  Capability
		name string
	}{
		{CapBasic, "basic"},
		{CapHistorical, "historical"},
		{CapDebug, "debug"},
		{CapTrace, "trace"},
	}
	result := ""
	for _, n := range names {
		if c&n.cap != 0 {
			if result != "" {
				result += ","
			}
			result += n.name
		}
	}
	return result
}

// Has reports whether c includes all bits of other.
func (c Capability) Has(other Capability) bool {
	return c&other == other
}
