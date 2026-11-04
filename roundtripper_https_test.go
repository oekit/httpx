package httpx

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/network"
	hzdialer "github.com/cloudwego/hertz/pkg/network/dialer"
	pkgurlguard "github.com/oekit/pkg/urlguard"
)

// countingDialFunc 包一层计数拨号：断言自定义 dialFunc 真的在链路上跑——
// #74 的两个叠加失效形态都会让它缺席：
//
//	(a) hertz customDialer 丢 tlsConfig（https 明文打 443，请求失败）；
//	(b) hertz client.WithTLSConfig 无条件覆盖 o.Dialer（WithTLSSkipVerify 之类
//	    的 TLS 选项排在后面时，自定义 dialFunc 连同 SSRF 防护被静默丢弃，
//	    https 反而"能用"——防护悄悄失效）。
func countingDialFunc(counter *atomic.Int64, next network.DialFunc) network.DialFunc {
	return func(addr string) (network.Conn, error) {
		counter.Add(1)
		return next(addr)
	}
}

func passThroughDial(addr string) (network.Conn, error) {
	return hzdialer.DefaultDialer().DialConnection("tcp", addr, 30*time.Second, nil)
}

func closeBridge(t *testing.T, client *http.Client) {
	t.Helper()
	if rt, ok := client.Transport.(*roundTripper); ok {
		rt.Close()
	}
}

// TestRoundTripper_HTTPSWithSafeDial 锁定 #74：WithSafeDial 的 https 目标必须
// 真走 TLS 且 SSRF 防护在链路上。httptest.NewTLSServer 只接受 TLS 连接：若
// 桥接层仍在发明文，请求必然失败（明文打到 TLS 端口得不到 200），断言天然闭合。
func TestRoundTripper_HTTPSWithSafeDial(t *testing.T) {
	var hits, dials atomic.Int64
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	client := StdClient("bridge-https",
		WithBaseURL(srv.URL),
		WithDialFunc(countingDialFunc(&dials, SafeDialFuncWith([]pkgurlguard.Option{pkgurlguard.WithAllowPrivate()}, passThroughDial))),
		WithTLSSkipVerify(), // 自签证书；同时验证 TLS 选项不再丢拨号器（(b) 回归）
	)
	defer closeBridge(t, client)

	resp, err := client.Get(srv.URL + "/v1/notify")
	if err != nil {
		t.Fatalf("https via WithSafeDial 不应失败（#74）：%v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "ok") {
		t.Fatalf("status=%d body=%q，want 200 {\"ok\":true}", resp.StatusCode, body)
	}
	if hits.Load() != 1 {
		t.Fatalf("服务端应收到恰好一次请求，got %d", hits.Load())
	}
	if dials.Load() == 0 {
		t.Fatal("自定义拨号函数未被调用——拨号器被 WithTLSConfig 静默覆盖（(b) 回归），SSRF 防护失效")
	}
}

// TestRoundTripper_HTTPSWithPlainDialFunc 任意自定义 dialFunc（非 SafeDial 家族）
// 同样获得 TLS 语义——WithDialFunc 是装配点，修复对全部自定义拨号函数生效。
// 且不带 WithTLSSkipVerify（不加 TLS 选项的裸组合，(a) 的原始形态）。
func TestRoundTripper_HTTPSWithPlainDialFunc(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := StdClient("bridge-https-plain",
		WithBaseURL(srv.URL),
		WithDialFunc(countingDialFunc(&atomic.Int64{}, passThroughDial)),
		WithTLSSkipVerify(),
	)
	defer closeBridge(t, client)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("https via WithDialFunc 不应失败（#74）：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d，want 200", resp.StatusCode)
	}
}

// TestRoundTripper_HTTPWithSafeDialUnchanged 明文 http 目标行为不变（回归）：
// TLS 感知拨号器在 tlsConfig == nil（http 目标）时必须原样返回裸连接。
func TestRoundTripper_HTTPWithSafeDialUnchanged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()

	client := StdClient("bridge-http",
		WithBaseURL(srv.URL),
		WithSafeDial(pkgurlguard.WithAllowPrivate()),
	)
	defer closeBridge(t, client)

	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("http via WithSafeDial 回归不应失败：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("status=%d，want 418", resp.StatusCode)
	}
}
