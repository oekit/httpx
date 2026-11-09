package httpx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/cloudwego/hertz/pkg/protocol"
)

// headerEchoServer 返回一个把关心的请求头回显成 JSON 的 httptest 服务。
func headerEchoServer(t *testing.T, keys ...string) (*httptest.Server, func() map[string]string) {
	t.Helper()
	var mu sync.Mutex
	seen := make(map[string]string, len(keys))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		for _, k := range keys {
			seen[k] = r.Header.Get(k)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"ok": "1"})
	}))
	t.Cleanup(srv.Close)
	return srv, func() map[string]string {
		mu.Lock()
		defer mu.Unlock()
		out := make(map[string]string, len(seen))
		for k, v := range seen {
			out[k] = v
		}
		return out
	}
}

// TestWithHeaders_StaticHeadersOnEveryRequest 回归（#128）：WithHeaders 声明的
// 静态头对**每请求**注入——纯静态 API-Key 场景此前只能手搓 OnBeforeRequest 闭包。
//
// 覆盖三条路径：封装方法 DoJSON（含无 body 请求）、DoRaw（带 body 请求）、
// 裸 Hertz().Do——静态头走中间件链，三条路径行为必须一致。
func TestWithHeaders_StaticHeadersOnEveryRequest(t *testing.T) {
	srv, headers := headerEchoServer(t, "X-API-Key", "X-Tenant")

	cli := MustNewClient("svc",
		WithBaseURL(srv.URL),
		WithHeaders(map[string]string{"X-API-Key": "key-123", "X-Tenant": "acme"}),
	)

	var out map[string]string
	if err := cli.DoJSON(context.Background(), http.MethodPost, "/pay", nil, &out); err != nil {
		t.Fatalf("DoJSON (no body): %v", err)
	}
	got := headers()
	if got["X-API-Key"] != "key-123" || got["X-Tenant"] != "acme" {
		t.Errorf("DoJSON headers = %v, want X-API-Key=key-123 X-Tenant=acme", got)
	}

	if _, err := cli.DoRaw(context.Background(), http.MethodPost, "/raw", map[string]string{"a": "1"}); err != nil {
		t.Fatalf("DoRaw: %v", err)
	}
	got = headers()
	if got["X-API-Key"] != "key-123" || got["X-Tenant"] != "acme" {
		t.Errorf("DoRaw headers = %v, want X-API-Key=key-123 X-Tenant=acme", got)
	}

	req, resp := protocol.AcquireRequest(), protocol.AcquireResponse()
	defer protocol.ReleaseRequest(req)
	defer protocol.ReleaseResponse(resp)
	req.SetMethod(http.MethodGet)
	req.SetRequestURI(srv.URL + "/bare")
	if err := cli.Hertz().Do(context.Background(), req, resp); err != nil {
		t.Fatalf("bare Hertz().Do: %v", err)
	}
	got = headers()
	if got["X-API-Key"] != "key-123" || got["X-Tenant"] != "acme" {
		t.Errorf("bare Do headers = %v, want X-API-Key=key-123 X-Tenant=acme", got)
	}
}

// TestWithHeader_SinglePair 回归（#128）：WithHeader 单条声明的形态与
// WithHeaders 等价（多值时逐条并入同一张静态头表）。
func TestWithHeader_SinglePair(t *testing.T) {
	srv, headers := headerEchoServer(t, "X-API-KEY")

	cli := MustNewClient("svc",
		WithBaseURL(srv.URL),
		WithHeader("X-API-KEY", "apisix-key"),
	)

	var out map[string]string
	if err := cli.DoJSON(context.Background(), http.MethodGet, "/x", nil, &out); err != nil {
		t.Fatalf("DoJSON: %v", err)
	}
	if got := headers()["X-API-KEY"]; got != "apisix-key" {
		t.Errorf("X-API-KEY = %q, want %q", got, "apisix-key")
	}
}

// TestWithHeaders_MultipleCallsMergeLaterWins 语义钉子：多次 WithHeaders/WithHeader
// 按键合并、后写者赢——配置分两层（基础头 + 场景覆盖头）时无需手工并 map。
func TestWithHeaders_MultipleCallsMergeLaterWins(t *testing.T) {
	srv, headers := headerEchoServer(t, "A", "B", "C")

	cli := MustNewClient("svc",
		WithBaseURL(srv.URL),
		WithHeaders(map[string]string{"A": "1", "B": "1"}),
		WithHeaders(map[string]string{"A": "2", "C": "3"}), // A 覆盖为 2
		WithHeader("B", "4"), // B 覆盖为 4
	)

	var out map[string]string
	if err := cli.DoJSON(context.Background(), http.MethodGet, "/x", nil, &out); err != nil {
		t.Fatalf("DoJSON: %v", err)
	}
	got := headers()
	want := map[string]string{"A": "2", "B": "4", "C": "3"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("header %s = %q, want %q (merge must be last-writer-wins per key)", k, got[k], v)
		}
	}
}

// TestWithHeaders_UserClosureOverridesStatic 叠加语义：用户 OnBeforeRequest
// 闭包注册在静态头之后（内层）执行 ⇒ 同名键以闭包为准——静态值兜底、
// 动态值（时间戳/签名）覆盖；未冲突的键两者并存。
func TestWithHeaders_UserClosureOverridesStatic(t *testing.T) {
	srv, headers := headerEchoServer(t, "X-API-Key", "1Panel-Timestamp")

	cli := MustNewClient("svc",
		WithBaseURL(srv.URL),
		WithHeaders(map[string]string{"X-API-Key": "static-key"}),
		WithOnBeforeRequest(func(_ context.Context, r *protocol.Request) error {
			r.SetHeader("1Panel-Timestamp", "1700000000") // 动态签名族：每请求现算
			r.SetHeader("X-API-Key", "dynamic-key")       // 同名覆盖
			return nil
		}),
	)

	var out map[string]string
	if err := cli.DoJSON(context.Background(), http.MethodGet, "/x", nil, &out); err != nil {
		t.Fatalf("DoJSON: %v", err)
	}
	got := headers()
	if got["X-API-Key"] != "dynamic-key" {
		t.Errorf("X-API-Key = %q, want %q (user closure runs inside the static-header mw)", got["X-API-Key"], "dynamic-key")
	}
	if got["1Panel-Timestamp"] != "1700000000" {
		t.Errorf("1Panel-Timestamp = %q, want %q (closure-added keys pass through)", got["1Panel-Timestamp"], "1700000000")
	}
}
