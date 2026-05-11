package upstream

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChickenBenny/AegisRPC/internal/capability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubProber returns a canned capability per URL. If the URL is not in the
// map, Probe returns CapBasic alone (mirrors EthProber behaviour for an
// uncooperative upstream).
type stubProber struct {
	mu        sync.Mutex
	results   map[string]capability.Capability
	errors    map[string]error
	delay     time.Duration
	callCount atomic.Int64
}

func (s *stubProber) Probe(ctx context.Context, nodeURL string) (capability.Capability, error) {
	s.callCount.Add(1)
	if s.delay > 0 {
		select {
		case <-ctx.Done():
			return capability.CapBasic, ctx.Err()
		case <-time.After(s.delay):
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err, ok := s.errors[nodeURL]; ok {
		return capability.CapBasic, err
	}
	if caps, ok := s.results[nodeURL]; ok {
		return caps, nil
	}
	return capability.CapBasic, nil
}

func TestPoolNext_AllHealthy(t *testing.T) {
	pool, err := NewPool([]string{
		"https://node1.example.com",
		"https://node2.example.com",
	})
	require.NoError(t, err)

	node := pool.Next()
	require.NotNil(t, node, "expected a healthy node, got nil")
	assert.Equal(t, "node1.example.com", node.URL.Host)
}

func TestPoolNext_FirstUnhealthy(t *testing.T) {
	pool, err := NewPool([]string{
		"https://node1.example.com",
		"https://node2.example.com",
	})
	require.NoError(t, err)

	pool.nodes[0].SetHealthy(false)

	node := pool.Next()
	require.NotNil(t, node, "expected node2 as fallback, got nil")
	assert.Equal(t, "node2.example.com", node.URL.Host)
}

func TestPoolNext_AllUnhealthy(t *testing.T) {
	pool, err := NewPool([]string{"https://node1.example.com"})
	require.NoError(t, err)

	pool.nodes[0].SetHealthy(false)

	assert.Nil(t, pool.Next())
}

func TestPoolNext_RoundRobin(t *testing.T) {
	pool, err := NewPool([]string{
		"https://node1.example.com",
		"https://node2.example.com",
		"https://node3.example.com",
	})
	require.NoError(t, err)

	// 三個請求應該輪流打到不同節點
	assert.Equal(t, "node1.example.com", pool.Next().URL.Host)
	assert.Equal(t, "node2.example.com", pool.Next().URL.Host)
	assert.Equal(t, "node3.example.com", pool.Next().URL.Host)
	assert.Equal(t, "node1.example.com", pool.Next().URL.Host) // 回到頭
}

func TestPoolNext_RoundRobin_SkipsUnhealthy(t *testing.T) {
	pool, err := NewPool([]string{
		"https://node1.example.com",
		"https://node2.example.com",
		"https://node3.example.com",
	})
	require.NoError(t, err)

	pool.nodes[1].SetHealthy(false) // node2 掛掉

	assert.Equal(t, "node1.example.com", pool.Next().URL.Host)
	assert.Equal(t, "node3.example.com", pool.Next().URL.Host) // 跳過 node2
	assert.Equal(t, "node1.example.com", pool.Next().URL.Host)
}

// ---------------------------------------------------------------------------
// Pool.NextWithCapability
// ---------------------------------------------------------------------------

func TestPool_NextWithCapability_ReturnsMatchingNode(t *testing.T) {
	pool, err := NewPool([]string{
		"https://basic.example.com",
		"https://debug.example.com",
	})
	require.NoError(t, err)

	pool.nodes[0].SetCapabilities(capability.CapBasic)
	pool.nodes[1].SetCapabilities(capability.CapBasic | capability.CapDebug)

	node := pool.NextWithCapability(capability.CapDebug)
	require.NotNil(t, node)
	assert.Equal(t, "debug.example.com", node.URL.Host)
}

func TestPool_NextWithCapability_NoneQualified_ReturnsNil(t *testing.T) {
	pool, err := NewPool([]string{"https://basic.example.com"})
	require.NoError(t, err)

	pool.nodes[0].SetCapabilities(capability.CapBasic)

	assert.Nil(t, pool.NextWithCapability(capability.CapTrace))
}

func TestPool_NextWithCapability_SkipsUnhealthyNode(t *testing.T) {
	pool, err := NewPool([]string{
		"https://debug1.example.com",
		"https://debug2.example.com",
	})
	require.NoError(t, err)

	pool.nodes[0].SetCapabilities(capability.CapBasic | capability.CapDebug)
	pool.nodes[1].SetCapabilities(capability.CapBasic | capability.CapDebug)
	pool.nodes[0].SetHealthy(false)

	node := pool.NextWithCapability(capability.CapDebug)
	require.NotNil(t, node)
	assert.Equal(t, "debug2.example.com", node.URL.Host)
}

func TestPool_Nodes_ReturnsAllNodes(t *testing.T) {
	pool, err := NewPool([]string{
		"https://node1.example.com",
		"https://node2.example.com",
	})
	require.NoError(t, err)

	nodes := pool.Nodes()
	assert.Len(t, nodes, 2)
	assert.Equal(t, "node1.example.com", nodes[0].URL.Host)
	assert.Equal(t, "node2.example.com", nodes[1].URL.Host)
}

func TestMarkLaggingNodes_MarksBehindNodes(t *testing.T) {
	pool, err := NewPool([]string{
		"https://node1.example.com",
		"https://node2.example.com",
		"https://node3.example.com",
	})
	require.NoError(t, err)

	pool.nodes[0].SetBlockHeight(1000)
	pool.nodes[1].SetBlockHeight(1000)
	pool.nodes[2].SetBlockHeight(850) // 落後 150

	pool.markLaggingNodes(10) // 門檻：落後超過 10 個 block

	assert.True(t, pool.nodes[0].IsHealthy())
	assert.True(t, pool.nodes[1].IsHealthy())
	assert.False(t, pool.nodes[2].IsHealthy(), "node3 should be marked unhealthy")
}

func TestMarkLaggingNodes_AllSameHeight(t *testing.T) {
	pool, err := NewPool([]string{
		"https://node1.example.com",
		"https://node2.example.com",
	})
	require.NoError(t, err)

	pool.nodes[0].SetBlockHeight(1000)
	pool.nodes[1].SetBlockHeight(1000)

	pool.markLaggingNodes(10)

	assert.True(t, pool.nodes[0].IsHealthy())
	assert.True(t, pool.nodes[1].IsHealthy())
}

func TestMarkLaggingNodes_WithinThreshold(t *testing.T) {
	pool, err := NewPool([]string{
		"https://node1.example.com",
		"https://node2.example.com",
	})
	require.NoError(t, err)

	pool.nodes[0].SetBlockHeight(1000)
	pool.nodes[1].SetBlockHeight(995) // 落後 5，門檻是 10

	pool.markLaggingNodes(10)

	assert.True(t, pool.nodes[0].IsHealthy())
	assert.True(t, pool.nodes[1].IsHealthy(), "node2 is within threshold, should stay healthy")
}

// ---------------------------------------------------------------------------
// ParseAnnotatedURL — parses "https://host[cap1,cap2]" into URL + Capability
// ---------------------------------------------------------------------------

func TestParseAnnotatedURL_NoAnnotation(t *testing.T) {
	url, caps, err := ParseAnnotatedURL("https://node.example.com")
	require.NoError(t, err)
	assert.Equal(t, "https://node.example.com", url)
	assert.Equal(t, capability.Capability(0), caps, "no annotation means caps not set (prober will handle)")
}

func TestParseAnnotatedURL_Debug(t *testing.T) {
	url, caps, err := ParseAnnotatedURL("https://node.example.com[debug]")
	require.NoError(t, err)
	assert.Equal(t, "https://node.example.com", url)
	assert.Equal(t, capability.CapBasic|capability.CapDebug, caps)
}

func TestParseAnnotatedURL_Trace(t *testing.T) {
	url, caps, err := ParseAnnotatedURL("https://node.example.com[trace]")
	require.NoError(t, err)
	assert.Equal(t, "https://node.example.com", url)
	assert.Equal(t, capability.CapBasic|capability.CapTrace, caps)
}

func TestParseAnnotatedURL_Historical(t *testing.T) {
	url, caps, err := ParseAnnotatedURL("https://node.example.com[historical]")
	require.NoError(t, err)
	assert.Equal(t, "https://node.example.com", url)
	assert.Equal(t, capability.CapBasic|capability.CapHistorical, caps)
}

func TestParseAnnotatedURL_MultipleCaps(t *testing.T) {
	url, caps, err := ParseAnnotatedURL("https://node.example.com[debug,trace]")
	require.NoError(t, err)
	assert.Equal(t, "https://node.example.com", url)
	assert.Equal(t, capability.CapBasic|capability.CapDebug|capability.CapTrace, caps)
}

func TestParseAnnotatedURL_TrimsSpaces(t *testing.T) {
	url, caps, err := ParseAnnotatedURL("https://node.example.com[debug, trace]")
	require.NoError(t, err)
	assert.Equal(t, "https://node.example.com", url)
	assert.Equal(t, capability.CapBasic|capability.CapDebug|capability.CapTrace, caps)
}

func TestParseAnnotatedURL_UnknownCap_ReturnsError(t *testing.T) {
	_, _, err := ParseAnnotatedURL("https://node.example.com[unknown]")
	require.Error(t, err)
}

func TestParseAnnotatedURL_EmptyBrackets_ReturnsError(t *testing.T) {
	_, _, err := ParseAnnotatedURL("https://node.example.com[]")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// NewPool — annotated URLs set capabilities on nodes
// ---------------------------------------------------------------------------

func TestNewPool_AnnotatedURL_SetsCapabilities(t *testing.T) {
	pool, err := NewPool([]string{
		"https://debug.example.com[debug]",
		"https://trace.example.com[trace]",
	})
	require.NoError(t, err)

	nodes := pool.Nodes()
	assert.Equal(t, capability.CapBasic|capability.CapDebug, nodes[0].Capabilities())
	assert.Equal(t, capability.CapBasic|capability.CapTrace, nodes[1].Capabilities())
}

func TestNewPool_MixedAnnotated_UnannotatedHasBasicCap(t *testing.T) {
	pool, err := NewPool([]string{
		"https://plain.example.com",
		"https://debug.example.com[debug]",
	})
	require.NoError(t, err)

	nodes := pool.Nodes()
	// Unannotated nodes default to CapBasic so NextWithCapability(CapBasic) can route to them.
	assert.Equal(t, capability.CapBasic, nodes[0].Capabilities(), "unannotated node should default to CapBasic")
	assert.Equal(t, capability.CapBasic|capability.CapDebug, nodes[1].Capabilities())
}

func TestNewPool_InvalidAnnotation_ReturnsError(t *testing.T) {
	_, err := NewPool([]string{"https://node.example.com[bad_cap]"})
	require.Error(t, err)
}

func TestPool_ProbeCapabilities_ORMergesIntoExistingCaps(t *testing.T) {
	pool, err := NewPool([]string{
		"https://archive.example.com[basic,historical]",
		"https://plain.example.com",
	})
	require.NoError(t, err)

	prober := &stubProber{
		results: map[string]capability.Capability{
			"https://archive.example.com": capability.CapBasic | capability.CapDebug,
			"https://plain.example.com":   capability.CapBasic | capability.CapHistorical | capability.CapDebug,
		},
	}
	pool.ProbeCapabilities(context.Background(), prober)

	nodes := pool.Nodes()
	// archive: annotation gave basic+historical, probe added debug → union.
	assert.True(t, nodes[0].Capabilities().Has(capability.CapHistorical), "annotation must be preserved")
	assert.True(t, nodes[0].Capabilities().Has(capability.CapDebug), "probe additions must be applied")
	// plain: started with just CapBasic, probe enriches to historical+debug.
	assert.True(t, nodes[1].Capabilities().Has(capability.CapHistorical))
	assert.True(t, nodes[1].Capabilities().Has(capability.CapDebug))
}

// Annotation is a floor: even if probe omits a capability the operator
// declared, that capability stays.
func TestPool_ProbeCapabilities_AnnotationIsFloor(t *testing.T) {
	pool, err := NewPool([]string{
		"https://archive.example.com[basic,historical]",
	})
	require.NoError(t, err)

	prober := &stubProber{
		results: map[string]capability.Capability{
			"https://archive.example.com": capability.CapBasic, // probe failed to confirm historical
		},
	}
	pool.ProbeCapabilities(context.Background(), prober)

	caps := pool.Nodes()[0].Capabilities()
	assert.True(t, caps.Has(capability.CapHistorical),
		"annotation declared historical; probe disagreement must not downgrade")
}

// Probes run in parallel, not sequentially. Three nodes × 200ms each should
// complete in well under 600ms when run concurrently.
func TestPool_ProbeCapabilities_Parallel(t *testing.T) {
	pool, err := NewPool([]string{
		"https://a.example.com",
		"https://b.example.com",
		"https://c.example.com",
	})
	require.NoError(t, err)

	prober := &stubProber{delay: 200 * time.Millisecond}
	start := time.Now()
	pool.ProbeCapabilities(context.Background(), prober)
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 500*time.Millisecond, "probes must run concurrently")
	assert.Equal(t, int64(3), prober.callCount.Load())
}

// Per-node probe error leaves that node's caps untouched but does not
// affect other nodes.
func TestPool_ProbeCapabilities_PerNodeFailureIsolated(t *testing.T) {
	pool, err := NewPool([]string{
		"https://good.example.com",
		"https://bad.example.com[basic,debug]",
	})
	require.NoError(t, err)

	prober := &stubProber{
		results: map[string]capability.Capability{
			"https://good.example.com": capability.CapBasic | capability.CapHistorical,
		},
		errors: map[string]error{
			"https://bad.example.com": assertError("synthetic probe failure"),
		},
	}
	pool.ProbeCapabilities(context.Background(), prober)

	good := pool.Nodes()[0].Capabilities()
	bad := pool.Nodes()[1].Capabilities()

	assert.True(t, good.Has(capability.CapHistorical), "good node still gets enriched")
	assert.True(t, bad.Has(capability.CapDebug), "bad node keeps its annotation despite probe error")
	assert.False(t, bad.Has(capability.CapHistorical), "probe never claimed historical for bad node")
}

// ProbeCapabilities returns when the ctx deadline fires, even if individual
// probes are slower. Late results that arrive after deadline must not
// extend the wall-clock cost for the caller.
func TestPool_ProbeCapabilities_ContextDeadlineBounds(t *testing.T) {
	pool, err := NewPool([]string{"https://slow.example.com"})
	require.NoError(t, err)

	prober := &stubProber{delay: 2 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	pool.ProbeCapabilities(ctx, prober)
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 500*time.Millisecond, "must honour ctx deadline")
}

// Test-helper error type.
type stringError string

func (e stringError) Error() string { return string(e) }
func assertError(s string) error    { return stringError(s) }
