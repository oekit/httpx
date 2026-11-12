package httpx

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oekit/httpx/loadbalance"
	"github.com/oekit/registry"
)

// 本文件补 EndpointManager 刷新面（refreshNodes 分支 + refreshLoop 退出路径）
// 的测试债（#171）：此前仅 endpoint_test.go 的 4 个拼接用例与
// resolver_option_test.go 的 option 顺序回归覆盖，refreshNodes 的缓存降级
// 三分支与 Stop 幂等零测试。

// failingResolver 恒报错的解析器（驱动 refreshNodes 错误分支）。
type failingResolver struct{ calls atomic.Int64 }

func (r *failingResolver) Resolve(context.Context) ([]string, error) {
	r.calls.Add(1)
	return nil, errors.New("resolver down")
}

// emptyResolver 恒返回空列表的解析器（驱动 refreshNodes 空列表分支）。
type emptyResolver struct{ calls atomic.Int64 }

func (r *emptyResolver) Resolve(context.Context) ([]string, error) {
	r.calls.Add(1)
	return []string{}, nil
}

// stubInstanceResolver 返回固定实例视图的 InstanceResolver（驱动权重/元数据
// 贯通分支）。
type stubInstanceResolver struct {
	insts []registry.Instance
	err   error
	empty bool
}

func (r *stubInstanceResolver) Resolve(context.Context) ([]string, error) {
	return nil, errors.New("use ResolveInstances")
}

func (r *stubInstanceResolver) ResolveInstances(context.Context) ([]registry.Instance, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.empty {
		return nil, nil
	}
	return r.insts, nil
}

func waitFor(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

// TestEndpointManager_RefreshErrorKeepsCached 回归（#171）：resolver 报错时
// 保留现有快照（缓存降级）——修复前该分支无任何测试。
func TestEndpointManager_RefreshErrorKeepsCached(t *testing.T) {
	m := NewEndpointManager(nil, nil, 0)
	defer m.Stop()
	m.SetNodes(NodesFromEndpoints([]string{"http://10.0.0.1:8080"}))

	r := &failingResolver{}
	m.resolver = r
	m.refreshNodes(context.Background())

	nodes := m.GetNodes()
	if len(nodes) != 1 || nodes[0].Address() != "http://10.0.0.1:8080" {
		t.Errorf("resolver error must keep cached nodes, got %v", nodes)
	}
	// 最新视图与缓存视图都未被错误路径清空
	if len(m.nodes) != 1 || len(m.cachedNodes) != 1 {
		t.Errorf("refresh error must not wipe nodes/cachedNodes: %d/%d", len(m.nodes), len(m.cachedNodes))
	}
	if r.calls.Load() != 1 {
		t.Errorf("resolver called %d times, want 1", r.calls.Load())
	}
}

// TestEndpointManager_RefreshEmptyKeepsCached 回归（#171）：resolver 返回空
// 列表时保留现有快照（空 != 下线成空转，缓存降级语义）。
func TestEndpointManager_RefreshEmptyKeepsCached(t *testing.T) {
	m := NewEndpointManager(nil, nil, 0)
	defer m.Stop()
	m.SetNodes(NodesFromEndpoints([]string{"http://10.0.0.1:8080"}))

	r := &emptyResolver{}
	m.resolver = r
	m.refreshNodes(context.Background())

	if got := m.GetNodes(); len(got) != 1 || got[0].Address() != "http://10.0.0.1:8080" {
		t.Errorf("empty resolver result must keep cached nodes, got %v", got)
	}
}

// TestEndpointManager_InstanceResolverErrorAndEmpty 回归（#171）：实例视图
// 路径的报错/空列表分支同样走缓存降级。
func TestEndpointManager_InstanceResolverErrorAndEmpty(t *testing.T) {
	m := NewEndpointManager(nil, nil, 0)
	defer m.Stop()
	m.SetNodes(NodesFromEndpoints([]string{"http://10.0.0.1:8080"}))

	m.resolver = &stubInstanceResolver{err: errors.New("nns down")}
	m.refreshNodes(context.Background())
	if got := m.GetNodes(); len(got) != 1 {
		t.Errorf("instance resolver error must keep cached nodes, got %d", len(got))
	}

	m.resolver = &stubInstanceResolver{empty: true}
	m.refreshNodes(context.Background())
	if got := m.GetNodes(); len(got) != 1 {
		t.Errorf("instance resolver empty list must keep cached nodes, got %d", len(got))
	}
}

// TestEndpointManager_InstanceResolverWeightsFlowThrough 回归（#171）：检测到
// InstanceResolver 时权重与元数据必须贯通到 Selector 节点（加权策略与
// WithFilter 生效的前提）；地址 scheme 从 Metadata（键 MetadataKeyScheme）取。
func TestEndpointManager_InstanceResolverWeightsFlowThrough(t *testing.T) {
	m := NewEndpointManager(nil, nil, 0)
	defer m.Stop()
	m.resolver = &stubInstanceResolver{insts: []registry.Instance{
		{Service: "svc", IP: "10.0.0.1", Port: 8080, Weight: 3,
			Metadata: map[string]string{"scheme": "https", "version": "v2"}},
		{Service: "svc", IP: "10.0.0.2", Port: 8081, Weight: 1},
	}}

	m.refreshNodes(context.Background())

	nodes := m.GetNodes()
	if len(nodes) != 2 {
		t.Fatalf("len(nodes) = %d, want 2", len(nodes))
	}
	byAddr := map[string]loadbalanceNodeForTest{}
	for _, n := range nodes {
		byAddr[n.Address()] = loadbalanceNodeForTest{weight: n.Weight(), meta: n.Metadata()}
	}
	n1, ok := byAddr["https://10.0.0.1:8080"]
	if !ok {
		t.Fatalf("https scheme not applied from instance metadata: addrs=%v", nodeAddrs(nodes))
	}
	if n1.weight != 3 {
		t.Errorf("weight = %v, want 3（权重必须贯通）", n1.weight)
	}
	if n1.meta["version"] != "v2" {
		t.Errorf("metadata = %v, want version=v2 贯通", n1.meta)
	}
	n2, ok := byAddr["http://10.0.0.2:8081"]
	if !ok {
		t.Fatalf("default scheme not applied: addrs=%v", nodeAddrs(nodes))
	}
	if n2.weight != 1 {
		t.Errorf("weight = %v, want 1", n2.weight)
	}
}

// loadbalanceNodeForTest 是测试内节点断言用的轻量快照（避免直接依赖
// loadbalance 的具体实现类型）。
type loadbalanceNodeForTest struct {
	weight float64
	meta   map[string]string
}

func nodeAddrs(nodes []loadbalance.Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Address())
	}
	return out
}

// TestEndpointManager_StopIdempotentAndStopsRefreshing 回归（#171）：Stop
// 幂等（重复调用不 panic、ticker 停止），刷新循环退出后 resolver 不再被
// 调用（refreshLoop 的 stopRefresh/ctx 退出路径）。
func TestEndpointManager_StopIdempotentAndStopsRefreshing(t *testing.T) {
	r := &countingResolver{}
	m := NewEndpointManager(r, nil, 20*time.Millisecond)

	waitFor(t, func() bool { return r.calls.Load() >= 1 }, 2*time.Second)

	m.Stop()
	m.Stop() // 幂等：第二次调用不得 panic
	if m.refreshTicker != nil {
		m.refreshTicker.Stop()
	}

	calls := r.calls.Load()
	time.Sleep(120 * time.Millisecond)
	if got := r.calls.Load(); got != calls {
		t.Errorf("resolver called %d → %d after Stop, refresh loop must exit (#171)", calls, got)
	}
}
