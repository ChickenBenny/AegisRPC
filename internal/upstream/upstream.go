package upstream

import (
	"fmt"
	"net/url"
	"strings"
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
	cleanURL, caps, err := ParseAnnotatedURL(rawURL)
	if err != nil {
		return nil, err
	}

	u, err := url.Parse(cleanURL)
	if err != nil {
		return nil, err
	}

	// Unannotated nodes get CapBasic so NextWithCapability(CapBasic) can route
	// to them. Annotated nodes already encode their capabilities explicitly.
	if caps == 0 {
		caps = capability.CapBasic
	}

	return &Upstream{URL: u, healthy: true, capabilities: caps}, nil
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

// Nodes returns the pool's node list for inspection and probe setup.
// Callers must not modify the returned slice.
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
	start := p.counter.Load()
	for i := uint64(0); i < n; i++ {
		idx := (start + i) % n
		node := p.nodes[idx]
		if node.IsHealthy() && node.Capabilities().Has(required) {
			p.counter.Add(i + 1)
			return node
		}
	}
	return nil
}

// markLaggingNodes marks nodes unhealthy if they lag behind the best node by more than threshold
// blocks. It returns the best observed block height across all healthy nodes.
func (p *Pool) markLaggingNodes(threshold uint64) uint64 {
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
	return maxHeight
}

func ParseAnnotatedURL(rawUrl string) (string, capability.Capability, error) {
	startIdx := strings.Index(rawUrl, "[")
	if startIdx == -1 {
		return rawUrl, capability.Capability(0), nil
	}

	endIdx := strings.Index(rawUrl, "]")
	if endIdx == -1 || endIdx < startIdx {
		return "", capability.Capability(0), fmt.Errorf("invalid annotated URL: missing closing ']' in %s", rawUrl)
	}

	capStr := rawUrl[startIdx+1 : endIdx]
	if strings.TrimSpace(capStr) == "" {
		return "", capability.Capability(0), fmt.Errorf("invalid annotated URL: empty brackets in %s", rawUrl)
	}

	caps := capability.CapBasic
	for _, c := range strings.Split(capStr, ",") {
		switch strings.TrimSpace(c) {
		case "basic":
			caps |= capability.CapBasic
		case "historical":
			caps |= capability.CapHistorical
		case "debug":
			caps |= capability.CapDebug
		case "trace":
			caps |= capability.CapTrace
		default:
			return "", capability.Capability(0), fmt.Errorf("unknown capability '%s' in %s", c, rawUrl)
		}
	}

	return rawUrl[:startIdx], caps, nil
}
