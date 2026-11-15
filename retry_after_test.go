package httpx

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/protocol"
)

// TestRetry_DefaultPredicateStatusMatrix 钉住默认重试谓词的状态码面（#234）：
// 429 必须进入缺省可重试集合（修复前只判 >= 500，限流永不重试），4xx 拒收
// 照旧不重试——403/408 的缺省口径与矩阵单源 concurrency/retry/httpx.Retryable
// 一致（403 的头感知形态、408 需要的调用方经 WithRetryIfFunc 显式接）。
func TestRetry_DefaultPredicateStatusMatrix(t *testing.T) {
	cases := []struct {
		name     string
		code     int
		wantHits int32
	}{
		{name: "429 限流重试（#234）", code: http.StatusTooManyRequests, wantHits: 2},
		{name: "503 重试", code: http.StatusServiceUnavailable, wantHits: 2},
		{name: "500 重试", code: http.StatusInternalServerError, wantHits: 2},
		{name: "403 不重试（头感知形态经 WithRetryIfFunc 接）", code: http.StatusForbidden, wantHits: 1},
		{name: "408 不在缺省集合（Retryable 口径）", code: http.StatusRequestTimeout, wantHits: 1},
		{name: "400 不重试", code: http.StatusBadRequest, wantHits: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.WriteHeader(tc.code)
			}))
			t.Cleanup(srv.Close)

			c := MustNewClient("status-matrix",
				WithBaseURL(srv.URL),
				WithRetryConfig(2, time.Millisecond, 10*time.Millisecond, 0),
			)
			t.Cleanup(c.Close)

			_ = c.GetJSON(context.Background(), "/x", nil)
			if got := hits.Load(); got != tc.wantHits {
				t.Fatalf("hits = %d, want %d（code=%d）", got, tc.wantHits, tc.code)
			}
		})
	}
}

// TestRetry_429StillGatedByIdempotentMethod 钉住幂等门对 429 同样生效（#234）：
// 补 429 只放宽状态码面，POST 等非幂等请求依旧不自动重放——限流重放的副作用
// 风险与 5xx 重放同级。
func TestRetry_429StillGatedByIdempotentMethod(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)

	c := MustNewClient("429-post",
		WithBaseURL(srv.URL),
		WithRetryConfig(3, time.Millisecond, 10*time.Millisecond, 0),
	)
	t.Cleanup(c.Close)

	_ = c.PostJSON(context.Background(), "/x", map[string]any{"a": 1}, nil)
	if got := hits.Load(); got != 1 {
		t.Fatalf("hits = %d, want 1（POST 非幂等，429 不得自动重放）", got)
	}
}

// TestRetry_RetryAfterOverridesBackoff 端到端钉住 Retry-After 接线（#234）：
// 服务端建议必须真正从响应头读出并**覆盖**客户端计算退避。
//
//   - 建议 0（立即重试）：退避基数给到 300ms（首档 600ms），若建议被无视，
//     请求至少要等 600ms——本用例用 <500ms 反证覆盖生效；
//   - 建议 1s：退避基数压到 50ms（首档 100ms），若建议被无视，100ms 就会
//     发出第二次请求——本用例用 ≥900ms 反证服务端建议被采纳。
func TestRetry_RetryAfterOverridesBackoff(t *testing.T) {
	t.Run("建议 0 覆盖 600ms 退避", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if hits.Add(1) == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))
		t.Cleanup(srv.Close)

		c := MustNewClient("hint-zero",
			WithBaseURL(srv.URL),
			WithRetryConfig(3, 300*time.Millisecond, 10*time.Second, 0),
		)
		t.Cleanup(c.Close)

		var out map[string]any
		start := time.Now()
		if err := c.GetJSON(context.Background(), "/x", &out); err != nil {
			t.Fatalf("GetJSON: %v", err)
		}
		elapsed := time.Since(start)
		if got := hits.Load(); got != 2 {
			t.Fatalf("hits = %d, want 2（429 必须重试）", got)
		}
		if elapsed >= 500*time.Millisecond {
			t.Fatalf("elapsed = %v, want < 500ms（Retry-After: 0 必须覆盖 600ms 计算退避，#234）", elapsed)
		}
	})

	t.Run("建议 1s 覆盖 100ms 退避", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if hits.Add(1) == 1 {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))
		t.Cleanup(srv.Close)

		c := MustNewClient("hint-one-second",
			WithBaseURL(srv.URL),
			WithRetryConfig(3, 50*time.Millisecond, 10*time.Second, 0),
		)
		t.Cleanup(c.Close)

		var out map[string]any
		start := time.Now()
		if err := c.GetJSON(context.Background(), "/x", &out); err != nil {
			t.Fatalf("GetJSON: %v", err)
		}
		elapsed := time.Since(start)
		if got := hits.Load(); got != 2 {
			t.Fatalf("hits = %d, want 2", got)
		}
		if elapsed < 900*time.Millisecond {
			t.Fatalf("elapsed = %v, want ≥900ms（Retry-After: 1 必须按服务端建议等待，#234）", elapsed)
		}
	})
}

// TestRetryAfterHint_Parsing 钉住 Retry-After 解析面（#234）：delay-seconds 与
// HTTP-date 双形态、0 与「未携带」可区分、坏值回落计算退避——解析复用矩阵单源
// concurrency/retry/httpx.ParseRetryAfter，本测试钉的是 httpx 侧的接线口径。
func TestRetryAfterHint_Parsing(t *testing.T) {
	mk := func(v string) *protocol.Response {
		resp := protocol.AcquireResponse()
		t.Cleanup(func() { protocol.ReleaseResponse(resp) })
		resp.SetStatusCode(http.StatusTooManyRequests)
		resp.Header.Set("Retry-After", v)
		return resp
	}

	cases := []struct {
		name    string
		value   string
		wantOK  bool
		wantMin time.Duration
		wantMax time.Duration
	}{
		{name: "delay-seconds", value: "120", wantOK: true, wantMin: 120 * time.Second, wantMax: 120 * time.Second},
		{name: "0 = 立即重试", value: "0", wantOK: true, wantMin: 0, wantMax: 0},
		{name: "空串 = 未携带", value: "", wantOK: false},
		{name: "坏值 = 未携带", value: "soon", wantOK: false},
		{name: "负秒数 = 未携带", value: "-5", wantOK: false},
		{
			name:    "HTTP-date 未来时刻",
			value:   time.Now().UTC().Add(2 * time.Second).Format("Mon, 02 Jan 2006 15:04:05 GMT"),
			wantOK:  true,
			wantMin: 0,
			wantMax: 3 * time.Second,
		},
		{
			name:    "HTTP-date 已过时刻钳到 0",
			value:   time.Now().UTC().Add(-time.Hour).Format("Mon, 02 Jan 2006 15:04:05 GMT"),
			wantOK:  true,
			wantMin: 0,
			wantMax: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := retryAfterHint(mk(tc.value))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (got %v)", ok, tc.wantOK, got)
			}
			if !tc.wantOK {
				return
			}
			if got < tc.wantMin || got > tc.wantMax {
				t.Fatalf("hint = %v, want [%v, %v]", got, tc.wantMin, tc.wantMax)
			}
		})
	}

	t.Run("nil resp 不 panic", func(t *testing.T) {
		if _, ok := retryAfterHint(nil); ok {
			t.Fatal("nil resp 应返回 ok=false")
		}
	})
}

// TestRetry_WithoutRetryAfterFallsBackToBackoff 反面钉住回落路径（#234）：
// 没有 Retry-After 的 429 仍按客户端计算退避走（本用例把退避压到 10ms 级，
// 只断言「发生了等待」的下界不现实，改为断言重试次数与成功——退避下界由
// TestRetry_DelayPolicyActive 钉住）。
func TestRetry_WithoutRetryAfterFallsBackToBackoff(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests) // 无 Retry-After 头
			_, _ = fmt.Fprint(w, "slow down")
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	c := MustNewClient("no-hint",
		WithBaseURL(srv.URL),
		WithRetryConfig(3, 5*time.Millisecond, 50*time.Millisecond, 0),
	)
	t.Cleanup(c.Close)

	var out map[string]any
	if err := c.GetJSON(context.Background(), "/x", &out); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("hits = %d, want 2（无建议也要按计算退避重试）", got)
	}
}
