package httpx

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// TestRetryPolicy_Delay 钉住退避计算（#236/#234）：指数档位与 hertz
// BackOffDelayPolicy 同口径（initDelay<<attempts，首档 2×initDelay）、
// maxDelay>0 封顶 / 0 显式不封顶、溢出饱和不回绕、initDelay<=0 关闭退避，
// 以及 Retry-After 建议覆盖计算值、maxDelay 对建议同样封顶（#234）。
func TestRetryPolicy_Delay(t *testing.T) {
	cases := []struct {
		name     string
		p        retryPolicy
		attempts uint
		hint     time.Duration
		hasHint  bool
		want     time.Duration
	}{
		{
			name:     "首档 = 2×initDelay（与 hertz 同口径）",
			p:        retryPolicy{initDelay: 100 * time.Millisecond},
			attempts: 1,
			want:     200 * time.Millisecond,
		},
		{
			name:     "第 3 档 = 8×initDelay",
			p:        retryPolicy{initDelay: 100 * time.Millisecond},
			attempts: 3,
			want:     800 * time.Millisecond,
		},
		{
			name:     "initDelay<=0 关闭指数退避（hertz 同口径）",
			p:        retryPolicy{initDelay: 0},
			attempts: 5,
			want:     0,
		},
		{
			name:     "maxDelay>0 封顶",
			p:        retryPolicy{initDelay: time.Second, maxDelay: 10 * time.Second},
			attempts: 5, // 1s<<5 = 32s → 封到 10s
			want:     10 * time.Second,
		},
		{
			name:     "maxDelay=0 显式不封顶",
			p:        retryPolicy{initDelay: time.Second},
			attempts: 5,
			want:     32 * time.Second,
		},
		{
			name:     "指数溢出饱和为 MaxInt64（不回绕成负值）",
			p:        retryPolicy{initDelay: time.Duration(1) << 62},
			attempts: 3,
			want:     time.Duration(math.MaxInt64),
		},
		{
			name:     "负 maxJitter 不抖也不 panic",
			p:        retryPolicy{initDelay: 100 * time.Millisecond, maxJitter: -time.Second},
			attempts: 1,
			want:     200 * time.Millisecond,
		},
		{
			name:     "Retry-After 覆盖计算退避与抖动（#234）",
			p:        retryPolicy{initDelay: time.Second, maxJitter: 50 * time.Millisecond},
			attempts: 5, // 计算值 32s + 抖动，全部被 60s 建议覆盖
			hint:     60 * time.Second,
			hasHint:  true,
			want:     60 * time.Second,
		},
		{
			name:     "Retry-After: 0 = 立即重试（与「未携带」可区分）",
			p:        retryPolicy{initDelay: time.Second},
			attempts: 5,
			hint:     0,
			hasHint:  true,
			want:     0,
		},
		{
			name:     "建议同样受 maxDelay 封顶（#234）",
			p:        retryPolicy{initDelay: time.Second, maxDelay: 10 * time.Second},
			attempts: 5,
			hint:     60 * time.Second,
			hasHint:  true,
			want:     10 * time.Second,
		},
		{
			name:     "hasHint=false 时 hint 被忽略",
			p:        retryPolicy{initDelay: 100 * time.Millisecond},
			attempts: 1,
			hint:     60 * time.Second,
			hasHint:  false,
			want:     200 * time.Millisecond,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.delay(tc.attempts, tc.hint, tc.hasHint); got != tc.want {
				t.Fatalf("delay(%d, hint=%v, hasHint=%v) = %v, want %v",
					tc.attempts, tc.hint, tc.hasHint, got, tc.want)
			}
		})
	}
}

// TestRetryPolicy_DelayJitterBounds 抖动口径 [0, maxJitter)：实际等待落在
// [计算值, 计算值+maxJitter)，且 0 抖动时精确等于计算值。
func TestRetryPolicy_DelayJitterBounds(t *testing.T) {
	p := retryPolicy{initDelay: 100 * time.Millisecond, maxJitter: 50 * time.Millisecond}
	lo, hi := 200*time.Millisecond, 250*time.Millisecond
	for i := 0; i < 500; i++ {
		got := p.delay(1, 0, false)
		if got < lo || got >= hi {
			t.Fatalf("delay(1) = %v, want [%v, %v)", got, lo, hi)
		}
	}
	// 抖动封顶同样作用于含抖动的结果
	capped := retryPolicy{initDelay: 100 * time.Millisecond, maxJitter: 50 * time.Millisecond, maxDelay: 210 * time.Millisecond}
	for i := 0; i < 500; i++ {
		if got := capped.delay(1, 0, false); got > 210*time.Millisecond {
			t.Fatalf("delay(1) = %v, want <= 210ms（maxDelay 封顶含抖动）", got)
		}
	}
}

// TestWaitRetry_Synctest 用 testing/synctest 假时钟钉住退避等待的时序（#236）：
// 等待恰好推进一个档位、ctx 已取消零等待、等待期取消按取消时刻返回——
// 全程不睡真时间。
func TestWaitRetry_Synctest(t *testing.T) {
	t.Run("等待满额", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			waitRetry(context.Background(), 5*time.Second)
			if got := time.Since(start); got != 5*time.Second {
				t.Fatalf("elapsed = %v, want 恰好 5s（假时钟推进一个等待档）", got)
			}
		})
	})

	t.Run("ctx 已取消则零等待", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			start := time.Now()
			waitRetry(ctx, time.Hour)
			if got := time.Since(start); got != 0 {
				t.Fatalf("elapsed = %v, want 0（ctx 已取消不得进入等待）", got)
			}
		})
	})

	t.Run("等待期取消立即返回", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() {
				time.Sleep(100 * time.Millisecond)
				cancel()
			}()
			start := time.Now()
			waitRetry(ctx, time.Hour) // 档位远大于取消时刻：不被取消就要等 1h
			if got := time.Since(start); got != 100*time.Millisecond {
				t.Fatalf("elapsed = %v, want 100ms（退避期取消必须立即生效）", got)
			}
		})
	})
}

// TestWithRetryConfig_ConstructValidation 构造期校验（#236）：maxAttempts 超上限
// 与负 maxDelay 都要 NewClient 显式报错（不静默放行），合法边界照常通过。
func TestWithRetryConfig_ConstructValidation(t *testing.T) {
	t.Run("maxAttempts 超上限报错", func(t *testing.T) {
		_, err := NewClient("too-many", WithRetryConfig(maxRetryAttempts+1, time.Millisecond, time.Second, 0))
		if err == nil {
			t.Fatalf("maxAttempts=%d 应构造期报错", maxRetryAttempts+1)
		}
		if !strings.Contains(err.Error(), "maxAttempts") {
			t.Errorf("err = %v, want 提及 maxAttempts", err)
		}
	})

	t.Run("负 maxDelay 报错", func(t *testing.T) {
		_, err := NewClient("neg-delay", WithRetryConfig(3, time.Millisecond, -time.Second, 0))
		if err == nil {
			t.Fatal("maxDelay<0 应构造期报错")
		}
		if !strings.Contains(err.Error(), "maxDelay") {
			t.Errorf("err = %v, want 提及 maxDelay", err)
		}
	})

	t.Run("合法边界通过", func(t *testing.T) {
		for _, opts := range [][]ClientOption{
			{WithRetryConfig(maxRetryAttempts, time.Millisecond, time.Second, time.Millisecond)},
			{WithRetryConfig(0, 0, 0, 0)},                // 0/0/0：不重试、不退避、不封顶——均合法
			{WithRetryConfig(3, time.Millisecond, 0, 0)}, // maxDelay=0 显式不封顶
		} {
			c, err := NewClient("valid", opts...)
			if err != nil {
				t.Fatalf("合法参数构造失败: %v", err)
			}
			c.Close()
		}
	})
}

// TestRetry_BackoffInterruptibleByContext 端到端（#236）：退避期间 ctx 取消
// 必须立即生效——handler 在首个响应里取消 ctx，随后的退避档位是 2s
// （initDelay=1s，首档 2×），修复前 hertz 裸 time.Sleep 会睡满 2s，
// 修复后立即以 context.Canceled 收尾。
func TestRetry_BackoffInterruptibleByContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var hits atomic.Int32
	// handler 在写 500 之前取消 ctx：客户端拿到响应进入退避时 ctx 必已取消
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		cancel()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	c := MustNewClient("ctx-cancel-backoff",
		WithBaseURL(srv.URL),
		WithRetryConfig(3, time.Second, 10*time.Second, 0),
	)
	t.Cleanup(c.Close)

	start := time.Now()
	err := c.GetJSON(ctx, "/x", nil)
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want errors.Is(err, context.Canceled)", err)
	}
	if elapsed >= time.Second {
		t.Fatalf("elapsed = %v, want < 1s（退避 2s 档必须被 ctx 取消打断，#236）", elapsed)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("handler hit = %d, want 1（取消后不得再发请求）", got)
	}
}
