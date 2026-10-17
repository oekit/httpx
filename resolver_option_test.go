package httpx

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// countingResolver 统计 Resolve 调用次数的 mock 解析器。
type countingResolver struct {
	calls atomic.Int64
}

func (r *countingResolver) Resolve(_ context.Context) ([]string, error) {
	r.calls.Add(1)
	return []string{"http://10.0.0.1:8080"}, nil
}

// TestOptionOrder_EndpointsFirst 回归测试：WithEndpoints 之后调用
// WithRefreshInterval/WithSelector 不得丢失静态 endpoints（L4）。
func TestOptionOrder_EndpointsFirst(t *testing.T) {
	sel, err := NewConsistentHashSelector()
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient("svc",
		WithEndpoints("http://10.0.0.1:8080", "http://10.0.0.2:8080"),
		WithRefreshInterval(time.Hour),
		WithSelector(sel),
	)
	defer c.Close()

	if c.endpointManager == nil {
		t.Fatal("endpointManager is nil")
	}
	nodes := c.endpointManager.GetNodes()
	if len(nodes) != 2 {
		t.Errorf("len(nodes) = %d, want 2（静态 endpoints 被后续 ClientOption 丢失）", len(nodes))
	}
	if _, ok := c.endpointManager.selector.(*ConsistentHashSelector); !ok {
		t.Errorf("selector = %T, want *ConsistentHashSelector", c.endpointManager.selector)
	}
}

// TestOptionOrder_SelectorFirst 回归测试：WithSelector 先于 EndpointManager
// 创建时不得被静默丢弃，选项顺序不影响最终行为。
func TestOptionOrder_SelectorFirst(t *testing.T) {
	sel, err := NewConsistentHashSelector()
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient("svc",
		WithSelector(sel),
		WithRefreshInterval(time.Hour),
		WithEndpoints("http://10.0.0.1:8080"),
	)
	defer c.Close()

	if _, ok := c.endpointManager.selector.(*ConsistentHashSelector); !ok {
		t.Errorf("selector = %T, want *ConsistentHashSelector", c.endpointManager.selector)
	}
	if nodes := c.endpointManager.GetNodes(); len(nodes) != 1 {
		t.Errorf("len(nodes) = %d, want 1", len(nodes))
	}
}

// TestOptionOrder_RefreshIntervalBeforeResolver 回归测试：WithRefreshInterval
// 先于 WithServiceResolver/WithNNS 时，自定义刷新间隔仍生效
// （默认 30s 只会在启动时立即刷新 1 次）。
func TestOptionOrder_RefreshIntervalBeforeResolver(t *testing.T) {
	resolver := &countingResolver{}
	c := NewClient("svc",
		WithRefreshInterval(10*time.Millisecond),
		WithServiceResolver(resolver),
	)
	defer c.Close()

	if c.endpointManager == nil || c.endpointManager.refreshTicker == nil {
		t.Fatal("refresh loop not started")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if n := resolver.calls.Load(); n >= 3 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("resolver called %d times within deadline, want >= 3（刷新间隔丢失，回退默认 30s）",
		resolver.calls.Load())
}

// TestPriorityQueue_ReleaseUnknownPriority 回归测试：未知优先级按
// PriorityNormal 释放（与 Acquire 的 default 分支对齐），不再泄漏许可（L5）。
func TestPriorityQueue_ReleaseUnknownPriority(t *testing.T) {
	q := NewPriorityQueue(1)
	ctx := context.Background()

	// 未知优先级经 Acquire 的 default 分支从 normal 获取
	if !q.Acquire(ctx, Priority(42)) {
		t.Fatal("Acquire(unknown priority) = false, want true")
	}
	if got := q.Len(); got != 1 {
		t.Fatalf("Len() after Acquire = %d, want 1", got)
	}

	q.Release(Priority(42))
	if got := q.Len(); got != 0 {
		t.Errorf("Len() after Release = %d, want 0（许可泄漏）", got)
	}

	// 释放后容量恢复，可再次获取
	if !q.Acquire(ctx, PriorityNormal) {
		t.Error("Acquire after Release = false, want true")
	}
}
