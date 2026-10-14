# ekit-httpx

> HTTP 客户端 + 负载均衡——Hertz Client 高层封装：服务发现/熔断/重试/指标/OTel 透传/实例权重贯通。

本仓库是 [ekit](https://git.enjoye.top/enjoydream/ekit)（`git.enjoye.top/enjoydream/ekit` v0.34.0）
拆分出的独立 Go module，属于 **yi-nology** ekit 子项目矩阵（Round-2 深拆粒度）。

## 安装

```bash
go get github.com/yi-nology/ekit-httpx@v0.1.0
```

## 包清单

- \`httpx/\` — NewClient + WithNNS/WithCircuitBreaker/WithRetryConfig(指数退避+抖动)/WithMetrics/WithTracePropagation
- \`loadbalance/\` — 加权负载均衡策略

## 依赖关系

子项目矩阵与依赖方向（自底向上，19 仓）：

```
ekit-core            文本/数据工具 + governance/lifecycle/version      （零内部依赖）
ekit-concurrency     并发与流控原语                                     （零内部依赖）
ekit-logx            日志门面 + shutdown                                （零内部依赖）
ekit-istio           Istio Proxyless                                    （零内部依赖）
  ekit-metrics       Prometheus 门面                          → core
  ekit-tracing       OTel + traceid                           → logx
  ekit-registry      注册发现 + 配置                          → core, logx
    ekit-data        缓存抽象 + 定时任务                      → core, logx, concurrency
      ekit-gorm      SQL/GORM 全家                            → core, logx
      ekit-redis     Redis 全家                               → data, core, logx
    ekit-httpx       HTTP 客户端 + 负载均衡                   → core, concurrency, logx, metrics, tracing, registry
      ekit-nns       服务治理客户端                           → httpx, registry, core, logx, tracing, metrics
        ekit-bootstrap 应用引导
    ekit-middleware  Hertz 中间件栈                           → core, concurrency, logx, metrics, tracing, registry
    ekit-hertz       Hertz 运行时/适配/instrumentation        → middleware, gorm, nns, registry, metrics, tracing, logx, core
    ekit-kitex       Kitex 生态                               → nns, registry, core, concurrency, logx, metrics
  ekit-workflow      流程编排                                 → concurrency, logx
```

> 注：`ekit-observability` 已在 Round-2 深拆中解散——logx 迁 [ekit-logx]、metrics 迁 [ekit-metrics]、
> tracing/traceid 迁 [ekit-tracing]、shutdown 迁 [ekit-logx]；原仓库已归档。

依赖：[ekit-core](https://github.com/yi-nology/ekit-core)、[ekit-concurrency](https://github.com/yi-nology/ekit-concurrency)、[ekit-logx](https://github.com/yi-nology/ekit-logx)、[ekit-metrics](https://github.com/yi-nology/ekit-metrics)、[ekit-tracing](https://github.com/yi-nology/ekit-tracing)、[ekit-registry](https://github.com/yi-nology/ekit-registry)。矩阵中最重的网络仓——只要轻量 WebSocket/NATS 请用 [ekit-network](https://github.com/yi-nology/ekit-network)。

## 与 ekit 单体仓库的关系

- 代码与 ekit v0.34.0 等价，module 路径 `git.enjoye.top/enjoydream/ekit/...` → `github.com/yi-nology/ekit-httpx/...`
- 各子仓库独立发版（SemVer）；开发规范沿用 ekit AGENTS.md 约定

## License

见 [LICENSE](LICENSE)
