package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/sony/gobreaker"
)

// metricValue 从 Registry 黑盒采集指定 metric + labels 的值（单值 gauge/counter）。
func metricValue(t *testing.T, reg *prom.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			match := true
			for k, v := range labels {
				found := false
				for _, lp := range m.GetLabel() {
					if lp.GetName() == k && lp.GetValue() == v {
						found = true
						break
					}
				}
				if !found {
					match = false
					break
				}
			}
			if match {
				if m.GetGauge() != nil {
					return m.GetGauge().GetValue()
				}
				return m.GetCounter().GetValue()
			}
		}
	}
	t.Fatalf("metric %s with labels %v not found", name, labels)
	return 0
}

// TestClient_ResilienceMetrics 端到端钉住 httpx 弹性指标：
// 重试计数（引擎每次将重试的判定）、熔断迁移与状态 gauge、open 期拒绝计数。
// WithMetrics 与 WithCircuitBreaker 的传参顺序任意（闭包捕获 c，顺序无关）。
func TestClient_ResilienceMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	reg := prom.NewRegistry()
	c := NewClient("test-svc",
		WithBaseURL(srv.URL),
		// 故意把 WithCircuitBreaker 放在 WithMetrics 之前，验证顺序无关
		WithCircuitBreaker("test-svc", gobreaker.Settings{
			ReadyToTrip: func(counts gobreaker.Counts) bool { return counts.ConsecutiveFailures >= 1 },
			Timeout:     time.Hour,
		}),
		WithMetrics(reg, ""),
		WithRetryConfig(3, time.Millisecond, 5*time.Millisecond, time.Millisecond),
	)
	defer c.Close()

	var out map[string]any
	_ = c.GetJSON(context.Background(), "/boom", &out) // 3 次尝试全 500 → 熔断 trip

	if got := metricValue(t, reg, "http_client_retry_attempts_total",
		map[string]string{"service": "test-svc", "kind": "httpx"}); got != 2 {
		t.Fatalf("expected 2 retry attempts (maxAttempts=3, 1 initial + 2 retries), got %v", got)
	}
	if got := metricValue(t, reg, "http_client_breaker_state",
		map[string]string{"service": "test-svc", "kind": "httpx"}); got != 2 {
		t.Fatalf("expected open state (2), got %v", got)
	}
	if got := metricValue(t, reg, "http_client_breaker_transitions_total",
		map[string]string{"service": "test-svc", "kind": "httpx", "from": "closed", "to": "open"}); got != 1 {
		t.Fatalf("expected 1 closed→open transition, got %v", got)
	}

	// 第二次请求：熔断拒绝
	err := c.GetJSON(context.Background(), "/boom", &out)
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got %v", err)
	}
	if got := metricValue(t, reg, "http_client_breaker_rejected_total",
		map[string]string{"service": "test-svc", "kind": "httpx"}); got != 1 {
		t.Fatalf("expected 1 rejection, got %v", got)
	}
}

// TestClient_MetricsSharedAcrossClients 同 Registry 多 Client 复用 collector
// （一个服务调多个上游的常态）。
func TestClient_MetricsSharedAcrossClients(t *testing.T) {
	reg := prom.NewRegistry()
	c1 := NewClient("svc-x", WithMetrics(reg, ""), WithCircuitBreaker("svc-x", gobreaker.Settings{
		ReadyToTrip: func(counts gobreaker.Counts) bool { return counts.ConsecutiveFailures >= 1 },
	}))
	defer c1.Close()
	c2 := NewClient("svc-y", WithMetrics(reg, ""), WithCircuitBreaker("svc-y", gobreaker.Settings{
		ReadyToTrip: func(counts gobreaker.Counts) bool { return counts.ConsecutiveFailures >= 1 },
	}))
	defer c2.Close()

	if got := metricValue(t, reg, "http_client_breaker_state",
		map[string]string{"service": "svc-x", "kind": "httpx"}); got != 0 {
		t.Fatalf("expected c1 breaker gauge initialized closed, got %v", got)
	}
	if got := metricValue(t, reg, "http_client_breaker_state",
		map[string]string{"service": "svc-y", "kind": "httpx"}); got != 0 {
		t.Fatalf("expected c2 breaker gauge initialized closed, got %v", got)
	}
}
