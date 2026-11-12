package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oekit/httpx/loadbalance"
)

// TestJoinEndpoint 回归（#112）：base 与 path 拼接前归一分隔符——base 去尾
// "/"、path 补前导 "/"。修复前直接相加，path 缺前导 "/" 时 host 被吃进 path
// （"http://h:8080" + "api/x" ⇒ "http://h:8080api/x"）。
func TestJoinEndpoint(t *testing.T) {
	cases := []struct {
		base, path, want string
	}{
		{"http://h:8080", "/api", "http://h:8080/api"},
		{"http://h:8080", "api", "http://h:8080/api"},            // path 缺前导 /（#112 主形态）
		{"http://h:8080/", "api", "http://h:8080/api"},           // 双侧都不规整
		{"http://h:8080/", "/api", "http://h:8080/api"},          // base 带尾 /
		{"http://h:8080//", "api", "http://h:8080/api"},          // 多个尾 / 归一
		{"127.0.0.1:8080", "api", "127.0.0.1:8080/api"},          // 静态端点形态
		{"http://h:8080/base/", "api", "http://h:8080/base/api"}, // base 带路径前缀
	}
	for _, tc := range cases {
		if got := joinEndpoint(tc.base, tc.path); got != tc.want {
			t.Errorf("joinEndpoint(%q, %q) = %q, want %q", tc.base, tc.path, got, tc.want)
		}
	}
}

// TestResolveEndpoint_PathWithoutLeadingSlash 回归（#112）：baseURL 与静态
// 端点两条分支都必须归一 path，端点选点降级分支同修。
func TestResolveEndpoint_PathWithoutLeadingSlash(t *testing.T) {
	t.Run("baseURL", func(t *testing.T) {
		c := MustNewClient("v112", WithBaseURL("http://h:8080/"))
		defer c.Close()
		got, _ := c.resolveEndpoint(context.Background(), "api/x")
		if got != "http://h:8080/api/x" {
			t.Fatalf("resolveEndpoint = %q, want http://h:8080/api/x", got)
		}
	})
	t.Run("baseURL-trailing-slash", func(t *testing.T) {
		c := MustNewClient("v112", WithBaseURL("http://h:8080"))
		defer c.Close()
		got, _ := c.resolveEndpoint(context.Background(), "api/x")
		if got != "http://h:8080/api/x" {
			t.Fatalf("resolveEndpoint = %q, want http://h:8080/api/x", got)
		}
	})
	t.Run("static-endpoint", func(t *testing.T) {
		c := MustNewClient("v112", WithEndpoints("http://h:8080"))
		defer c.Close()
		got, _ := c.resolveEndpoint(context.Background(), "api/x")
		if got != "http://h:8080/api/x" {
			t.Fatalf("resolveEndpoint = %q, want http://h:8080/api/x", got)
		}
	})
	t.Run("absolute-path-unchanged", func(t *testing.T) {
		c := MustNewClient("v112", WithBaseURL("http://h:8080"))
		defer c.Close()
		got, _ := c.resolveEndpoint(context.Background(), "/api/x?k=v")
		if got != "http://h:8080/api/x?k=v" {
			t.Fatalf("resolveEndpoint = %q, want http://h:8080/api/x?k=v", got)
		}
	})
}

// TestDoRaw_PathWithoutLeadingSlash_Live 回归（#112）端到端：修复前请求被拨向
// "127.0.0.1:<port>no-slash" 这种非法主机（报误导性的 unknown port），根本没
// 发出去；修复后服务端必须看到 "/no-slash"。
func TestDoRaw_PathWithoutLeadingSlash_Live(t *testing.T) {
	var sawPath atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath.Store(r.URL.Path)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := MustNewClient("v112-live", WithBaseURL(srv.URL))
	defer c.Close()

	body, err := c.DoRaw(context.Background(), http.MethodGet, "no-slash", nil)
	if err != nil {
		t.Fatalf("path without leading slash must not corrupt host, err = %v", err)
	}
	if string(body) != "ok" {
		t.Fatalf("body = %q, want ok", body)
	}
	if got, _ := sawPath.Load().(string); got != "/no-slash" {
		t.Fatalf("server saw path %q, want /no-slash", got)
	}
}

// TestResolveEndpoint_Fallback_PathWithoutLeadingSlash：选点失败回退 baseURL
// 的降级分支同样归一（#112 全分支覆盖）。
func TestResolveEndpoint_Fallback_PathWithoutLeadingSlash(t *testing.T) {
	// 无可用节点（空端点列表）⇒ Pick 失败 ⇒ 回退 baseURL
	c := MustNewClient("v112-fb", WithBaseURL("http://h:8080"), WithEndpoints("http://stale:1"))
	c.endpointManager.SetEndpoints(nil)
	defer c.Close()
	got, done := c.resolveEndpoint(context.Background(), "api/x")
	if got != "http://h:8080/api/x" {
		t.Fatalf("fallback resolveEndpoint = %q, want http://h:8080/api/x", got)
	}
	if done != nil {
		t.Fatalf("fallback done should be nil")
	}
}

// testNodeFor 是构造单节点快照的测试助手（#166 用）。
func testNodeFor(addr string) []loadbalance.Node {
	return NodesFromEndpoints([]string{addr})
}

// TestEndpointManager_GetNodes_ReturnsDefensiveCopy 回归（#166）：GetNodes
// 必须返回内部切片的副本——修复前直接返回 m.nodes，调用方对返回切片做
// 元素替换即并发改写选择器视图（别名写穿面暴露在导出 API）。
func TestEndpointManager_GetNodes_ReturnsDefensiveCopy(t *testing.T) {
	m := NewEndpointManager(nil, nil, 0)
	m.SetNodes(testNodeFor("http://10.0.0.1:8080"))

	got := m.GetNodes()
	if len(got) != 1 {
		t.Fatalf("len(nodes) = %d, want 1", len(got))
	}
	// 改写返回切片：管理器视图不受影响
	got[0] = testNodeFor("http://evil:1")[0]

	again := m.GetNodes()
	if again[0].Address() != "http://10.0.0.1:8080" {
		t.Errorf("GetNodes exposed internal slice: after caller mutation, address = %q, want unchanged (#166)", again[0].Address())
	}
	// 两次 GetNodes 是独立副本：改写第一次的返回值不影响第二次
	if got[0].Address() != "http://evil:1" {
		t.Errorf("first copy was not caller-writable: %q", got[0].Address())
	}
}

// TestEndpointManager_SetNodes_DefensiveCopy 回归（#166）：SetNodes 存储
// 前必须拷贝入参——调用方在 SetNodes 之后修改自己的切片不得写穿管理器
// 视图（修复前 m.nodes 与调用方切片共享底层数组）。
func TestEndpointManager_SetNodes_DefensiveCopy(t *testing.T) {
	m := NewEndpointManager(nil, nil, 0)
	nodes := testNodeFor("http://10.0.0.1:8080")
	m.SetNodes(nodes)

	// 调用方改写入参（替换元素）
	nodes[0] = testNodeFor("http://evil:1")[0]

	if got := m.GetNodes()[0].Address(); got != "http://10.0.0.1:8080" {
		t.Errorf("SetNodes aliased caller slice: after caller mutation, address = %q, want unchanged (#166)", got)
	}
}

// TestEndpointManager_SetNodes_NilAndEmpty 回归（#166 附带）：nil/空入参
// 经防御性拷贝后为空快照，GetNodes 降级到缓存或返回空，不得 panic。
func TestEndpointManager_SetNodes_NilAndEmpty(t *testing.T) {
	m := NewEndpointManager(nil, nil, 0)
	m.SetNodes(nil)
	if got := m.GetNodes(); len(got) != 0 {
		t.Errorf("SetNodes(nil) then GetNodes = %d nodes, want 0", len(got))
	}
	m.SetNodes([]loadbalance.Node{})
	if got := m.GetNodes(); len(got) != 0 {
		t.Errorf("SetNodes(empty) then GetNodes = %d nodes, want 0", len(got))
	}
}

// TestWithRefreshInterval_NoSharedArray 回归（#166）：WithRefreshInterval
// 迁移节点（m.SetNodes(old.GetNodes())）后，新旧两个 manager 不得共享同一
// 底层数组——修复前 old.GetNodes() 原样返回内部切片，新旧视图别名，改写
// 任意一侧返回值都会串到另一侧。
func TestWithRefreshInterval_NoSharedArray(t *testing.T) {
	c := MustNewClient("svc", WithEndpoints("http://10.0.0.1:8080"))
	defer c.Close()
	old := c.endpointManager

	// 重新应用 option（与 NewClient 期组合完全同一路径）：替换 manager 并
	// 经 m.SetNodes(old.GetNodes()) 迁移节点——#166 场景的本体
	WithRefreshInterval(time.Hour)(c)
	fresh := c.endpointManager
	if fresh == nil || fresh == old {
		t.Fatal("WithRefreshInterval did not replace endpointManager")
	}

	nodes := fresh.GetNodes()
	if len(nodes) != 1 {
		t.Fatalf("len(nodes) = %d, want 1", len(nodes))
	}
	// 改写新 manager 的返回切片：新旧两个 manager 的视图都不得被写穿
	nodes[0] = testNodeFor("http://evil:1")[0]
	if got := fresh.GetNodes()[0].Address(); got != "http://10.0.0.1:8080" {
		t.Errorf("fresh manager view mutated via returned slice (#166): %q", got)
	}
	if got := old.GetNodes()[0].Address(); got != "http://10.0.0.1:8080" {
		t.Errorf("old manager shares array with fresh manager (#166): %q", got)
	}
}
