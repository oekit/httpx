package httpx

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/network"
)

// TestWithDialFunc_InstallsDialer 验证 WithDialFunc 装配 TLS 感知拨号器（#74）。
// 装配面已从 c.hzOpts 挪到 c.dialer（NewClient 末位兜底 append——hertz 的
// client.WithTLSConfig 会无条件覆盖 o.Dialer，hzOpts 位置的装配会被排在其后的
// TLS 选项清掉），断言类型即可。
func TestWithDialFunc_InstallsDialer(t *testing.T) {
	var dials []string
	fn := func(addr string) (network.Conn, error) {
		dials = append(dials, addr)
		return nil, errors.New("blocked")
	}
	c := MustNewClient("test", WithDialFunc(fn))
	if c == nil {
		t.Fatal("NewClient returned nil")
	}
	td, ok := c.dialer.(*tlsAwareDialer)
	if !ok {
		t.Fatalf("expected *tlsAwareDialer installed, got %T", c.dialer)
	}
	if td.fn == nil {
		t.Fatal("tlsAwareDialer.fn should wrap the injected dial func")
	}
}

// TestWithDialFunc_UsedForConnections 验证注入的拨号函数真实接管建连：
// 拨号层拒绝时请求失败，且错误透传给调用方（SSRF 拦截生效点）。
func TestWithDialFunc_UsedForConnections(t *testing.T) {
	srvURL, _ := jsonServer(t, http.StatusOK, `{}`)
	c := MustNewClient("test",
		WithBaseURL(srvURL),
		WithDialFunc(func(addr string) (network.Conn, error) {
			return nil, errors.New("blocked by dial guard")
		}),
	)
	defer c.Close()

	_, _, err := c.DoDetail(context.Background(), http.MethodGet, "/x", nil)
	if err == nil {
		t.Fatal("expected dial guard error, got nil")
	}
	if !strings.Contains(err.Error(), "blocked by dial guard") {
		t.Fatalf("expected dial guard error to surface, got: %v", err)
	}
}

// TestWithDialFunc_WithSafeDial_MutuallyExclusive 回归（#276）：两者都直接
// 装配 c.dialer，后应用者会静默顶掉先应用者——WithSafeDial(), WithDialFunc(f)
// 让 tlsAwareDialer.fn 覆盖 guarded，拨号层 SSRF 判定整体消失且构造期无
// 错误；反向顺序则自定义拨号被丢弃。修复后两种顺序都在构造期经
// setConstructErr 显式报错（与 #89 同口径），错误信息点名双方。
func TestWithDialFunc_WithSafeDial_MutuallyExclusive(t *testing.T) {
	fn := func(string) (network.Conn, error) { return nil, errors.New("custom dial") }
	cases := []struct {
		name string
		opts []ClientOption
	}{
		{"WithSafeDial 在前 WithDialFunc 在后（SSRF 防护会消失）", []ClientOption{WithSafeDial(), WithDialFunc(fn)}},
		{"WithDialFunc 在前 WithSafeDial 在后（自定义拨号会消失）", []ClientOption{WithDialFunc(fn), WithSafeDial()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := NewClient("svc-dial-conflict", tc.opts...)
			if err == nil {
				c.Close()
				t.Fatal("期望构造期报错（WithDialFunc 与 WithSafeDial 互斥），got nil")
			}
			if c != nil {
				t.Errorf("期望错误路径返回 nil client，got %v", c)
			}
			for _, want := range []string{"互斥", "WithDialFunc", "WithSafeDial"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("错误信息应包含 %q 以指明冲突双方，got %q", want, err.Error())
				}
			}
		})
	}
}

// TestWithDialFunc_WithSafeDial_SameOptionRepeatable：同一选项重复应用不属
// 冲突（最后一次为准），不得误报构造期错误。
func TestWithDialFunc_WithSafeDial_SameOptionRepeatable(t *testing.T) {
	c := MustNewClient("svc-dial-repeat", WithSafeDial(), WithSafeDial())
	defer c.Close()
	if _, ok := c.dialer.(*tlsAwareDialer); !ok {
		t.Fatalf("expected *tlsAwareDialer, got %T", c.dialer)
	}
}

// TestWithHertzOption_EscapeHatch 验证 WithHertzOption 逐条追加到 c.hzOpts。
func TestWithHertzOption_EscapeHatch(t *testing.T) {
	opts := []config.ClientOption{
		{F: func(o *config.ClientOptions) {}},
		{F: func(o *config.ClientOptions) {}},
	}
	// 直接构造，避免 NewClient 消费 hzOpts 后无法断言追加
	c := &Client{}
	WithHertzOption(opts...)(c)
	if len(c.hzOpts) != 2 {
		t.Fatalf("expected 2 hzOpts appended, got %d", len(c.hzOpts))
	}
}

// TestDoDetail_SuccessReturnsStatusAndRawBody 验证 2xx 时返回 status + 原始 body
// （区别于 DoJSON 只解到 out）。
func TestDoDetail_SuccessReturnsStatusAndRawBody(t *testing.T) {
	srvURL, _ := jsonServer(t, http.StatusCreated, `{"id":7}`)
	c := MustNewClient("test", WithBaseURL(srvURL))
	defer c.Close()

	status, body, err := c.DoDetail(context.Background(), http.MethodPost, "/items", map[string]int{"n": 1})
	if err != nil {
		t.Fatalf("DoDetail: %v", err)
	}
	if status != http.StatusCreated {
		t.Errorf("expected status 201, got %d", status)
	}
	if string(body) != `{"id":7}` {
		t.Errorf("expected raw body {\"id\":7}, got %s", body)
	}
}

// TestDoDetail_ErrorStillReturnsStatusAndBody 验证非 2xx 时仍返回
// status + body，同时携带 *ServiceError。
func TestDoDetail_ErrorStillReturnsStatusAndBody(t *testing.T) {
	srvURL, _ := jsonServer(t, http.StatusBadRequest, `{"error":"bad"}`)
	c := MustNewClient("test", WithBaseURL(srvURL))
	defer c.Close()

	status, body, err := c.DoDetail(context.Background(), http.MethodGet, "/bad", nil)
	if err == nil {
		t.Fatal("expected error for 400, got nil")
	}
	if status != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", status)
	}
	if string(body) != `{"error":"bad"}` {
		t.Errorf("expected raw body on error, got %s", body)
	}
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected *ServiceError, got %T: %v", err, err)
	}
	if svcErr.Code != http.StatusBadRequest {
		t.Errorf("expected ServiceError code 400, got %d", svcErr.Code)
	}
}

// TestDoDetail_TransportError 验证传输层失败时 status=0、body 为 nil。
func TestDoDetail_TransportError(t *testing.T) {
	srvURL, _ := jsonServer(t, http.StatusOK, `{}`)
	c := MustNewClient("test",
		WithBaseURL(srvURL),
		WithDialFunc(func(string) (network.Conn, error) {
			return nil, errors.New("dial refused")
		}),
	)
	defer c.Close()

	status, body, err := c.DoDetail(context.Background(), http.MethodGet, "/x", nil)
	if err == nil {
		t.Fatal("expected transport error, got nil")
	}
	if status != 0 || body != nil {
		t.Errorf("expected status=0 body=nil on transport error, got status=%d body=%s", status, body)
	}
}
