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

## 矩阵总览（20 活跃仓 + 19 微库，自动生成）

| 仓库 | 内容 | 矩阵内依赖 |
|---|---|---|
| [ekit-bootstrap](https://github.com/yi-nology/ekit-bootstrap) | （见 README） | [ekit-bootstrap](https://github.com/yi-nology/ekit-bootstrap), [ekit-concurrency](https://github.com/yi-nology/ekit-concurrency), [ekit-core](https://github.com/yi-nology/ekit-core), [ekit-logx](https://github.com/yi-nology/ekit-logx), [ekit-metrics](https://github.com/yi-nology/ekit-metrics), [ekit-nns](https://github.com/yi-nology/ekit-nns), [ekit-registry](https://github.com/yi-nology/ekit-registry), [ekit-tracing](https://github.com/yi-nology/ekit-tracing) |
| [ekit-cache](https://github.com/yi-nology/ekit-cache) | 进程内缓存微库——LRU O(1)、TOCTOU 修复的 LocalCache | — |
| [ekit-concurrency](https://github.com/yi-nology/ekit-concurrency) | （见 README） | [ekit-concurrency](https://github.com/yi-nology/ekit-concurrency) |
| [ekit-convert](https://github.com/yi-nology/ekit-convert) | 类型转换微库——字符串/数字/布尔/切片的宽容转换 | — |
| [ekit-core](https://github.com/yi-nology/ekit-core) | Go 后端基础工具库——应用基建件（governance 治理 schema / lifecycle 生命周期契约 / network IP 工具 / version） | [ekit-core](https://github.com/yi-nology/ekit-core) |
| [ekit-data](https://github.com/yi-nology/ekit-data) | （见 README） | [ekit-concurrency](https://github.com/yi-nology/ekit-concurrency), [ekit-logx](https://github.com/yi-nology/ekit-logx) |
| [ekit-encoding](https://github.com/yi-nology/ekit-encoding) | 编解码微库——JSON/msgpack 等序列化助手 | — |
| [ekit-enumx](https://github.com/yi-nology/ekit-enumx) | 枚举工具微库——确定性顺序的 All/Values/解析 | — |
| [ekit-errors](https://github.com/yi-nology/ekit-errors) | 错误处理微库——Wrap/Cause 链、BizError（业务码/HTTP 码双轨）、sentinel 错误 | — |
| [ekit-gorm](https://github.com/yi-nology/ekit-gorm) | （见 README） | [ekit-errors](https://github.com/yi-nology/ekit-errors), [ekit-gorm](https://github.com/yi-nology/ekit-gorm), [ekit-logx](https://github.com/yi-nology/ekit-logx), [ekit-pagination](https://github.com/yi-nology/ekit-pagination) |
| [ekit-handler](https://github.com/yi-nology/ekit-handler) | （见 README） | [ekit-core](https://github.com/yi-nology/ekit-core), [ekit-errors](https://github.com/yi-nology/ekit-errors), [ekit-gorm](https://github.com/yi-nology/ekit-gorm), [ekit-handler](https://github.com/yi-nology/ekit-handler), [ekit-middleware](https://github.com/yi-nology/ekit-middleware), [ekit-pagination](https://github.com/yi-nology/ekit-pagination), [ekit-tracing](https://github.com/yi-nology/ekit-tracing), [ekit-validator](https://github.com/yi-nology/ekit-validator) |
| [ekit-hertz](https://github.com/yi-nology/ekit-hertz) | （见 README） | [ekit-concurrency](https://github.com/yi-nology/ekit-concurrency), [ekit-core](https://github.com/yi-nology/ekit-core), [ekit-hertz](https://github.com/yi-nology/ekit-hertz), [ekit-logx](https://github.com/yi-nology/ekit-logx), [ekit-metrics](https://github.com/yi-nology/ekit-metrics), [ekit-nns](https://github.com/yi-nology/ekit-nns), [ekit-registry](https://github.com/yi-nology/ekit-registry), [ekit-tracing](https://github.com/yi-nology/ekit-tracing) |
| [ekit-httpjson](https://github.com/yi-nology/ekit-httpjson) | HTTP JSON 微库——DoJSON 带 retry 语义与 Retry-After 尊重 | [ekit-stringx](https://github.com/yi-nology/ekit-stringx) |
| [ekit-httpx](https://github.com/yi-nology/ekit-httpx) | （见 README） | [ekit-concurrency](https://github.com/yi-nology/ekit-concurrency), [ekit-httpx](https://github.com/yi-nology/ekit-httpx), [ekit-logx](https://github.com/yi-nology/ekit-logx), [ekit-metrics](https://github.com/yi-nology/ekit-metrics), [ekit-registry](https://github.com/yi-nology/ekit-registry) |
| [ekit-istio](https://github.com/yi-nology/ekit-istio) | （见 README） | — |
| [ekit-jsonrepair](https://github.com/yi-nology/ekit-jsonrepair) | JSON 修复微库——宽容修复链：截断/尾逗号/单引号/非标准值归一 | — |
| [ekit-kitex](https://github.com/yi-nology/ekit-kitex) | （见 README） | [ekit-concurrency](https://github.com/yi-nology/ekit-concurrency), [ekit-core](https://github.com/yi-nology/ekit-core), [ekit-errors](https://github.com/yi-nology/ekit-errors), [ekit-kitex](https://github.com/yi-nology/ekit-kitex), [ekit-logx](https://github.com/yi-nology/ekit-logx), [ekit-metrics](https://github.com/yi-nology/ekit-metrics), [ekit-nns](https://github.com/yi-nology/ekit-nns), [ekit-registry](https://github.com/yi-nology/ekit-registry), [ekit-tracing](https://github.com/yi-nology/ekit-tracing) |
| [ekit-kitex-examples](https://github.com/yi-nology/ekit-kitex-examples) | （见 README） | [ekit-bootstrap](https://github.com/yi-nology/ekit-bootstrap), [ekit-kitex](https://github.com/yi-nology/ekit-kitex), [ekit-kitex-examples](https://github.com/yi-nology/ekit-kitex-examples), [ekit-logx](https://github.com/yi-nology/ekit-logx), [ekit-registry](https://github.com/yi-nology/ekit-registry), [ekit-tracing](https://github.com/yi-nology/ekit-tracing) |
| [ekit-logx](https://github.com/yi-nology/ekit-logx) | （见 README） | [ekit-logx](https://github.com/yi-nology/ekit-logx) |
| [ekit-mathx](https://github.com/yi-nology/ekit-mathx) | 数学工具微库——安全随机、区间、平均值等评测统计助手 | — |
| [ekit-metrics](https://github.com/yi-nology/ekit-metrics) | （见 README） | [ekit-core](https://github.com/yi-nology/ekit-core), [ekit-metrics](https://github.com/yi-nology/ekit-metrics) |
| [ekit-middleware](https://github.com/yi-nology/ekit-middleware) | （见 README） | [ekit-concurrency](https://github.com/yi-nology/ekit-concurrency), [ekit-errors](https://github.com/yi-nology/ekit-errors), [ekit-logx](https://github.com/yi-nology/ekit-logx), [ekit-metrics](https://github.com/yi-nology/ekit-metrics), [ekit-middleware](https://github.com/yi-nology/ekit-middleware), [ekit-pathx](https://github.com/yi-nology/ekit-pathx), [ekit-registry](https://github.com/yi-nology/ekit-registry), [ekit-tracing](https://github.com/yi-nology/ekit-tracing) |
| [ekit-network](https://github.com/yi-nology/ekit-network) | （见 README） | [ekit-concurrency](https://github.com/yi-nology/ekit-concurrency), [ekit-logx](https://github.com/yi-nology/ekit-logx), [ekit-tracing](https://github.com/yi-nology/ekit-tracing) |
| [ekit-nns](https://github.com/yi-nology/ekit-nns) | （见 README） | [ekit-concurrency](https://github.com/yi-nology/ekit-concurrency), [ekit-core](https://github.com/yi-nology/ekit-core), [ekit-logx](https://github.com/yi-nology/ekit-logx), [ekit-nns](https://github.com/yi-nology/ekit-nns), [ekit-registry](https://github.com/yi-nology/ekit-registry) |
| [ekit-nns-endpoint](https://github.com/yi-nology/ekit-nns-endpoint) | NNS 端点工厂——per-service 熔断（4xx 不计失败）、HTTP 换实例重试、gRPC 连接池、弹性指标 | [ekit-httpx](https://github.com/yi-nology/ekit-httpx), [ekit-logx](https://github.com/yi-nology/ekit-logx), [ekit-metrics](https://github.com/yi-nology/ekit-metrics), [ekit-nns](https://github.com/yi-nology/ekit-nns), [ekit-registry](https://github.com/yi-nology/ekit-registry) |
| [ekit-pagination](https://github.com/yi-nology/ekit-pagination) | 分页微库——PageRequest/PageResponse 与换算 | — |
| [ekit-pathx](https://github.com/yi-nology/ekit-pathx) | 路径匹配微库——glob（`vendor/**`、`*.test`）匹配 | — |
| [ekit-ptrx](https://github.com/yi-nology/ekit-ptrx) | 指针工具微库——To/From/安全解引用 | — |
| [ekit-redis](https://github.com/yi-nology/ekit-redis) | （见 README） | [ekit-concurrency](https://github.com/yi-nology/ekit-concurrency), [ekit-data](https://github.com/yi-nology/ekit-data), [ekit-logx](https://github.com/yi-nology/ekit-logx), [ekit-redis](https://github.com/yi-nology/ekit-redis) |
| [ekit-registry](https://github.com/yi-nology/ekit-registry) | （见 README） | [ekit-core](https://github.com/yi-nology/ekit-core), [ekit-logx](https://github.com/yi-nology/ekit-logx), [ekit-registry](https://github.com/yi-nology/ekit-registry) |
| [ekit-slicex](https://github.com/yi-nology/ekit-slicex) | 切片工具微库——Partition 二分组、GroupBy、去重与映射 | — |
| [ekit-stringx](https://github.com/yi-nology/ekit-stringx) | 字符串工具微库——ID 生成、关键词匹配、缩进、复数化、模板替换 | — |
| [ekit-timertask](https://github.com/yi-nology/ekit-timertask) | 定时任务微库——分片定时任务（FNV-1a hash、心跳续约） | [ekit-concurrency](https://github.com/yi-nology/ekit-concurrency), [ekit-logx](https://github.com/yi-nology/ekit-logx) |
| [ekit-timex](https://github.com/yi-nology/ekit-timex) | 时间工具微库——格式化/解析/时间戳 + cron 表达式 | — |
| [ekit-tracing](https://github.com/yi-nology/ekit-tracing) | （见 README） | [ekit-logx](https://github.com/yi-nology/ekit-logx) |
| [ekit-uidgen](https://github.com/yi-nology/ekit-uidgen) | ID 生成微库——UUID、雪花、随机串 | — |
| [ekit-validator](https://github.com/yi-nology/ekit-validator) | 参数校验微库——go-playground/validator 封装，ValidationError 归一为 BizError(400) | [ekit-errors](https://github.com/yi-nology/ekit-errors) |
| [ekit-workflow](https://github.com/yi-nology/ekit-workflow) | （见 README） | [ekit-concurrency](https://github.com/yi-nology/ekit-concurrency), [ekit-logx](https://github.com/yi-nology/ekit-logx) |

> `ekit-observability` 已在 Round-2 解散归档；本表由源码 import 自动生成。
> 依赖严格单向：工具微库/L0 → 可观测门面 → registry → data/httpx/middleware → nns/bootstrap → hertz/kitex。

## 当前仓库依赖

详见上方矩阵表中本仓库行。

## 与 ekit 单体仓库的关系

- 代码与 ekit v0.34.0 等价，module 路径 `git.enjoye.top/enjoydream/ekit/...` → `github.com/yi-nology/ekit-httpx/...`
- 各子仓库独立发版（SemVer）；开发规范沿用 ekit AGENTS.md 约定

## License

见 [LICENSE](LICENSE)
