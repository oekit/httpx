package httpx

import (
	"context"
	"fmt"
	"testing"

	"github.com/oekit/httpx/loadbalance"
)

func consistentNodes(addrs ...string) []loadbalance.Node {
	return NodesFromEndpoints(addrs)
}

func pickAll(t *testing.T, b *ConsistentHashSelector, nodeAddrs []string, keys []string) map[string]string {
	t.Helper()
	nodes := consistentNodes(nodeAddrs...)
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		n, _, err := b.Select(context.Background(), nodes, loadbalance.WithKey(k))
		if err != nil {
			t.Fatalf("select %q: %v", k, err)
		}
		out[k] = n.Address()
	}
	return out
}

func testKeys(n int) []string {
	keys := make([]string, 0, n)
	for i := 0; i < n; i++ {
		keys = append(keys, fmt.Sprintf("tenant-%03d", i))
	}
	return keys
}

func TestConsistentHash_Deterministic(t *testing.T) {
	b, err := NewConsistentHashSelector()
	if err != nil {
		t.Fatal(err)
	}
	endpoints := []string{"10.0.0.1:8080", "10.0.0.2:8080", "10.0.0.3:8080"}
	first := pickAll(t, b, endpoints, testKeys(50))
	for round := 0; round < 3; round++ {
		for k, want := range first {
			if got := pickAll(t, b, endpoints, []string{k})[k]; got != want {
				t.Fatalf("key %s: expected %s, got %s", k, want, got)
			}
		}
	}
}

func TestConsistentHash_SingleNode(t *testing.T) {
	b, _ := NewConsistentHashSelector()
	nodes := consistentNodes("10.0.0.1:8080")
	for _, k := range testKeys(10) {
		n, _, err := b.Select(context.Background(), nodes, loadbalance.WithKey(k))
		if err != nil || n.Address() != "10.0.0.1:8080" {
			t.Fatalf("single node select broken: %v %v", n, err)
		}
	}
}

func TestConsistentHash_EmptyNodes(t *testing.T) {
	b, _ := NewConsistentHashSelector()
	if _, _, err := b.Select(context.Background(), nil, loadbalance.WithKey("k")); !errNoAvailable(err) {
		t.Fatalf("err = %v, want ErrNoAvailable", err)
	}
}

func errNoAvailable(err error) bool {
	return err != nil && err.Error() == "loadbalance: no available node"
}

// TestConsistentHash_StickyAfterRemoval 钉住一致性核心语义：移除一个节点后
// 其余键的映射保持稳定（只发生相邻重映射，且大部分键仍在原节点）。
func TestConsistentHash_StickyAfterRemoval(t *testing.T) {
	b, _ := NewConsistentHashSelector()
	before := pickAll(t, b, []string{"10.0.0.1:8080", "10.0.0.2:8080", "10.0.0.3:8080"}, testKeys(200))
	after := pickAll(t, b, []string{"10.0.0.1:8080", "10.0.0.3:8080"}, testKeys(200))

	sticky := 0
	total := 0
	for k, oldAddr := range before {
		if oldAddr == "10.0.0.2:8080" {
			continue // 被移除节点上的键必然重映射
		}
		total++
		if after[k] == oldAddr {
			sticky++
		}
	}
	if total == 0 {
		t.Fatal("no surviving keys sampled")
	}
	ratio := float64(sticky) / float64(total)
	if ratio < 0.9 {
		t.Fatalf("expected >=90%% sticky after removal, got %.3f (%d/%d)", ratio, sticky, total)
	}
}

// TestConsistentHash_WeightedReplicas 权重折算虚拟副本（白盒精确断言 +
// 黑盒不反向；黑盒不断言比例带——FNV-1a 对顺序数字后缀键有已知聚簇性，
// 与 v0.3.x 一致按非反向断言）。
func TestConsistentHash_WeightedReplicas(t *testing.T) {
	// 白盒：副本折算精确到均值归一比例（权重 3/1 × base 100 → 150/50）
	nodes := loadbalance.NodesFrom([]string{"a:1", "b:2"},
		func(s string) string { return s },
		func(s string) float64 {
			if s == "a:1" {
				return 3
			}
			return 1
		},
		nil,
		func(string) string { return "svc" },
	)
	ring := buildConsistentRing(nodes, 100, defaultConsistentHash)
	var c0, c1 int
	for _, n := range ring {
		if n.idx == 0 {
			c0++
		} else {
			c1++
		}
	}
	if c0 != 150 || c1 != 50 {
		t.Fatalf("expected virtual replicas 150/50, got %d/%d", c0, c1)
	}

	// 全零/负权重退化为等权
	eq := loadbalance.NodesFrom([]string{"a:1", "b:2"},
		func(s string) string { return s },
		func(string) float64 { return 0 },
		nil,
		func(string) string { return "svc" },
	)
	ring = buildConsistentRing(eq, 100, defaultConsistentHash)
	c0, c1 = 0, 0
	for _, n := range ring {
		if n.idx == 0 {
			c0++
		} else {
			c1++
		}
	}
	if c0 != 100 || c1 != 100 {
		t.Fatalf("expected equal replicas fallback 100/100, got %d/%d", c0, c1)
	}

	// 黑盒：大副本数下负载不得反向（权重 3 的节点不应少于权重 1 的）
	sel, err := NewConsistentHashSelector(WithConsistentReplicas(500))
	if err != nil {
		t.Fatal(err)
	}
	light := []string{"10.0.0.1:8080", "10.0.0.2:8080"}
	wnodes := loadbalance.NodesFrom(light,
		func(s string) string { return s },
		func(s string) float64 {
			if s == light[0] {
				return 3
			}
			return 1
		},
		nil,
		func(string) string { return "svc" },
	)

	keys := testKeys(4000)
	counts := make(map[string]int)
	for _, k := range keys {
		n, _, err := sel.Select(context.Background(), wnodes, loadbalance.WithKey(k))
		if err != nil {
			t.Fatal(err)
		}
		counts[n.Address()]++
	}
	if counts[light[0]] < counts[light[1]] {
		t.Fatalf("3x weighted node under-loaded: %d vs %d", counts[light[0]], counts[light[1]])
	}
}
