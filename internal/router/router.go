package router

import (
	"fmt"
	"strings"

	"github.com/ChickenBenny/AegisRPC/internal/capability"
	"github.com/ChickenBenny/AegisRPC/internal/upstream"
)

type Router struct {
	pool *upstream.Pool
}

func New(pool *upstream.Pool) *Router {
	return &Router{
		pool: pool,
	}
}

func (r *Router) Route(method string) (*upstream.Upstream, error) {
	required := MethodCapability(method)
	node := r.pool.NextWithCapability(required)
	if node == nil {
		return nil, fmt.Errorf("no upstream available with required capability: %v", required)
	}
	return node, nil
}

func MethodCapability(method string) capability.Capability {
	switch {
	case strings.HasPrefix(method, "debug_"):
		return capability.CapDebug
	case strings.HasPrefix(method, "trace_"):
		return capability.CapTrace
	default:
		return capability.CapBasic
	}
}
