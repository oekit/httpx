// Package httpx 提供 JSON HTTP 客户端。
//
// 所有 enjoydream 后端服务共享此包，统一 HTTP 调用方式。
//
// # 核心特性
//
//   - JSON 请求/响应自动序列化
//   - Prometheus 指标采集
//   - 熔断器（gobreaker）
//   - 指数退避重试
//   - OpenTelemetry 追踪
//   - 文件上传（multipart/form-data）
//   - DoDetail：status + 原始 body（2xx 也返回）
//   - net/http 桥接层：NewRoundTripper/StdClient——存量标准库调用方一行
//     Transport 即接入熔断/重试/指标/链路（重试缺省关、显式开）
//   - WithDialFunc/WithHertzOption：拨号层拦截与 Hertz 原生选项透传（SSRF 防护）
//   - WithStream：流式响应预设（关读超时、保留拨号超时）
//   - DoOnce：client 可注入的低阶一次性请求原语（构造→发出→读尽→返回
//     status/body；无重试/熔断，弹性语义单点在引擎层）
//
// # 快速接入
//
// 创建客户端（构造失败显式返回 error，与 gormx.Open 等家族构造器一致，
// #83）：
//
//	client, err := ehttp.NewClient("user-service",
//	    ehttp.WithBaseURL("http://user-service:8080"),
//	    ehttp.WithTimeout(5*time.Second),
//	    ehttp.WithTracePropagation(),
//	)
//	if err != nil {
//	    return err
//	}
//
// 构造参数静态合法时可用 MustNewClient 一行接入。
//
// GET 请求：
//
//	var users []User
//	err = client.GetJSON(ctx, "/api/users", &users)
//
// POST 请求：
//
//	var result CreateUserResp
//	err := client.PostJSON(ctx, "/api/users", req, &result)
//
// 需要状态码与原始响应体时用 DoDetail（2xx 也返回 body；非 2xx 额外返回
// *ServiceError）：
//
//	status, body, err := client.DoDetail(ctx, http.MethodPost, "/api/users", req)
//
// # 流式响应与超时语义
//
// 默认配置注入 30s 拨号/读超时——**只适合短请求**。读超时是「响应必须
// 在 30s 内读完」的硬顶，对流式响应（SSE 转发、k8s proxy 长连接、WS
// 升级、chunked 大响应）会静默掐断长流（症状是连接被远端/代理切断，
// 极难归因到客户端读超时）。流式场景必须显式关读超时：
//
//	client, err := ehttp.NewClient("k8s-proxy",
//	    ehttp.WithStream(), // 关读超时 + 保留拨号超时（#87）
//	)
//
// 超时语义族：WithTimeout(d) 同时覆盖拨号与读取（各以 d 为独立上限，
// 非全生命周期总预算）；仅调读/拨号单项经 WithHertzOption
// (client.WithClientReadTimeout / WithDialTimeout)；写超时经
// WithHertzOption(client.WithWriteTimeout)。流式的总时长预算由调用方经
// ctx 控制。
//
// # 出站 SSRF 防护
//
// 调用方 URL 来自不受信输入时做两层检查：URL 层字面量拒绝内网 IP/主机名；
// 拨号层用 WithDialFunc 在建连前校验解析后的 IP（防 DNS rebinding）。
// 详见 README「出站 SSRF 防护套路」。
//
// # 监控集成
//
// 使用 WithMetrics 启用 Prometheus 指标：
//
//	client, err := ehttp.NewClient("user-service",
//	    ehttp.WithMetrics(metrics.Registry, "myservice"),
//	)
//
// 指标包括（namespace 未显式指定时前缀为 http_client）：
//   - {ns}_requests_total{service, target, method, status_code}
//   - {ns}_request_duration_seconds{service, target, method}
//   - {ns}_requests_in_flight{service, target, method}
//
// 同一 Registry + namespace 下创建多个 Client 是安全的：同名指标自动复用
// 已注册的 collector。禁止 host/IP/port/path 等高基数 label。
//
// # 熔断器
//
// 使用 WithCircuitBreaker 启用熔断保护：
//
//	client := ehttp.NewClient("user-service",
//	    ehttp.WithCircuitBreaker("user-service", gobreaker.Settings{
//	        MaxRequests: 3,
//	        Interval:    10 * time.Second,
//	        Timeout:     30 * time.Second,
//	    }),
//	)
//
// 熔断器状态变化会自动记录到日志。
//
// # 错误处理
//
// 返回 ServiceError 类型，包含服务名、操作、状态码和响应体：
//
//	var svcErr *ehttp.ServiceError
//	if errors.As(err, &svcErr) {
//	    log.Printf("service: %s, action: %s, code: %d, message: %s",
//	        svcErr.Service, svcErr.Action, svcErr.Code, svcErr.Message)
//	}
package httpx
