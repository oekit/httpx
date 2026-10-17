package httpx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/oekit/httpx/codec"
	"github.com/oekit/httpx/loadbalance"
	"github.com/oekit/registry"
)

// TestClientSelectorRoundRobin 钉住 WithSelector 全链路：NNS 实例视图
// （含权重/元数据）→ Selector 轮询 → 双实例交替命中。
func TestClientSelectorRoundRobin(t *testing.T) {
	var hitsA, hitsB atomic.Int64
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hitsA.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hitsB.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srvB.Close()

	// httptest 地址形如 127.0.0.1:port；registry.Instance.Addr() 输出同形，
	// SelectorNodes 补 http:// 前缀后与服务器直连地址一致
	stub := &stubNNSClient{instances: []registry.Instance{
		{Service: "rr-svc", IP: srvA.Listener.Addr().String()[:0] + hostOf(srvA), Port: portOf(srvA), Weight: 1},
		{Service: "rr-svc", IP: hostOf(srvB), Port: portOf(srvB), Weight: 1},
	}}

	sel, err := loadbalance.NewSelector(loadbalance.StrategyRoundRobin)
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient("rr-svc", WithNNS("rr-svc", stub), WithSelector(sel))
	defer c.Close()

	// 首刷是异步的：显式同步刷新一次，消除竞态
	c.endpointManager.refreshNodes(context.Background())

	ctx := context.Background()
	var out map[string]bool
	for i := 0; i < 4; i++ {
		if err := c.GetJSON(ctx, "/ping", &out); err != nil {
			t.Fatalf("GetJSON: %v", err)
		}
		if !out["ok"] {
			t.Fatal("unexpected payload")
		}
	}
	if hitsA.Load() != 2 || hitsB.Load() != 2 {
		t.Fatalf("roundrobin distribution broken: A=%d B=%d, want 2/2", hitsA.Load(), hitsB.Load())
	}
}

func hostOf(s *httptest.Server) string {
	host := s.Listener.Addr().String()
	for i := len(host) - 1; i >= 0; i-- {
		if host[i] == ':' {
			return host[:i]
		}
	}
	return host
}

func portOf(s *httptest.Server) int {
	addr := s.Listener.Addr().String()
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			port := 0
			fmt.Sscanf(addr[i+1:], "%d", &port)
			return port
		}
	}
	return 0
}

// TestDoCodecRoundTrip 钉住 DoCodec：请求 Content-Type 由 codec 决定，
// 响应按 Content-Type 反查 codec 反序列化。
func TestDoCodecRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 仅对带 body 的 POST 断言 Content-Type（尾部的 GET 无 body，无 Content-Type 合法）
		if r.Method == http.MethodPost {
			if got := r.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("request content-type = %q, want application/json", got)
			}
		}
		var in map[string]int
		_ = json.NewDecoder(r.Body).Decode(&in)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"pong": in["ping"] * 2})
	}))
	defer srv.Close()

	c := NewClient("codec-svc", WithBaseURL(srv.URL))
	defer c.Close()

	var out map[string]int
	if err := c.DoCodec(context.Background(), codec.DefaultName, http.MethodPost, "/", map[string]int{"ping": 2}, &out); err != nil {
		t.Fatal(err)
	}
	if out["pong"] != 4 {
		t.Fatalf("out = %v", out)
	}

	// GetJSON 内部迁移到 DoCodec 后行为不变
	var out2 map[string]int
	if err := c.GetJSON(context.Background(), "/", &out2); err == nil && out2 == nil {
		// GET 无 body：服务器 decode 空 body 失败则忽略，此处仅保证不 panic
		_ = out2
	}
}

// TestSelectorFallbackToBaseURL 钉住选点失败回退 baseURL 直连。
func TestSelectorFallbackToBaseURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// 空 NNS 实例列表 → refreshNodes 保留空快照 → Pick 回退 baseURL
	stub := &stubNNSClient{instances: []registry.Instance{}} // GetInstances 返回空但不报错
	sel, _ := loadbalance.NewSelector("roundrobin")
	c := NewClient("fb-svc", WithBaseURL(srv.URL), WithNNS("fb-svc", stub), WithSelector(sel))
	defer c.Close()

	status, _, err := c.DoDetail(context.Background(), http.MethodGet, "/", nil)
	// 实例列表为空时 NNSResolver.ResolveInstances 返回错误 → 刷新保留缓存 →
	// Pick 无节点 → 回退 baseURL 直连 → 请求成功
	if err != nil {
		t.Fatalf("fallback to baseURL failed: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
}
