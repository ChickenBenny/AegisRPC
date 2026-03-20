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

func newTestNode(t *testing.T, serverURL string) *Upstream {
	t.Helper()
	node, err := NewUpstream(serverURL)
	require.NoError(t, err)
	return node
}

func TestCheckNode_Healthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","result":"0x1","id":1}`))
	}))
	defer server.Close()

	node := newTestNode(t, server.URL)
	checkNode(node)

	assert.True(t, node.IsHealthy())
}

func TestCheckNode_RPCError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32000,"message":"node is syncing"},"id":1}`))
	}))
	defer server.Close()

	node := newTestNode(t, server.URL)
	checkNode(node)

	assert.False(t, node.IsHealthy())
}

func TestCheckNode_Unreachable(t *testing.T) {
	node := newTestNode(t, "http://127.0.0.1:1")
	checkNode(node)

	assert.False(t, node.IsHealthy())
}

func TestCheckNode_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`not json`))
	}))
	defer server.Close()

	node := newTestNode(t, server.URL)
	checkNode(node)

	assert.False(t, node.IsHealthy())
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

	pool.StartHealthChecks(ctx, 50*time.Millisecond)

	time.Sleep(100 * time.Millisecond)
	assert.True(t, pool.nodes[0].IsHealthy(), "should be healthy initially")

	healthy.Store(false)
	time.Sleep(100 * time.Millisecond)
	assert.False(t, pool.nodes[0].IsHealthy(), "should be unhealthy after node goes down")

	healthy.Store(true)
	time.Sleep(100 * time.Millisecond)
	assert.True(t, pool.nodes[0].IsHealthy(), "should recover after node comes back")
}
