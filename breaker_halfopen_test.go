package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/sony/gobreaker"
)

// TestWithCircuitBreaker_HalfOpenProbeExhaustedIsCircuitOpen 回归（#235）：
// 半开态探测名额耗尽（gobreaker.ErrTooManyRequests）必须与 open 态拒绝同样
// 收敛——errors.Is(err, ErrCircuitOpen) 为真、breaker_rejected_total 计数 +1。
//
// 修复前中间件只认 ErrOpenState：半开窗口内第 2 个请求拿到裸
// "too many requests" 原样漏出——按「熔断打开→降级/快速失败」写的分支恒不触发
// （请求被当成普通上游错误走重试或兜底）、半开期（最该计的那段）的拒绝漏计、
// 文案还极易被误读成上游 429。
//
// 时序（gobreaker v1.0.0 beforeRequest：半开态 counts.Requests >= maxRequests
// 即拒，counts 在 onRequest 时就递增，故在飞探测会占住名额）：
//
//	500 → trip 打开 → 等 Timeout 过期 → 探测 A 进 handler 阻塞（名额占满）
//	→ 探测 B 被拒（本测试钉的路径）→ 放行 A → A 正常完成。
func TestWithCircuitBreaker_HalfOpenProbeExhaustedIsCircuitOpen(t *testing.T) {
	const svc = "halfopen-svc"

	var (
		phase         atomic.Bool // false = trip 阶段（500），true = probe 阶段
		probeHits     atomic.Int32
		probeStarted  = make(chan struct{})
		releaseProbe  = make(chan struct{})
		probeStartedN atomic.Bool
		releaseOnce   sync.Once
	)
	release := func() { releaseOnce.Do(func() { close(releaseProbe) }) }
	// 兜底放行：任一断言失败（t.Fatal）也要解开 handler，否则 httptest.Server.Close
	// 会等在未完成请求上把失败变成挂死。
	defer release()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !phase.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		probeHits.Add(1)
		if probeStartedN.CompareAndSwap(false, true) {
			close(probeStarted)
		}
		<-releaseProbe
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	reg := prom.NewRegistry()
	c := MustNewClient(svc,
		WithBaseURL(srv.URL),
		WithCircuitBreaker(svc, gobreaker.Settings{
			ReadyToTrip: func(counts gobreaker.Counts) bool { return counts.ConsecutiveFailures >= 1 },
			Timeout:     30 * time.Millisecond,
		}),
		WithMetrics(reg, ""),
	)
	t.Cleanup(c.Close)

	// 1) 一次 500 → 熔断打开
	var out map[string]any
	if err := c.GetJSON(context.Background(), "/x", &out); err == nil {
		t.Fatal("expected 500 error to trip the breaker")
	}
	// 2) 等 Timeout 过期：下一次请求把 Open 推进到 HalfOpen
	time.Sleep(80 * time.Millisecond)

	// 3) 探测 A 进 handler 阻塞，占满半开名额（MaxRequests 缺省 1）
	phase.Store(true)
	aErr := make(chan error, 1)
	go func() {
		var aOut map[string]any
		aErr <- c.GetJSON(context.Background(), "/probe", &aOut)
	}()
	select {
	case <-probeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("probe request never reached the handler")
	}

	// 4) 探测 B：名额耗尽 → 必须收敛为 ErrCircuitOpen + rejected 指标 +1
	//    （带超时兜底：修复缺失时 B 会穿进 handler 阻塞，测试要快速失败而非挂死）
	ctxB, cancelB := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelB()
	bErr := c.GetJSON(ctxB, "/probe", &out)
	if !errors.Is(bErr, ErrCircuitOpen) {
		t.Fatalf("half-open probe exhausted: err = %v, want errors.Is(err, ErrCircuitOpen)", bErr)
	}
	if got := metricValue(t, reg, "http_client_breaker_rejected_total",
		map[string]string{"service": svc, "kind": "httpx"}); got != 1 {
		t.Fatalf("breaker_rejected_total = %v, want 1（半开名额耗尽必须计拒绝）", got)
	}
	if got := probeHits.Load(); got != 1 {
		t.Fatalf("handler hit = %d, want 1（被拒请求不得打到后端）", got)
	}

	// 5) 放行探测 A：应正常完成、熔断回到 closed
	release()
	select {
	case err := <-aErr:
		if err != nil {
			t.Fatalf("probe A: %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("probe A never returned")
	}
}
