package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/network"
	"github.com/oekit/httpx/codec"
	"github.com/oekit/httpx/loadbalance"
	"github.com/oekit/registry"
)

// 本文件是请求执行骨架（request.go 五个封装方法 + roundtripper.go RoundTrip
// 共用的 priority 守卫 / resolveEndpoint / Do+lbDone / 错误包装，#159/#162
// 收敛单源）的等价性回归基线：重构前后外部行为必须逐项不变。

// fillPriorityQueue 占满每级容量为 1 的优先级队列，返回释放函数。
func fillPriorityQueue(t *testing.T, q *PriorityQueue) func() {
	t.Helper()
	if !q.Acquire(context.Background(), PriorityNormal) {
		t.Fatal("pre-acquire failed")
	}
	return func() { q.Release(PriorityNormal) }
}

// TestExecSkeleton_PriorityQueueFull_RejectsAllPaths 钉住骨架第一环：
// priority 守卫在**构造请求体与寻址之前**——队列满时六个入口一律返回
// 「request cancelled due to priority queue」，即使 body 本身不可序列化
// 也轮不到 marshal 报错（守卫先行是全入口一致的次序契约）。
func TestExecSkeleton_PriorityQueueFull_RejectsAllPaths(t *testing.T) {
	srvURL, _ := jsonServer(t, http.StatusOK, `{}`)
	c := MustNewClient("test", WithBaseURL(srvURL), WithPriorityQueue(1))
	defer c.Close()
	release := fillPriorityQueue(t, c.priorityQueue)
	defer release()

	unmarshalable := map[string]any{"ch": make(chan int)}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 六个入口全部命中同一守卫文案
	if err := c.DoCodec(ctx, codec.DefaultName, http.MethodGet, "/x", unmarshalable, nil); err == nil || !strings.Contains(err.Error(), "request cancelled due to priority queue") {
		t.Errorf("DoCodec: got %v, want priority-queue rejection", err)
	}
	if _, err := c.DoRaw(ctx, http.MethodGet, "/x", unmarshalable); err == nil || !strings.Contains(err.Error(), "request cancelled due to priority queue") {
		t.Errorf("DoRaw: got %v, want priority-queue rejection", err)
	}
	if _, _, err := c.DoDetail(ctx, http.MethodGet, "/x", unmarshalable); err == nil || !strings.Contains(err.Error(), "request cancelled due to priority queue") {
		t.Errorf("DoDetail: got %v, want priority-queue rejection", err)
	}
	if err := c.UploadFile(ctx, "/x", "/nonexistent-file-xyz", "f", nil, nil); err == nil || !strings.Contains(err.Error(), "request cancelled due to priority queue") {
		t.Errorf("UploadFile: got %v, want priority-queue rejection（守卫先于读文件）", err)
	}
	if err := c.UploadBytes(ctx, "/x", []byte("data"), "f", "a.txt", nil, nil); err == nil || !strings.Contains(err.Error(), "request cancelled due to priority queue") {
		t.Errorf("UploadBytes: got %v, want priority-queue rejection", err)
	}
	rt := &roundTripper{c: c}
	// RoundTrip 的 ctx 取自 req.Context()，必须带超时——Acquire 阻塞等取消
	req := httptest.NewRequest(http.MethodGet, srvURL+"/x", nil).WithContext(ctx)
	resp, err := rt.RoundTrip(req)
	if err == nil || !strings.Contains(err.Error(), "request cancelled due to priority queue") {
		t.Errorf("RoundTrip: got %v, want priority-queue rejection", err)
	}
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
}

// trackingBody 记录 Close 是否被调用的请求体（#186 锚点用）。
type trackingBody struct {
	io.Reader
	closed atomic.Bool
}

func (b *trackingBody) Close() error {
	b.closed.Store(true)
	return nil
}

// TestExecSkeleton_RoundTripGuardReject_ClosesBody 回归 #186：net/http
// RoundTripper 契约「RoundTrip must always close the body, including on
// errors」——guardPriority 拒绝路径（限流拒绝，恰是高负载窗口）此前直接
// 返回，req.Body 既不读也不关 ⇒ *os.File/管道/流式 body 的 fd 与上游资源
// 泄漏，密度与限流拒绝频率成正比。标准库 send() 在 rt.RoundTrip 返回
// error 时只 stopTimer 不碰 req.Body，调用方按契约不兜底关——收尾责任
// 在桥接层实现（修法：收尾 defer 提到方法入口，所有提前返回路径统一关）。
func TestExecSkeleton_RoundTripGuardReject_ClosesBody(t *testing.T) {
	srvURL, _ := jsonServer(t, http.StatusOK, `{}`)
	c := MustNewClient("test", WithBaseURL(srvURL), WithPriorityQueue(1))
	defer c.Close()
	// 占满容量 1 的队列：拒绝路径确定命中（Acquire 只在 ctx.Done 时返回 false）
	release := fillPriorityQueue(t, c.priorityQueue)
	defer release()

	// 已取消 ctx：队列满 + ctx.Done ⇒ Acquire 立即拒绝（等价真实
	// 「队列被在途请求占满 + 本次请求超时」的高负载窗口）
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	body := &trackingBody{Reader: strings.NewReader("payload")}
	req := httptest.NewRequest(http.MethodPost, srvURL+"/x", nil)
	req.Body = body
	req = req.WithContext(ctx)

	rt := &roundTripper{c: c}
	resp, err := rt.RoundTrip(req)
	if err == nil || !errors.Is(err, ErrPriorityUnavailable) {
		t.Fatalf("got %v, want ErrPriorityUnavailable", err)
	}
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if !body.closed.Load() {
		t.Fatal("#186 回归：guardPriority 拒绝路径未关闭 req.Body（违反 RoundTripper 契约：must always close the body, including on errors）")
	}
}

// TestExecSkeleton_PermitReleasedOnErrorPaths 钉住骨架第二环：任何错误路径
// （marshal 失败、传输层失败）都必须释放已获取的准入许可——泄漏即容量
// 永久缩小，队列满后全部请求被拒。
func TestExecSkeleton_PermitReleasedOnErrorPaths(t *testing.T) {
	srvURL, reqs := jsonServer(t, http.StatusOK, `{}`)
	c := MustNewClient("test", WithBaseURL(srvURL), WithPriorityQueue(1))
	defer c.Close()
	ctx := context.Background()

	// marshal 失败：错误返回且许可释放（Len 回零），请求未发出
	if err := c.DoCodec(ctx, codec.DefaultName, http.MethodPost, "/x", map[string]any{"ch": make(chan int)}, nil); err == nil || !strings.Contains(err.Error(), "marshal body") {
		t.Fatalf("DoCodec: got %v, want marshal error", err)
	}
	if got := c.priorityQueue.Len(); got != 0 {
		t.Fatalf("permit leaked on marshal error: Len = %d, want 0", got)
	}
	select {
	case <-reqs:
		t.Fatal("marshal error must not reach the wire")
	default:
	}

	// 传输层失败：同样释放（后续同队列请求可正常准入）
	blocker := MustNewClient("test", WithBaseURL(srvURL), WithPriorityQueue(1),
		WithDialFunc(func(string) (network.Conn, error) { return nil, errors.New("dial refused") }))
	defer blocker.Close()
	if _, _, err := blocker.DoDetail(ctx, http.MethodGet, "/x", nil); err == nil {
		t.Fatal("expected transport error")
	}
	if got := blocker.priorityQueue.Len(); got != 0 {
		t.Fatalf("permit leaked on transport error: Len = %d, want 0", got)
	}
}

// TestDoRaw_ErrorReturnsNilBody 钉住 DoRaw 与 DoDetail 的语义差异：
// DoRaw 非 2xx 时返回 **nil body** + *ServiceError（不是 DoDetail 丢弃
// status 的别名——错误路径 body 形态不同，收敛时不得合并）。
func TestDoRaw_ErrorReturnsNilBody(t *testing.T) {
	srvURL, _ := jsonServer(t, http.StatusInternalServerError, `{"error":"boom"}`)
	c := MustNewClient("test", WithBaseURL(srvURL))
	defer c.Close()

	raw, err := c.DoRaw(context.Background(), http.MethodGet, "/x", nil)
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if raw != nil {
		t.Errorf("DoRaw on error must return nil body, got %q", raw)
	}
	var svcErr *ServiceError
	if !errAs(err, &svcErr) || svcErr.Code != 500 {
		t.Fatalf("expected *ServiceError code 500, got %T: %v", err, err)
	}

	// 对照组：DoDetail 同场景返回 status + body
	status, body, derr := c.DoDetail(context.Background(), http.MethodGet, "/x", nil)
	if derr == nil || status != 500 || string(body) != `{"error":"boom"}` {
		t.Errorf("DoDetail: got (%d, %q, %v), want (500, body, err)", status, body, derr)
	}
}

// TestUploadBytes_FormShapeAndErrorAction 钉住 UploadBytes 的线上形态：
// multipart 文件 part（filename 透传）、extraFields 表单字段、Content-Type
// 前缀，以及非 2xx 时 ServiceError.Action == "UPLOAD <path>"（与 Do* 的
// "METHOD path" 文案不同，收敛时不得统一掉）。
func TestUploadBytes_FormShapeAndErrorAction(t *testing.T) {
	var (
		mu       sync.Mutex
		fileName string
		fileData []byte
		extra    string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data; boundary=") {
			t.Errorf("Content-Type = %q, want multipart/form-data", r.Header.Get("Content-Type"))
		}
		f, fh, err := r.FormFile("upload")
		if err != nil {
			t.Errorf("FormFile(upload): %v", err)
			return
		}
		defer f.Close()
		fileName = fh.Filename
		fileData = make([]byte, fh.Size)
		_, _ = f.Read(fileData)
		extra = r.FormValue("note")
		w.WriteHeader(http.StatusOK)
	}))
	srvURL := srv.URL
	t.Cleanup(srv.Close)

	c := MustNewClient("test", WithBaseURL(srvURL))
	defer c.Close()
	if err := c.UploadBytes(context.Background(), "/upload", []byte("hello-bytes"), "upload", "a.txt", map[string]string{"note": "hi"}, nil); err != nil {
		t.Fatalf("UploadBytes: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if fileName != "a.txt" || string(fileData) != "hello-bytes" || extra != "hi" {
		t.Errorf("form shape mismatch: file=%q data=%q extra=%q", fileName, fileData, extra)
	}

	// 非 2xx：Action 文案 "UPLOAD /upload"
	errSrv, _ := jsonServer(t, http.StatusForbidden, `{"error":"no"}`)
	cErr := MustNewClient("test", WithBaseURL(errSrv))
	defer cErr.Close()
	err := cErr.UploadBytes(context.Background(), "/upload", []byte("x"), "upload", "a.txt", nil, nil)
	var svcErr *ServiceError
	if !errAs(err, &svcErr) || svcErr.Action != "UPLOAD /upload" || svcErr.Code != 403 {
		t.Fatalf("expected ServiceError{Action: UPLOAD /upload, Code: 403}, got %T: %v", err, err)
	}
}

// recordingSelector 包装内层 Selector，记录每次 done 回写的最终错误。
type recordingSelector struct {
	inner loadbalance.Selector
	mu    sync.Mutex
	errs  []error
}

func (s *recordingSelector) Select(ctx context.Context, nodes []loadbalance.Node, opts ...loadbalance.SelectOption) (loadbalance.Node, loadbalance.DoneFunc, error) {
	n, done, err := s.inner.Select(ctx, nodes, opts...)
	if err != nil {
		return n, done, err
	}
	return n, func(ctx context.Context, di loadbalance.DoneInfo) {
		s.mu.Lock()
		s.errs = append(s.errs, di.Err)
		s.mu.Unlock()
		if done != nil {
			done(ctx, di)
		}
	}, nil
}

func (s *recordingSelector) lastErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.errs) == 0 {
		return errNoDoneRecorded
	}
	return s.errs[len(s.errs)-1]
}

var errNoDoneRecorded = errors.New("no done recorded")

// newRecordingClient 组装带记录 Selector 的单端点客户端。
func newRecordingClient(t *testing.T, srvURL string, extra ...ClientOption) (*Client, *recordingSelector) {
	t.Helper()
	sel, err := loadbalance.NewSelector(loadbalance.StrategyRoundRobin)
	if err != nil {
		t.Fatal(err)
	}
	rs := &recordingSelector{inner: sel}
	u, err := url.Parse(srvURL)
	if err != nil {
		t.Fatalf("parse srvURL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse srvURL port: %v", err)
	}
	stub := &stubNNSClient{instances: []registry.Instance{{IP: "127.0.0.1", Port: port}}}
	opts := append([]ClientOption{WithNNS("rec-svc", stub), WithSelector(rs)}, extra...)
	c := MustNewClient("rec-svc", opts...)
	t.Cleanup(c.Close)
	// 等首轮刷新把节点灌进来（构造后立即刷新一次）
	deadline := time.Now().Add(2 * time.Second)
	for len(c.endpointManager.GetNodes()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("endpoint refresh never produced nodes")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return c, rs
}

// TestExecSkeleton_LbDoneCalledWithTransportErr 钉住骨架第三环：传输层
// 错误必须回写 lbDone（DoneInfo.Err 非 nil）——P2C 摘除依赖它。
func TestExecSkeleton_LbDoneCalledWithTransportErr(t *testing.T) {
	srvURL, _ := jsonServer(t, http.StatusOK, `{}`)
	c, rs := newRecordingClient(t, srvURL, WithDialFunc(func(string) (network.Conn, error) {
		return nil, errors.New("dial refused")
	}))
	_, _, err := c.DoDetail(context.Background(), http.MethodGet, "/x", nil)
	if err == nil {
		t.Fatal("expected transport error")
	}
	if got := rs.lastErr(); got == nil || errors.Is(got, errNoDoneRecorded) {
		t.Errorf("transport error must reach lbDone, got %v", got)
	}
}

// TestExecSkeleton_LbDoneSuccessNilErr 钉住成功路径 DoneInfo.Err 为 nil；
// 同时钉住**当前**次序：非 2xx 不是传输层错误，不进 DoneInfo（#164 在
// 跟踪该次序缺陷，本测试只钉现状防重构漂移，行为变更归 #164）。
func TestExecSkeleton_LbDoneSuccessNilErr(t *testing.T) {
	srvURL, _ := jsonServer(t, http.StatusOK, `{"ok":true}`)
	c, rs := newRecordingClient(t, srvURL)
	var out map[string]any
	if err := c.DoCodec(context.Background(), codec.DefaultName, http.MethodGet, "/x", nil, &out); err != nil {
		t.Fatalf("DoCodec: %v", err)
	}
	if got := rs.lastErr(); got != nil {
		t.Errorf("success DoneInfo.Err = %v, want nil", got)
	}

	errSrv, _ := jsonServer(t, http.StatusBadRequest, `bad`)
	cErr, rsErr := newRecordingClient(t, errSrv)
	if _, _, err := cErr.DoDetail(context.Background(), http.MethodGet, "/x", nil); err == nil {
		t.Fatal("expected ServiceError")
	}
	if got := rsErr.lastErr(); got != nil {
		t.Errorf("non-2xx DoneInfo.Err = %v, want nil（现状：lbDone 在 handleResponse 之前回写，#164）", got)
	}
}

// TestExecSkeleton_PriorityRejectionSentinel 回归（#165）：队列拒绝错误
// 必须可经哨兵 ErrPriorityUnavailable 与 ctx 取消原因双路 errors.Is 分流
// （修复前两者都只埋在文案里，调用方只能 strings.Contains）。
func TestExecSkeleton_PriorityRejectionSentinel(t *testing.T) {
	srvURL, _ := jsonServer(t, http.StatusOK, `{}`)
	c := MustNewClient("test", WithBaseURL(srvURL), WithPriorityQueue(1))
	defer c.Close()
	release := fillPriorityQueue(t, c.priorityQueue)
	defer release()

	// ctx 带短超时：Acquire 阻塞等待至超时（队列满 → ctx.Done → 返回 false）
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := c.DoCodec(ctx, codec.DefaultName, http.MethodGet, "/x", nil, nil)
	if err == nil {
		t.Fatal("saturated queue must reject, got nil")
	}
	if !errors.Is(err, ErrPriorityUnavailable) {
		t.Errorf("err = %v, want errors.Is(err, ErrPriorityUnavailable)", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want errors.Is(err, context.DeadlineExceeded)（ctx 取消原因须以 %%w 携带）", err)
	}
	// 文案前缀保持历史逐字节等价（既有基线以 Contains 钉住）
	if !strings.Contains(err.Error(), "request cancelled due to priority queue") {
		t.Errorf("err = %v, want historical message prefix", err)
	}
}
