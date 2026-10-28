package httpx

import (
	"strings"
	"testing"

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
