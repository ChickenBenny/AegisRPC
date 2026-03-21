package upstream

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
