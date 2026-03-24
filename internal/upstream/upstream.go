package upstream

import (
	"net/url"
	"sync"
	"sync/atomic"

	"github.com/ChickenBenny/AegisRPC/internal/capability"
)

// Upstream represents a single RPC node.
type Upstream struct {
	URL          *url.URL
	blockHeight  uint64
	healthy      bool
	capabilities capability.Capability
	mu           sync.RWMutex
}

func NewUpstream(rawURL string) (*Upstream, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	return &Upstream{URL: u, healthy: true}, nil
}

func (u *Upstream) SetHealthy(healthy bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.healthy = healthy
}

func (u *Upstream) IsHealthy() bool {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.healthy
}

func (u *Upstream) SetBlockHeight(height uint64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.blockHeight = height
}

func (u *Upstream) BlockHeight() uint64 {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.blockHeight
}

func (u *Upstream) SetCapabilities(caps capability.Capability) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.capabilities = caps
}

func (u *Upstream) Capabilities() capability.Capability {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.capabilities
}

// Pool manages a list of upstream nodes.
type Pool struct {
	nodes   []*Upstream
	counter atomic.Uint64
}

func NewPool(urls []string) (*Pool, error) {
	pool := &Pool{}
	for _, rawURL := range urls {
		node, err := NewUpstream(rawURL)
		if err != nil {
			return nil, err
		}
		pool.nodes = append(pool.nodes, node)
	}
	return pool, nil
}

func (p *Pool) Nodes() []*Upstream {
	return p.nodes
}

// Next returns the first healthy upstream, or nil if none available.
func (p *Pool) Next() *Upstream {
	n := uint64(len(p.nodes))
	for i := uint64(0); i < n; i++ {
		idx := (p.counter.Add(1) - 1) % n
		node := p.nodes[idx]
		if node.IsHealthy() {
			return node
		}
	}
	return nil
}

func (p *Pool) NextWithCapability(required capability.Capability) *Upstream {
	n := uint64(len(p.nodes))
	for i := uint64(0); i < n; i++ {
		idx := (p.counter.Add(1) - 1) % n
		node := p.nodes[idx]
		if node.IsHealthy() && node.Capabilities().Has(required) {
			return node
		}
	}
	return nil
}

// markLaggingNodes marks nodes unhealthy if they lag behind the best node by more than threshold blocks.
func (p *Pool) markLaggingNodes(threshold uint64) {
	var maxHeight uint64
	for _, node := range p.nodes {
		if node.IsHealthy() && node.BlockHeight() > maxHeight {
			maxHeight = node.BlockHeight()
		}
	}

	for _, node := range p.nodes {
		if node.IsHealthy() && maxHeight > threshold && node.BlockHeight() < maxHeight-threshold {
			node.SetHealthy(false)
		}
	}
}
