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
//
// # 快速接入
//
// 创建客户端：
//
//	client := ehttp.NewClient("user-service",
//	    ehttp.WithBaseURL("http://user-service:8080"),
//	    ehttp.WithTimeout(5*time.Second),
//	    ehttp.WithTracePropagation(),
//	)
//
// GET 请求：
//
//	var users []User
//	err := client.GetJSON(ctx, "/api/users", &users)
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
//	client := ehttp.NewClient("user-service",
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
