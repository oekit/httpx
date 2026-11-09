package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/protocol"
)

// TestRetryIfFunc_ExplicitWinsRegardlessOfOptionOrder 回归测试（#41）：
// **显式设置的重试谓词必须赢，且与 option 顺序无关**。
//
// 缺陷成因：`WithRetryConfig` 与 `WithRetryIfFunc` 写同一个字段
// （`c.retryIf`）且都无条件赋值，而 option 按传入顺序依次 apply
// ⇒ 「后写者赢」⇒ 把 `WithRetryIfFunc` 写在 `WithRetryConfig` **之前**时，
// 用户谓词被**无声丢弃**，退回默认谓词——而默认谓词**不看 HTTP 方法**
// （5xx + network error 一律重试）⇒ **POST 被自动重放**。
//
// 后果：下单 / 支付 / 发 webhook 这类 POST 收到下游 500 时被重复执行
// ⇒ 重复订单、重复扣款、重复通知；且不报错、不 panic，
// 现象与「下游真的失败」难以区分。
func TestRetryIfFunc_ExplicitWinsRegardlessOfOptionOrder(t *testing.T) {
	onlyGET := func(req *protocol.Request, _ *protocol.Response, _ error) bool {
		return req != nil && string(req.Method()) == http.MethodGet
	}

	cases := []struct {
		name string
		opts []ClientOption
	}{
		{
			name: "谓词在前（缺陷形态）",
			opts: []ClientOption{
				WithRetryIfFunc(onlyGET),
				WithRetryConfig(3, 10*time.Millisecond, 20*time.Millisecond, 0),
			},
		},
		{
			name: "谓词在后",
			opts: []ClientOption{
				WithRetryConfig(3, 10*time.Millisecond, 20*time.Millisecond, 0),
				WithRetryIfFunc(onlyGET),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(srv.Close)

			opts := append([]ClientOption{WithEndpoints(srv.URL)}, tc.opts...)
			cli := MustNewClient("svc", opts...)

			_, _, err := cli.DoDetail(context.Background(), http.MethodPost, "/x", nil)
			if err == nil {
				t.Fatal("下游返回 500，期望返回错误")
			}
			if got := hits.Load(); got != 1 {
				t.Fatalf("POST 被请求了 %d 次，期望 1 次（显式谓词应生效：只重试 GET）", got)
			}
		})
	}
}

// TestRetryIfFunc_DefaultDoesNotRetryNonIdempotent 回归（#85）：没有设置
// 显式谓词时，默认谓词带**幂等方法门**——POST 等非幂等请求遇 5xx 不再被
// 自动重放（旧默认不看方法，网关超时但后端已写入时会重复下单/重复创建）。
//
// 旧护栏 TestRetryIfFunc_DefaultStillRetriesNonIdempotent 钉住的是
// 「POST 也重试」的旧口径，#85 论证「保持兼容 ≠ 安全默认」后语义翻转，
// 由本测试钉住新口径；需要旧语义经 WithRetryIfFunc 显式声明。
func TestRetryIfFunc_DefaultDoesNotRetryNonIdempotent(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	cli := MustNewClient("svc",
		WithEndpoints(srv.URL),
		WithRetryConfig(3, 10*time.Millisecond, 20*time.Millisecond, 0),
	)

	body := map[string]string{"order": "dup-risk"}
	_, _, _ = cli.DoDetail(context.Background(), http.MethodPost, "/x", body)
	if got := hits.Load(); got != 1 {
		t.Fatalf("默认谓词不应重试带 body 的 POST（重复下单风险），实际请求 %d 次", got)
	}
}

// TestRetryIfFunc_DefaultStillRetriesIdempotent 回归（#85）：幂等方法（GET）
// 的 5xx 重试保留——方法门收紧的是非幂等重放，不是重试本身。
func TestRetryIfFunc_DefaultStillRetriesIdempotent(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	cli := MustNewClient("svc",
		WithEndpoints(srv.URL),
		WithRetryConfig(3, 10*time.Millisecond, 20*time.Millisecond, 0),
	)

	_, _, _ = cli.DoDetail(context.Background(), http.MethodGet, "/x", nil)
	if got := hits.Load(); got < 2 {
		t.Fatalf("默认谓词应重试幂等 GET 的 5xx，实际请求 %d 次", got)
	}
}

// TestRetryIfFunc_IdempotentGateUnit 单元钉住幂等门判定：只按**方法**判定
// （#129）——幂等方法可重试，POST（无论有无 body）与 PATCH 等一律不可。
func TestRetryIfFunc_IdempotentGateUnit(t *testing.T) {
	newReq := func(method string, body []byte) *protocol.Request {
		req := &protocol.Request{}
		req.SetMethod(method)
		req.SetRequestURI("http://127.0.0.1/x")
		if body != nil {
			req.SetBody(body)
		}
		return req
	}

	for _, tc := range []struct {
		method string
		body   []byte
		want   bool
	}{
		{http.MethodGet, nil, true},
		{http.MethodHead, nil, true},
		{http.MethodOptions, nil, true},
		{http.MethodPut, []byte(`{"a":1}`), true},
		{http.MethodDelete, nil, true},
		{http.MethodTrace, nil, true},
		{http.MethodPost, nil, false},               // #129：无 body 的 POST 同样判非幂等（副作用在方法+路径上）
		{http.MethodPost, []byte(`{"a":1}`), false}, // 带 body 的 POST：重复下单风险
		{http.MethodPatch, []byte(`{"a":1}`), false},
	} {
		if got := idempotentForRetry(newReq(tc.method, tc.body)); got != tc.want {
			t.Errorf("idempotentForRetry(%s, body=%v) = %v, want %v", tc.method, tc.body != nil, got, tc.want)
		}
	}

	// 默认谓词整体：幂等 5xx 重试、POST（含无 body）5xx/网络错误不重试、
	// 幂等方法错误按方法门放行
	srvErr := &protocol.Response{}
	srvErr.SetStatusCode(500)
	if !defaultRetryIf(newReq(http.MethodGet, nil), srvErr, nil) {
		t.Error("defaultRetryIf: GET 5xx 应重试")
	}
	if defaultRetryIf(newReq(http.MethodPost, nil), srvErr, nil) {
		t.Error("defaultRetryIf: 无 body 的 POST 5xx 不应重试（#129）")
	}
	if defaultRetryIf(newReq(http.MethodPost, []byte(`{}`)), srvErr, nil) {
		t.Error("defaultRetryIf: 带 body 的 POST 5xx 不应重试")
	}
	if defaultRetryIf(newReq(http.MethodPost, nil), nil, errors.New("net")) {
		t.Error("defaultRetryIf: 无 body 的 POST 网络错误不应重试（后端可能已写入，#129）")
	}
	if defaultRetryIf(newReq(http.MethodPost, []byte(`{}`)), nil, errors.New("net")) {
		t.Error("defaultRetryIf: 带 body 的 POST 网络错误不应重试（后端可能已写入）")
	}
	if !defaultRetryIf(newReq(http.MethodGet, nil), nil, errors.New("net")) {
		t.Error("defaultRetryIf: GET 网络错误应重试")
	}
}

// TestRetryIfFunc_DefaultDoesNotRetryNoBodyPost 回归（#129）：无 body 的
// POST 不再被默认谓词重放。
//
// 旧的幂等门在方法判定之外留了「len(req.Body()) == 0 即幂等」的兜底——空
// body 的 POST 恰恰是副作用型接口最常见的形态（POST /pay、POST /orders/{id}/cancel：
// 副作用在方法+路径上，不在 payload 里）。实测（修复前，同一路径 /pay、
// maxAttempts=3）：有 body 的 POST 上游被打 1 次，**无 body 的 POST 被打满 3 次**；
// 网关超时但后端已写入时即重复下单/重复扣款。修复后幂等门只按方法判定，
// 无 body 的 POST 与有 body 的一样只打 1 次。
func TestRetryIfFunc_DefaultDoesNotRetryNoBodyPost(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	t.Cleanup(srv.Close)

	cli := MustNewClient("svc",
		WithBaseURL(srv.URL),
		WithRetryConfig(3, 10*time.Millisecond, 50*time.Millisecond, 0),
	)

	var out map[string]any
	// 与 issue 复现形态逐字对齐：DoJSON + 显式 nil body（非空 map 序列化后
	// body 非空，走的是另一条已被 #85 钉住的路径）。
	_ = cli.DoJSON(context.Background(), http.MethodPost, "/pay", nil, &out)
	if got := hits.Load(); got != 1 {
		t.Fatalf("无 body 的 POST 被请求了 %d 次，期望 1 次（#129：空 body 不是幂等豁免，副作用在方法+路径上）", got)
	}
}

// TestRetryIfFunc_ExplicitPredicateIsUsed 回归护栏：显式谓词真的被用上了
// （谓词返回 true 时该重试就重试）——防止「显式谓词被忽略」换个方向复发。
// 该测试同时钉住 #129 的逃生舱：确实需要重放 POST 的调用方经 WithRetryIfFunc
// 显式声明（复现形态即无 body 的 POST）。
func TestRetryIfFunc_ExplicitPredicateIsUsed(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	cli := MustNewClient("svc",
		WithEndpoints(srv.URL),
		WithRetryIfFunc(func(_ *protocol.Request, _ *protocol.Response, _ error) bool { return true }),
		WithRetryConfig(3, 10*time.Millisecond, 20*time.Millisecond, 0),
	)

	_, _, _ = cli.DoDetail(context.Background(), http.MethodPost, "/x", nil)
	if got := hits.Load(); got < 2 {
		t.Fatalf("显式谓词返回 true 应当触发重试，实际请求 %d 次", got)
	}
}
