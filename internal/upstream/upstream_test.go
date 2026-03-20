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
