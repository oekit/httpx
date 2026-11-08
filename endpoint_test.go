package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
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
