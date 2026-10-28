package httpx

import (
	"context"
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
			cli := NewClient("svc", opts...)

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

// TestRetryIfFunc_DefaultStillRetriesNonIdempotent 回归护栏：没有设置显式
// 谓词时，默认谓词（5xx + network error，**不看方法**）的行为不变——
// 这是 WithRetryConfig 的既有口径，不能被本次修复悄悄改掉。
func TestRetryIfFunc_DefaultStillRetriesNonIdempotent(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	cli := NewClient("svc",
		WithEndpoints(srv.URL),
		WithRetryConfig(3, 10*time.Millisecond, 20*time.Millisecond, 0),
	)

	_, _, _ = cli.DoDetail(context.Background(), http.MethodPost, "/x", nil)
	if got := hits.Load(); got < 2 {
		t.Fatalf("默认谓词应重试 POST（既有口径），实际请求 %d 次", got)
	}
}

// TestRetryIfFunc_ExplicitPredicateIsUsed 回归护栏：显式谓词真的被用上了
// （谓词返回 true 时该重试就重试）——防止「显式谓词被忽略」换个方向复发。
func TestRetryIfFunc_ExplicitPredicateIsUsed(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	cli := NewClient("svc",
		WithEndpoints(srv.URL),
		WithRetryIfFunc(func(_ *protocol.Request, _ *protocol.Response, _ error) bool { return true }),
		WithRetryConfig(3, 10*time.Millisecond, 20*time.Millisecond, 0),
	)

	_, _, _ = cli.DoDetail(context.Background(), http.MethodPost, "/x", nil)
	if got := hits.Load(); got < 2 {
		t.Fatalf("显式谓词返回 true 应当触发重试，实际请求 %d 次", got)
	}
}
