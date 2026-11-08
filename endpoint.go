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

// joinEndpoint 把 endpoint/baseURL 与请求 path 拼成完整 URL，拼接前归一
// 分隔符：base 去尾 "/"、path 补前导 "/"（#112）。修复前直接相加——path 缺
// 前导 "/" 时 host 被吃进 path（baseURL="http://h:8080" + "api/x" ⇒
// "http://h:8080api/x"，拨号报误导性的 unknown port）；静态端点同分支同修。
func joinEndpoint(base, path string) string {
	base = strings.TrimRight(base, "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}

// resolveEndpoint 解析请求目标地址，返回完整地址前缀与负载均衡结果回写句柄。
// done 需在请求最终结果确定后以最终错误调用（nil 表示无需回写）；
// 无 EndpointManager 或选点失败时回退 baseURL 直连（done 为 nil）。
func (c *Client) resolveEndpoint(ctx context.Context, path string) (string, loadbalance.DoneFunc) {
	if c.endpointManager == nil {
		if c.baseURL != "" {
			return joinEndpoint(c.baseURL, path), nil
		}
		return path, nil
	}

	endpoint, done, err := c.endpointManager.Pick(ctx, path)
	if err != nil || endpoint == "" {
		// 选点失败（无可用节点）：回退 baseURL 直连，保持历史降级行为
		if c.baseURL != "" {
			return joinEndpoint(c.baseURL, path), nil
		}
		return path, nil
	}
	return joinEndpoint(endpoint, path), done
}
