package httpx

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/oekit/registry"
	"github.com/oekit/tracing/traceid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/sony/gobreaker"
)

type requestSnapshot struct {
	Method string
	URL    *url.URL
	Header http.Header
	Body   []byte
}

func jsonServer(t *testing.T, status int, body string) (url string, requests <-chan *requestSnapshot) {
	t.Helper()
	ch := make(chan *requestSnapshot, 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		select {
		case ch <- &requestSnapshot{
			Method: r.Method,
			URL:    r.URL,
			Header: r.Header.Clone(),
			Body:   bodyBytes,
		}:
		default:
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, ch
}

func TestGetJSON_HappyPath(t *testing.T) {
	srvURL, _ := jsonServer(t, http.StatusOK, `{"name":"alice","age":30}`)
	c := MustNewClient("test", WithBaseURL(srvURL))
	defer c.Close()
	var got map[string]any
	if err := c.GetJSON(context.Background(), "/users/1", &got); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if got["name"] != "alice" {
		t.Errorf("expected name=alice, got %v", got)
	}
}

func TestGetJSON_4xxError(t *testing.T) {
	srvURL, _ := jsonServer(t, http.StatusNotFound, `{"error":"not found"}`)
	c := MustNewClient("test", WithBaseURL(srvURL))
	defer c.Close()
	var got map[string]any
	err := c.GetJSON(context.Background(), "/missing", &got)
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
	var svcErr *ServiceError
	if !errAs(err, &svcErr) {
		t.Fatalf("expected *ServiceError, got %T: %v", err, err)
	}
	if svcErr.Code != 404 {
		t.Errorf("expected code 404, got %d", svcErr.Code)
	}
}

func TestGetJSON_5xxError(t *testing.T) {
	srvURL, _ := jsonServer(t, http.StatusInternalServerError, `{"error":"boom"}`)
	c := MustNewClient("test", WithBaseURL(srvURL))
	defer c.Close()
	var got map[string]any
	err := c.GetJSON(context.Background(), "/explode", &got)
	if err == nil {
		t.Fatal("expected error for 500, got nil")
	}
	var svcErr *ServiceError
	if !errAs(err, &svcErr) {
		t.Fatalf("expected *ServiceError, got %T", err)
	}
	if svcErr.Code != 500 {
		t.Errorf("expected 500, got %d", svcErr.Code)
	}
}

func TestGetJSON_TruncatesLargeErrorBody(t *testing.T) {
	big := make([]byte, 1000)
	for i := range big {
		big[i] = 'x'
	}
	srvURL, _ := jsonServer(t, http.StatusBadRequest, string(big))
	c := MustNewClient("test", WithBaseURL(srvURL))
	defer c.Close()
	err := c.GetJSON(context.Background(), "/big", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := err.Error(); len(got) > 600 {
		t.Errorf("expected truncated error (<600 chars), got %d chars", len(got))
	}
}

func TestPostJSON_HappyPath(t *testing.T) {
	srvURL, requests := jsonServer(t, http.StatusOK, `{"id":42}`)
	c := MustNewClient("test", WithBaseURL(srvURL))
	defer c.Close()
	var got map[string]any
	body := map[string]any{"name": "alice", "age": 30}
	if err := c.PostJSON(context.Background(), "/items", body, &got); err != nil {
		t.Fatalf("PostJSON: %v", err)
	}
	if got["id"].(float64) != 42 {
		t.Errorf("expected id=42, got %v", got["id"])
	}
	select {
	case r := <-requests:
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		var decoded map[string]any
		if err := json.Unmarshal(r.Body, &decoded); err != nil {
			t.Errorf("body is not JSON: %v (raw: %s)", err, r.Body)
		}
		if decoded["name"] != "alice" {
			t.Errorf("expected name=alice in body, got %v", decoded["name"])
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for request")
	}
}

func TestPostJSON_5xxError(t *testing.T) {
	srvURL, _ := jsonServer(t, http.StatusServiceUnavailable, "down")
	c := MustNewClient("test", WithBaseURL(srvURL))
	defer c.Close()
	err := c.PostJSON(context.Background(), "/x", nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var svcErr *ServiceError
	if !errAs(err, &svcErr) {
		t.Fatalf("expected *ServiceError, got %T", err)
	}
	if svcErr.Code != 503 {
		t.Errorf("expected 503, got %d", svcErr.Code)
	}
}

func TestDoJSON_PutMethod(t *testing.T) {
	srvURL, requests := jsonServer(t, http.StatusOK, `{"updated":true}`)
	c := MustNewClient("test", WithBaseURL(srvURL))
	defer c.Close()
	var got map[string]any
	body := map[string]any{"key": "value"}
	if err := c.DoJSON(context.Background(), http.MethodPut, "/items/1", body, &got); err != nil {
		t.Fatalf("DoJSON: %v", err)
	}
	if got["updated"] != true {
		t.Errorf("expected updated=true, got %v", got["updated"])
	}
	select {
	case r := <-requests:
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for request")
	}
}

func TestDoJSON_NilBody(t *testing.T) {
	srvURL, _ := jsonServer(t, http.StatusOK, `{}`)
	c := MustNewClient("test", WithBaseURL(srvURL))
	defer c.Close()
	var got map[string]any
	if err := c.DoJSON(context.Background(), http.MethodGet, "/no-body", nil, &got); err != nil {
		t.Fatalf("DoJSON with nil body: %v", err)
	}
}

func TestDoRaw_ReturnsRawBytes(t *testing.T) {
	srvURL, _ := jsonServer(t, http.StatusOK, `plain text body`)
	c := MustNewClient("test", WithBaseURL(srvURL))
	defer c.Close()
	got, err := c.DoRaw(context.Background(), http.MethodGet, "/raw", nil)
	if err != nil {
		t.Fatalf("DoRaw: %v", err)
	}
	if string(got) != "plain text body" {
		t.Errorf("expected %q, got %q", "plain text body", string(got))
	}
}

func TestDoRaw_5xxError(t *testing.T) {
	srvURL, _ := jsonServer(t, http.StatusInternalServerError, "fail")
	c := MustNewClient("test", WithBaseURL(srvURL))
	defer c.Close()
	_, err := c.DoRaw(context.Background(), http.MethodGet, "/x", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestBearerToken_PropagatesToRequest(t *testing.T) {
	srvURL, requests := jsonServer(t, http.StatusOK, `{}`)
	c := MustNewClient("test", WithBaseURL(srvURL), WithBearerToken("secret-token-123"))
	defer c.Close()
	if err := c.GetJSON(context.Background(), "/auth", nil); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	select {
	case r := <-requests:
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token-123" {
			t.Errorf("expected Bearer token, got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for request")
	}
}

func TestBasicAuth_PropagatesToRequest(t *testing.T) {
	srvURL, requests := jsonServer(t, http.StatusOK, `{}`)
	c := MustNewClient("test", WithBaseURL(srvURL), WithBasicAuth("user", "pass"))
	defer c.Close()
	if err := c.GetJSON(context.Background(), "/auth", nil); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	select {
	case r := <-requests:
		auth := r.Header.Get("Authorization")
		if auth == "" {
			t.Fatal("expected Authorization header, got empty")
		}
		if len(auth) < 6 || auth[:6] != "Basic " {
			t.Errorf("expected 'Basic ' prefix, got %q", auth)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for request")
	}
}

func TestTraceID_PropagatesToOutgoingRequest(t *testing.T) {
	srvURL, requests := jsonServer(t, http.StatusOK, `{}`)
	c := MustNewClient("test",
		WithBaseURL(srvURL),
		WithOnBeforeRequest(func(ctx context.Context, req *protocol.Request) error {
			if tid := traceid.FromContext(ctx); tid != "" {
				req.Header.Set(traceid.HeaderKey, tid)
			}
			return nil
		}),
	)
	defer c.Close()
	ctx := traceid.ContextWith(context.Background(), "trace-abc-001")
	if err := c.GetJSON(ctx, "/traced", nil); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	select {
	case r := <-requests:
		if got := r.Header.Get(traceid.HeaderKey); got != "trace-abc-001" {
			t.Errorf("expected %s=trace-abc-001, got %q", traceid.HeaderKey, got)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for request")
	}
}

func TestRetry_OnServerError(t *testing.T) {
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "fail")
	}))
	t.Cleanup(srv.Close)
	// maxAttempts=3 (1 + 2 retries), 走 Hertz 官方 retry 引擎
	c := MustNewClient("test", WithBaseURL(srv.URL), WithRetryConfig(3, 10*time.Millisecond, 100*time.Millisecond, 5*time.Millisecond))
	defer c.Close()
	err := c.GetJSON(context.Background(), "/retry", nil)
	if err == nil {
		t.Fatal("expected error after retries exhausted, got nil")
	}
	if got := count.Load(); got != 3 {
		t.Errorf("expected 3 total attempts (1 + 2 retries), got %d", got)
	}
}

// TestRetry_DelayPolicyActive 钉住退避策略真实生效:hertz WithRetryConfig 默认
// DelayPolicy 恒返回 0,ekit 必须显式注入 CombineDelay(BackOff, Random),
// 否则 initDelay/maxJitter 参数全部无效、重试零等待(2026-09-05 修复的回归)。
func TestRetry_DelayPolicyActive(t *testing.T) {
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	// 1 次重试,退避基数 80ms → BackOffDelayPolicy 至少 80ms
	// (attempts 从 0 或 1 起,延迟 80ms 或 160ms;Random 抖动 [0,5ms))
	c := MustNewClient("test", WithBaseURL(srv.URL), WithRetryConfig(2, 80*time.Millisecond, 500*time.Millisecond, 5*time.Millisecond))
	defer c.Close()
	start := time.Now()
	_ = c.GetJSON(context.Background(), "/retry-delay", nil)
	elapsed := time.Since(start)
	if got := count.Load(); got != 2 {
		t.Errorf("expected 2 total attempts, got %d", got)
	}
	if elapsed < 60*time.Millisecond {
		t.Errorf("expected >=60ms retry backoff (initDelay=80ms), got %v — DelayPolicy 没生效,重试零退避", elapsed)
	}
}

func TestRetry_StopsOnSuccess(t *testing.T) {
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := count.Add(1)
		if n < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "fail")
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)
	// maxAttempts=4 (允许 3 次重试),2 次成功即可
	c := MustNewClient("test", WithBaseURL(srv.URL), WithRetryConfig(4, 10*time.Millisecond, 100*time.Millisecond, 5*time.Millisecond))
	defer c.Close()
	var got map[string]any
	if err := c.GetJSON(context.Background(), "/retry-then-ok", &got); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if got["ok"] != true {
		t.Errorf("expected ok=true, got %v", got["ok"])
	}
	if got := count.Load(); got != 2 {
		t.Errorf("expected 2 attempts (fail, then success), got %d", got)
	}
}

func TestCircuitBreaker_OpensOnConsecutiveFailures(t *testing.T) {
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "boom")
	}))
	t.Cleanup(srv.Close)
	c := MustNewClient("test", WithBaseURL(srv.URL), WithCircuitBreaker("test-cb", gobreaker.Settings{
		Interval: 10 * time.Second,
		Timeout:  10 * time.Second,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= 3
		},
	}))
	defer c.Close()
	for i := 0; i < 3; i++ {
		_ = c.GetJSON(context.Background(), "/cb", nil)
	}
	hitsBefore := count.Load()
	err := c.GetJSON(context.Background(), "/cb", nil)
	if err == nil {
		t.Fatal("expected error from open circuit, got nil")
	}
	if !errIs(err, ErrCircuitOpen) {
		t.Errorf("expected ErrCircuitOpen, got %v", err)
	}
	if got := count.Load(); got != hitsBefore {
		t.Errorf("expected server not to be hit after open, but got %d -> %d", hitsBefore, got)
	}
}

func TestMetrics_TargetLabel_AppearsInRegistry(t *testing.T) {
	srvURL, _ := jsonServer(t, http.StatusOK, `{}`)
	reg := prometheus.NewRegistry()
	c := MustNewClient("test", WithBaseURL(srvURL), WithMetrics(reg, "ekit_test"), WithTarget("user-svc"))
	defer c.Close()
	if err := c.GetJSON(context.Background(), "/m", nil); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	expectedName := "ekit_test_requests_total"
	gathered, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	saw := false
	for _, mf := range gathered {
		if mf.GetName() != expectedName {
			continue
		}
		for _, m := range mf.Metric {
			for _, lp := range m.Label {
				if lp.GetName() == "target" {
					saw = true
					if lp.GetValue() != "user-svc" {
						t.Errorf("expected target=user-svc, got %q", lp.GetValue())
					}
				}
			}
		}
	}
	if !saw {
		t.Errorf("expected at least one 'target' label in %s, found none", expectedName)
	}
}

func TestMetrics_TargetLabel_DefaultsToDefault(t *testing.T) {
	srvURL, _ := jsonServer(t, http.StatusOK, `{}`)
	reg := prometheus.NewRegistry()
	c := MustNewClient("test", WithBaseURL(srvURL), WithMetrics(reg, "ekit_test"))
	defer c.Close()
	if err := c.GetJSON(context.Background(), "/m", nil); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if got := testutil.CollectAndCount(prometheus.Collector(c.metrics.requestsTotal)); got == 0 {
		t.Error("expected metrics to be recorded, got 0 series")
	}
	gathered, _ := reg.Gather()
	sawDefault := false
	for _, mf := range gathered {
		for _, m := range mf.Metric {
			for _, lp := range m.Label {
				if lp.GetName() == "target" && lp.GetValue() == defaultTargetLabel {
					sawDefault = true
				}
			}
		}
	}
	if !sawDefault {
		t.Errorf("expected at least one metric with target=%q", defaultTargetLabel)
	}
}

func TestResolveEndpoint_PicksAndConcatenates(t *testing.T) {
	var gotURL atomic.Pointer[string]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL.Store(&r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	host, port := parseHostPort(srv.URL)
	endpoint := "http://" + host + ":" + intToStr(port)
	c := MustNewClient("test", WithEndpoints(endpoint))
	defer c.Close()
	if err := c.GetJSON(context.Background(), "/api/v1/items", nil); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if got := gotURL.Load(); got == nil {
		t.Fatal("no request captured")
	} else if *got != "/api/v1/items" {
		t.Errorf("expected path=/api/v1/items, got %q", *got)
	}
}

func TestResolveEndpoint_NoManager(t *testing.T) {
	srvURL, _ := jsonServer(t, http.StatusOK, `{}`)
	c := MustNewClient("test", WithBaseURL(srvURL))
	defer c.Close()
	if err := c.GetJSON(context.Background(), "/direct", nil); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
}

func TestNNSResolver_Resolve(t *testing.T) {
	stub := &stubNNSClient{instances: []registry.Instance{{IP: "10.0.0.1", Port: 8080}, {IP: "10.0.0.2", Port: 8080}}}
	r := NewNNSResolver("svc-a", stub)
	got, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 endpoints, got %d", len(got))
	}
	want0 := "http://10.0.0.1:8080"
	if got[0] != want0 {
		t.Errorf("expected %q, got %q", want0, got[0])
	}
}

func TestNNSResolver_ResolveError(t *testing.T) {
	stub := &stubNNSClient{err: context.DeadlineExceeded}
	r := NewNNSResolver("svc-a", stub)
	_, err := r.Resolve(context.Background())
	if err == nil {
		t.Fatal("expected error from NNSClient, got nil")
	}
}

func TestNNSResolver_EmptyInstances(t *testing.T) {
	stub := &stubNNSClient{instances: nil}
	r := NewNNSResolver("svc-a", stub)
	_, err := r.Resolve(context.Background())
	if err == nil {
		t.Fatal("expected error for empty instances, got nil")
	}
}

func errAs(err error, target any) bool {
	for {
		if err == nil {
			return false
		}
		switch v := target.(type) {
		case **ServiceError:
			if se, ok := err.(*ServiceError); ok {
				*v = se
				return true
			}
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
}

func errIs(err, target error) bool {
	for {
		if err == target {
			return true
		}
		if err == nil {
			return false
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
}

func parseHostPort(rawURL string) (string, int) {
	const prefix = "http://"
	if len(rawURL) < len(prefix) || rawURL[:len(prefix)] != prefix {
		return rawURL, 0
	}
	rest := rawURL[len(prefix):]
	colon := -1
	for i := 0; i < len(rest); i++ {
		if rest[i] == ':' {
			colon = i
			break
		}
	}
	if colon < 0 {
		return rest, 0
	}
	port := 0
	for i := colon + 1; i < len(rest); i++ {
		ch := rest[i]
		if ch < '0' || ch > '9' {
			break
		}
		port = port*10 + int(ch-'0')
	}
	return rest[:colon], port
}

func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

type stubNNSClient struct {
	instances []registry.Instance
	err       error
}

func (s *stubNNSClient) GetInstances(_ context.Context, _ string) ([]registry.Instance, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.instances, nil
}

func TestRawDo_BaseURLAuthAndCookieJar(t *testing.T) {
	// 裸 Hertz().Do 路径必须与封装方法行为一致：相对 URI 拼 baseURL、
	// 自动带 Basic auth、cookie jar 注入并回收 Set-Cookie。
	// （历史上裸 Do 三样全缺，下游踩出 5 处生产 bug。）
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var gotPath, gotAuth, gotCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotCookie = r.Header.Get("Cookie")
		mu.Unlock()
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "abc123", Path: "/"})
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(srv.Close)

	c := MustNewClient("test",
		WithBaseURL(srv.URL),
		WithBasicAuth("u1", "p1"),
		WithCookieJar(jar),
	)
	defer c.Close()

	// 第一跳：裸 Do 相对 URI + auth
	req := &protocol.Request{}
	req.SetMethod("GET")
	req.SetRequestURI("/login")
	resp := &protocol.Response{}
	if err := c.Hertz().Do(context.Background(), req, resp); err != nil {
		t.Fatalf("raw Do: %v", err)
	}
	mu.Lock()
	p1, a1 := gotPath, gotAuth
	mu.Unlock()
	if p1 != "/login" {
		t.Errorf("base URL not applied, path=%q", p1)
	}
	if !strings.HasPrefix(a1, "Basic ") {
		t.Errorf("basic auth not applied, auth=%q", a1)
	}

	// 查询串拼接：base + path + ? + query（曾漏 ? 令 pathquery 粘连 404）
	reqQ := &protocol.Request{}
	reqQ.SetMethod("GET")
	reqQ.SetRequestURI("/search?q=abc&page=2")
	respQ := &protocol.Response{}
	if err := c.Hertz().Do(context.Background(), reqQ, respQ); err != nil {
		t.Fatalf("raw Do query: %v", err)
	}
	mu.Lock()
	pq := gotPath
	mu.Unlock()
	if pq != "/search" {
		t.Errorf("query join mangled path: %q", pq)
	}

	// 第二跳：jar 注入第一跳回收的 sid
	req2 := &protocol.Request{}
	req2.SetMethod("GET")
	req2.SetRequestURI("/next")
	resp2 := &protocol.Response{}
	if err := c.Hertz().Do(context.Background(), req2, resp2); err != nil {
		t.Fatalf("raw Do 2: %v", err)
	}
	mu.Lock()
	finalCookie := gotCookie
	mu.Unlock()
	if !strings.Contains(finalCookie, "sid=abc123") {
		t.Errorf("cookie jar not injected, cookie=%q", finalCookie)
	}
}

// TestClient_Close_ReleasesIdleConnections 回归（#84）：Close 除停止
// EndpointManager 刷新循环外，还必须释放底层 Hertz client 的空闲连接池——
// 修复前底层 client 从不关闭，短生命周期场景（测试反复建 Client、临时任务
// 客户端）每次泄漏一套连接池，长跑进程累积。
func TestClient_Close_ReleasesIdleConnections(t *testing.T) {
	var mu sync.Mutex
	openConns := make(map[net.Conn]struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	// ConnState 必须在 Start 前挂上（Server 字段运行期不可变，避免与
	// accept 循环的读写竞争）
	srv.Config.ConnState = func(c net.Conn, s http.ConnState) {
		mu.Lock()
		if s == http.StateClosed {
			delete(openConns, c)
		} else {
			openConns[c] = struct{}{}
		}
		mu.Unlock()
	}
	srv.Start()
	t.Cleanup(srv.Close)
	countOpen := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(openConns)
	}

	c := MustNewClient("test", WithBaseURL(srv.URL))
	if err := c.GetJSON(context.Background(), "/keepalive", nil); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}

	// keep-alive：请求完成后连接留在客户端池中（服务端视角已 idle 但仍打开）
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && countOpen() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := countOpen(); got == 0 {
		t.Fatal("expected a pooled keep-alive connection before Close")
	}

	c.Close()
	// 底层 Hertz client 的空闲连接被主动关闭：服务端观察到 StateClosed
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && countOpen() != 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := countOpen(); got != 0 {
		t.Fatalf("Close 后仍有 %d 条连接未释放（底层 Hertz client 未关闭）", got)
	}

	c.Close() // 幂等：重复 Close 不 panic
}
