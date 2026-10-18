package httpx

import (
	"time"

	"github.com/cloudwego/hertz/pkg/network"
	"github.com/cloudwego/hertz/pkg/network/dialer"
	"github.com/oekit/httpx/urlguard"
)

// SafeDialFunc 包装 network.DialFunc：建连前用 urlguard.GuardAddr 校验
// 解析出的 IP（含 DNS rebinding 场景），命中受保护网段则拒绝且不调用 next。
//
// 推荐与 WithDialFunc 组合：
//
//	c := httpx.NewClient("user-callback",
//	    httpx.WithDialFunc(httpx.SafeDialFunc(next)))
//
// next 为 nil 时使用 Hertz 默认 dialer。
func SafeDialFunc(next network.DialFunc) network.DialFunc {
	if next == nil {
		d := dialer.DefaultDialer()
		next = func(addr string) (network.Conn, error) {
			return d.DialConnection("tcp", addr, 30*time.Second, nil)
		}
	}
	return func(addr string) (network.Conn, error) {
		guarded, err := urlguard.GuardAddr(addr)
		if err != nil {
			return nil, err
		}
		return next(guarded)
	}
}

// WithSafeDial 一步启用出站 SSRF 防护（URL 层请另调 urlguard.IsBlockedURL）。
// 等价于 WithDialFunc(SafeDialFunc(nil))。
func WithSafeDial() ClientOption {
	return WithDialFunc(SafeDialFunc(nil))
}
