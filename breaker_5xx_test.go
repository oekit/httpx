package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sony/gobreaker"
)

// TestWithCircuitBreaker_5xxKeepsServiceErrorAndBody 回归（#107）：开熔断后
// 5xx 不得转成普通 error——引擎层 handleResponse 的 *ServiceError 路径必须
// 仍然可达（errors.As 取状态码与响应体，doc.go 明写的错误处理写法）。
// 修复前熔断中间件把 5xx 转成 fmt.Errorf("server error: %d") 外抛：响应体
// 丢失、*ServiceError 恒不可达，开/关熔断改变错误类型。
func TestWithCircuitBreaker_5xxKeepsServiceErrorAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom-body-detail"))
	}))
	defer srv.Close()

	c := MustNewClient("v107", WithBaseURL(srv.URL),
		WithCircuitBreaker("v107", gobreaker.Settings{Interval: 10 * time.Second}))
	defer c.Close()

	var out map[string]any
	err := c.DoJSON(context.Background(), http.MethodGet, "/500", nil, &out)
	if err == nil {
		t.Fatal("5xx must return error")
	}
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) {
		t.Fatalf("err = %T (%v), want errors.As *ServiceError", err, err)
	}
	if svcErr.Code != http.StatusInternalServerError {
		t.Fatalf("svcErr.Code = %d, want 500", svcErr.Code)
	}
	if !strings.Contains(svcErr.Message, "boom-body-detail") {
		t.Fatalf("svcErr.Message = %q, want body preserved", svcErr.Message)
	}
}

// TestWithCircuitBreaker_5xxStillTripsCircuit 回归（#107 配套）：5xx 不再外抛
// 但必须仍计入熔断失败统计——失败次数达标后熔断打开，后续请求拒绝且不打到
// 后端（这正是熔断中间件存在的意义，修的是「错误形态」不是「统计」）。
func TestWithCircuitBreaker_5xxStillTripsCircuit(t *testing.T) {
	var executed atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		executed.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := MustNewClient("v107-trip", WithBaseURL(srv.URL),
		WithCircuitBreaker("v107-trip", gobreaker.Settings{
			Interval: 0, // 计数不重置
			ReadyToTrip: func(counts gobreaker.Counts) bool {
				return counts.TotalFailures >= 2
			},
		}))
	defer c.Close()

	ctx := context.Background()
	for i := 0; i < 2; i++ {
		var out map[string]any
		err := c.DoJSON(ctx, http.MethodGet, "/500", nil, &out)
		var svcErr *ServiceError
		if !errors.As(err, &svcErr) {
			t.Fatalf("request %d: err = %v, want *ServiceError", i+1, err)
		}
	}
	// 第三次：熔断已打开，拒绝且不执行
	var out map[string]any
	err := c.DoJSON(ctx, http.MethodGet, "/500", nil, &out)
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("third request err = %v, want ErrCircuitOpen", err)
	}
	if got := executed.Load(); got != 2 {
		t.Fatalf("handler executed %d times, want 2（熔断拒绝不得打到后端）", got)
	}
}

// TestWithCircuitBreaker_IsSuccessfulStillHonored 回归（#107 配套）：用户自定义
// IsSuccessful 钩子对 5xx 统计照常生效——判定为「成功」的 5xx 不计入失败，
// 熔断不打开（gobreaker.Settings 已有该钩子，修复前后都必须透传）。
func TestWithCircuitBreaker_IsSuccessfulStillHonored(t *testing.T) {
	var executed atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		executed.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := MustNewClient("v107-isok", WithBaseURL(srv.URL),
		WithCircuitBreaker("v107-isok", gobreaker.Settings{
			Interval: 0,
			ReadyToTrip: func(counts gobreaker.Counts) bool {
				return counts.TotalFailures >= 1
			},
			// 用户判定一切可接受（如按错误体分级）：5xx 不计失败
			IsSuccessful: func(err error) bool { return true },
		}))
	defer c.Close()

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		var out map[string]any
		err := c.DoJSON(ctx, http.MethodGet, "/500", nil, &out)
		var svcErr *ServiceError
		if !errors.As(err, &svcErr) {
			t.Fatalf("request %d: err = %v, want *ServiceError（统计判定不得改变错误形态）", i+1, err)
		}
	}
	if got := executed.Load(); got != 3 {
		t.Fatalf("handler executed %d times, want 3（IsSuccessful 判成功则熔断不得打开）", got)
	}
}

// TestRoundTripper_5xxStillTripsCircuitViaBridge 回归（#107 配套）：桥接层
// 5xx 按标准库语义透传（roundtripper_test.go 的 KeepNon2xx 用例钉形态），
// 本用例钉统计：桥接路径上 5xx 同样把熔断推向打开。
func TestRoundTripper_5xxStillTripsCircuitViaBridge(t *testing.T) {
	var executed atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		executed.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	rt := NewRoundTripper("v107-bridge-trip", WithBaseURL(srv.URL),
		WithCircuitBreaker("v107-bridge-trip", gobreaker.Settings{
			Interval: 0,
			ReadyToTrip: func(counts gobreaker.Counts) bool {
				return counts.TotalFailures >= 1
			},
		}))
	defer rt.(*roundTripper).Close()

	resp, err := rt.RoundTrip(httptest.NewRequest(http.MethodGet, srv.URL+"/500", nil))
	if err != nil {
		t.Fatalf("first 5xx must stay non-error, err = %v", err)
	}
	_ = resp.Body.Close()

	resp2, err := rt.RoundTrip(httptest.NewRequest(http.MethodGet, srv.URL+"/500", nil))
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("second request err = %v, want ErrCircuitOpen", err)
	}
	if resp2 != nil {
		_ = resp2.Body.Close()
	}
	if got := executed.Load(); got != 1 {
		t.Fatalf("handler executed %d times, want 1", got)
	}
}

// TestDoCodec_UnknownCodecReturnsError 回归（#114）：DoCodec 请求路径上的
// codec 名解析必须返回 error 而非 panic——codec 名来自配置时，未注册即
// panic 是「配置字符串触发进程死亡」（#83 同族契约）。
func TestDoCodec_UnknownCodecReturnsError(t *testing.T) {
	c := MustNewClient("v114", WithBaseURL("http://127.0.0.1:1"))
	defer c.Close()

	var out map[string]any
	err := c.DoCodec(context.Background(), "protobuf", http.MethodGet, "/x", nil, &out)
	if err == nil {
		t.Fatal("unknown codec must return error, got nil")
	}
	if !strings.Contains(err.Error(), `unknown codec "protobuf"`) {
		t.Fatalf("err = %v, want mention of unknown codec", err)
	}
	if !strings.HasPrefix(err.Error(), "v114: ") {
		t.Fatalf("err = %v, want service-prefixed message", err)
	}
}

// TestDoCodec_UnknownCodecSentinel 回归（#165）：unknown codec 错误必须可经
// 哨兵 ErrUnknownCodec 分流（errors.Is 单源判定，不再依赖文案匹配）。
func TestDoCodec_UnknownCodecSentinel(t *testing.T) {
	c := MustNewClient("v165", WithBaseURL("http://127.0.0.1:1"))
	defer c.Close()

	var out map[string]any
	err := c.DoCodec(context.Background(), "no-such-codec", http.MethodGet, "/x", nil, &out)
	if err == nil {
		t.Fatal("unknown codec must return error, got nil")
	}
	if !errors.Is(err, ErrUnknownCodec) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnknownCodec)", err)
	}
	// 文案保持历史形态（service 前缀 + quoted codec 名）
	if !strings.HasPrefix(err.Error(), "v165: unknown codec \"no-such-codec\"") {
		t.Fatalf("err = %v, want historical message form", err)
	}
}
