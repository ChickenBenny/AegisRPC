package upstream

import (
	"net/url"
	"sync"
)

// Upstream represents a single RPC node.
type Upstream struct {
	URL     *url.URL
	Healthy bool
	mu      sync.RWMutex
}

func NewUpstream(rawURL string) (*Upstream, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	return &Upstream{URL: u, Healthy: true}, nil
}

func (u *Upstream) SetHealthy(healthy bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.Healthy = healthy
}

func (u *Upstream) IsHealthy() bool {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.Healthy
}

// Pool manages a list of upstream nodes.
type Pool struct {
	nodes []*Upstream
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

// Next returns the first healthy upstream, or nil if none available.
func (p *Pool) Next() *Upstream {
	for _, node := range p.nodes {
		if node.IsHealthy() {
			return node
		}
	}
	return nil
}
