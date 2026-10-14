package httpx

import (
	"context"
	"net/http"
	"testing"

	"github.com/yi-nology/ekit-httpx/loadbalance"
	"github.com/yi-nology/ekit-registry/registry"
)

// TestWeightedResolver_EndpointManagerWeightPropagation 钉住权重贯通语义：
// NNSResolver（实现 WeightedResolver）+ 加权 LB 策略时，注册中心配置的
// 实例权重真实生效。修复前 NNSResolver 把实例压平成字符串、lbAdapter
// 恒按权重 1 适配——加权策略在 httpx 路径名存实亡。
func TestWeightedResolver_EndpointManagerWeightPropagation(t *testing.T) {
	stub := &stubNNSClient{instances: []registry.Instance{
		{IP: "10.0.0.1", Port: 8080, Weight: 9},
		{IP: "10.0.0.2", Port: 8080, Weight: 1},
	}}
	r := NewNNSResolver("svc-a", stub)

	// interval=0 不启动后台刷新；直接触发一次同步刷新
	m := NewEndpointManager(r, NewBalanceAdapter(loadbalance.NewWeighted()), 0)
	defer m.Stop()
	m.refreshEndpoints(context.Background())

	req, _ := http.NewRequest(http.MethodGet, "http://x/api", nil)
	const picks = 2000
	heavy := 0
	for i := 0; i < picks; i++ {
		addr := m.Pick(req)
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

// TestWeightedResolver_EqualWeightFallbackWithoutWeights 等权回归：
// resolver 不实现 WeightedResolver（StaticResolver）时保持等权语义。
func TestWeightedResolver_EqualWeightFallbackWithoutWeights(t *testing.T) {
	r := NewStaticResolver([]string{"http://10.0.0.1:8080", "http://10.0.0.2:8080"})
	m := NewEndpointManager(r, NewBalanceAdapter(loadbalance.NewWeighted()), 0)
	defer m.Stop()
	m.refreshEndpoints(context.Background())

	req, _ := http.NewRequest(http.MethodGet, "http://x/api", nil)
	const picks = 2000
	first := 0
	for i := 0; i < picks; i++ {
		if m.Pick(req) == "http://10.0.0.1:8080" {
			first++
		}
	}
	ratio := float64(first) / picks
	if ratio < 0.43 || ratio > 0.57 {
		t.Fatalf("expected ~0.50 ratio for equal weights, got %.3f", ratio)
	}
}

// TestLBAdapter_SetWeightsRebuild 钉住 SetWeights 触发实例视图重建：
// 同一 endpoint 数量下更新权重后，Pick 分布随之变化。
func TestLBAdapter_SetWeightsRebuild(t *testing.T) {
	a := &lbAdapter{balancer: loadbalance.NewWeighted()}
	endpoints := []string{"http://10.0.0.1:8080", "http://10.0.0.2:8080"}
	req, _ := http.NewRequest(http.MethodGet, "http://x/api", nil)

	const picks = 1000
	countFirst := func() int {
		n := 0
		for i := 0; i < picks; i++ {
			if a.Pick(endpoints, req) == endpoints[0] {
				n++
			}
		}
		return n
	}

	// 初始等权：~50%
	if n := countFirst(); n < 400 || n > 600 {
		t.Fatalf("expected ~500/1000 for equal weights, got %d", n)
	}

	// 权重大幅倾斜到第一实例（同 endpoint 数量，仅权重变化）
	a.SetWeights([]float64{1000, 1})
	if n := countFirst(); n < picks-50 {
		t.Fatalf("expected near-total traffic on first instance after SetWeights, got %d/%d", n, picks)
	}
}

// TestNNSResolver_ResolveWeighted 验证带权重视图的地址与权重映射。
func TestNNSResolver_ResolveWeighted(t *testing.T) {
	stub := &stubNNSClient{instances: []registry.Instance{
		{IP: "10.0.0.1", Port: 8080, Weight: 9},
		{IP: "10.0.0.2", Port: 8080, Weight: 1},
	}}
	r := NewNNSResolver("svc-a", stub)
	got, err := r.ResolveWeighted(context.Background())
	if err != nil {
		t.Fatalf("ResolveWeighted: %v", err)
	}
	if len(got) != 2 || got[0].Addr != "http://10.0.0.1:8080" || got[0].Weight != 9 || got[1].Weight != 1 {
		t.Fatalf("unexpected weighted endpoints: %+v", got)
	}
}
