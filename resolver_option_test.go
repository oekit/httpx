package httpx

import (
	"context"
	"strings"
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
	c := MustNewClient("svc",
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
	c := MustNewClient("svc",
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
	c := MustNewClient("svc",
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

// TestOptionOrder_DynamicResolverReplaceStopsOldLoop 回归（#89）：组合
// option 反复替换 EndpointManager 时，旧 manager 的刷新循环必须随替换
// 停止——修复前 WithNNS/WithServiceResolver 直接覆盖 endpointManager，
// 旧 refreshLoop goroutine 每 30s 继续打旧 resolver 直到进程退出。
func TestOptionOrder_DynamicResolverReplaceStopsOldLoop(t *testing.T) {
	old := &countingResolver{}
	fresh := &countingResolver{}
	c := MustNewClient("svc",
		WithRefreshInterval(10*time.Millisecond),
		WithServiceResolver(old),
		WithServiceResolver(fresh),
	)
	defer c.Close()

	// 新 manager 的刷新循环已跑起来（fresh 被解析 >= 3 次）
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && fresh.calls.Load() < 3 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := fresh.calls.Load(); got < 3 {
		t.Fatalf("新 resolver 在期限内仅被调用 %d 次，刷新循环未启动", got)
	}

	// 旧 manager 的刷新循环已停：old 的计数在观察窗口内不再增长
	// （修复前 old 的 refreshLoop 每 10ms 继续打点，窗口内必然增长）
	base := old.calls.Load()
	stableDeadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(stableDeadline) {
		if got := old.calls.Load(); got != base {
			t.Fatalf("旧 EndpointManager 的刷新循环未随替换停止：resolver 调用 %d → %d（goroutine 泄漏）", base, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestOptionOrder_EndpointsAndDynamicMutuallyExclusive 回归（#89）：
// WithEndpoints 与动态 resolver 互斥，构造期报错（NewClient 返回 error），
// 不再随 option 顺序漂移——「动态在前」时静态端点每 30s 被 NNS 结果覆盖，
// 「静态在前」时静态端点被动态 manager 静默丢弃。
func TestOptionOrder_EndpointsAndDynamicMutuallyExclusive(t *testing.T) {
	cases := []struct {
		name string
		opts []ClientOption
	}{
		{
			name: "动态在前静态在后（静态端点会被刷新循环覆盖）",
			opts: []ClientOption{
				WithServiceResolver(&countingResolver{}),
				WithEndpoints("http://10.0.0.1:8080"),
			},
		},
		{
			name: "静态在前动态在后（静态端点被静默丢弃）",
			opts: []ClientOption{
				WithEndpoints("http://10.0.0.1:8080"),
				WithServiceResolver(&countingResolver{}),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := NewClient("svc", tc.opts...)
			if err == nil {
				c.Close()
				t.Fatal("期望构造期报错（静态端点与动态 resolver 互斥），got nil")
			}
			if c != nil {
				t.Errorf("期望错误路径返回 nil client，got %v", c)
			}
			if !strings.Contains(err.Error(), "互斥") {
				t.Errorf("错误信息应指明互斥原因，got %q", err.Error())
			}
		})
	}
}

// TestNewClient_ConflictErrorStopsManager 回归（#89）：构造期冲突报错的
// 路径上，此前 option 已创建的 EndpointManager（带刷新循环）必须被 Stop——
// 错误路径不允许泄漏 goroutine。
func TestNewClient_ConflictErrorStopsManager(t *testing.T) {
	resolver := &countingResolver{}
	_, err := NewClient("svc",
		WithRefreshInterval(10*time.Millisecond),
		WithServiceResolver(resolver),
		WithEndpoints("http://10.0.0.1:8080"),
	)
	if err == nil {
		t.Fatal("期望构造期报错")
	}

	// 收敛窗口：潜在的 straggler 首刷落地（80ms >> 10ms 刷新间隔的多个周期）
	time.Sleep(80 * time.Millisecond)
	base := resolver.calls.Load()
	time.Sleep(200 * time.Millisecond)
	if got := resolver.calls.Load(); got != base {
		t.Fatalf("构造错误路径泄漏刷新循环：resolver 调用 %d → %d（200ms 观察窗口）", base, got)
	}
}

// TestNewPriorityQueue_InvalidCapacityPanics 回归（#88）：capacity 非法
// （0 = 无缓冲 channel，首个请求即死锁到 ctx 取消；负数 = make 直接 panic）
// 必须在构造期 panic 并指明原因，而不是运行期全量超时。
func TestNewPriorityQueue_InvalidCapacityPanics(t *testing.T) {
	for _, capacity := range []int{0, -1, -100} {
		func(cap int) {
			defer func() {
				r := recover()
				if r == nil {
					t.Errorf("NewPriorityQueue(%d): 期望 panic, got none", cap)
					return
				}
				msg, ok := r.(string)
				if !ok {
					t.Errorf("NewPriorityQueue(%d): panic 值应为 string, got %T", cap, r)
					return
				}
				if !strings.Contains(msg, "capacity must be >= 1") {
					t.Errorf("NewPriorityQueue(%d): panic 信息应指明原因, got %q", cap, msg)
				}
			}()
			_ = NewPriorityQueue(cap)
		}(capacity)
	}
}

// TestWithPriorityQueue_InvalidCapacityPanics 经 option 路径同样构造期暴露
// （配置缺省值传 0 的场景）。
func TestWithPriorityQueue_InvalidCapacityPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("WithPriorityQueue(0): 期望 panic, got none")
		}
	}()
	_ = MustNewClient("svc", WithPriorityQueue(0))
}

// TestNewPriorityQueue_MinCapacityWorks 边界：capacity=1 可用（修复目标，
// 非法值钳到「显式传 1」的合法形态，而非静默放行 0）。
func TestNewPriorityQueue_MinCapacityWorks(t *testing.T) {
	q := NewPriorityQueue(1)
	ctx := context.Background()
	if !q.Acquire(ctx, PriorityNormal) {
		t.Fatal("Acquire(capacity=1) = false, want true")
	}
	if q.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", q.Len())
	}
	q.Release(PriorityNormal)
	if q.Len() != 0 {
		t.Errorf("Len() after Release = %d, want 0", q.Len())
	}
}
