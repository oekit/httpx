package httpx

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// recordingTransport 包装底层 Transport，证明 DoOnce 的 client/Transport
// 可注入（收敛点不再绑死共享 client，#91 的核心诉求）。
type recordingTransport struct {
	inner http.RoundTripper
	calls atomic.Int32
}

func (t *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return t.inner.RoundTrip(req)
}

// closeTrackingBody 记录 Close 被调用（DoOnce 必须读尽并关闭响应体）。
type closeTrackingBody struct {
	io.ReadCloser
	closed atomic.Bool
}

func (b *closeTrackingBody) Close() error {
	b.closed.Store(true)
	return b.ReadCloser.Close()
}

func TestDoOnce_InjectableClientAndFullBody(t *testing.T) {
	const payload = "part1-"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, payload)
		_, _ = io.WriteString(w, "part2") // 两段写：读尽语义需要全量返回
	}))
	t.Cleanup(srv.Close)

	rt := &recordingTransport{inner: http.DefaultTransport}
	hc := &http.Client{Transport: rt} // 宿主决策的注入 client

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/once", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Probe", "injectable")

	status, body, err := DoOnce(context.Background(), hc, req)
	if err != nil {
		t.Fatalf("DoOnce: %v", err)
	}
	if status != http.StatusCreated {
		t.Errorf("status = %d, want 201", status)
	}
	if string(body) != "part1-part2" {
		t.Errorf("body = %q, want %q（响应体未读尽）", body, "part1-part2")
	}
	if got := rt.calls.Load(); got != 1 {
		t.Errorf("注入的 Transport 被调用 %d 次, want 1", got)
	}
}

// TestDoOnce_ClosesBody 钉住响应体总是被关闭（连接复用/资源释放的前提）。
func TestDoOnce_ClosesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "body")
	}))
	t.Cleanup(srv.Close)

	var tracked *closeTrackingBody
	hc := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		tracked = &closeTrackingBody{ReadCloser: resp.Body}
		resp.Body = tracked
		return resp, nil
	})}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if _, _, err := DoOnce(context.Background(), hc, req); err != nil {
		t.Fatalf("DoOnce: %v", err)
	}
	if tracked == nil || !tracked.closed.Load() {
		t.Error("响应体未被关闭")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// TestDoOnce_Non2xxNotError 标准库语义：非 2xx 返回 status 与 body，
// 不是 error——调用方自行判定（与引擎层 ServiceError 语义不同，doc 已声明）。
func TestDoOnce_Non2xxNotError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/x", nil)
	status, body, err := DoOnce(context.Background(), nil, req) // nil client → DefaultClient
	if err != nil {
		t.Fatalf("404 不应是 error, got %v", err)
	}
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", status)
	}
	if len(body) == 0 {
		t.Error("expected non-empty body for 404")
	}
}

// TestDoOnce_NilRequestRejected 入参防御：nil request 显式报错。
func TestDoOnce_NilRequestRejected(t *testing.T) {
	if _, _, err := DoOnce(context.Background(), nil, nil); err == nil {
		t.Fatal("expected error for nil request, got nil")
	}
}

// TestDoOnce_ContextCancelled ctx 取消透传到请求（总时长预算由调用方经
// ctx 控制）。
func TestDoOnce_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	cancel()
	if _, _, err := DoOnce(ctx, nil, req); err == nil {
		t.Fatal("expected error for cancelled ctx, got nil")
	}
}

// TestDoOnce_TransportError 传输失败：status 0、body nil、err 非 nil。
func TestDoOnce_TransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // 立即关闭：拨号必失败

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	status, body, err := DoOnce(context.Background(), nil, req)
	if err == nil {
		t.Fatal("expected transport error, got nil")
	}
	if status != 0 || body != nil {
		t.Errorf("transport error 应返回 (0, nil), got (%d, %q)", status, body)
	}
}
