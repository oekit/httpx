package httpx

import (
	"context"

	"sync/atomic"
	"time"

	"github.com/cloudwego/hertz/pkg/app/client"
	"github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/oekit/httpx/loadbalance"
	"github.com/oekit/metrics/resilience"
)

// Client 是封装了 Hertz HTTP 客户端的高层 HTTP 调用器，支持服务发现、负载均衡、熔断和指标。
type Client struct {
	client  *client.Client
	service string
	// target 指标 label：SetTarget 文档化运行时更新、请求路径并发读，
	// 用 atomic.Pointer 避免 data race
	target                 atomic.Pointer[string]
	baseURL                string
	metrics                *clientMetrics
	bearerToken            string
	basicUser              string
	basicPass              string
	endpointManager        *EndpointManager
	priorityQueue          *PriorityQueue
	pendingSelector        loadbalance.Selector
	pendingRefreshInterval time.Duration
	hzOpts                 []config.ClientOption
	mw                     []client.Middleware
	lastMW                 []client.Middleware // 走 Hertz client.UseAsLast 注入(trace propagation 等)
	retryIf                func(req *protocol.Request, resp *protocol.Response, err error) bool
	hasBreaker             bool
}

// NewClient 创建 HTTP 客户端实例，应用指定的配置选项。
func NewClient(service string, opts ...ClientOption) *Client {
	c := &Client{
		service: service,
	}
	for _, opt := range opts {
		opt(c)
	}

	hzOpts := append([]config.ClientOption{
		client.WithDialTimeout(30 * time.Second),
		client.WithClientReadTimeout(30 * time.Second),
	}, c.hzOpts...)

	hc, err := client.NewClient(hzOpts...)
	if err != nil {
		panic("httpx: client.NewClient with defaults failed: " + err.Error())
	}
	c.client = hc

	if c.metrics != nil {
		c.setupMetricsMiddleware()
	}
	if c.retryIf != nil {
		userRetryIf := c.retryIf
		c.client.SetRetryIfFunc(func(req *protocol.Request, resp *protocol.Response, err error) bool {
			// 将发生重试时记一次弹性指标（闭包运行时 metrics 已装配，顺序无关）
			if ok := userRetryIf(req, resp, err); ok {
				if c.metrics != nil {
					c.metrics.rm.ObserveRetry(c.service, metricsKind)
				}
				return true
			}
			return false
		})
	}
	// 熔断 gauge 显式初始化为 closed，series 从创建即存在
	if c.metrics != nil && c.hasBreaker {
		c.metrics.rm.ObserveBreakerState(c.service, metricsKind, resilience.StateClosed)
	}
	if len(c.mw) > 0 {
		c.client.Use(c.mw...)
	}
	// lastMW 走 Hertz client.UseAsLast 注入,在所有 mw 之后执行。
	// UseAsLast 只接受一个 mw,超出报错(已注册过就忽略)。
	// 多于一个时,首个走 UseAsLast,剩余走 Use(在 Use 链尾提前)。
	if len(c.lastMW) > 0 {
		if err := c.client.UseAsLast(c.lastMW[0]); err != nil {
			httpLog.Warn("httpx: UseAsLast failed, falling back to Use",
				"err", err.Error())
			c.client.Use(c.lastMW[0])
		}
		for _, mw := range c.lastMW[1:] {
			c.client.Use(mw)
		}
	}
	return c
}

// Close 关闭客户端，释放资源。
func (c *Client) Close() {
	if c.endpointManager != nil {
		c.endpointManager.Stop()
	}
}

func (c *Client) targetLabel() string {
	if c.target.Load() == nil || *c.target.Load() == "" {
		return defaultTargetLabel
	}
	return *c.target.Load()
}

func (c *Client) setupMetricsMiddleware() {
	c.mw = append(c.mw, func(next client.Endpoint) client.Endpoint {
		return func(ctx context.Context, req *protocol.Request, resp *protocol.Response) error {
			method := string(req.Method())
			c.metrics.requestsInFlight.WithLabelValues(c.service, c.targetLabel(), method).Inc()
			start := time.Now()
			err := next(ctx, req, resp)
			statusCode := 0
			if err == nil {
				statusCode = resp.StatusCode()
			}
			c.metrics.recordRequestEnd(c.service, c.targetLabel(), method, statusCodeStr(statusCode), time.Since(start))
			return err
		}
	})
}

// Hertz 返回底层 Hertz Client 实例，用于直接操作。
func (c *Client) Hertz() *client.Client {
	return c.client
}
