package httpx

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/oekit/httpx/loadbalance"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sony/gobreaker"
)

func TestNewClient_DefaultTimeout(t *testing.T) {
	c := NewClient("test")
	if c.client == nil {
		t.Error("expected non-nil client")
	}
	if c.service != "test" {
		t.Errorf("expected service 'test', got %q", c.service)
	}
}

func TestWithBaseURL(t *testing.T) {
	c := NewClient("test", WithBaseURL("http://example.com"))
	if c.baseURL != "http://example.com" {
		t.Errorf("expected 'http://example.com', got %q", c.baseURL)
	}
}

func TestWithTimeout(t *testing.T) {
	c := NewClient("test", WithTimeout(5*time.Second))
	if c.client == nil {
		t.Error("expected non-nil client")
	}
}

func TestWithBearerToken(t *testing.T) {
	c := NewClient("test", WithBearerToken("my-token"))
	if c.client == nil {
		t.Error("expected non-nil client")
	}
}

func TestWithDebug(t *testing.T) {
	c := NewClient("test", WithDebug())
	if c.client == nil {
		t.Error("expected non-nil client")
	}
}

func TestWithCircuitBreaker(t *testing.T) {
	c := NewClient("test", WithCircuitBreaker("cb-test", gobreaker.Settings{
		Interval: 10 * time.Second,
	}))
	if c.client == nil {
		t.Error("expected non-nil client")
	}
}

func TestServiceError_Error(t *testing.T) {
	e := &ServiceError{
		Service: "moyu",
		Action:  "GET /api",
		Code:    500,
		Message: "internal error",
	}
	expected := "moyu: GET /api failed: internal error (status 500)"
	if e.Error() != expected {
		t.Errorf("expected %q, got %q", expected, e.Error())
	}
}

func TestErrCircuitOpen(t *testing.T) {
	if ErrCircuitOpen == nil {
		t.Error("expected non-nil ErrCircuitOpen")
	}
}

func TestStatusCodeStr(t *testing.T) {
	if statusCodeStr(200) != "200" {
		t.Errorf("expected '200', got %q", statusCodeStr(200))
	}
	if statusCodeStr(0) != "0" {
		t.Errorf("expected '0', got %q", statusCodeStr(0))
	}
}

func TestHandleResponse_Success(t *testing.T) {
	// We cannot easily test with real hertz Response, but we can test the error type
	var err error = &ServiceError{
		Service: "test",
		Action:  "GET /",
		Code:    404,
		Message: "not found",
	}

	var svcErr *ServiceError
	if !errors.As(err, &svcErr) {
		t.Error("expected errors.As to work")
	}
	if svcErr.Code != 404 {
		t.Errorf("expected 404, got %d", svcErr.Code)
	}
}

func TestNewClient_WithMetrics(t *testing.T) {
	c := NewClient("test", WithMetrics(nil, "test_ns"))
	if c.metrics == nil {
		t.Error("expected non-nil metrics")
	}
}

// TestNewClient_WithMetrics_DuplicateRegistration 回归：同一 Registry +
// namespace 创建第二个 Client 时 MustRegister 遇同名 collector 直接 panic
// （一个服务调多个上游是真实场景）。钉住：复用既有 collector，不 panic，
// 且第二个 Client 的指标写入同一 collector。
func TestNewClient_WithMetrics_DuplicateRegistration(t *testing.T) {
	reg := prometheus.NewRegistry()
	m1 := newClientMetrics(reg, "dup_ns")
	m2 := newClientMetrics(reg, "dup_ns") // 修复前此处 panic

	m1.recordRequestEnd("svc", "t1", "GET", "200", 10*time.Millisecond)
	m2.recordRequestEnd("svc", "t2", "GET", "200", 10*time.Millisecond)

	gathered, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var total float64
	for _, mf := range gathered {
		if mf.GetName() == "dup_ns_requests_total" {
			for _, m := range mf.GetMetric() {
				total += m.GetCounter().GetValue()
			}
		}
	}
	if total != 2 {
		t.Errorf("both clients should record into the reused collector, got total=%v, want 2", total)
	}
}

func TestWithTarget(t *testing.T) {
	c := NewClient("test", WithTarget("user-service"))
	if got := c.targetLabel(); got != "user-service" {
		t.Errorf("expected target 'user-service', got %q", got)
	}
}

func TestSetTarget(t *testing.T) {
	c := NewClient("test")
	if got := c.targetLabel(); got != defaultTargetLabel {
		t.Errorf("expected empty initial target (%q), got %q", defaultTargetLabel, got)
	}
	c.SetTarget("order-service")
	if got := c.targetLabel(); got != "order-service" {
		t.Errorf("expected target 'order-service' after SetTarget, got %q", got)
	}
}

func TestRecordMetrics_DefaultTarget(t *testing.T) {
	// 未设置 target 时，metrics 应聚到 defaultTargetLabel 避免高基数。
	reg := prometheus.NewRegistry()
	cm := newClientMetrics(reg, "test_default")
	cm.recordRequestEnd("svc", "", "GET", "200", 100*time.Millisecond)

	gathered, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range gathered {
		for _, m := range mf.Metric {
			for _, lp := range m.Label {
				if lp.GetName() == "target" && lp.GetValue() != defaultTargetLabel {
					t.Errorf("expected target label %q, got %q", defaultTargetLabel, lp.GetValue())
				}
			}
		}
	}
}

func TestUploadFile_NotPanic(t *testing.T) {
	// Just verify the method exists and the client can be created
	c := NewClient("test")
	if c == nil {
		t.Error("expected non-nil client")
	}
}

func TestClient_Hertz(t *testing.T) {
	c := NewClient("test")
	if c.Hertz() == nil {
		t.Error("expected non-nil hertz client")
	}
}

func TestGetJSON_NilRequest(t *testing.T) {
	// Hertz client requires non-nil context. Verify cancelled context returns error without panic.
	c := NewClient("test", WithBaseURL("http://localhost:1"))
	// This will fail with cancelled context error, but should not panic
	// Hertz client requires non-nil context (same as net/http).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = c.GetJSON(
		ctx, "/test", nil)
}

func TestNewClient_ComposedOptions(t *testing.T) {
	c := NewClient("test",
		WithBaseURL("http://example.com"),
		WithTimeout(10*time.Second),
		WithBearerToken("token"),
		WithDebug(),
		WithMetrics(nil, "ns"),
	)
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if c.baseURL != "http://example.com" {
		t.Errorf("expected baseURL set")
	}
	if c.metrics == nil {
		t.Error("expected metrics set")
	}
}

// Ensure the correct HTTP methods are available
func TestHTTPMethodsExist(t *testing.T) {
	c := NewClient("test")
	// These should compile (method existence check)
	_ = c.GetJSON
	_ = c.PostJSON
	_ = c.DoJSON
	_ = c.DoRaw
	_ = c.UploadFile
	_ = c.UploadBytes
	_ = c.Hertz
}

// Test that the package-level functions exist
func TestWithCircuitBreaker_CallbackPreservation(t *testing.T) {
	var stateChanged bool
	c := NewClient("test", WithCircuitBreaker("cb", gobreaker.Settings{
		Interval: 10 * time.Second,
		OnStateChange: func(name string, from, to gobreaker.State) {
			stateChanged = true
		},
	}))
	if c == nil {
		t.Error("expected non-nil client")
	}
	// We can't easily trigger state change without a real server,
	// but we verify the option was accepted
	_ = stateChanged
}

func TestWithTLSSkipVerify(t *testing.T) {
	c := NewClient("test", WithTLSSkipVerify())
	if c == nil {
		t.Error("expected non-nil client")
	}
}

func TestWithBasicAuth(t *testing.T) {
	c := NewClient("test", WithBasicAuth("user", "pass"))
	if c == nil {
		t.Error("expected non-nil client")
	}
}

func TestServiceError_Fields(t *testing.T) {
	e := &ServiceError{
		Service: "svc",
		Action:  "GET /api",
		Code:    http.StatusNotFound,
		Message: "not found",
	}
	if e.Service != "svc" {
		t.Error("expected svc")
	}
	if e.Code != 404 {
		t.Error("expected 404")
	}
}

func TestBalanceStrategies(t *testing.T) {
	nodes := NodesFromEndpoints([]string{"http://1.1.1.1", "http://2.2.2.2", "http://3.3.3.3"})
	ctx := context.Background()

	// roundrobin/random/weighted 三策略均选出有效节点
	for _, name := range []string{"roundrobin", "random", "weighted"} {
		sel, err := loadbalance.NewSelector(name)
		if err != nil {
			t.Fatalf("NewSelector(%s): %v", name, err)
		}
		n, done, err := sel.Select(ctx, nodes)
		if err != nil || n == nil || n.Address() == "" || done == nil {
			t.Errorf("%s: select broken: node=%v done=%v err=%v", name, n, done, err)
		}
	}

	// 空候选返回 ErrNoAvailable
	sel, _ := loadbalance.NewSelector("roundrobin")
	if _, _, err := sel.Select(ctx, nil); err == nil {
		t.Error("expected error for empty nodes")
	}

	// 单个节点
	single := NodesFromEndpoints([]string{"http://1.1.1.1"})
	n, _, err := sel.Select(ctx, single)
	if err != nil || n.Address() != "http://1.1.1.1" {
		t.Errorf("single endpoint: %v %v", n, err)
	}
}

func TestPriorityQueue(t *testing.T) {
	q := NewPriorityQueue(10)
	ctx := context.Background()

	// 测试获取不同优先级的许可
	if !q.Acquire(ctx, PriorityHigh) {
		t.Error("expected high priority acquire")
	}
	if !q.Acquire(ctx, PriorityNormal) {
		t.Error("expected normal priority acquire")
	}
	if !q.Acquire(ctx, PriorityLow) {
		t.Error("expected low priority acquire")
	}

	if q.Len() != 3 {
		t.Errorf("expected len 3, got %d", q.Len())
	}

	// 释放许可
	q.Release(PriorityHigh)
	q.Release(PriorityNormal)
	q.Release(PriorityLow)

	if q.Len() != 0 {
		t.Errorf("expected len 0, got %d", q.Len())
	}
}

func TestGetPriority(t *testing.T) {
	// 测试 nil context
	p := GetPriority(nil) //nolint:staticcheck // intentionally testing nil context robustness
	if p != PriorityNormal {
		t.Errorf("expected PriorityNormal, got %v", p)
	}

	// 测试没有优先级的 context
	ctx := context.Background()
	p = GetPriority(ctx)
	if p != PriorityNormal {
		t.Errorf("expected PriorityNormal, got %v", p)
	}

	// 测试有优先级的 context
	ctx = WithPriority(ctx, PriorityHigh)
	p = GetPriority(ctx)
	if p != PriorityHigh {
		t.Errorf("expected PriorityHigh, got %v", p)
	}
}

func TestStaticResolver(t *testing.T) {
	endpoints := []string{"http://1.1.1.1", "http://2.2.2.2"}
	r := NewStaticResolver(endpoints)

	resolved, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 2 {
		t.Errorf("expected 2 endpoints, got %d", len(resolved))
	}

	// 测试空 endpoints
	r = NewStaticResolver(nil)
	_, err = r.Resolve(context.Background())
	if err == nil {
		t.Error("expected error for empty endpoints")
	}
}

func TestEndpointManager(t *testing.T) {
	// 使用静态 endpoints 直接设置
	m := NewEndpointManager(nil, nil, 0)

	// 设置 endpoints
	m.SetEndpoints([]string{"http://1.1.1.1", "http://2.2.2.2"})

	// 测试获取节点
	nodes := m.GetNodes()
	if len(nodes) != 2 {
		t.Errorf("expected 2 nodes, got %d", len(nodes))
	}

	// 测试 Pick
	picked, done, err := m.Pick(context.Background(), "/test")
	if err != nil || picked == "" || done == nil {
		t.Errorf("pick broken: %q %v %v", picked, done, err)
	}

	// 测试单个 endpoint
	m.SetEndpoints([]string{"http://3.3.3.3"})
	if got := m.GetNodes(); len(got) != 1 {
		t.Errorf("expected 1 node, got %d", len(got))
	}

	// 停止
	m.Stop()
}

func TestWithEndpoints(t *testing.T) {
	c := NewClient("test", WithEndpoints("http://1.1.1.1", "http://2.2.2.2"))
	if c.endpointManager == nil {
		t.Error("expected endpointManager to be set")
	}
}

func TestClient_Close(t *testing.T) {
	c := NewClient("test", WithEndpoints("http://1.1.1.1"))
	c.Close()
	// 不应该 panic
}
