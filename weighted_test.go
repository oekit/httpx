package httpx

import (
	"context"
	"testing"

	"github.com/oekit/httpx/loadbalance"
	"github.com/oekit/registry"
)

// stubNNSClient 测试用 NNS 客户端（复用自集成测试）。

// TestInstanceResolver_WeightPropagation 钉住权重贯通语义：
// NNSResolver（实现 InstanceResolver）+ 加权 Selector 时，注册中心配置的
// 实例权重真实生效。v0.3.x 曾把实例压平成字符串导致加权策略名存实亡，
// 本测试防止回归。
func TestInstanceResolver_WeightPropagation(t *testing.T) {
	stub := &stubNNSClient{instances: []registry.Instance{
		{Service: "svc-a", IP: "10.0.0.1", Port: 8080, Weight: 9},
		{Service: "svc-a", IP: "10.0.0.2", Port: 8080, Weight: 1},
	}}
	r := NewNNSResolver("svc-a", stub)

	sel, err := loadbalance.NewSelector(loadbalance.StrategyWeighted)
	if err != nil {
		t.Fatal(err)
	}
	// interval=0 不启动后台刷新；直接触发一次同步刷新
	m := NewEndpointManager(r, sel, 0)
	defer m.Stop()
	m.refreshNodes(context.Background())

	ctx := context.Background()
	const picks = 2000
	heavy := 0
	for i := 0; i < picks; i++ {
		addr, done, err := m.Pick(ctx, "/api")
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		done(ctx, loadbalance.DoneInfo{})
		switch addr {
		case "http://10.0.0.1:8080":
			heavy++
		case "http://10.0.0.2:8080":
		default:
			t.Fatalf("unexpected endpoint %q", addr)
		}
	}
	// 期望 ~90%（9/10）；2000 次采样 3σ ≈ 2%，容忍 ±5%
	ratio := float64(heavy) / picks
	if ratio < 0.85 || ratio > 0.95 {
		t.Fatalf("expected heavy instance ratio ~0.90, got %.3f (%d/%d)", ratio, heavy, picks)
	}
}

// TestStaticResolver_EqualWeightFallback 等权回归：resolver 不实现
// InstanceResolver（StaticResolver）时保持等权语义。
func TestStaticResolver_EqualWeightFallback(t *testing.T) {
	r := NewStaticResolver([]string{"http://10.0.0.1:8080", "http://10.0.0.2:8080"})
	sel, _ := loadbalance.NewSelector(loadbalance.StrategyWeighted)
	m := NewEndpointManager(r, sel, 0)
	defer m.Stop()
	m.refreshNodes(context.Background())

	ctx := context.Background()
	const picks = 2000
	first := 0
	for i := 0; i < picks; i++ {
		addr, _, err := m.Pick(ctx, "/api")
		if err != nil {
			t.Fatal(err)
		}
		if addr == "http://10.0.0.1:8080" {
			first++
		}
	}
	ratio := float64(first) / picks
	if ratio < 0.43 || ratio > 0.57 {
		t.Fatalf("expected ~0.50 ratio for equal weights, got %.3f", ratio)
	}
}

// TestNNSResolver_ResolveInstances 验证实例视图的地址与权重映射。
func TestNNSResolver_ResolveInstances(t *testing.T) {
	stub := &stubNNSClient{instances: []registry.Instance{
		{Service: "svc-a", IP: "10.0.0.1", Port: 8080, Weight: 9},
		{Service: "svc-a", IP: "10.0.0.2", Port: 8080, Weight: 1},
	}}
	r := NewNNSResolver("svc-a", stub)
	got, err := r.ResolveInstances(context.Background())
	if err != nil {
		t.Fatalf("ResolveInstances: %v", err)
	}
	if len(got) != 2 || got[0].Weight != 9 || got[1].Weight != 1 {
		t.Fatalf("unexpected instances: %+v", got)
	}

	nodes := SelectorNodes(got)
	if nodes[0].Address() != "http://10.0.0.1:8080" || nodes[0].Weight() != 9 {
		t.Fatalf("SelectorNodes mapping broken: %+v", nodes[0])
	}
	if nodes[0].ServiceName() != "svc-a" {
		t.Fatalf("service name mapping broken: %q", nodes[0].ServiceName())
	}
}

// TestSelectorNodes_MetadataMapping 验证元数据贯通（WithFilter 数据源）。
func TestSelectorNodes_MetadataMapping(t *testing.T) {
	insts := []registry.Instance{
		{Service: "svc", IP: "10.0.0.1", Port: 8080, Weight: 5, Metadata: map[string]string{"cluster": "blue"}},
	}
	nodes := SelectorNodes(insts)
	if nodes[0].Metadata()["cluster"] != "blue" {
		t.Fatalf("metadata not propagated: %+v", nodes[0].Metadata())
	}
}
