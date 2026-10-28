package httpx

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/cloudwego/hertz/pkg/app/client"
	hertzretry "github.com/cloudwego/hertz/pkg/app/client/retry"
	"github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/network"
	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/oekit/httpx/loadbalance"
	"github.com/oekit/metrics/resilience"
	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/sony/gobreaker"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// 编译期断言：gobreaker.State 与 resilience.CircuitState 的数值必须一致。
//
// 钉子放在**消费侧**——谁改动 resilience 侧的数值，httpx 立刻编译红，
// 而不是运行期把熔断状态标签悄悄记反（熔断打开被记成 half_open）。
//
// 手法：数组长度必须为 0，差值非 0 即编译失败（正负都拦得住）。
// 不用 `uint8(差值)` 形态——它只在差值为**负**时报错，resilience 侧把 2
// 改成 3 会静默通过（uint8(1) 是合法常量），标签从此错位。
var (
	_ [0]struct{} = [int(resilience.CircuitStateClosed) - int(gobreaker.StateClosed)]struct{}{}
	_ [0]struct{} = [int(resilience.CircuitStateHalfOpen) - int(gobreaker.StateHalfOpen)]struct{}{}
	_ [0]struct{} = [int(resilience.CircuitStateOpen) - int(gobreaker.StateOpen)]struct{}{}
)

// ClientOption 是 Client 的配置函数类型。
type ClientOption func(*Client)

// WithBaseURL 设置客户端的基础 URL。
func WithBaseURL(u string) ClientOption {
	return func(c *Client) { c.baseURL = u }
}

// WithTimeout 设置 HTTP 请求超时时间。
func WithTimeout(d time.Duration) ClientOption {
	return func(c *Client) {
		c.hzOpts = append(c.hzOpts, client.WithClientReadTimeout(d))
	}
}

// WithBearerToken 为所有请求设置 Bearer Token 认证。
func WithBearerToken(token string) ClientOption {
	return func(c *Client) { c.bearerToken = token }
}

// WithBasicAuth 为所有请求设置 Basic Auth 认证。
func WithBasicAuth(user, password string) ClientOption {
	return func(c *Client) {
		c.basicUser = user
		c.basicPass = password
	}
}

// WithCookieJar 为客户端设置 Cookie Jar。
// Hertz client 不支持运行时设置 CookieJar；此选项记录警告并忽略。
func WithCookieJar(jar http.CookieJar) ClientOption {
	return func(_ *Client) {
		httpLog.Warn("WithCookieJar is not supported with Hertz client, ignored")
	}
}

// WithTLSSkipVerify 跳过 TLS 证书验证。
func WithTLSSkipVerify() ClientOption {
	return func(c *Client) {
		c.hzOpts = append(c.hzOpts, client.WithTLSConfig(&tls.Config{InsecureSkipVerify: true})) //nolint:gosec
	}
}

// WithDialFunc 注入自定义拨号函数，透传至 Hertz client.WithDialFunc。
// 典型用途是出站 SSRF 防护的拨号层拦截：在真正建连前校验 addr 解析出的
// IP 是否落在内网/黑名单（URL 层字面量检查挡不住 DNS rebinding）。
// dialers 可选，用于覆盖 Hertz 默认传输实现。
func WithDialFunc(fn network.DialFunc, dialers ...network.Dialer) ClientOption {
	return func(c *Client) {
		c.hzOpts = append(c.hzOpts, client.WithDialFunc(fn, dialers...))
	}
}

// WithHertzOption 透传任意 Hertz client 配置项（逃生舱）。
// 优先使用语义化选项（WithTimeout/WithDialFunc/WithRetryConfig 等）；
// 仅当某项 Hertz 能力尚未封装时使用，按传入顺序追加到
// client.NewClient 的选项列表尾部。
func WithHertzOption(opts ...config.ClientOption) ClientOption {
	return func(c *Client) {
		c.hzOpts = append(c.hzOpts, opts...)
	}
}

// WithDebug 开启调试日志。
// Hertz client 不支持运行时调试开关（日志由 hlog 全局控制），此选项留作占位。
func WithDebug() ClientOption {
	return func(_ *Client) {}
}

// WithTracePropagation 启用 OpenTelemetry 上下文透传(traceparent / tracestate / baggage),
// 走 Hertz client.UseAsLast 注入,在所有自定义中间件之后执行,
//
//	避免 trace 头被其他中间件覆盖或重复。
//
// propagator 通过 otel.GetTextMapPropagator() 读取(默认 W3C TraceContext + Baggage),
// 与 ekit observability/tracing 全局配置保持一致 — 调用方只需先初始化 tracer + propagator。
//
// 官方依据:https://www.cloudwego.io/zh/docs/hertz/tutorials/basic-feature/client/
// (UseAsLast 章节:"中间件设置在 ctx.lastMiddleware 中,使用 Use 设置的中间件链存放在
// ctx.mws 中,两者相对独立,只是在执行客户端中间件链的最后才执行 ctx.lastMiddleware")
func WithTracePropagation() ClientOption {
	return func(c *Client) {
		prop := otel.GetTextMapPropagator()
		c.lastMW = append(c.lastMW, func(next client.Endpoint) client.Endpoint {
			return func(ctx context.Context, req *protocol.Request, resp *protocol.Response) error {
				carrier := propagation.HeaderCarrier(make(http.Header))
				prop.Inject(ctx, carrier)
				for k, vs := range carrier {
					for _, v := range vs {
						req.Header.Set(k, v)
					}
				}
				return next(ctx, req, resp)
			}
		})
	}
}

// WithMetrics 为客户端启用 Prometheus 指标采集。
func WithMetrics(registry prom.Registerer, namespace string) ClientOption {
	return func(c *Client) {
		c.metrics = newClientMetrics(registry, namespace)
	}
}

// WithTarget 设置指标 target label 的值。
func WithTarget(target string) ClientOption {
	return func(c *Client) { c.target.Store(&target) }
}

// SetTarget 运行时更新 target label。
func (c *Client) SetTarget(target string) {
	c.target.Store(&target)
}

// WithCircuitBreaker 为客户端启用熔断器，使用指定的名称和配置。
func WithCircuitBreaker(name string, settings gobreaker.Settings) ClientOption {
	return func(c *Client) {
		userOnStateChange := settings.OnStateChange
		userReadyToTrip := settings.ReadyToTrip
		userIsSuccessful := settings.IsSuccessful

		breaker := gobreaker.NewCircuitBreaker(gobreaker.Settings{
			Name:         name,
			MaxRequests:  settings.MaxRequests,
			Interval:     settings.Interval,
			Timeout:      settings.Timeout,
			ReadyToTrip:  userReadyToTrip,
			IsSuccessful: userIsSuccessful,
			// 闭包捕获 c：Option 求值时 c.metrics 可能尚未创建
			// （WithMetrics 顺序无关），运行期读取已装配的指标
			OnStateChange: func(name string, from, to gobreaker.State) {
				httpLog.Warn("circuit breaker state change", "name", name, "from", from, "to", to)
				if c.metrics != nil {
					// 类型化传递：状态不再以裸 int 跨模块流转。转换安全性由本文件
					// 顶部的编译期钉子保证（gobreaker.State 与
					// resilience.CircuitState 数值必须一致，否则此处编译失败）。
					c.metrics.rm.ObserveTransitionTyped(c.service, metricsKind,
						resilience.CircuitState(from), resilience.CircuitState(to))
				}
				if userOnStateChange != nil {
					userOnStateChange(name, from, to)
				}
			},
		})

		c.hasBreaker = true
		c.mw = append(c.mw, func(next client.Endpoint) client.Endpoint {
			return func(ctx context.Context, req *protocol.Request, resp *protocol.Response) error {
				_, execErr := breaker.Execute(func() (any, error) {
					err := next(ctx, req, resp)
					if err != nil {
						return nil, err
					}
					if resp.StatusCode() >= 500 {
						return nil, fmt.Errorf("server error: %d", resp.StatusCode())
					}
					return nil, nil
				})
				if execErr != nil {
					if errors.Is(execErr, gobreaker.ErrOpenState) {
						if c.metrics != nil {
							c.metrics.rm.ObserveRejected(c.service, metricsKind)
						}
						return ErrCircuitOpen
					}
					return execErr
				}
				return nil
			}
		})
	}
}

// WithRetryConfig 配置请求重试策略,走 Hertz 官方 client.WithRetryConfig。
// 参数语义:
//   - maxAttempts:总尝试次数(>=1,含首次请求);0 = 走 Hertz 默认 1 次
//   - initDelay:首次重试前的等待时间(指数退避基数,对应 retry.Config.Delay)
//   - maxDelay:重试延迟上限(避免退避无界增长,retry.Delay 内部封顶)
//   - maxJitter:随机抖动上限(避免雪崩,对应 retry.Config.MaxJitter)
//
// DelayPolicy 必须显式设置:hertz WithRetryConfig 的默认 DelayPolicy 是
// retry.DefaultDelayPolicy(恒返回 0,参见 hertz pkg/app/client/retry/retry.go),
// 不设置的话 initDelay/maxDelay/maxJitter 三个参数全部无效、重试零等待。
// ekit 固定注入 CombineDelay(BackOffDelayPolicy, RandomDelayPolicy):
// 每次重试延迟 = initDelay<<attempts + [0, maxJitter) 随机抖动,MaxDelay 封顶。
//
// ⚠️ 重试触发条件(与 Hertz 默认不同):
//
//	Hertz 官方 DefaultRetryIf 仅在网络错误时重试,不重试 5xx 响应(只检查
//	idempotent 方法 — 参见 vendor 源码 protocol/client/client.go:82)。
//	ekit WithRetryConfig 默认同时重试 5xx + network error,语义对齐旧
//	WithRetry(手写 middleware) 行为。如果需要更严格的条件(例如只重试
//	GET/HEAD/OPTIONS/TRACE/特定状态码),可通过 WithRetryIfFunc 覆盖。
//
// 官方依据:https://www.cloudwego.io/zh/docs/hertz/tutorials/basic-feature/client/
// (Client 配置表 WithRetryConfig 示例明确列出 retry.WithDelayPolicy;
// 退避/抖动语义见 hertz pkg/app/client/retry 包:BackOffDelayPolicy = Delay<<attempts,
// RandomDelayPolicy = fastrand.Int63n(MaxJitter),CombineDelay 求和)
//
// 用法:
//
//	cli := httpx.NewClient("my-svc",
//	    httpx.WithRetryConfig(3, 100*time.Millisecond, 1*time.Second, 50*time.Millisecond),
//	)
func WithRetryConfig(maxAttempts uint, initDelay, maxDelay, maxJitter time.Duration) ClientOption {
	return func(c *Client) {
		c.hzOpts = append(c.hzOpts, client.WithRetryConfig(
			hertzretry.WithMaxAttemptTimes(maxAttempts),
			hertzretry.WithInitDelay(initDelay),
			hertzretry.WithMaxDelay(maxDelay),
			hertzretry.WithMaxJitter(maxJitter),
			// 不传 DelayPolicy 时 hertz 默认 DefaultDelayPolicy 恒为 0,
			// 上面三个延迟参数会全部失效(2026-09-05 复审发现并修复)。
			hertzretry.WithDelayPolicy(hertzretry.CombineDelay(
				hertzretry.BackOffDelayPolicy,
				hertzretry.RandomDelayPolicy,
			)),
		))
		// 默认 5xx + network error 都重试;ekit 旧 WithRetry 行为兼容。
		// RetryIfFunc 必须在 client.NewClient 之后通过 SetRetryIfFunc 设置,
		// 所以这里只存到 c.retryIf,NewClient 末尾统一应用。
		//
		// **只有用户没有显式设置过谓词时才注入默认谓词**（#41）：两个 option
		// 写同一个字段时，原本是「后写者赢」⇒ 把 WithRetryIfFunc 写在
		// WithRetryConfig 之前就会让用户谓词被**无声丢弃**，退回这个**不看
		// HTTP 方法**的默认谓词 ⇒ POST 在 5xx 上被自动重放（重复下单/
		// 重复扣款级）。现在显式谓词永远赢，与 option 顺序无关。
		if c.retryIfExplicit {
			return
		}
		c.retryIf = func(_ *protocol.Request, resp *protocol.Response, err error) bool {
			if err != nil {
				return true
			}
			if resp != nil && resp.StatusCode() >= 500 {
				return true
			}
			return false
		}
	}
}

// WithRetryIfFunc 覆盖 ekit 默认重试触发条件(5xx + network error),
// 由用户传更严格的 predicate(例如只对 GET 重试、特定状态码范围等)。
// 不调 WithRetryIfFunc 时,WithRetryConfig 注入默认 5xx + network error 条件。
//
// **显式谓词永远赢，与 option 顺序无关**（#41）：即便本 option 写在
// WithRetryConfig 之前也不会被默认谓词覆盖 ⇒ 不会再出现「以为收窄了重试、
// 实际 POST 仍被重放」这种静默失效。
func WithRetryIfFunc(fn func(req *protocol.Request, resp *protocol.Response, err error) bool) ClientOption {
	return func(c *Client) {
		c.retryIf = fn
		c.retryIfExplicit = true
	}
}

// WithEndpoints 设置静态 endpoint 列表。
func WithEndpoints(endpoints ...string) ClientOption {
	return func(c *Client) {
		if c.endpointManager == nil {
			c.endpointManager = c.newEndpointManager(nil, 0)
		}
		c.endpointManager.SetEndpoints(endpoints)
	}
}

// WithNNS 启用 NNS 服务发现。
func WithNNS(serviceName string, nnsClient NNSClient) ClientOption {
	return func(c *Client) {
		resolver := NewNNSResolver(serviceName, nnsClient)
		c.endpointManager = c.newEndpointManager(resolver, 30*time.Second)
	}
}

// WithServiceResolver 设置自定义服务发现解析器。
func WithServiceResolver(resolver ServiceResolver) ClientOption {
	return func(c *Client) {
		c.endpointManager = c.newEndpointManager(resolver, 30*time.Second)
	}
}

// WithRefreshInterval 设置 endpoint 刷新间隔。
func WithRefreshInterval(d time.Duration) ClientOption {
	return func(c *Client) {
		c.pendingRefreshInterval = d
		if c.endpointManager == nil {
			return
		}
		old := c.endpointManager
		old.Stop()
		m := c.newEndpointManager(old.resolver, d)
		m.SetNodes(old.GetNodes())
		c.endpointManager = m
	}
}

// WithSelector 设置节点选择器（roundrobin/random/weighted/一致性哈希等，
// 经 loadbalance.NewSelector 或 NewConsistentHashSelector 构造）。
// v0.4.0 起替代 WithBalance。
func WithSelector(sel loadbalance.Selector) ClientOption {
	return func(c *Client) {
		if sel == nil {
			return
		}
		c.pendingSelector = sel
		if c.endpointManager != nil {
			c.endpointManager.selector = sel
		}
	}
}

// WithPriorityQueue 启用请求优先级队列。
func WithPriorityQueue(capacity int) ClientOption {
	return func(c *Client) {
		c.priorityQueue = NewPriorityQueue(capacity)
	}
}

// WithOnBeforeRequest 注册请求前置钩子。
// ⚠️ Breaking: 签名从 func(*req.Client, *req.Request) 改为
// func(ctx context.Context, req *protocol.Request)（hertz protocol 类型）。
func WithOnBeforeRequest(fn func(ctx context.Context, req *protocol.Request) error) ClientOption {
	return func(c *Client) {
		c.mw = append(c.mw, func(next client.Endpoint) client.Endpoint {
			return func(ctx context.Context, req *protocol.Request, resp *protocol.Response) error {
				if err := fn(ctx, req); err != nil {
					return err
				}
				return next(ctx, req, resp)
			}
		})
	}
}
