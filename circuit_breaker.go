package httpx

import breaker "github.com/oekit/concurrency/breaker"

// ErrCircuitOpen 表示熔断器处于打开状态，请求被拒绝。
//
// 规范哨兵定义于 concurrency/breaker（错误消息逐字为 "circuit breaker is open"），
// httpx 此处仅别名它：gobreaker 熔断路径与 adaptive 熔断器（concurrency/breaker）
// 拒绝时返回同一 sentinel，errors.Is 全链统一判定。判定一律用 errors.Is。
var ErrCircuitOpen = breaker.ErrCircuitOpen
