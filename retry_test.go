package httpx

import (
	"testing"
	"time"

	"github.com/sony/gobreaker"
)

// TestWithRetryConfig_AppendsHertzOption 验证 WithRetryConfig 把
// client.WithRetryConfig(...) 添加到 c.hzOpts——次数上限仍交 hertz 的重试循环
// （HostClient.Do），退避等待自 #236 起由 httpx 层执行（retryPolicy 非 nil）。
func TestWithRetryConfig_AppendsHertzOption(t *testing.T) {
	c := MustNewClient("test",
		WithRetryConfig(3, 100*time.Millisecond, 1*time.Second, 50*time.Millisecond),
	)
	if c == nil {
		t.Fatal("NewClient returned nil")
	}
	// c.hzOpts 长度应 >= 1(WithRetryConfig 至少加 1 个 RetryConfig option)
	if len(c.hzOpts) == 0 {
		t.Fatal("expected hzOpts to include WithRetryConfig, got empty")
	}
	// 退避参数快照落在 retryPolicy（#236：等待在 httpx 层算、可被 ctx 打断）
	if c.retryPolicy == nil {
		t.Fatal("expected retryPolicy snapshot from WithRetryConfig, got nil")
	}
	if c.retryIf == nil {
		t.Fatal("expected default retry predicate injected by WithRetryConfig")
	}
}

// TestWithRetryConfig_ZeroMaxAttempts 验证 maxAttempts=0 走 Hertz 默认
// (不 panic,client.NewClient 成功)。
func TestWithRetryConfig_ZeroMaxAttempts(t *testing.T) {
	c := MustNewClient("test",
		WithRetryConfig(0, 0, 0, 0),
	)
	if c == nil {
		t.Fatal("NewClient with 0 attempts returned nil")
	}
}

// TestWithRetryConfig_CombinedWithCircuitBreaker 验证 WithRetryConfig 跟
// WithCircuitBreaker 共存(场景:重试 + 熔断,跟生产用法一致)。
func TestWithRetryConfig_CombinedWithCircuitBreaker(t *testing.T) {
	c := MustNewClient("test",
		WithRetryConfig(3, 100*time.Millisecond, 1*time.Second, 50*time.Millisecond),
		WithCircuitBreaker("test-cb", gobreaker.Settings{Name: "test", Timeout: 1 * time.Second}),
	)
	if c == nil {
		t.Fatal("NewClient with retry+breaker returned nil")
	}
	// 次数上限走 hertz(进 hzOpts,不进 c.mw),退避等待走 httpx 层(#236),
	// breaker 走手写 middleware(进 c.mw)
	if len(c.mw) == 0 {
		t.Error("expected CircuitBreaker middleware in c.mw")
	}
	if len(c.hzOpts) == 0 {
		t.Error("expected WithRetryConfig in c.hzOpts")
	}
}
