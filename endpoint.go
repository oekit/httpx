package httpx

import (
	"context"
	"time"

	"github.com/oekit/httpx/loadbalance"
	pkgerrors "github.com/oekit/pkg/errors"
	"github.com/oekit/pkg/pathx"
)

func (c *Client) newEndpointManager(resolver ServiceResolver, defaultInterval time.Duration) *EndpointManager {
	interval := defaultInterval
	if c.pendingRefreshInterval > 0 {
		interval = c.pendingRefreshInterval
	}
	return NewEndpointManager(resolver, c.pendingSelector, interval)
}

// joinEndpoint 把 endpoint/baseURL 与请求 path 拼成完整 URL，拼接前归一
// 分隔符：base 去尾 "/"、path 补前导 "/"（#112）。修复前直接相加——path 缺
// 前导 "/" 时 host 被吃进 path（baseURL="http://h:8080" + "api/x" ⇒
// "http://h:8080api/x"，拨号报误导性的 unknown port）；静态端点同分支同修。
//
// 拼接规则已单源下沉 pathx.JoinURL（#532，#112 语义一致），此处薄委托；
// #112 的回归测试（TestJoinEndpoint）与调用点仍锚定本名称。path 为空时
// JoinURL 返回 base 原样（原实现会补尾 "/"）——空请求 URI 在 HTTP 层本就
// 归一为 "/"，线上请求形态不变。
func joinEndpoint(base, path string) string {
	return pathx.JoinURL(base, path)
}

// resolveEndpoint 解析请求目标地址，返回完整地址前缀、负载均衡结果回写句柄
// 与解析错误。done 需在请求最终结果确定后以最终错误调用（nil 表示无需回写）。
//
// 选点失败（典型 loadbalance.ErrNoAvailable：无可用节点、注册中心为空、
// 过滤后无候选）的边界（#276）：
//   - 配了 baseURL：回退 baseURL 直连，保持历史降级行为（done 为 nil）；
//   - 没配 baseURL：返回包装后的选点错误（errors.Is 可判定
//     loadbalance.ErrNoAvailable）。此前返回相对 path，scheme 为空在传输层
//     报 unsupported protocol ""——把「无实例」掩盖成协议错误，调用方无法
//     做无实例降级/告警，errors.Is(ErrNoAvailable) 永不成立。
func (c *Client) resolveEndpoint(ctx context.Context, path string) (string, loadbalance.DoneFunc, error) {
	if c.endpointManager == nil {
		if c.baseURL != "" {
			return joinEndpoint(c.baseURL, path), nil, nil
		}
		return path, nil, nil
	}

	endpoint, done, err := c.endpointManager.Pick(ctx, path)
	if err != nil || endpoint == "" {
		if c.baseURL != "" {
			// 选点失败（无可用节点）：回退 baseURL 直连，保持历史降级行为
			return joinEndpoint(c.baseURL, path), nil, nil
		}
		if err == nil {
			// 形如「空地址 + nil error」的异常选点同样归为无可用节点，
			// 不让空地址流到传输层变成误导性的协议错误
			err = loadbalance.ErrNoAvailable
		}
		return "", nil, pkgerrors.Wrapf(err, "httpx: %s: resolve endpoint %q (no baseURL to fall back to)", c.service, path)
	}
	return joinEndpoint(endpoint, path), done, nil
}
