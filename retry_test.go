package httpx

import (
	"testing"
	"time"

	"github.com/sony/gobreaker"
)

// TestWithRetryConfig_AppendsHertzOption 验证 WithRetryConfig 把
// client.WithRetryConfig(...) 添加到 c.hzOpts,hertz 内部 retry 引擎
// 接管重试逻辑(不走我们手写 middleware)。
func TestWithRetryConfig_AppendsHertzOption(t *testing.T) {
	c := NewClient("test",
		WithRetryConfig(3, 100*time.Millisecond, 1*time.Second, 50*time.Millisecond),
	)
	if c == nil {
		t.Fatal("NewClient returned nil")
	}
	// c.hzOpts 长度应 >= 1(WithRetryConfig 至少加 1 个 RetryConfig option)
	if len(c.hzOpts) == 0 {
		t.Fatal("expected hzOpts to include WithRetryConfig, got empty")
	}
}

// TestWithRetryConfig_ZeroMaxAttempts 验证 maxAttempts=0 走 Hertz 默认
// (不 panic,client.NewClient 成功)。
func TestWithRetryConfig_ZeroMaxAttempts(t *testing.T) {
	c := NewClient("test",
		WithRetryConfig(0, 0, 0, 0),
	)
	if c == nil {
		t.Fatal("NewClient with 0 attempts returned nil")
	}
}

// TestWithRetryConfig_CombinedWithCircuitBreaker 验证 WithRetryConfig 跟
// WithCircuitBreaker 共存(场景:重试 + 熔断,跟生产用法一致)。
func TestWithRetryConfig_CombinedWithCircuitBreaker(t *testing.T) {
	c := NewClient("test",
		WithRetryConfig(3, 100*time.Millisecond, 1*time.Second, 50*time.Millisecond),
		WithCircuitBreaker("test-cb", gobreaker.Settings{Name: "test", Timeout: 1 * time.Second}),
	)
	if c == nil {
		t.Fatal("NewClient with retry+breaker returned nil")
	}
	// retry 走 hertz 内部(不进 c.mw),breaker 走手写 middleware(进 c.mw)
	if len(c.mw) == 0 {
		t.Error("expected CircuitBreaker middleware in c.mw")
	}
	if len(c.hzOpts) == 0 {
		t.Error("expected WithRetryConfig in c.hzOpts")
	}
}
