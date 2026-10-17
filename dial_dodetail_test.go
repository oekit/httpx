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

// TestWithDialFunc_AppendsHertzOption 验证 WithDialFunc 透传到 c.hzOpts。
func TestWithDialFunc_AppendsHertzOption(t *testing.T) {
	var dials []string
	fn := func(addr string) (network.Conn, error) {
		dials = append(dials, addr)
		return nil, errors.New("blocked")
	}
	c := NewClient("test", WithDialFunc(fn))
	if c == nil {
		t.Fatal("NewClient returned nil")
	}
	if len(c.hzOpts) == 0 {
		t.Fatal("expected hzOpts to include WithDialFunc, got empty")
	}
}

// TestWithDialFunc_UsedForConnections 验证注入的拨号函数真实接管建连：
// 拨号层拒绝时请求失败，且错误透传给调用方（SSRF 拦截生效点）。
func TestWithDialFunc_UsedForConnections(t *testing.T) {
	srvURL, _ := jsonServer(t, http.StatusOK, `{}`)
	c := NewClient("test",
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
	c := NewClient("test", WithBaseURL(srvURL))
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
	c := NewClient("test", WithBaseURL(srvURL))
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
	if !errAs(err, &svcErr) {
		t.Fatalf("expected *ServiceError, got %T: %v", err, err)
	}
	if svcErr.Code != http.StatusBadRequest {
		t.Errorf("expected ServiceError code 400, got %d", svcErr.Code)
	}
}

// TestDoDetail_TransportError 验证传输层失败时 status=0、body 为 nil。
func TestDoDetail_TransportError(t *testing.T) {
	srvURL, _ := jsonServer(t, http.StatusOK, `{}`)
	c := NewClient("test",
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
