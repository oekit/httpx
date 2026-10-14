package httpx

import (
	"fmt"
)

// ServiceError 表示 HTTP 服务调用返回的业务错误。
type ServiceError struct {
	Service string
	Action  string
	Code    int
	Message string
}

// Error 实现 error 接口，返回格式化的错误信息。
func (e *ServiceError) Error() string {
	return fmt.Sprintf("%s: %s failed: %s (status %d)", e.Service, e.Action, e.Message, e.Code)
}
