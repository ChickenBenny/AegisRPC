package capability

import "context"

// Prober detects what capabilities a node supports.
// Each chain implements its own Prober.
type Prober interface {
	Probe(ctx context.Context, nodeURL string) (Capability, error)
}
