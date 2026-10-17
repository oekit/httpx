package httpx

import (
	"context"
	"strings"
	"time"

	"github.com/oekit/httpx/loadbalance"
)

func (c *Client) newEndpointManager(resolver ServiceResolver, defaultInterval time.Duration) *EndpointManager {
	interval := defaultInterval
	if c.pendingRefreshInterval > 0 {
		interval = c.pendingRefreshInterval
	}
	return NewEndpointManager(resolver, c.pendingSelector, interval)
}

// resolveEndpoint 解析请求目标地址，返回完整地址前缀与负载均衡结果回写句柄。
// done 需在请求最终结果确定后以最终错误调用（nil 表示无需回写）；
// 无 EndpointManager 或选点失败时回退 baseURL 直连（done 为 nil）。
func (c *Client) resolveEndpoint(ctx context.Context, path string) (string, loadbalance.DoneFunc) {
	if c.endpointManager == nil {
		if c.baseURL != "" {
			return strings.TrimRight(c.baseURL, "/") + path, nil
		}
		return path, nil
	}

	endpoint, done, err := c.endpointManager.Pick(ctx, path)
	if err != nil || endpoint == "" {
		// 选点失败（无可用节点）：回退 baseURL 直连，保持历史降级行为
		if c.baseURL != "" {
			return strings.TrimRight(c.baseURL, "/") + path, nil
		}
		return path, nil
	}
	return endpoint + path, done
}
