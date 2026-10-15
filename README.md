# httpx

> HTTP 客户端 + 负载均衡——Hertz Client 高层封装：服务发现/熔断/重试/指标/OTel 透传/实例权重贯通。

> 项目主页：**https://oekit.github.io/** · 名称由来：**O**pen **E**asy **Kit**（开源 · 易用 · 工具集合）

本仓库是 [ekit](https://git.enjoye.top/enjoydream/ekit)（`git.enjoye.top/enjoydream/ekit` v0.34.0）
拆分出的独立 Go module，属于 **oekit** oekit（Open Easy Kit）模块矩阵。

## 安装

```bash
go get github.com/oekit/httpx@v0.1.4
```

## 包清单

- \`httpx/\` — NewClient + WithNNS/WithCircuitBreaker/WithRetryConfig(指数退避+抖动)/WithMetrics/WithTracePropagation
- \`loadbalance/\` — 加权负载均衡策略

## 矩阵总览（23 活跃模块，自动生成）

| 模块 | 内容 | 矩阵内依赖 |
|---|---|---|
| [pkg](https://github.com/oekit/pkg) | 微工具集 16 合 1（errors/validator/stringx/slicex/enumx/convert/encoding/jsonrepair/httpjson/mathx/pathx/pagination/ptrx/timex/uidgen/cache） | — |
| [concurrency](https://github.com/oekit/concurrency) | 并发与流控原语（async/group/singleflight/progress/syncx/ratelimit/retry） | — |
| [logx](https://github.com/oekit/logx) | 统一日志门面（多后端可替换 + Sink + caller 归属）+ 优雅关停 | — |
| [core](https://github.com/oekit/core) | 应用基建件（governance 治理 schema / lifecycle 契约 / network IP 工具 / version） | — |
| [istio](https://github.com/oekit/istio) | Istio Proxyless——Kitex xDS 动态熔断 | — |
| [metrics](https://github.com/oekit/metrics) | Prometheus 门面 + 弹性指标族 | [core](https://github.com/oekit/core) |
| [tracing](https://github.com/oekit/tracing) | OpenTelemetry 追踪 + trace_id 真相源 | [logx](https://github.com/oekit/logx) |
| [registry](https://github.com/oekit/registry) | 注册发现核心 + Nacos 实现 + 配置加载 + sdbridge | [core](https://github.com/oekit/core)、[logx](https://github.com/oekit/logx) |
| [data](https://github.com/oekit/data) | 缓存检查器（TTL/failTTL/inflight 合并）+ LocalCache | [concurrency](https://github.com/oekit/concurrency)、[logx](https://github.com/oekit/logx) |
| [gorm](https://github.com/oekit/gorm) | SQL/GORM 全家——连接池增强、框架无关 CRUD Service、仓储 | [logx](https://github.com/oekit/logx)、[pkg](https://github.com/oekit/pkg) |
| [redis](https://github.com/oekit/redis) | Redis 全家——客户端封装、缓存后端、分布式锁 | [concurrency](https://github.com/oekit/concurrency)、[data](https://github.com/oekit/data)、[logx](https://github.com/oekit/logx) |
| [httpx](https://github.com/oekit/httpx) | HTTP 客户端 + 负载均衡——服务发现/熔断/重试/指标/OTel 透传 | [concurrency](https://github.com/oekit/concurrency)、[core](https://github.com/oekit/core)、[logx](https://github.com/oekit/logx)、[metrics](https://github.com/oekit/metrics)、[registry](https://github.com/oekit/registry)、[tracing](https://github.com/oekit/tracing) |
| [network](https://github.com/oekit/network) | 轻量网络客户端——WebSocket/NATS/MQ 抽象、连接与流工具、Octopus 推送 | [concurrency](https://github.com/oekit/concurrency)、[logx](https://github.com/oekit/logx)、[tracing](https://github.com/oekit/tracing) |
| [timertask](https://github.com/oekit/timertask) | 分片定时任务（FNV-1a、心跳续约） | [concurrency](https://github.com/oekit/concurrency)、[logx](https://github.com/oekit/logx) |
| [middleware](https://github.com/oekit/middleware) | Hertz 自研标准中间件栈 + contrib 桥接 + hmw | [concurrency](https://github.com/oekit/concurrency)、[core](https://github.com/oekit/core)、[logx](https://github.com/oekit/logx)、[metrics](https://github.com/oekit/metrics)、[pkg](https://github.com/oekit/pkg)、[registry](https://github.com/oekit/registry)、[tracing](https://github.com/oekit/tracing) |
| [nns](https://github.com/oekit/nns) | NNS 服务治理客户端——provider 无关发现/配置/实例缓存 | [concurrency](https://github.com/oekit/concurrency)、[core](https://github.com/oekit/core)、[logx](https://github.com/oekit/logx)、[registry](https://github.com/oekit/registry) |
| [nns-endpoint](https://github.com/oekit/nns-endpoint) | NNS 端点工厂——per-service 熔断、换实例重试、gRPC 池 | [concurrency](https://github.com/oekit/concurrency)、[core](https://github.com/oekit/core)、[httpx](https://github.com/oekit/httpx)、[logx](https://github.com/oekit/logx)、[metrics](https://github.com/oekit/metrics)、[nns](https://github.com/oekit/nns)、[registry](https://github.com/oekit/registry) |
| [bootstrap](https://github.com/oekit/bootstrap) | 应用生命周期引导——bootstrap.Run 骨架 + healthz | [concurrency](https://github.com/oekit/concurrency)、[core](https://github.com/oekit/core)、[logx](https://github.com/oekit/logx)、[metrics](https://github.com/oekit/metrics)、[nns](https://github.com/oekit/nns)、[registry](https://github.com/oekit/registry)、[tracing](https://github.com/oekit/tracing) |
| [handler](https://github.com/oekit/handler) | HTTP handler 层——通用 CRUD Handler、参数绑定、版本端点 | [concurrency](https://github.com/oekit/concurrency)、[core](https://github.com/oekit/core)、[gorm](https://github.com/oekit/gorm)、[logx](https://github.com/oekit/logx)、[metrics](https://github.com/oekit/metrics)、[middleware](https://github.com/oekit/middleware)、[pkg](https://github.com/oekit/pkg)、[registry](https://github.com/oekit/registry)、[tracing](https://github.com/oekit/tracing) |
| [hertz](https://github.com/oekit/hertz) | Hertz 运行时集成——日志桥接、生命周期、SSE/health/metrics、适配器、OTel | [bootstrap](https://github.com/oekit/bootstrap)、[concurrency](https://github.com/oekit/concurrency)、[core](https://github.com/oekit/core)、[logx](https://github.com/oekit/logx)、[metrics](https://github.com/oekit/metrics)、[nns](https://github.com/oekit/nns)、[registry](https://github.com/oekit/registry)、[tracing](https://github.com/oekit/tracing) |
| [kitex](https://github.com/oekit/kitex) | Kitex 生态——日志桥接、治理热更、生命周期、BizError 桥接、kitexotel | [bootstrap](https://github.com/oekit/bootstrap)、[concurrency](https://github.com/oekit/concurrency)、[core](https://github.com/oekit/core)、[logx](https://github.com/oekit/logx)、[metrics](https://github.com/oekit/metrics)、[nns](https://github.com/oekit/nns)、[pkg](https://github.com/oekit/pkg)、[registry](https://github.com/oekit/registry)、[tracing](https://github.com/oekit/tracing) |
| [workflow](https://github.com/oekit/workflow) | 组合式流程编排——串行/并行/异步/条件分支 + Hook AOP | [concurrency](https://github.com/oekit/concurrency)、[logx](https://github.com/oekit/logx) |
| [kitex-examples](https://github.com/oekit/kitex-examples) | Kitex echo 示例——client/server + IDL + 治理配置 | [bootstrap](https://github.com/oekit/bootstrap)、[concurrency](https://github.com/oekit/concurrency)、[core](https://github.com/oekit/core)、[kitex](https://github.com/oekit/kitex)、[logx](https://github.com/oekit/logx)、[metrics](https://github.com/oekit/metrics)、[nns](https://github.com/oekit/nns)、[registry](https://github.com/oekit/registry)、[tracing](https://github.com/oekit/tracing) |

> 依赖严格自底向上；微库已于 2026-10 合并为 [pkg](https://github.com/oekit/pkg)，旧微库仓已删除。
## 当前仓库依赖

详见上方矩阵表中本仓库行。

## 与 ekit 单体仓库的关系

- 代码与 ekit v0.34.0 等价，module 路径 `git.enjoye.top/enjoydream/ekit/...` → `github.com/oekit/httpx/...`
- 各子仓库独立发版（SemVer）；开发规范沿用 ekit AGENTS.md 约定

## License

见 [LICENSE](LICENSE)
