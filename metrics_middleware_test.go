package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudwego/hertz/pkg/protocol"
	prom "github.com/prometheus/client_golang/prometheus"
)

// metricValueFound 从 registry 黑盒采集指定 metric + labels 的值；series
// 不存在时 found=false（resilience 测试 metricValue 的非致命变体：断言
// 「series 未被错误创建」或「本就不该存在」时用，不 t.Fatalf）。
func metricValueFound(t *testing.T, reg *prom.Registry, name string, labels map[string]string) (float64, bool) {
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
			if !match {
				continue
			}
			switch {
			case m.GetGauge() != nil:
				return m.GetGauge().GetValue(), true
			case m.GetCounter() != nil:
				return m.GetCounter().GetValue(), true
			}
		}
	}
	return 0, false
}

// TestMetricsMiddleware_InFlightTargetSwitch_KeepsSeriesPaired 回归（#203
// 失配一）：SetTarget 在请求 in-flight 期间切换时，Inc/Dec 必须落在同一
// target series——修复前入口与收尾各读一次 targetLabel，Inc 落旧 label、
// Dec 落新 label：旧 series in-flight 永久 +1（面板虚高不回落）、新 series
// 被减到负数，每次切换留下一对错位。
func TestMetricsMiddleware_InFlightTargetSwitch_KeepsSeriesPaired(t *testing.T) {
	reg := prom.NewRegistry()
	inHandler := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(inHandler)
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := MustNewClient("switch-svc", WithBaseURL(srv.URL),
		WithMetrics(reg, "switchprobe"), WithTarget("old-target"))
	t.Cleanup(c.Close)

	// 请求卡在 handler 里 = 确定处于 in-flight 窗口，此刻切换 target
	done := make(chan error, 1)
	go func() {
		done <- c.GetJSON(context.Background(), "/x", nil)
	}()
	<-inHandler
	c.SetTarget("new-target")
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("request: %v", err)
	}

	// 旧 series：Inc/Dec 同落（单次读 label），必须归零（修复前永久 +1）
	got, found := metricValueFound(t, reg, "switchprobe_requests_in_flight", map[string]string{
		"service": "switch-svc", "target": "old-target", "method": http.MethodGet,
	})
	if !found {
		t.Fatal("old-target series missing: Inc should have landed there")
	}
	if got != 0 {
		t.Errorf("old-target in-flight = %v, want 0（Inc/Dec 必须同 series，#203）", got)
	}
	// 新 series：修复前 Dec 单独落这里 ⇒ 减到 -1；修复后 label 单次读于
	// 请求入口，该 series 根本不会被本次请求创建
	if got, found := metricValueFound(t, reg, "switchprobe_requests_in_flight", map[string]string{
		"service": "switch-svc", "target": "new-target", "method": http.MethodGet,
	}); found && got != 0 {
		t.Errorf("new-target in-flight = %v, want series absent or 0（不得被错位 Dec 减到负数）", got)
	}
}

// TestMetricsMiddleware_PanicStillPairsInFlight 回归（#203 失配二）：next
// panic 时回写必须由 defer 兜底配对——修复前 Inc 后直串 next，panic 让
// recordRequestEnd 不执行，同 label +1 永久泄漏；上层 recover 后进程继续，
// gauge 已错且无任何信号。panic 路径 statusCode 记 0（与 err != nil 同
// 口径），panic 原样继续上抛。
func TestMetricsMiddleware_PanicStillPairsInFlight(t *testing.T) {
	reg := prom.NewRegistry()
	c := MustNewClient("panic-svc", WithMetrics(reg, "panicprobe"))
	t.Cleanup(c.Close)

	if len(c.mw) == 0 {
		t.Fatal("metrics middleware not registered")
	}
	// setupMetricsMiddleware 是 NewClient 里最后一个向 c.mw 追加的 option
	// 收尾（options 全部求值之后），末位即 metrics 中间件；白盒直调其
	// inner endpoint，让 next 直接 panic（等价链路深处 panic 上抛穿过本层）
	mw := c.mw[len(c.mw)-1]
	endpoint := mw(func(context.Context, *protocol.Request, *protocol.Response) error {
		panic("boom")
	})

	req, resp := protocol.AcquireRequest(), protocol.AcquireResponse()
	defer protocol.ReleaseRequest(req)
	defer protocol.ReleaseResponse(resp)
	req.SetMethod(http.MethodGet)

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("panic must propagate to caller (recover semantics unchanged)")
			}
		}()
		_ = endpoint(context.Background(), req, resp)
	}()

	got, found := metricValueFound(t, reg, "panicprobe_requests_in_flight", map[string]string{
		"service": "panic-svc", "target": defaultTargetLabel, "method": http.MethodGet,
	})
	if !found {
		t.Fatal("in-flight series missing")
	}
	if got != 0 {
		t.Errorf("in-flight after panic = %v, want 0（Inc 必须有 defer Dec 配对，#203）", got)
	}
	// 收尾照常记账：status_code=0（panic 无响应码的口径）
	if got, found := metricValueFound(t, reg, "panicprobe_requests_total", map[string]string{
		"service": "panic-svc", "target": defaultTargetLabel,
		"method": http.MethodGet, "status_code": "0",
	}); !found || got != 1 {
		t.Errorf("requests_total{status_code=0} = %v (found=%v), want 1（panic 路径 statusCode 记 0）", got, found)
	}
}
