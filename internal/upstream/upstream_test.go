package upstream

import (
	"testing"
)

func TestPoolNext_AllHealthy(t *testing.T) {
	pool, _ := NewPool([]string{
		"https://node1.example.com",
		"https://node2.example.com",
	})

	node := pool.Next()
	if node == nil {
		t.Fatal("expected a healthy node, got nil")
	}
	if node.URL.Host != "node1.example.com" {
		t.Errorf("expected node1, got %s", node.URL.Host)
	}
}

func TestPoolNext_FirstUnhealthy(t *testing.T) {
	pool, _ := NewPool([]string{
		"https://node1.example.com",
		"https://node2.example.com",
	})

	pool.nodes[0].SetHealthy(false)

	node := pool.Next()
	if node == nil {
		t.Fatal("expected node2 as fallback, got nil")
	}
	if node.URL.Host != "node2.example.com" {
		t.Errorf("expected node2, got %s", node.URL.Host)
	}
}

func TestPoolNext_AllUnhealthy(t *testing.T) {
	pool, _ := NewPool([]string{"https://node1.example.com"})
	pool.nodes[0].SetHealthy(false)

	node := pool.Next()
	if node != nil {
		t.Errorf("expected nil, got %s", node.URL.Host)
	}
}
