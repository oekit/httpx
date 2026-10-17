package httpx

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
)

func newConsistentRequest(path string) *http.Request {
	req, _ := http.NewRequest(http.MethodGet, "http://svc"+path, nil)
	return req
}

// pickAll 用 keys 对 endpoints 全量路由，返回 key → endpoint 映射。
func pickAll(b *ConsistentHashBalance, endpoints []string, keys []string) map[string]string {
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		out[k] = b.Pick(endpoints, newConsistentRequest("/"+k))
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
	b := NewConsistentHashBalance()
	endpoints := []string{"10.0.0.1:8080", "10.0.0.2:8080", "10.0.0.3:8080"}
	first := pickAll(b, endpoints, testKeys(50))
	for round := 0; round < 3; round++ {
		for k, want := range first {
			if got := b.Pick(endpoints, newConsistentRequest("/"+k)); got != want {
				t.Fatalf("key %s: expected %s, got %s", k, want, got)
			}
		}
	}
}

func TestConsistentHash_Distribution(t *testing.T) {
	b := NewConsistentHashBalance()
	endpoints := []string{"10.0.0.1:8080", "10.0.0.2:8080", "10.0.0.3:8080", "10.0.0.4:8080"}
	mapping := pickAll(b, endpoints, testKeys(1000))

	counts := make(map[string]int)
	for _, ep := range mapping {
		counts[ep]++
	}
	if len(counts) != len(endpoints) {
		t.Fatalf("expected all %d endpoints used, got %d", len(endpoints), len(counts))
	}
	for ep, n := range counts {
		if n < 1000/len(endpoints)/4 {
			t.Fatalf("endpoint %s under-loaded: %d/1000", ep, n)
		}
	}
}

func TestConsistentHash_MinimalRemapOnAdd(t *testing.T) {
	b := NewConsistentHashBalance()
	before := make([]string, 0, 4) // 预分配：后面会 append 新节点（prealloc）
	before = append(before, "10.0.0.1:8080", "10.0.0.2:8080", "10.0.0.3:8080")
	keys := testKeys(1000)
	old := pickAll(b, before, keys)

	after := pickAll(b, append(before, "10.0.0.4:8080"), keys)
	moved := 0
	for k, oldEp := range old {
		if after[k] != oldEp {
			moved++
		}
	}
	// 3→4 节点理论迁移约 1/4；一致性哈希允许略偏，超过 1/2 即为坏环
	if moved > len(keys)/2 {
		t.Fatalf("adding one endpoint moved %d/%d keys, expected ~1/4", moved, len(keys))
	}
}

func TestConsistentHash_RemovedEndpointGone(t *testing.T) {
	b := NewConsistentHashBalance()
	before := []string{"10.0.0.1:8080", "10.0.0.2:8080", "10.0.0.3:8080", "10.0.0.4:8080"}
	keys := testKeys(1000)
	old := pickAll(b, before, keys)

	after := pickAll(b, []string{"10.0.0.1:8080", "10.0.0.3:8080"}, keys)
	for k, newEp := range after {
		if newEp != "10.0.0.1:8080" && newEp != "10.0.0.3:8080" {
			t.Fatalf("key %s routed to removed endpoint %s", k, newEp)
		}
	}
	moved := 0
	for k, oldEp := range old {
		if oldEp == "10.0.0.2:8080" || oldEp == "10.0.0.4:8080" {
			continue // 原本就在被摘节点上的 key 必然迁移
		}
		if after[k] != oldEp {
			moved++
		}
	}
	if moved > len(keys)/2 {
		t.Fatalf("removing two endpoints moved %d surviving keys, expected ~0", moved)
	}
}

func TestConsistentHash_WeightedReplicas(t *testing.T) {
	// 白盒：副本折算精确到均值归一比例（3/2 与 1/2 × base 100 → 150/50）
	ring := buildConsistentRing([]string{"a:1", "b:2"}, []float64{3, 1}, 100, defaultConsistentHash)
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
	ring = buildConsistentRing([]string{"a:1", "b:2"}, []float64{0, -1}, 100, defaultConsistentHash)
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
	b := NewConsistentHashBalance(WithConsistentReplicas(500))
	light := []string{"10.0.0.1:8080", "10.0.0.2:8080"}
	b.SetWeights([]float64{3, 1})

	keys := testKeys(4000)
	counts := make(map[string]int)
	for _, k := range keys {
		counts[b.Pick(light, newConsistentRequest("/"+k))]++
	}
	if counts[light[0]] < counts[light[1]] {
		t.Fatalf("3x weighted endpoint under-loaded: %d vs %d", counts[light[0]], counts[light[1]])
	}
}

func TestConsistentHash_CustomKeyFunc(t *testing.T) {
	b := NewConsistentHashBalance(WithConsistentKeyFunc(func(req *http.Request) string {
		return req.Header.Get("X-Tenant")
	}))
	endpoints := []string{"10.0.0.1:8080", "10.0.0.2:8080", "10.0.0.3:8080"}

	req1, _ := http.NewRequest(http.MethodGet, "http://svc/a", nil)
	req2, _ := http.NewRequest(http.MethodGet, "http://svc/b", nil)
	req1.Header.Set("X-Tenant", "acme")
	req2.Header.Set("X-Tenant", "acme")

	if got := b.Pick(endpoints, req1); got != b.Pick(endpoints, req2) {
		t.Fatal("same tenant header should route to same endpoint regardless of path")
	}
}

func TestConsistentHash_Edges(t *testing.T) {
	b := NewConsistentHashBalance()
	if got := b.Pick(nil, newConsistentRequest("/x")); got != "" {
		t.Fatalf("expected empty for nil endpoints, got %s", got)
	}
	if got := b.Pick([]string{"only:8080"}, newConsistentRequest("/x")); got != "only:8080" {
		t.Fatalf("expected single endpoint passthrough, got %s", got)
	}
	// nil req（防御）：等价空 key，仍应稳定返回
	if got := b.Pick([]string{"a:1", "b:2"}, nil); got == "" {
		t.Fatal("expected non-empty result for nil req")
	}
}

func TestConsistentHash_ConcurrentPickRebuild(t *testing.T) {
	b := NewConsistentHashBalance()
	base := make([]string, 0, 4) // 预分配：后面会 append 新节点（prealloc）
	base = append(base, "10.0.0.1:8080", "10.0.0.2:8080", "10.0.0.3:8080")
	grown := append(base, "10.0.0.4:8080")

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			var eps []string // 每轮循环必被重赋值，无初值（ineffassign）
			for i := 0; i < 300; i++ {
				if i%50 == 25 {
					eps = grown // 模拟刷新期列表变化
				} else {
					eps = base
				}
				b.Pick(eps, newConsistentRequest("/k"))
			}
		}(g)
	}
	wg.Wait()
}
