package router_test

import (
	"testing"

	"github.com/ChickenBenny/AegisRPC/internal/capability"
	"github.com/ChickenBenny/AegisRPC/internal/router"
	"github.com/ChickenBenny/AegisRPC/internal/upstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// MethodCapability — maps RPC method name → required Capability
// ---------------------------------------------------------------------------

func TestMethodCapability_Basic(t *testing.T) {
	basicMethods := []string{
		"eth_blockNumber",
		"eth_getBalance",
		"eth_call",
		"eth_sendRawTransaction",
		"net_version",
		"web3_clientVersion",
		"unknown_anything",
	}
	for _, m := range basicMethods {
		t.Run(m, func(t *testing.T) {
			assert.Equal(t, capability.CapBasic, router.MethodCapability(m))
		})
	}
}

func TestMethodCapability_Debug(t *testing.T) {
	debugMethods := []string{
		"debug_traceBlockByNumber",
		"debug_traceTransaction",
		"debug_traceCall",
	}
	for _, m := range debugMethods {
		t.Run(m, func(t *testing.T) {
			assert.Equal(t, capability.CapDebug, router.MethodCapability(m))
		})
	}
}

func TestMethodCapability_Trace(t *testing.T) {
	traceMethods := []string{
		"trace_block",
		"trace_transaction",
		"trace_call",
		"trace_filter",
	}
	for _, m := range traceMethods {
		t.Run(m, func(t *testing.T) {
			assert.Equal(t, capability.CapTrace, router.MethodCapability(m))
		})
	}
}

// ---------------------------------------------------------------------------
// Router.Route — selects the right upstream node for a given method
// ---------------------------------------------------------------------------

// helper: build a pool and pre-set each node's capabilities.
// caps is indexed in the same order as urls.
func newTestPool(t *testing.T, urls []string, caps []capability.Capability) *upstream.Pool {
	t.Helper()
	pool, err := upstream.NewPool(urls)
	require.NoError(t, err)

	nodes := pool.Nodes()
	require.Len(t, nodes, len(urls), "Nodes() must return one entry per URL")

	for i, c := range caps {
		nodes[i].SetCapabilities(c)
	}
	return pool
}

func TestRouter_Route_BasicMethodUsesAnyHealthyNode(t *testing.T) {
	pool := newTestPool(t,
		[]string{"https://node1.example.com", "https://node2.example.com"},
		[]capability.Capability{capability.CapBasic, capability.CapBasic | capability.CapDebug},
	)

	r := router.New(pool)
	node, err := r.Route("eth_blockNumber")
	require.NoError(t, err)
	require.NotNil(t, node)
}

func TestRouter_Route_DebugMethodRoutesToDebugNode(t *testing.T) {
	pool := newTestPool(t,
		[]string{"https://basic.example.com", "https://debug.example.com"},
		[]capability.Capability{capability.CapBasic, capability.CapBasic | capability.CapDebug},
	)

	r := router.New(pool)
	node, err := r.Route("debug_traceBlockByNumber")
	require.NoError(t, err)
	assert.Equal(t, "debug.example.com", node.URL.Host)
}

func TestRouter_Route_TraceMethodRoutesToTraceNode(t *testing.T) {
	pool := newTestPool(t,
		[]string{"https://basic.example.com", "https://trace.example.com"},
		[]capability.Capability{capability.CapBasic, capability.CapBasic | capability.CapTrace},
	)

	r := router.New(pool)
	node, err := r.Route("trace_block")
	require.NoError(t, err)
	assert.Equal(t, "trace.example.com", node.URL.Host)
}

func TestRouter_Route_NoCapableNode_ReturnsError(t *testing.T) {
	// Pool only has basic nodes; trace method cannot be served.
	pool := newTestPool(t,
		[]string{"https://basic.example.com"},
		[]capability.Capability{capability.CapBasic},
	)

	r := router.New(pool)
	_, err := r.Route("trace_block")
	require.Error(t, err)
}

func TestRouter_Route_UnhealthyCapableNode_ReturnsError(t *testing.T) {
	// The debug node is capable but unhealthy; no fallback exists.
	pool := newTestPool(t,
		[]string{"https://debug.example.com"},
		[]capability.Capability{capability.CapBasic | capability.CapDebug},
	)
	pool.Nodes()[0].SetHealthy(false)

	r := router.New(pool)
	_, err := r.Route("debug_traceBlockByNumber")
	require.Error(t, err)
}

func TestRouter_Route_SkipsUnhealthyCapableNode(t *testing.T) {
	// Two debug nodes; first is down. Should route to the second.
	pool := newTestPool(t,
		[]string{"https://debug1.example.com", "https://debug2.example.com"},
		[]capability.Capability{
			capability.CapBasic | capability.CapDebug,
			capability.CapBasic | capability.CapDebug,
		},
	)
	pool.Nodes()[0].SetHealthy(false)

	r := router.New(pool)
	node, err := r.Route("debug_traceBlockByNumber")
	require.NoError(t, err)
	assert.Equal(t, "debug2.example.com", node.URL.Host)
}
