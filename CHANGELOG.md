# Changelog — oekit/httpx

所有 notable 变更记录如下。格式参考 [Keep a Changelog](https://keepachangelog.com/)。

## [v0.4.0] — 2026-10-17

### Breaking（go-kratos 对齐：负载均衡全量切换 Selector，无兼容路径）
- `loadbalance` 包重写为 Selector 形态（参照 go-kratos selector）：
  Node 携带完整实例信息（Address/Weight/ServiceName/Metadata）、`WithFilter` 按元数据过滤、
  `DoneFunc` 回写调用结果（`WithDoneHook` 采集，为 P2C/EWMA 预留）
- 删除：`loadbalance.Balancer`/`Instance`/`ToInstances`/`New`/`NewRoundRobin`/`NewRandom`/`NewWeighted`、
  `httpx.BalanceStrategy`/`WeightedBalanceStrategy`/`NewBalanceAdapter`/`HashBalance`、
  `httpx.WithBalance`、`httpx.NewConsistentHashBalance`、`httpx.WeightedEndpoint`/`WeightedResolver`

| 旧 API | 替代 |
|---|---|
| `loadbalance.New("roundrobin")` + `Balancer` | `loadbalance.NewSelector("roundrobin")` + `Selector` |
| `httpx.WithBalance(strategy)` | `httpx.WithSelector(sel)` |
| `httpx.NewConsistentHashBalance(...)` | `httpx.NewConsistentHashSelector(...)`（选点键 = 请求 PATH，经 SelectOption WithKey 传入） |
| `httpx.WeightedResolver`/`WeightedEndpoint` | `httpx.InstanceResolver`（`ResolveInstances`，实例权重与元数据全量贯通） |

- `EndpointManager` 重构为节点快照缓存（`GetNodes`/`SetNodes`，替代 `GetEndpoints`/`SetEndpoints`
  ——后者保留但等权合成）；`Pick(ctx, key)` 返回 `(addr, DoneFunc, err)`，调用出口回写最终错误
- registry 依赖升级 v0.4.0（SPI 包提升至模块根）

### Added
- `codec` 新子包：注册制编解码器（`Codec` 接口 + `RegisterCodec`/`GetCodec`，json 内置，
  参照 go-kratos encoding）；`Client.DoCodec(ctx, codecName, method, path, body, out)`——
  请求按 codec 序列化并设置 Content-Type，响应按 Content-Type 反查（查不到回退请求 codec）；
  `GetJSON`/`PostJSON`/`DoJSON` 内部迁移至 `DoCodec(json)`，行为不变

## [v0.3.2] — 2026-10-17

- concurrency 地板 v0.5.2 → v0.5.4

## [v0.3.1] — 2026-10-17

- fix(deps): 内部依赖地板全量对齐最新——concurrency v0.5.2、logx v0.5.0、metrics v0.1.6、registry v0.3.1、tracing v0.2.1；go 维持 1.26.0（tracing/OTel 生态传导）

## [v0.3.0] — 2026-10-17

仓库结构重置后的首个版本（历史与旧版本号 v0.1.0–v0.2.0 已清空重做，从 v0.3.0 起算）。

### 目录结构

```
github.com/oekit/httpx                    # package httpx（模块根）
└── loadbalance/                          # package loadbalance — 负载均衡策略
```

- **核心包提升到模块根**：import 从 `github.com/oekit/httpx/httpx`
  改为 `github.com/oekit/httpx`（包名仍为 `httpx`）
- `github.com/oekit/httpx/loadbalance` 路径不变

### 能力面（结构重置保留）

- JSON 请求/响应自动序列化（GetJSON / PostJSON / DoDetail）
- 熔断器（gobreaker）+ 规范哨兵 `ErrCircuitOpen`
- 指数退避重试、弹性指标、OTel 透传
- 服务发现 / 一致性哈希（权重感知虚拟副本）
- `WithDialFunc` / `WithHertzOption` — 拨号层拦截与 Hertz 原生选项透传（SSRF 防护）
- `loadbalance` — RoundRobin / Random / Weighted

### 迁移（从旧路径）

| 旧 | 新 |
|---|---|
| `github.com/oekit/httpx/httpx` | `github.com/oekit/httpx` |
| `github.com/oekit/httpx/loadbalance` | 不变 |
