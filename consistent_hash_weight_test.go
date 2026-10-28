package httpx

import (
	"context"
	"testing"

	"github.com/oekit/httpx/loadbalance"
)

// weightedNodes 构造带权重的节点列表（地址不变、权重可变）。
func weightedNodes(weights map[string]float64, addrs ...string) []loadbalance.Node {
	return loadbalance.NodesFrom(addrs,
		func(s string) string { return s },
		func(s string) float64 { return weights[s] },
		nil,
		nil,
	)
}

// TestConsistentHash_WeightChangeRebuildsRing 回归测试（#42）：**只改权重**
// （地址集合不变）时，哈希环必须重建——否则权重折算出的虚拟副本数沿用旧值，
// **调权静默失效**。
//
// 缺陷成因：`sameViewLocked` 只比较节点**地址**。而权重确实参与建环
// （buildConsistentRing 按 weight/mean 折算副本数）⇒ 每轮刷新都会带来
// 新权重对象，但地址相同 ⇒ 判定为「同一视图」⇒ 环被永久缓存 ⇒ 新权重丢弃。
//
// 判据：环的**长度**会随副本数变化 ⇒ 用 len(ring) 观测重建是否发生。
func TestConsistentHash_WeightChangeRebuildsRing(t *testing.T) {
	b, err := NewConsistentHashSelector()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	addrs := []string{"10.0.0.1:80", "10.0.0.2:80"}

	// 第一轮：node2 权重 1 ⇒ 两个节点副本数相同。
	nodes1 := weightedNodes(map[string]float64{
		"10.0.0.1:80": 1,
		"10.0.0.2:80": 1,
	}, addrs...)
	if _, _, err := b.Select(ctx, nodes1, loadbalance.WithKey("k1")); err != nil {
		t.Fatalf("select: %v", err)
	}
	ringLen1 := len(b.ring)

	// 第二轮：**只改权重**（node2 从 1 调到 10），地址集合完全不变。
	nodes2 := weightedNodes(map[string]float64{
		"10.0.0.1:80": 1,
		"10.0.0.2:80": 10,
	}, addrs...)
	if _, _, err := b.Select(ctx, nodes2, loadbalance.WithKey("k1")); err != nil {
		t.Fatalf("select: %v", err)
	}
	ringLen2 := len(b.ring)

	if ringLen2 == ringLen1 {
		t.Fatalf("调权后哈希环未重建（环长度仍为 %d）——权重折算的副本数沿用旧值 ⇒ 调权静默失效", ringLen1)
	}
}

// TestConsistentHash_SameViewReusesRing 回归护栏：地址与权重都没变时，环必须
// **复用**（不重建）——否则每次刷新都重建环，白白浪费 CPU。
func TestConsistentHash_SameViewReusesRing(t *testing.T) {
	b, err := NewConsistentHashSelector()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	addrs := []string{"10.0.0.1:80", "10.0.0.2:80"}
	weights := map[string]float64{"10.0.0.1:80": 1, "10.0.0.2:80": 3}

	nodes1 := weightedNodes(weights, addrs...)
	if _, _, err := b.Select(ctx, nodes1, loadbalance.WithKey("k1")); err != nil {
		t.Fatalf("select: %v", err)
	}
	first := b.ring[0].hash

	// 同样地址、同样权重，但**是新的对象**（每轮刷新都会 new 一批）。
	nodes2 := weightedNodes(weights, addrs...)
	if _, _, err := b.Select(ctx, nodes2, loadbalance.WithKey("k1")); err != nil {
		t.Fatalf("select: %v", err)
	}
	if b.ring[0].hash != first {
		t.Fatal("地址与权重均未变却重建了环（应当复用）")
	}
}
