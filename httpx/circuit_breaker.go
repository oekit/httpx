package httpx

import "errors"

// ErrCircuitOpen 表示熔断器处于打开状态，请求被拒绝。
var ErrCircuitOpen = errors.New("circuit breaker is open")
