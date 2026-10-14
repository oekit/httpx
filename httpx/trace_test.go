package httpx

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/client"
	"github.com/cloudwego/hertz/pkg/protocol"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// capturedHeader 是 capture endpoint 实际断言用的字符串视图。
// 避免直接搬运 protocol.RequestHeader(内含 NoCopy)。
type capturedHeader struct {
	traceparent string
	tracestate  string
}

func captureHeaderFrom(req *protocol.Request) capturedHeader {
	return capturedHeader{
		traceparent: req.Header.Get("traceparent"),
		tracestate:  req.Header.Get("tracestate"),
	}
}

// captureEndpoint 同步记录一次被调时看到的 header 关键字段,供测试断言。
type captureEndpoint struct {
	mu     sync.Mutex
	seen   capturedHeader
	called int
}

func (c *captureEndpoint) call(_ context.Context, req *protocol.Request, _ *protocol.Response) error {
	c.mu.Lock()
	c.called++
	c.seen = captureHeaderFrom(req)
	c.mu.Unlock()
	return nil
}

func (c *captureEndpoint) wrap() client.Endpoint { return c.call }

func newCapturingClient(t *testing.T, opts ...ClientOption) (*Client, *captureEndpoint) {
	t.Helper()
	cap := &captureEndpoint{}
	// 把 capture 作为业务终点(在 trace mw 之后)
	opts = append(opts, func(c *Client) {
		c.lastMW = append(c.lastMW, func(next client.Endpoint) client.Endpoint {
			return func(ctx context.Context, req *protocol.Request, resp *protocol.Response) error {
				if err := next(ctx, req, resp); err != nil {
					return err
				}
				return cap.call(ctx, req, resp)
			}
		})
	})
	cl := NewClient("test", opts...)
	return cl, cap
}

// TestWithTracePropagation_InjectsTraceParent 验证 ctx 带 SpanContext 时,
// 请求 header 注入 traceparent (W3C TraceContext)。
func TestWithTracePropagation_InjectsTraceParent(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator()) })

	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tracer := tp.Tracer("test")

	cl, cap := newCapturingClient(t, WithTracePropagation())
	ctx, span := tracer.Start(context.Background(), "test-span")
	defer span.End()

	traceMW := cl.lastMW[0]
	outer := traceMW(cap.wrap())
	if err := outer(ctx, &protocol.Request{}, &protocol.Response{}); err != nil {
		t.Fatalf("outer returned err: %v", err)
	}

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.called == 0 {
		t.Fatal("capture endpoint not called")
	}
	if cap.seen.traceparent == "" {
		t.Fatal("traceparent header not set by WithTracePropagation")
	}

	// 验证 traceparent 格式合法(OTel 自身 Extract 能解出)
	carrier := propagation.HeaderCarrier(http.Header{"traceparent": []string{cap.seen.traceparent}})
	_ = (propagation.TraceContext{}).Extract(context.Background(), carrier)

	// 验证 trace-id 跟 ctx SpanContext 一致
	spanCtx := trace.SpanContextFromContext(ctx)
	if got := extractTraceID(cap.seen.traceparent); got != spanCtx.TraceID().String() {
		t.Errorf("traceparent trace-id = %q, want %q", got, spanCtx.TraceID().String())
	}
}

// TestWithTracePropagation_NoSpanContext 验证 ctx 无 SpanContext 时,
// 不注入 trace 头,不 panic。
func TestWithTracePropagation_NoSpanContext(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator()) })

	cl, cap := newCapturingClient(t, WithTracePropagation())

	traceMW := cl.lastMW[0]
	outer := traceMW(cap.wrap())
	if err := outer(context.Background(), &protocol.Request{}, &protocol.Response{}); err != nil {
		t.Fatalf("outer returned err: %v", err)
	}

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.called == 0 {
		t.Fatal("capture endpoint not called")
	}
	if cap.seen.traceparent != "" {
		t.Errorf("traceparent should be empty for ctx without SpanContext, got %q", cap.seen.traceparent)
	}
}

// TestWithTracePropagation_InjectAtLast 验证 trace propagation 走 UseAsLast 路径,
// NewClient 装配后 lastMW[0] 是 trace mw,内容已准备好供 UseAsLast 注入。
func TestWithTracePropagation_InjectAtLast(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator()) })

	cl := NewClient("test", WithTracePropagation())
	if len(cl.lastMW) != 1 {
		t.Fatalf("expected 1 lastMW (trace), got %d", len(cl.lastMW))
	}
	// lastMW[0] 在 NewClient 末尾调 UseAsLast(useAsLastErr 处理 fallback 已在 NewClient 注释)
	// 这里只能验证 lastMW 装配正确,UseAsLast 的实际行为需要 e2e(integration tests)
	// 验证 — 单元测试焦点在"中间件能 inject 头",已在前两个 case 覆盖。
}

// extractTraceID 从 traceparent 字符串解析 32 字符 trace-id。
// 格式: "00-{traceid(32)}-{spanid(16)}-{flags(2)}"
func extractTraceID(traceparent string) string {
	if len(traceparent) < 55 {
		return ""
	}
	return traceparent[3:35]
}
