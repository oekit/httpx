package httpx

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/hertz/pkg/protocol"
)

// TestCookieJar_DoesNotOverrideCallerCookie 回归（#113）：jar 注入必须跳过
// 调用方已设的同名 cookie——base 中间件注释承诺「不覆盖调用方已有设置」，
// 修复前用 SetCookie 注入，同键替换静默改写调用方值。
func TestCookieJar_DoesNotOverrideCallerCookie(t *testing.T) {
	var mu sync.Mutex
	var gotCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			// 一次下发两个 cookie：sid（调用方要覆盖）+ other（jar 独有）
			w.Header().Add("Set-Cookie", "sid=jar-value; Path=/")
			w.Header().Add("Set-Cookie", "other=jar-other; Path=/")
		}
		mu.Lock()
		gotCookie = r.Header.Get("Cookie")
		mu.Unlock()
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := MustNewClient("v113", WithBaseURL(srv.URL), WithCookieJar(jar))
	defer c.Close()
	ctx := context.Background()

	// 第一跳：服务端下发 sid/other，base 中间件回收进 jar
	if _, err := c.DoRaw(ctx, http.MethodGet, "/login", nil); err != nil {
		t.Fatalf("login: %v", err)
	}

	// 第二跳：调用方自己设同名 sid（请求级值必须赢），other 无调用方设置
	// ⇒ jar 注入。服务端应同时看到两者，且 sid 是调用方的值。
	req, resp := protocol.AcquireRequest(), protocol.AcquireResponse()
	defer protocol.ReleaseRequest(req)
	defer protocol.ReleaseResponse(resp)
	req.SetRequestURI(srv.URL + "/echo")
	req.Header.SetCookie("sid", "caller-value")
	if err := c.Hertz().Do(ctx, req, resp); err != nil {
		t.Fatalf("echo: %v", err)
	}
	mu.Lock()
	cookie := gotCookie
	mu.Unlock()

	if !strings.Contains(cookie, "sid=caller-value") {
		t.Fatalf("caller cookie overridden: %q（同名必须保留调用方值）", cookie)
	}
	if !strings.Contains(cookie, "other=jar-other") {
		t.Fatalf("jar-only cookie not injected: %q（无冲突的 jar cookie 照常注入）", cookie)
	}
	if strings.Contains(cookie, "jar-value") {
		t.Fatalf("jar value leaked over caller value: %q", cookie)
	}

	// 第三跳：调用方完全不设 cookie ⇒ jar 两个都注入（原有行为不回归）
	req3, resp3 := protocol.AcquireRequest(), protocol.AcquireResponse()
	defer protocol.ReleaseRequest(req3)
	defer protocol.ReleaseResponse(resp3)
	req3.SetRequestURI(srv.URL + "/echo")
	if err := c.Hertz().Do(ctx, req3, resp3); err != nil {
		t.Fatalf("echo3: %v", err)
	}
	mu.Lock()
	cookie3 := gotCookie
	mu.Unlock()
	if !strings.Contains(cookie3, "sid=jar-value") || !strings.Contains(cookie3, "other=jar-other") {
		t.Fatalf("jar cookies not injected when caller silent: %q", cookie3)
	}
}
