package httpx

import (
	"strings"
	"testing"

	"github.com/oekit/registry"
)

// 本文件是 #163 的回归基线：WithNNS/NewNNSResolver 对 nil NNSClient 必须
// 构造期 fail-fast。修复前 nil 客户端放行到首个刷新 tick 的 GetInstances
// 才 panic——async.GoSafe 只 recover 不重启，刷新循环静默死亡，节点恒空，
// 全部请求静默回落 baseURL，故障完全不可见。

// TestNewNNSResolver_NilClient_Panics 钉住直接构造路径的构造期 panic
// （对齐 hertz/nns NewRegistry 的 "requires non-nil client" 口径）。
func TestNewNNSResolver_NilClient_Panics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("NewNNSResolver(nil client) 应构造期 panic")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic 值应为 string，实得 %T（%v）", r, r)
		}
		if !strings.Contains(msg, "non-nil nnsClient") {
			t.Errorf("panic 信息应指明 nil client 原因：%q", msg)
		}
	}()
	_ = NewNNSResolver("svc-a", nil)
}

// TestWithNNS_NilClient_ConstructError 钉住 option 路径的构造期校验：
// NewClient 显式返回 error（#83 口径，不升级为进程死亡），且不再创建
// 带刷新循环的 EndpointManager（nil resolver 的空转循环正是本单缺陷根因）。
func TestWithNNS_NilClient_ConstructError(t *testing.T) {
	c, err := NewClient("svc-a", WithNNS("svc-a", nil))
	if err == nil {
		t.Fatal("WithNNS(nil client) 应使 NewClient 返回 error")
	}
	if c != nil {
		t.Fatalf("构造失败应返回 nil client，实得 %T", c)
	}
	if !strings.Contains(err.Error(), "nnsClient 为 nil") {
		t.Errorf("错误信息应指明 nil client 原因：%v", err)
	}
}

// TestWithNNS_NilClientAfterValidOptions 构造失败路径不得泄漏资源：
// 先装合法 NNS 再被后续 nil NNS 拒绝时，已创建的刷新循环被 Stop
// （NewClient 的 constructErr 分支负责），进程无残留 goroutine。
func TestWithNNS_NilClientAfterValidOptions(t *testing.T) {
	stub := &stubNNSClient{instances: []registry.Instance{{IP: "10.0.0.1", Port: 8080}}}
	c, err := NewClient("svc-a", WithNNS("svc-a", stub), WithNNS("svc-a", nil))
	if err == nil {
		t.Fatal("组合中任一 nil NNSClient 都应构造期报错")
	}
	if c != nil {
		t.Fatalf("构造失败应返回 nil client，实得 %T", c)
	}
}
