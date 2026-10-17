package httpx

import (
	"context"
)

type contextKey string

const priorityKey contextKey = "httpx_priority"

// WithPriority 将优先级注入 context。
func WithPriority(ctx context.Context, priority Priority) context.Context {
	return context.WithValue(ctx, priorityKey, priority)
}

// GetPriority 从 context 获取优先级。
func GetPriority(ctx context.Context) Priority {
	if ctx == nil {
		return PriorityNormal
	}
	if p, ok := ctx.Value(priorityKey).(Priority); ok {
		return p
	}
	return PriorityNormal
}
