package httpx

import (
	"strings"
	"testing"
	"time"

	"github.com/oekit/metrics"
	prom "github.com/prometheus/client_golang/prometheus"
)

// TestMustRegisterReuse_TypeMismatch_ActionablePanic 回归测试：同名同描述符
// 但**类型不同**时，panic 的信息必须能定位问题。
//
// 缺陷成因：既有 collector 与请求的 collector 描述符相同（fqName + help +
// 标签名都一致）但类型不同 ⇒ prometheus 返回 AlreadyRegisteredError（不是
// 直接拒绝）⇒ `are.ExistingCollector.(T)` 是一次**无检查的类型断言** ⇒
// panic 成一句不可定位的 "interface conversion: ..."。
func TestMustRegisterReuse_TypeMismatch_ActionablePanic(t *testing.T) {
	reg := prom.NewRegistry()
	// 描述符与 httpx 自己的 requests_in_flight 完全一致，但类型是 CounterVec。
	reg.MustRegister(prom.NewCounterVec(prom.CounterOpts{
		Name:      "requests_in_flight",
		Namespace: "http_client",
		Help:      "Number of outgoing HTTP requests currently in flight.",
	}, []string{"service", "target", "method"}))

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("类型不符时应 panic")
		}
		msg, ok := r.(error)
		if !ok {
			t.Fatalf("panic 值应为 error，实得 %T（%v）", r, r)
		}
		text := msg.Error()
		if !strings.Contains(text, "类型不符") {
			t.Errorf("panic 信息未说明「类型不符」：%q", text)
		}
		if !strings.Contains(text, "*prometheus.CounterVec") ||
			!strings.Contains(text, "*prometheus.GaugeVec") {
			t.Errorf("panic 信息未同时给出既有类型与请求类型：%q", text)
		}
	}()

	_ = newClientMetrics(reg, "http_client")
}

// TestNewClientMetrics_NilRegistry_LandsInPlatformRegistry 回归（#181）：
// WithMetrics(nil, …)/newClientMetrics(nil, …) 的 nil 缺省路径必须落到
// metrics.Registry（平台 /metrics 端点 gather 的目标 registry，与
// resilience.NewMetrics 的 nil 回退统一）——修复前落 prom.DefaultRegisterer，
// 指标注册成功却抓不到，可观测性静默丢失。
func TestNewClientMetrics_NilRegistry_LandsInPlatformRegistry(t *testing.T) {
	const ns = "nilreg_probe181"
	m := newClientMetrics(nil, ns)
	m.recordRequestEnd("svc", "tgt", "GET", "200", 10*time.Millisecond)

	// 平台 registry 可见
	platformSeen := gatherHasPrefix(metrics.Registry, ns+"_")
	if !platformSeen {
		t.Error("nil registry fallback must land in metrics.Registry (platform-visible, #181)")
	}
	// prom 默认 registry 不再收留（此前 nil 路径的错误落点）
	if gatherHasPrefix(prom.DefaultGatherer, ns+"_") {
		t.Error("nil registry fallback must NOT land in prom.DefaultRegisterer (#181)")
	}
}

// gatherHasPrefix 在 gatherer 的 metric family 名里找前缀。
func gatherHasPrefix(g prom.Gatherer, prefix string) bool {
	families, err := g.Gather()
	if err != nil {
		return false
	}
	for _, mf := range families {
		if strings.HasPrefix(mf.GetName(), prefix) {
			return true
		}
	}
	return false
}

// TestNewClientMetrics_ExplicitRegistryWins 钉住 #181 不误伤：显式传 registry
// 的调用方（doc.go 推荐形态 WithMetrics(metrics.Registry, …) 之外的任意
// registry）仍然写进显式 registry，nil 语义变更不影响显式路径。
func TestNewClientMetrics_ExplicitRegistryWins(t *testing.T) {
	reg := prom.NewRegistry()
	m := newClientMetrics(reg, "explicit_probe181")
	m.recordRequestEnd("svc", "tgt", "GET", "200", 10*time.Millisecond)

	if !gatherHasPrefix(reg, "explicit_probe181_") {
		t.Error("explicit registry must receive metrics")
	}
	if gatherHasPrefix(metrics.Registry, "explicit_probe181_") {
		t.Error("explicit registry must not leak into metrics.Registry")
	}
	if gatherHasPrefix(prom.DefaultGatherer, "explicit_probe181_") {
		t.Error("explicit registry must not leak into prom.DefaultRegisterer")
	}
}
