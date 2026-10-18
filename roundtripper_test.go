package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sony/gobreaker"
)

// TestRoundTripper_BasicRoundTrip 基础往返：方法/路径带 query/自定义头/请求体透传，
// 响应状态/头/体回填；非 2xx 不是错误（标准库语义）。
func TestRoundTripper_BasicRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var gotMethod, gotURI, gotCustom, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotMethod, gotURI, gotCustom, gotBody = r.Method, r.URL.RequestURI(), r.Header.Get("X-Custom"), string(b)
		mu.Unlock()
		w.Header().Set("X-Reply", "yes")
		if r.URL.Path == "/missing" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	rt := NewRoundTripper("bridge-test", WithBaseURL(srv.URL))
	defer rt.(*roundTripper).Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		srv.URL+"/v1/token?grant=cc", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Custom", "custom-value")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip err = %v, want nil", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("body = %q", body)
	}
	if resp.Header.Get("X-Reply") != "yes" {
		t.Fatalf("response header X-Reply = %q, want yes", resp.Header.Get("X-Reply"))
	}
	mu.Lock()
	gotMethodAssert, gotURICustomBody := gotMethod, (gotURI + "|" + gotCustom + "|" + gotBody)
	mu.Unlock()
	if gotMethodAssert != http.MethodPost || !strings.HasPrefix(gotURICustomBody, "/v1/token?grant=cc|") {
		t.Fatalf("server got %s %s, want POST /v1/token?grant=cc...", gotMethodAssert, gotURICustomBody)
	}
	if !strings.Contains(gotURICustomBody, "|custom-value|payload") {
		t.Fatalf("header/body passthrough broken: %q", gotURICustomBody)
	}

	// 非 2xx：不是错误，调用方自行判定 StatusCode
	req404, _ := http.NewRequestWithContext(context.Background(), http.MethodGet,
		srv.URL+"/missing", nil)
	resp404, err := rt.RoundTrip(req404)
	if err != nil {
		t.Fatalf("404 must not be an error, got %v", err)
	}
	defer resp404.Body.Close()
	if resp404.StatusCode != http.StatusNotFound {
		t.Fatalf("404 status = %d", resp404.StatusCode)
	}
}

// TestRoundTripper_HopByHopSkipped hop-by-hop 与 Host/Content-Length 不透传。
func TestRoundTripper_HopByHopSkipped(t *testing.T) {
	var mu sync.Mutex
	var gotConn, gotTE, gotHeaderCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotConn = len(r.Header.Get("Connection"))
		gotTE = len(r.Header.Get("Transfer-Encoding"))
		gotHeaderCount = len(r.Header.Values("X-Multi"))
		mu.Unlock()
	}))
	defer srv.Close()

	rt := NewRoundTripper("bridge-hop", WithBaseURL(srv.URL))
	defer rt.(*roundTripper).Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/", nil)
	req.Header.Set("Connection", "close")
	req.Header.Set("Transfer-Encoding", "chunked")
	req.Header.Add("X-Multi", "a")
	req.Header.Add("X-Multi", "b")
	respHop, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip err = %v", err)
	}
	respHop.Body.Close()
	mu.Lock()
	conn, te, multi := gotConn, gotTE, gotHeaderCount
	mu.Unlock()
	if conn != 0 || te != 0 {
		t.Fatalf("hop-by-hop headers leaked: connection=%d transfer-encoding=%d", conn, te)
	}
	if multi != 2 {
		t.Fatalf("multi-value header passthrough = %d values, want 2", multi)
	}
}

// TestRoundTripper_CircuitBreakerOpen 熔断拒绝经桥接透出为裸 ErrCircuitOpen，
// 被拒请求不执行。
func TestRoundTripper_CircuitBreakerOpen(t *testing.T) {
	var executed atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		executed.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	rt := NewRoundTripper("bridge-breaker", WithBaseURL(srv.URL),
		WithCircuitBreaker("bridge-bb", gobreaker.Settings{
			ReadyToTrip: func(counts gobreaker.Counts) bool { return counts.Requests >= 1 },
		}))
	defer rt.(*roundTripper).Close()

	newReq := func() *http.Request {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/", nil)
		return req
	}
	// 第一次：执行并失败（500），熔断计数达到阈值切 open（失败路径 resp 可能为 nil，
	// 拿得到就关闭）
	respFirst, firstErr := rt.RoundTrip(newReq())
	if firstErr == nil {
		t.Fatal("first request must fail (server 500)")
	}
	if respFirst != nil {
		respFirst.Body.Close()
	}
	if executed.Load() != 1 {
		t.Fatalf("executed = %d, want 1", executed.Load())
	}
	// 第二次：熔断打开，拒绝且不执行（resp 可能为 nil，拿到就关闭）
	respSecond, secondErr := rt.RoundTrip(newReq())
	if !errors.Is(secondErr, ErrCircuitOpen) {
		t.Fatalf("second request err = %v, want ErrCircuitOpen", secondErr)
	}
	if respSecond != nil {
		respSecond.Body.Close()
	}
	if executed.Load() != 1 {
		t.Fatalf("rejected request must not execute, executed = %d", executed.Load())
	}
}

// TestStdClient 冒烟：装好的 *http.Client 直接可用。
func TestStdClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	client := StdClient("bridge-std", WithBaseURL(srv.URL))
	resp, err := client.Get(srv.URL + "/ping")
	if err != nil {
		t.Fatalf("Get err = %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello" {
		t.Fatalf("body = %q, want hello", body)
	}
}
