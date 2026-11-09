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
	"github.com/cloudwego/hertz/pkg/network/dialer"
	"github.com/cloudwego/hertz/pkg/network/standard"
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

// WithTimeout 设置 HTTP 请求超时预算，同时覆盖**拨号**与**响应读取**两个
// 阶段（#90/#92）：`WithTimeout(3s)` 对不可达主机（黑洞路由/半开连接）在
// 拨号阶段即 3s 超时——修复前仅设读超时，拨号沿用 hertz 默认 30s，调用方
// 以为的 3s 实际可等 33s。
//
// 语义边界（超时语义族，另见 #87 的流式说明）：
//   - 覆盖面 = DialTimeout + ReadTimeout，各以 d 为独立上限（不是串行叠加
//     的总预算；与标准库 http.Client{Timeout} 的全生命周期覆盖不同——
//     TLS 握手计入拨号阶段，写超时不单独设，可经逃生舱
//     WithHertzOption(client.WithWriteTimeout(d)) 补充）；
//   - 仅想调读超时、保留 30s 拨号上限：WithHertzOption(client.WithClientReadTimeout(d))；
//   - 仅想调拨号：WithHertzOption(client.WithDialTimeout(d))；
//   - 流式响应（SSE/代理长连接/WS 升级）不能用整段读超时掐流，用 WithStream。
func WithTimeout(d time.Duration) ClientOption {
	return func(c *Client) {
		c.hzOpts = append(c.hzOpts,
			client.WithDialTimeout(d),
			client.WithClientReadTimeout(d),
		)
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

// WithHeaders 设置**每请求注入**的静态自定义头（#128），典型场景是静态
// API-Key（`WithHeaders(map[string]string{"X-API-Key": key})`）。
//
// 与 WithBearerToken/WithBasicAuth 同层：选项面已有的两个头注入选项是鉴权头
// 的特定形态，通用「静态自定义头」此前无一等选项，下游每个静态 API-Key 都要
// 手搓一个 WithOnBeforeRequest 闭包（3 处实证，其中 2 处纯静态）。
//
// 语义与边界：
//   - 实现为引擎预置的请求前置中间件（与 WithOnBeforeRequest 同一机制）：每个
//     请求对每个 k/v 执行 SetHeader——无条件 Set，覆盖请求上已设的同名值；
//   - 对全部请求路径生效（封装方法与裸 Hertz().Do 一致，走中间件链保证）；
//   - 用户可继续叠加自己的 OnBeforeRequest 闭包做动态签名/时间戳等：闭包注册
//     在静态头之后（内层）执行，同名键以闭包为准——静态值兜底、动态值覆盖；
//   - 多次调用 WithHeaders/WithHeader 按键合并、后写者赢（构造期语义，请求期
//     只读 map，并发安全）；
//   - 若声明了 Authorization，会覆盖 WithBearerToken/WithBasicAuth 注入的鉴权
//     头（静态头在 base 鉴权补齐之后执行 Set）——API-Key 形态的鉴权这正是
//     目标场景；Bearer/Basic 与 API-Key 混用属于罕见的显式声明，以静态头为准。
//
// 值应为常量（静态）：token 生命周期内轮换一次的场景在构造期重建 Client，
// 或经 WithOnBeforeRequest 闭包每请求现算。
func WithHeaders(headers map[string]string) ClientOption {
	return func(c *Client) {
		if len(headers) == 0 {
			return
		}
		if c.headers == nil {
			c.headers = make(map[string]string, len(headers))
		}
		for k, v := range headers {
			c.headers[k] = v
		}
	}
}

// WithHeader 设置单个静态自定义头（#128），等价 WithHeaders(map[string]string{key: value})。
// 多个头逐条声明时比拼 map 更顺手；成批声明用 WithHeaders。
func WithHeader(key, value string) ClientOption {
	return WithHeaders(map[string]string{key: value})
}

// WithCookieJar 为客户端设置 Cookie Jar。
// 通过 base 中间件在请求前注入 Cookie、响应后回收 Set-Cookie，
// 对全部请求路径生效（含裸 Hertz().Do）。
func WithCookieJar(jar http.CookieJar) ClientOption {
	return func(c *Client) { c.cookieJar = jar }
}

// WithStream 流式响应预设（#87）：关闭读超时（默认 30s 只适合短请求——
// 读超时与流式语义直接冲突，30s 硬顶会把任何长流掐死，症状是连接被远端/
// 代理静默切断、极难归因到客户端），保留拨号超时（建连阶段仍需兜底）。
//
// 适用面：SSE 转发、k8s proxy/长连接代理、WS 升级、chunked 大响应。
// 等价于 WithHertzOption(client.WithClientReadTimeout(0)) 的固化预设——
// 把「流式要关什么」的知识固化进库，不必每个消费方自己拼逃生舱选项。
//
// 语义与边界：
//   - 与 WithTimeout 按 option 顺序后者覆盖（都是 append 到 hzOpts）：
//     「预算拨号 + 不限读」写 WithTimeout(d), WithStream()；
//     「都不限」只写 WithStream()；
//   - 流的总时长预算由调用方经 ctx 控制（context.WithTimeout），引擎层
//     不替流式请求做整段超时；
//   - 引擎层 Client 的响应体是全量缓冲语义（GetJSON/DoRaw 返回时 body
//     已读尽）；真正的逐块消费经 net/http 桥接（StdClient + 本预设），
//     或直接用注入低阶 client 的 DoOnce。
func WithStream() ClientOption {
	return func(c *Client) {
		c.hzOpts = append(c.hzOpts, client.WithClientReadTimeout(0))
	}
}

// WithTLSSkipVerify 跳过 TLS 证书验证。
func WithTLSSkipVerify() ClientOption {
	return func(c *Client) {
		c.hzOpts = append(c.hzOpts, client.WithTLSConfig(&tls.Config{InsecureSkipVerify: true})) //nolint:gosec
	}
}

// WithDialFunc 注入自定义拨号函数（#74）。
// 典型用途是出站 SSRF 防护的拨号层拦截：在真正建连前校验 addr 解析出的
// IP 是否落在内网/黑名单（URL 层字面量检查挡不住 DNS rebinding）。
// dialers 可选，用于覆盖基础拨号器（缺省 DefaultDialer()）。
//
// 不再透传 Hertz 的 client.WithDialFunc：hertz 的 customDialer 在有 dialFunc 时
// **完全丢弃 tlsConfig**（pkg/app/client/option.go 的 DialConnection 直接
// `return m.dialFunc(address)`）——https 目标的 TLS 握手链路就此断裂，
// WithSafeDial 形态把明文请求静默打到 443 端口（症状是服务端 400，极难排障）。
// 本仓改为自行装配 TLS 感知拨号器 tlsAwareDialer：dialFunc 拨通裸连接后按
// hertz 传入的 tlsConfig 是否为 nil 决定是否补 TLS 握手（见 ssrf.go 实现说明）。
//
// 装配时机是 NewClient 的**末位兜底**（不再入 hzOpts）：hertz 的
// client.WithTLSConfig 会无条件覆盖 o.Dialer——WithTLSSkipVerify 排在本选项
// 之后时自定义拨号函数会被静默丢弃（SSRF 防护一并失效）。末位 append 保证
// WithDialFunc/WithSafeDial 的装配面赢。逃生舱 WithHertzOption 自带
// client.WithDialer 的例外：本选项未使用时不装配、不覆盖逃生舱的拨号器。
func WithDialFunc(fn network.DialFunc, dialers ...network.Dialer) ClientOption {
	return func(c *Client) {
		base := dialer.DefaultDialer()
		if len(dialers) > 0 && dialers[0] != nil {
			base = dialers[0]
		}
		c.dialer = &tlsAwareDialer{Dialer: base, fn: fn, tlsDialer: standard.NewDialer()}
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
					if code := resp.StatusCode(); code >= 500 {
						// 5xx 只计熔断失败统计，不外抛（#107）：标记错误仅在
						// Execute 内部存活，让 gobreaker（默认 IsSuccessful：
						// 非 nil 即失败；用户自定义 IsSuccessful 照常生效）把
						// 5xx 计入失败；出 Execute 后按「非 2xx 非错误」透传。
						return nil, &httpStatusError{code: code}
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
					// 5xx 标记错误：剥掉，响应原样交给上层——引擎层
					// handleResponse 转成携带状态码与响应体的 *ServiceError，
					// 桥接层按标准库语义返回 500 响应（#107）。修复前
					// fmt.Errorf("server error: %d") 直接外抛：响应体丢失、
					// errors.As(&ServiceError) 写法在开熔断后恒 false、
					// 桥接层「非 2xx 非错误」承诺被打破。
					var statusErr *httpStatusError
					if errors.As(execErr, &statusErr) {
						return nil
					}
					return execErr
				}
				return nil
			}
		})
	}
}

// httpStatusError 是熔断中间件内部的 5xx 标记错误（#107）：仅存活于
// breaker.Execute 内部，作用是把 5xx 计入熔断失败统计；Execute 返回后由
// 中间件剥掉（errors.As 判定），不外抛给调用方。
type httpStatusError struct{ code int }

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("server error: %d", e.code)
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
// ⚠️ 重试触发条件(与 Hertz 默认不同,带幂等方法门 #85):
//
//	Hertz 官方 DefaultRetryIf 仅在幂等方法 + 网络错误时重试,不重试 5xx。
//	ekit WithRetryConfig 默认重试「幂等方法」上的 5xx + network error——
//	比 Hertz 宽(补 5xx),比旧版「不看方法一律重试」严;幂等门只按方法判定,
//	无 body 的 POST 同样判非幂等(#129:副作用在方法+路径上,不在 payload 里)。
//	旧版默认对 POST 等**非幂等请求**的 5xx 无条件重放,网关恰好超时但
//	后端已实际写入时会重复下单/重复创建——「保持兼容」不等于「安全默认」。
//	需要放开旧语义(任何方法都重试)时,经 WithRetryIfFunc 显式声明。
//
// 官方依据:https://www.cloudwego.io/zh/docs/hertz/tutorials/basic-feature/client/
// (Client 配置表 WithRetryConfig 示例明确列出 retry.WithDelayPolicy;
// 退避/抖动语义见 hertz pkg/app/client/retry 包:BackOffDelayPolicy = Delay<<attempts,
// RandomDelayPolicy = fastrand.Int63n(MaxJitter),CombineDelay 求和)
//
// 用法:
//
//	cli, err := httpx.NewClient("my-svc",
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
		// 默认重试条件：幂等方法门 + (5xx | network error)（#85）。
		// RetryIfFunc 必须在 client.NewClient 之后通过 SetRetryIfFunc 设置,
		// 所以这里只存到 c.retryIf,NewClient 末尾统一应用。
		//
		// **只有用户没有显式设置过谓词时才注入默认谓词**（#41）：两个 option
		// 写同一个字段时，原本是「后写者赢」⇒ 把 WithRetryIfFunc 写在
		// WithRetryConfig 之前就会让用户谓词被**无声丢弃**，退回默认谓词。
		// 现在显式谓词永远赢，与 option 顺序无关。
		if c.retryIfExplicit {
			return
		}
		c.retryIf = defaultRetryIf
	}
}

// defaultRetryIf 是 WithRetryConfig 注入的默认重试条件（#85，#129 收口）：
//
//	幂等方法 && (network error | 5xx)
//
// 幂等门只按方法判定（#129：无 body 的 POST 同样判非幂等——副作用在方法+路径
// 上，不在 payload 里），对 network error 同样生效——POST 超时时后端可能已
// 收到并写入，重放与 5xx 重放同样是重复下单级风险。需要旧「不看方法」语义
// 或重放 POST 的调用方经 WithRetryIfFunc 显式放开。
func defaultRetryIf(req *protocol.Request, resp *protocol.Response, err error) bool {
	if req != nil && !idempotentForRetry(req) {
		return false
	}
	if err != nil {
		return true
	}
	return resp != nil && resp.StatusCode() >= 500
}

// idempotentForRetry 判定请求对默认重试是否安全：只按**方法**判定——幂等
// 方法（GET/HEAD/OPTIONS/PUT/DELETE/TRACE，对齐 hertz isIdempotent 的方法集）。
//
// #129：删除旧版「无 body 即幂等」兜底（`len(req.Body()) == 0`）。空 body 的
// POST 恰恰是副作用型接口最常见的形态——副作用在**方法 + 路径**上，不在
// payload 里（`POST /pay`、`POST /orders/{id}/cancel`、`POST /users/{id}/activate`…），
// 网关超时但后端已实际写入时，重放即重复下单/重复扣款/重复取消。旧兜底把
// 风险最高的一类 POST（空 body + 纯路径语义）留在了重放区，与 #85 的幂等门
// 意图直接冲突。
//
// 确实需要重放 POST 的调用方经 WithRetryIfFunc 显式声明（显式谓词永远赢，
// 与 option 顺序无关，#41）。
func idempotentForRetry(req *protocol.Request) bool {
	switch string(req.Method()) {
	case http.MethodGet, http.MethodHead, http.MethodOptions,
		http.MethodPut, http.MethodDelete, http.MethodTrace:
		return true
	}
	return false
}

// WithRetryIfFunc 覆盖 ekit 默认重试触发条件(幂等方法门 + 5xx +
// network error),由用户传自定义 predicate(更严格:只对特定方法/状态码;
// 更宽松:放开非幂等请求的重放)。
// 不调 WithRetryIfFunc 时,WithRetryConfig 注入默认条件。
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
//
// 与动态服务发现（WithNNS/WithServiceResolver）互斥（#89）：组合使用时
// 静态端点会被动态刷新循环在下一个刷新周期覆盖（或静态端点被静默丢弃），
// 行为随 option 顺序漂移且无告警——组合直接构造期报错（NewClient 返回
// error）。一个 Client 只能有一种端点来源；需要静态兜底 + 动态发现的
// 组合语义由 EndpointManager 的缓存降级提供（resolver 失败时保留快照）。
func WithEndpoints(endpoints ...string) ClientOption {
	return func(c *Client) {
		if c.endpointManager != nil && c.endpointManager.resolver != nil {
			c.setConstructErr(errors.New(
				"httpx: WithEndpoints 与动态服务发现（WithNNS/WithServiceResolver）互斥：静态端点会被刷新循环覆盖，一个 Client 只能有一种端点来源"))
			return
		}
		c.staticEndpoints = true
		if c.endpointManager == nil {
			c.endpointManager = c.newEndpointManager(nil, 0)
		}
		c.endpointManager.SetEndpoints(endpoints)
	}
}

// WithNNS 启用 NNS 服务发现。
func WithNNS(serviceName string, nnsClient NNSClient) ClientOption {
	return func(c *Client) {
		c.setDynamicResolver(NewNNSResolver(serviceName, nnsClient))
	}
}

// WithServiceResolver 设置自定义服务发现解析器。
func WithServiceResolver(resolver ServiceResolver) ClientOption {
	return func(c *Client) {
		c.setDynamicResolver(resolver)
	}
}

// setDynamicResolver 安装动态 resolver 并创建带刷新循环的 EndpointManager
// （#89）：
//   - 替换已存在的 EndpointManager 前先 Stop 旧实例——旧 manager 若带刷新
//     循环，其 refreshLoop goroutine/ticker 会一直跑到进程退出（组合 option
//     反复替换时逐个泄漏）；
//   - 与静态端点（WithEndpoints）互斥：动态发现会静默丢弃静态端点，
//     构造期报错（NewClient 返回 error），行为不再随 option 顺序漂移。
func (c *Client) setDynamicResolver(resolver ServiceResolver) {
	if c.staticEndpoints {
		c.setConstructErr(errors.New(
			"httpx: WithNNS/WithServiceResolver 与静态端点（WithEndpoints）互斥：一个 Client 只能有一种端点来源"))
		return
	}
	if c.endpointManager != nil {
		c.endpointManager.Stop()
	}
	c.endpointManager = c.newEndpointManager(resolver, 30*time.Second)
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
// capacity 是每级准入容量，必须 >= 1——0/负数在构造期 panic 并指明原因
// （0 会造成所有请求阻塞到 ctx 取消的死锁，见 NewPriorityQueue，#88）；
// 配置值缺省时请显式传 1。
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
