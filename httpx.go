package httpx

import (
	"context"
	"fmt"
	"net/http"

	"sync/atomic"
	"time"

	"github.com/cloudwego/hertz/pkg/app/client"
	"github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/network"
	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/oekit/httpx/loadbalance"
	"github.com/oekit/logx"
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
	headers                map[string]string // WithHeaders/WithHeader 声明的静态自定义头（#128）
	cookieJar              http.CookieJar
	endpointManager        *EndpointManager
	priorityQueue          *PriorityQueue
	pendingSelector        loadbalance.Selector
	pendingRefreshInterval time.Duration
	hzOpts                 []config.ClientOption
	dialer                 network.Dialer // WithDialFunc 装配的拨号器，NewClient 末位兜底装配（#74）
	mw                     []client.Middleware
	lastMW                 []client.Middleware // 走 Hertz client.UseAsLast 注入(trace propagation 等)
	retryIf                func(req *protocol.Request, resp *protocol.Response, err error) bool
	retryIfExplicit        bool  // 用户是否显式设置过重试谓词（WithRetryIfFunc）
	hasBreaker             bool  // WithCircuitBreaker 是否装配（熔断 gauge 初始化用）
	staticEndpoints        bool  // WithEndpoints 是否装配过（与动态 resolver 互斥判定，#89）
	constructErr           error // 构造期配置冲突（首个生效），NewClient 汇总返回（#83）
}

// NewClient 创建 HTTP 客户端实例，应用指定的配置选项。
//
// 构造失败显式返回 error（#83）：与家族同级构造器契约一致（gormx.Open、
// core/config.LoadFile 均为 error 显式返回），出站 HTTP 客户端用在
// webhook/工具调用等场景，构造失败不应升级为进程死亡。失败来源：
//   - WithHertzOption 逃生舱透传的非法 Hertz client 配置项；
//   - 构造期配置冲突（如 WithEndpoints 与 WithNNS/WithServiceResolver 互斥，#89）。
//
// BREAKING（0.x minor 改签名，先例 gormx v0.9.0 参数序 breaking）：签名从
// `NewClient(...) *Client` 改为 `NewClient(...) (*Client, error)`。旧代码
//
//	c := httpx.NewClient("svc", ...)
//
// 迁移为
//
//	c, err := httpx.NewClient("svc", ...)
//	if err != nil {
//	    return err
//	}
//
// 确认构造参数静态合法、不愿逐处判错的调用方用 [MustNewClient]。
func NewClient(service string, opts ...ClientOption) (*Client, error) {
	c := &Client{
		service: service,
	}
	for _, opt := range opts {
		opt(c)
	}

	// 构造期冲突（首个生效）：显式返回错误。冲突发生前 option 可能已创建
	// 带刷新循环的 EndpointManager，先停掉再报错，不在错误路径泄漏 goroutine。
	if c.constructErr != nil {
		if c.endpointManager != nil {
			c.endpointManager.Stop()
		}
		return nil, c.constructErr
	}

	hzOpts := append([]config.ClientOption{
		client.WithDialTimeout(30 * time.Second),
		client.WithClientReadTimeout(30 * time.Second),
	}, c.hzOpts...)
	// 拨号器**末位兜底**装配（#74）：hertz 的 client.WithTLSConfig 会无条件
	// 覆盖 o.Dialer = standard.NewDialer()——WithTLSSkipVerify 之类的 TLS 选项
	// 一旦排在 WithDialFunc 之后，自定义拨号函数（含 WithSafeDial 的 SSRF 防护）
	// 就被静默丢弃。选项按序生效、末位覆盖，这里最后 append 保证装配面赢。
	if c.dialer != nil {
		hzOpts = append(hzOpts, client.WithDialer(c.dialer))
	}

	hc, err := client.NewClient(hzOpts...)
	if err != nil {
		return nil, fmt.Errorf("httpx: client.NewClient with defaults failed: %w", err)
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
	// base 中间件最先注册 = 最外层：相对 URI 补 baseURL、补 auth、
	// cookie jar 注入/回收，使裸 Hertz().Do 与封装方法行为一致
	c.client.Use(c.baseMiddleware())
	// 静态头中间件紧随 base（#128）：先补 URI/鉴权，再注入静态头；用户
	// OnBeforeRequest 闭包在其后（内层）注册 ⇒ 同名键以用户闭包为准（叠加语义）
	if len(c.headers) > 0 {
		c.client.Use(c.staticHeaderMiddleware())
	}
	if len(c.mw) > 0 {
		c.client.Use(c.mw...)
	}
	// lastMW 走 Hertz client.UseAsLast 注入,在所有 mw 之后执行。
	// UseAsLast 只接受一个 mw,超出报错(已注册过就忽略)。
	// 多于一个时,首个走 UseAsLast,剩余走 Use(在 Use 链尾提前)。
	if len(c.lastMW) > 0 {
		if err := c.client.UseAsLast(c.lastMW[0]); err != nil {
			// #180：字段名统一 logx.ErrKey（禁止手写 "err" 字面量），值形态
			// 统一 error 对象（与 resolver.go 两处一致，按字段做类型化解析的
			// 下游看到同一形态）
			httpLog.Warn("httpx: UseAsLast failed, falling back to Use",
				logx.ErrKey, err)
			c.client.Use(c.lastMW[0])
		}
		for _, mw := range c.lastMW[1:] {
			c.client.Use(mw)
		}
	}
	return c, nil
}

// MustNewClient 是 [NewClient] 的 panic 版本：构造失败直接 panic（携带
// NewClient 返回的完整错误信息）。仅用于构造参数静态合法的场景（硬编码
// 配置、初始化期组装）；配置来自外部输入时一律用 NewClient 显式判错。
func MustNewClient(service string, opts ...ClientOption) *Client {
	c, err := NewClient(service, opts...)
	if err != nil {
		panic(err)
	}
	return c
}

// setConstructErr 记录构造期配置冲突（首个错误生效，#83）：ClientOption
// 无法返回错误，冲突先落在 Client 上，NewClient 汇总显式返回。
func (c *Client) setConstructErr(err error) {
	if c.constructErr == nil {
		c.constructErr = err
	}
}

// Close 关闭客户端，释放资源：停止 EndpointManager 刷新循环，并关闭底层
// Hertz client 的空闲连接池（hostClient 连接与清理协程随之收敛，#84）。
//
// 说明：钉住的 hertz v0.10.6 *client.Client 没有 Close() error，可用的
// 释放口是 CloseIdleConnections()——关闭全部 hostClient 的空闲连接后，
// hertz 的连接清理协程按 ShouldRemove 逐个回收 hostClient 并退出。
// Close 幂等，可安全重复调用。
func (c *Client) Close() {
	if c.endpointManager != nil {
		c.endpointManager.Stop()
	}
	if c.client != nil {
		c.client.CloseIdleConnections()
	}
}

func (c *Client) targetLabel() string {
	// #203：单次 Load——原实现连做三次 atomic.Load（nil 检查、空串检查、
	// 取值），SetTarget 并发切换时三次读可取到不同值，返回值自相矛盾。
	p := c.target.Load()
	if p == nil || *p == "" {
		return defaultTargetLabel
	}
	return *p
}

func (c *Client) setupMetricsMiddleware() {
	c.mw = append(c.mw, func(next client.Endpoint) client.Endpoint {
		return func(ctx context.Context, req *protocol.Request, resp *protocol.Response) (err error) {
			method := string(req.Method())
			// #203：target label 单次读取、Inc/Dec 复用同一值——SetTarget 是
			// 文档化的运行时更新点，请求 in-flight 期间切换 target 时双读会让
			// Inc 落旧 series、Dec 落新 series：旧 series in-flight 永久 +1、
			// 新 series 被减到负数，面板与真实并发永久失真。
			target := c.targetLabel()
			c.metrics.requestsInFlight.WithLabelValues(c.service, target, method).Inc()
			start := time.Now()
			statusCode := 0
			// #203：回写 defer 配对——原直串在 next panic 时 recordRequestEnd
			// 不执行，同 label +1 永久泄漏且无任何信号；panic 路径 statusCode
			// 保持 0（与 err != nil 同口径），panic 继续原样上抛。
			defer func() {
				c.metrics.recordRequestEnd(c.service, target, method, statusCodeStr(statusCode), time.Since(start))
			}()
			err = next(ctx, req, resp)
			if err == nil {
				statusCode = resp.StatusCode()
			}
			return err
		}
	})
}

// Hertz 返回底层 Hertz Client 实例，用于直接操作。
func (c *Client) Hertz() *client.Client {
	return c.client
}
