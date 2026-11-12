package httpx

import (
	"strconv"
	"time"

	"github.com/oekit/metrics"
	"github.com/oekit/metrics/resilience"
	prom "github.com/prometheus/client_golang/prometheus"
)

// defaultTargetLabel 是 target label 在调用方未显式设置时的回退值。
// 所有未指定 target 的请求会聚到同一 series，避免高基数标签爆炸。
const defaultTargetLabel = "default"

// clientMetrics 管理 HTTP 客户端的 Prometheus 指标。
//
// 标签约定（与 PLATFORM_CONTRACT.md 5.3 一致）：
//   - service: 调用方服务名（业务固定）
//   - target: 被调服务标识（业务通过 WithTarget 显式传入；不传时为 "default"）
//   - method: HTTP 方法
//   - status_code: HTTP 状态码
//
// 禁止使用 host/IP/port/path 等高基数来源作为 label。
type clientMetrics struct {
	requestsTotal    *prom.CounterVec
	requestDuration  *prom.HistogramVec
	requestsInFlight *prom.GaugeVec

	// rm 弹性指标（熔断/重试），与请求指标同一 Registry + namespace
	// （如 http_client_breaker_state）。kind 标签恒为 "httpx"。
	rm *resilience.Metrics
}

// newClientMetrics 创建并注册客户端指标。
//
// reg 为 nil 时回退 metrics.Registry（#181）：平台 /metrics 端点只 gather
// metrics.Registry（hertz/metrics 的 MetricsHandler 口径），此前回退
// prom.DefaultRegisterer 的 nil 缺省路径让请求指标 + resilience 弹性指标
// 全部注册成功却抓不到（可观测性静默丢失，metrics#50 点名的 httpx 半边——
// #50 只修了 NewMetrics 自己的回退，httpx 在调用前已把 nil 回填成非 nil，
// 修复对其不生效）。现与 resilience.NewMetrics 的 nil 回退、metrics.Init 的
// 注册目标三处统一；确需进 prom 默认全局 registry 的调用方显式传
// prom.DefaultRegisterer。
func newClientMetrics(reg prom.Registerer, namespace string) *clientMetrics {
	if reg == nil {
		reg = metrics.Registry
	}
	if namespace == "" {
		namespace = "http_client"
	}

	m := &clientMetrics{}
	// 注册复用经 metrics.RegisterReuse 单源（metrics#100 收编本仓本地实现）：
	// 同名 collector 已注册（同一 Registry + namespace 下创建多个 Client 是
	// 真实场景：一个服务调多个上游）时复用既有实例；同名异型 panic 人话文案。
	m.requestsTotal = metrics.RegisterReuse(reg, prom.NewCounterVec(prom.CounterOpts{
		Name:      "requests_total",
		Namespace: namespace,
		Help:      "Total number of outgoing HTTP requests.",
	}, []string{"service", "target", "method", "status_code"}), metrics.WithReuseOwner("httpx"))
	m.requestDuration = metrics.RegisterReuse(reg, prom.NewHistogramVec(prom.HistogramOpts{
		Name:      "request_duration_seconds",
		Namespace: namespace,
		Help:      "Duration of outgoing HTTP requests in seconds.",
		Buckets:   prom.DefBuckets,
	}, []string{"service", "target", "method"}), metrics.WithReuseOwner("httpx"))
	m.requestsInFlight = metrics.RegisterReuse(reg, prom.NewGaugeVec(prom.GaugeOpts{
		Name:      "requests_in_flight",
		Namespace: namespace,
		Help:      "Number of outgoing HTTP requests currently in flight.",
	}, []string{"service", "target", "method"}), metrics.WithReuseOwner("httpx"))
	m.rm = resilience.NewMetrics(reg, namespace)
	return m
}

// metricsKind 是 httpx 在弹性指标里的 kind 标签值。
const metricsKind = "httpx"

// recordRequestEnd 记录一次请求结束的指标：in-flight 减一 + 总数/时延。
// in-flight 的加一在请求开始处（setupMetricsMiddleware）直接 Inc。
func (m *clientMetrics) recordRequestEnd(service, target, method, statusCode string, duration time.Duration) {
	if target == "" {
		target = defaultTargetLabel
	}
	labels := make([]string, 0, 4)
	labels = append(labels, service, target, method)

	m.requestsInFlight.WithLabelValues(labels...).Dec()
	m.requestsTotal.WithLabelValues(append(labels, statusCode)...).Inc()
	m.requestDuration.WithLabelValues(labels...).Observe(duration.Seconds())
}

// statusCodeStr 将 HTTP 状态码转为字符串。
func statusCodeStr(code int) string {
	return strconv.Itoa(code)
}
