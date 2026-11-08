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
	"time"

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
	// 第一次：执行并按标准库语义返回 500 响应（非 2xx 非错误，#107），但 5xx
	// 计入熔断统计，计数达到阈值切 open（resp 拿得到就关闭）
	respFirst, firstErr := rt.RoundTrip(newReq())
	if firstErr != nil {
		t.Fatalf("first request must stay non-error (stdlib semantics, #107), err = %v", firstErr)
	}
	if respFirst == nil || respFirst.StatusCode != http.StatusInternalServerError {
		status := -1
		if respFirst != nil {
			status = respFirst.StatusCode
			respFirst.Body.Close()
		}
		t.Fatalf("first request status = %d, want 500", status)
	}
	respFirst.Body.Close()
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

// TestRoundTripper_ExplicitPort 无端点/baseURL 配置时，目标取 http.Request 自身的
// scheme://host——显式端口（httptest 随机端口）原样进入拨号，不落默认 80（#9）。
func TestRoundTripper_ExplicitPort(t *testing.T) {
	var mu sync.Mutex
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.RequestURI()
		mu.Unlock()
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	// 不带任何寻址选项：桥接必须按标准 RoundTripper 语义使用请求自身的 URL
	client := StdClient("bridge-port")
	resp, err := client.Post(srv.URL+"/token?grant=cc", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("request to %s err = %v (port must not be dropped)", srv.URL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	mu.Lock()
	got := gotPath
	mu.Unlock()
	if string(body) != "ok" || got != "/token?grant=cc" {
		t.Fatalf("body=%q path=%q, want ok /token?grant=cc", body, got)
	}
}

// TestRoundTripper_URISchemeMarkerInQuery 回归（#106）：Client 未配置
// 端点/baseURL 时，「fullURL 是否自带 scheme://host」必须解析式判定——
// path?query 里含字面量 "://"（?to=http://x）的合法形态曾让子串匹配误判
// ⇒ 请求既无 scheme 也无 host，被拨到默认 :80（凭证随错误寻址泄漏）。
func TestRoundTripper_URISchemeMarkerInQuery(t *testing.T) {
	var mu sync.Mutex
	var gotPath, gotQuery, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath, gotQuery, gotAuth = r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		mu.Unlock()
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	client := StdClient("bridge-scheme-marker")

	// 未编码的 "://" 出现在 query：必须仍按请求自身的 scheme://host 拨号
	resp, err := client.Get(srv.URL + "/redirect?to=http://example.com/x")
	if err != nil {
		t.Fatalf("query contains :// must not lose scheme/host, err = %v", err)
	}
	defer resp.Body.Close()
	mu.Lock()
	path, query := gotPath, gotQuery
	mu.Unlock()
	if path != "/redirect" || query != "to=http://example.com/x" {
		t.Fatalf("request misaddressed: server saw path=%q query=%q", path, query)
	}

	// path 中段含 "://" 同理（完整 URI 形态）
	resp2, err := client.Get(srv.URL + "/q?a=b%3A%2F%2F")
	if err != nil {
		t.Fatalf("encoded variant err = %v", err)
	}
	defer resp2.Body.Close()

	// 凭证头必须随请求原样到达目标（错误寻址即泄漏给非预期接收方）
	reqWithAuth, _ := http.NewRequestWithContext(context.Background(), http.MethodGet,
		srv.URL+"/auth?callback=http://cb.example/h", nil)
	reqWithAuth.Header.Set("Authorization", "Bearer caller-token")
	resp3, err := client.Do(reqWithAuth)
	if err != nil {
		t.Fatalf("auth variant err = %v", err)
	}
	defer resp3.Body.Close()
	mu.Lock()
	auth := gotAuth
	mu.Unlock()
	if auth != "Bearer caller-token" {
		t.Fatalf("Authorization misrouted: server saw %q", auth)
	}
}

// TestRoundTripper_BreakerKeepsNon2xxResponse 回归（#107 桥接侧）：开熔断后
// 非 2xx 仍不是错误（标准库语义）——500 响应必须原样回到调用方（状态码 +
// 响应体），修复前 5xx 被熔断中间件转成普通 error，响应体丢失。
func TestRoundTripper_BreakerKeepsNon2xxResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom-body-detail"))
	}))
	defer srv.Close()

	rt := NewRoundTripper("bridge-breaker-5xx", WithBaseURL(srv.URL),
		WithCircuitBreaker("bridge-breaker-5xx", gobreaker.Settings{Interval: 10 * time.Second}))
	defer rt.(*roundTripper).Close()

	resp, err := rt.RoundTrip(httptest.NewRequest(http.MethodGet, srv.URL+"/500", nil))
	if err != nil {
		t.Fatalf("5xx with breaker must stay non-error (stdlib semantics), err = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "boom-body-detail" {
		t.Fatalf("body = %q, want boom-body-detail（响应体不得丢失）", body)
	}
}
