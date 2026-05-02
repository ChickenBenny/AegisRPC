package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseBlockNumber(t *testing.T) {
	tests := []struct {
		input    string
		expected uint64
		wantErr  bool
	}{
		{"0x1", 1, false},
		{"0x3e8", 1000, false},
		{"0x0", 0, false},
		{"", 0, true},
		{"not-hex", 0, true},
	}

	for _, tt := range tests {
		result, err := parseBlockNumber(tt.input)
		if tt.wantErr {
			assert.Error(t, err, "input: %q", tt.input)
		} else {
			require.NoError(t, err, "input: %q", tt.input)
			assert.Equal(t, tt.expected, result, "input: %q", tt.input)
		}
	}
}

func newTestNode(t *testing.T, serverURL string) *Upstream {
	t.Helper()
	node, err := NewUpstream(serverURL)
	require.NoError(t, err)
	return node
}

func TestCheckNode_StoresBlockHeight(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","result":"0x3e8","id":1}`))
	}))
	defer server.Close()

	node := newTestNode(t, server.URL)
	checkNode(context.Background(), node, 5*time.Second)

	assert.True(t, node.IsHealthy())
	assert.Equal(t, uint64(1000), node.BlockHeight())
}

func TestCheckNode_Healthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","result":"0x1","id":1}`))
	}))
	defer server.Close()

	node := newTestNode(t, server.URL)
	checkNode(context.Background(), node, 5*time.Second)

	assert.True(t, node.IsHealthy())
}

func TestCheckNode_OversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 先寫合法 JSON prefix，後面接大量垃圾資料，超過 1MB
		w.Write([]byte(`{"jsonrpc":"2.0","result":"`))
		w.Write(make([]byte, 2*1024*1024)) // 2MB of garbage
		w.Write([]byte(`"}`))
	}))
	defer server.Close()

	node := newTestNode(t, server.URL)
	checkNode(context.Background(), node, 5*time.Second)

	// LimitReader 截斷後 JSON 不完整，應該 parse 失敗 → unhealthy
	assert.False(t, node.IsHealthy(), "oversized response should mark node unhealthy")
}

func TestCheckNode_RPCError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32000,"message":"node is syncing"},"id":1}`))
	}))
	defer server.Close()

	node := newTestNode(t, server.URL)
	checkNode(context.Background(), node, 5*time.Second)

	assert.False(t, node.IsHealthy())
}

func TestCheckNode_Unreachable(t *testing.T) {
	node := newTestNode(t, "http://127.0.0.1:1")
	checkNode(context.Background(), node, 5*time.Second)

	assert.False(t, node.IsHealthy())
}

func TestCheckNode_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`not json`))
	}))
	defer server.Close()

	node := newTestNode(t, server.URL)
	checkNode(context.Background(), node, 5*time.Second)

	assert.False(t, node.IsHealthy())
}

func TestStartHealthChecks_MarksLaggingNode(t *testing.T) {
	// node1: best block
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","result":"0x3e8","id":1}`)) // block 1000
	}))
	defer server1.Close()

	// node2: lagging far behind
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","result":"0x320","id":1}`)) // block 800
	}))
	defer server2.Close()

	pool, err := NewPool([]string{server1.URL, server2.URL})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool.StartHealthChecks(ctx, 50*time.Millisecond, 10, 5*time.Second)

	time.Sleep(150 * time.Millisecond)

	assert.True(t, pool.nodes[0].IsHealthy(), "node1 should be healthy")
	assert.False(t, pool.nodes[1].IsHealthy(), "node2 should be unhealthy (lagging 200 blocks)")
}

// TestStartHealthChecks_DoneChannelClosesOnCancel asserts the contract that
// the channel returned by StartHealthChecks is closed once the goroutine has
// exited after ctx cancellation. main relies on this for ordered shutdown,
// so regressing it would silently reintroduce the race fixed by this PR.
func TestStartHealthChecks_DoneChannelClosesOnCancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","result":"0x1","id":1}`))
	}))
	defer server.Close()

	pool, err := NewPool([]string{server.URL})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := pool.StartHealthChecks(ctx, 50*time.Millisecond, 10, 100*time.Millisecond)

	cancel()

	select {
	case <-done:
		// expected: goroutine observed ctx cancellation and exited
	case <-time.After(2 * time.Second):
		t.Fatal("done channel did not close within 2s of ctx cancel")
	}
}

func TestStartHealthChecks_UpdatesHealth(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(true)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if healthy.Load() {
			w.Write([]byte(`{"jsonrpc":"2.0","result":"0x1","id":1}`))
		} else {
			w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32000,"message":"down"},"id":1}`))
		}
	}))
	defer server.Close()

	pool, err := NewPool([]string{server.URL})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool.StartHealthChecks(ctx, 50*time.Millisecond, 10, 5*time.Second)

	time.Sleep(100 * time.Millisecond)
	assert.True(t, pool.nodes[0].IsHealthy(), "should be healthy initially")

	healthy.Store(false)
	time.Sleep(100 * time.Millisecond)
	assert.False(t, pool.nodes[0].IsHealthy(), "should be unhealthy after node goes down")

	healthy.Store(true)
	time.Sleep(100 * time.Millisecond)
	assert.True(t, pool.nodes[0].IsHealthy(), "should recover after node comes back")
}
