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

## 矩阵总览（20 活跃仓 + 19 微库，自动生成）

| 仓库 | 内容 | 矩阵内依赖 |
|---|---|---|
| [ekit-bootstrap](https://github.com/oekit/bootstrap) | （见 README） | [ekit-bootstrap](https://github.com/oekit/bootstrap), [ekit-concurrency](https://github.com/oekit/concurrency), [ekit-core](https://github.com/oekit/core), [ekit-logx](https://github.com/oekit/logx), [ekit-metrics](https://github.com/oekit/metrics), [ekit-nns](https://github.com/oekit/nns), [ekit-registry](https://github.com/oekit/registry), [ekit-tracing](https://github.com/oekit/tracing) |
| [ekit-cache](https://github.com/oekit/pkg/cache) | 进程内缓存微库——LRU O(1)、TOCTOU 修复的 LocalCache | — |
| [ekit-concurrency](https://github.com/oekit/concurrency) | （见 README） | [ekit-concurrency](https://github.com/oekit/concurrency) |
| [ekit-convert](https://github.com/oekit/pkg/convert) | 类型转换微库——字符串/数字/布尔/切片的宽容转换 | — |
| [ekit-core](https://github.com/oekit/core) | Go 后端基础工具库——应用基建件（governance 治理 schema / lifecycle 生命周期契约 / network IP 工具 / version） | [ekit-core](https://github.com/oekit/core) |
| [ekit-data](https://github.com/oekit/data) | （见 README） | [ekit-concurrency](https://github.com/oekit/concurrency), [ekit-logx](https://github.com/oekit/logx) |
| [ekit-encoding](https://github.com/oekit/pkg/encoding) | 编解码微库——JSON/msgpack 等序列化助手 | — |
| [ekit-enumx](https://github.com/oekit/pkg/enumx) | 枚举工具微库——确定性顺序的 All/Values/解析 | — |
| [ekit-errors](https://github.com/oekit/pkg/errors) | 错误处理微库——Wrap/Cause 链、BizError（业务码/HTTP 码双轨）、sentinel 错误 | — |
| [ekit-gorm](https://github.com/oekit/gorm) | （见 README） | [ekit-errors](https://github.com/oekit/pkg/errors), [ekit-gorm](https://github.com/oekit/gorm), [ekit-logx](https://github.com/oekit/logx), [ekit-pagination](https://github.com/oekit/pkg/pagination) |
| [ekit-handler](https://github.com/oekit/handler) | （见 README） | [ekit-core](https://github.com/oekit/core), [ekit-errors](https://github.com/oekit/pkg/errors), [ekit-gorm](https://github.com/oekit/gorm), [ekit-handler](https://github.com/oekit/handler), [ekit-middleware](https://github.com/oekit/middleware), [ekit-pagination](https://github.com/oekit/pkg/pagination), [ekit-tracing](https://github.com/oekit/tracing), [ekit-validator](https://github.com/oekit/pkg/validator) |
| [ekit-hertz](https://github.com/oekit/hertz) | （见 README） | [ekit-concurrency](https://github.com/oekit/concurrency), [ekit-core](https://github.com/oekit/core), [ekit-hertz](https://github.com/oekit/hertz), [ekit-logx](https://github.com/oekit/logx), [ekit-metrics](https://github.com/oekit/metrics), [ekit-nns](https://github.com/oekit/nns), [ekit-registry](https://github.com/oekit/registry), [ekit-tracing](https://github.com/oekit/tracing) |
| [ekit-httpjson](https://github.com/oekit/pkg/httpjson) | HTTP JSON 微库——DoJSON 带 retry 语义与 Retry-After 尊重 | [ekit-stringx](https://github.com/oekit/pkg/stringx) |
| [ekit-httpx](https://github.com/oekit/httpx) | （见 README） | [ekit-concurrency](https://github.com/oekit/concurrency), [ekit-httpx](https://github.com/oekit/httpx), [ekit-logx](https://github.com/oekit/logx), [ekit-metrics](https://github.com/oekit/metrics), [ekit-registry](https://github.com/oekit/registry) |
| [ekit-istio](https://github.com/oekit/istio) | （见 README） | — |
| [ekit-jsonrepair](https://github.com/oekit/pkg/jsonrepair) | JSON 修复微库——宽容修复链：截断/尾逗号/单引号/非标准值归一 | — |
| [ekit-kitex](https://github.com/oekit/kitex) | （见 README） | [ekit-concurrency](https://github.com/oekit/concurrency), [ekit-core](https://github.com/oekit/core), [ekit-errors](https://github.com/oekit/pkg/errors), [ekit-kitex](https://github.com/oekit/kitex), [ekit-logx](https://github.com/oekit/logx), [ekit-metrics](https://github.com/oekit/metrics), [ekit-nns](https://github.com/oekit/nns), [ekit-registry](https://github.com/oekit/registry), [ekit-tracing](https://github.com/oekit/tracing) |
| [ekit-kitex-examples](https://github.com/oekit/kitex-examples) | （见 README） | [ekit-bootstrap](https://github.com/oekit/bootstrap), [ekit-kitex](https://github.com/oekit/kitex), [ekit-kitex-examples](https://github.com/oekit/kitex-examples), [ekit-logx](https://github.com/oekit/logx), [ekit-registry](https://github.com/oekit/registry), [ekit-tracing](https://github.com/oekit/tracing) |
| [ekit-logx](https://github.com/oekit/logx) | （见 README） | [ekit-logx](https://github.com/oekit/logx) |
| [ekit-mathx](https://github.com/oekit/pkg/mathx) | 数学工具微库——安全随机、区间、平均值等评测统计助手 | — |
| [ekit-metrics](https://github.com/oekit/metrics) | （见 README） | [ekit-core](https://github.com/oekit/core), [ekit-metrics](https://github.com/oekit/metrics) |
| [ekit-middleware](https://github.com/oekit/middleware) | （见 README） | [ekit-concurrency](https://github.com/oekit/concurrency), [ekit-errors](https://github.com/oekit/pkg/errors), [ekit-logx](https://github.com/oekit/logx), [ekit-metrics](https://github.com/oekit/metrics), [ekit-middleware](https://github.com/oekit/middleware), [ekit-pathx](https://github.com/oekit/pkg/pathx), [ekit-registry](https://github.com/oekit/registry), [ekit-tracing](https://github.com/oekit/tracing) |
| [ekit-network](https://github.com/oekit/network) | （见 README） | [ekit-concurrency](https://github.com/oekit/concurrency), [ekit-logx](https://github.com/oekit/logx), [ekit-tracing](https://github.com/oekit/tracing) |
| [ekit-nns](https://github.com/oekit/nns) | （见 README） | [ekit-concurrency](https://github.com/oekit/concurrency), [ekit-core](https://github.com/oekit/core), [ekit-logx](https://github.com/oekit/logx), [ekit-nns](https://github.com/oekit/nns), [ekit-registry](https://github.com/oekit/registry) |
| [ekit-nns-endpoint](https://github.com/oekit/nns-endpoint) | NNS 端点工厂——per-service 熔断（4xx 不计失败）、HTTP 换实例重试、gRPC 连接池、弹性指标 | [ekit-httpx](https://github.com/oekit/httpx), [ekit-logx](https://github.com/oekit/logx), [ekit-metrics](https://github.com/oekit/metrics), [ekit-nns](https://github.com/oekit/nns), [ekit-registry](https://github.com/oekit/registry) |
| [ekit-pagination](https://github.com/oekit/pkg/pagination) | 分页微库——PageRequest/PageResponse 与换算 | — |
| [ekit-pathx](https://github.com/oekit/pkg/pathx) | 路径匹配微库——glob（`vendor/**`、`*.test`）匹配 | — |
| [ekit-ptrx](https://github.com/oekit/pkg/ptrx) | 指针工具微库——To/From/安全解引用 | — |
| [ekit-redis](https://github.com/oekit/redis) | （见 README） | [ekit-concurrency](https://github.com/oekit/concurrency), [ekit-data](https://github.com/oekit/data), [ekit-logx](https://github.com/oekit/logx), [ekit-redis](https://github.com/oekit/redis) |
| [ekit-registry](https://github.com/oekit/registry) | （见 README） | [ekit-core](https://github.com/oekit/core), [ekit-logx](https://github.com/oekit/logx), [ekit-registry](https://github.com/oekit/registry) |
| [ekit-slicex](https://github.com/oekit/pkg/slicex) | 切片工具微库——Partition 二分组、GroupBy、去重与映射 | — |
| [ekit-stringx](https://github.com/oekit/pkg/stringx) | 字符串工具微库——ID 生成、关键词匹配、缩进、复数化、模板替换 | — |
| [ekit-timertask](https://github.com/oekit/timertask) | 定时任务微库——分片定时任务（FNV-1a hash、心跳续约） | [ekit-concurrency](https://github.com/oekit/concurrency), [ekit-logx](https://github.com/oekit/logx) |
| [ekit-timex](https://github.com/oekit/pkg/timex) | 时间工具微库——格式化/解析/时间戳 + cron 表达式 | — |
| [ekit-tracing](https://github.com/oekit/tracing) | （见 README） | [ekit-logx](https://github.com/oekit/logx) |
| [ekit-uidgen](https://github.com/oekit/pkg/uidgen) | ID 生成微库——UUID、雪花、随机串 | — |
| [ekit-validator](https://github.com/oekit/pkg/validator) | 参数校验微库——go-playground/validator 封装，ValidationError 归一为 BizError(400) | [ekit-errors](https://github.com/oekit/pkg/errors) |
| [ekit-workflow](https://github.com/oekit/workflow) | （见 README） | [ekit-concurrency](https://github.com/oekit/concurrency), [ekit-logx](https://github.com/oekit/logx) |

> `ekit-observability` 已在 Round-2 解散归档；本表由源码 import 自动生成。
> 依赖严格单向：工具微库/L0 → 可观测门面 → registry → data/httpx/middleware → nns/bootstrap → hertz/kitex。

## 当前仓库依赖

详见上方矩阵表中本仓库行。

## 与 ekit 单体仓库的关系

- 代码与 ekit v0.34.0 等价，module 路径 `git.enjoye.top/enjoydream/ekit/...` → `github.com/oekit/httpx/...`
- 各子仓库独立发版（SemVer）；开发规范沿用 ekit AGENTS.md 约定

## License

见 [LICENSE](LICENSE)
