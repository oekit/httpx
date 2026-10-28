package httpx

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// authProbe 起一个只回显请求头的测试服务，并用 channel 把结果送回测试
// goroutine——**不能**用普通变量：handler 在别的 goroutine 里写、测试在
// 读，-race 会判竞争。
func authProbe(t *testing.T) (string, <-chan []string) {
	t.Helper()
	ch := make(chan []string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ch <- r.Header.Values("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(srv.Close)
	return srv.URL, ch
}

// TestRoundTripper_SingleAuthorizationHeader 回归测试（#43）：桥接层发出的
// 请求**只能有一个** Authorization 头（RFC 7235 §3.1）。
//
// 缺陷成因：桥接层先用 applyAuth（Set，写入客户端级凭证），再用 Add 追加
// 调用方自带的同名头 ⇒ 两个值并存 ⇒ 线上两行 Authorization。
//
// 各服务端对重复头的处理**归属不确定**：取首个 ⇒ 调用方的请求级凭证被静默忽略；
// 取末个 ⇒ 客户端配置的凭证被静默覆盖；部分网关直接 400。
func TestRoundTripper_SingleAuthorizationHeader(t *testing.T) {
	url, gotCh := authProbe(t)

	cli := StdClient("svc", WithBearerToken("client-token"))
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url+"/x", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer caller-token")

	resp, err := cli.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	_ = resp.Body.Close()

	got := <-gotCh
	if len(got) != 1 {
		t.Fatalf("发出 %d 个 Authorization 头（期望 1 个）：%v", len(got), got)
	}
	if got[0] != "Bearer caller-token" {
		t.Fatalf("Authorization = %q，期望调用方自带的值（调用方显式设置时优先）", got[0])
	}
}

// TestRoundTripper_ClientCredentialUsedWhenCallerHasNone 回归护栏：调用方
// **没有**带 Authorization 时，客户端级凭证仍要带上（既有行为不变）。
func TestRoundTripper_ClientCredentialUsedWhenCallerHasNone(t *testing.T) {
	url, gotCh := authProbe(t)

	cli := StdClient("svc", WithBearerToken("client-token"))
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url+"/x", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	resp, err := cli.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	_ = resp.Body.Close()

	got := <-gotCh
	if len(got) != 1 || got[0] != "Bearer client-token" {
		t.Fatalf("Authorization = %v，期望 [Bearer client-token]", got)
	}
}

// TestRoundTripper_OtherHeadersStillMultiValued 回归护栏：其余多值头仍按
// Add 透传（不能因为修了 Authorization 而把多值头压扁）。
func TestRoundTripper_OtherHeadersStillMultiValued(t *testing.T) {
	multiCh := make(chan []string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		multiCh <- r.Header.Values("X-Multi")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(srv.Close)

	cli := StdClient("svc", WithBearerToken("client-token"))
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/x", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Add("X-Multi", "a")
	req.Header.Add("X-Multi", "b")

	resp, err := cli.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	_ = resp.Body.Close()

	got := <-multiCh
	if len(got) != 2 {
		t.Fatalf("X-Multi 收到 %v，期望两个值（多值头应原样透传）", got)
	}
}
