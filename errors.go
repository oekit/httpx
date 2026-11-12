package httpx

import (
	"errors"
	"fmt"
)

// 可分流的规范哨兵错误（#165）：此前两类可判定错误只有拼进文案的裸字符串，
// 调用方只能 strings.Contains 匹配；哨兵化后 errors.Is 单源判定。
var (
	// ErrPriorityUnavailable 表示请求被分级准入队列拒绝：队列已满且等待期间
	// ctx 取消/超时（guardPriority，入口见 request.go）。调用方以此分流限流
	// 拒绝——同一错误同时以 %w 携带 context 取消原因，errors.Is 与
	// context.Canceled/Cause 判定同样成立。
	ErrPriorityUnavailable = errors.New("request cancelled due to priority queue")

	// ErrUnknownCodec 表示使用了未注册的 codec 名称（DoCodec，#114）。运行期
	// 判定名称是否已注册用 codec.GetCodec(name) != nil；本哨兵用于对返回的
	// error 做 errors.Is 分流。
	ErrUnknownCodec = errors.New("unknown codec")
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
