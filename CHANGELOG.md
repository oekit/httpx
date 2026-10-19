## [v0.8.0] — 2026-10-19

### refactor(urlguard)!: SSRF 防护收敛到 pkg/urlguard 单一事实源（[httpx#11](https://github.com/oekit/httpx/issues/11)）
- **breaking(包删除)**：移除 v0.7.0 内置的 `httpx/urlguard` 独立实现
  （`IsBlockedIP`/`IsBlockedHost`/`IsBlockedURL`/`GuardAddr`/`GuardAddrContext`/
  `SafeDialer`/`SetAllowPrivateForTest`）——与 pkg v0.15.0 的 `pkg/urlguard` 重叠，
  两套同名原语在矩阵内各自漂移不可接受。迁移：`urlguard.IsBlockedURL` →
  `pkgurlguard.IsBlockedURL`（pkg v0.15.0，同名单独 import）；
  `SetAllowPrivateForTest(true)` → 构造态 `WithSafeDial(pkgurlguard.WithAllowPrivate())`
  （进程级全局开关移除，与矩阵注入风格一致）
- **`SafeDialFunc` / `SafeDialFuncWith(opts, next)` / `WithSafeDial(opts...)` 胶水保留**
  （hertz `network.DialFunc` 形态，core/chainmw「内核单点、薄壳按框架」模式），
  实现改基于 pkg Guard，并**新增端点核对**：拨号前解析 IP 全量预检 + 建连后核对实际
  RemoteAddr、违规关连接——v0.7.0 的「钉住已验证 IP 直接拨号」会在 TLS SNI 场景把
  addr 换成 IP（证书校验风险），端点核对以同一保障覆盖重解析窗口且不改拨号语义
- `WithSafeDial` 变参扩展（`opts ...pkgurlguard.Option`），无参调用向后兼容
- pkg 地板 v0.11.0 → **v0.15.0**（urlguard 原语所在版本）

## [v0.8.1] — 2026-10-19

- build: 矩阵内依赖地板对齐（align_floors 不动点）：concurrency v0.5.4 → v0.17.0、logx v0.6.0 → v0.7.0、pkg v0.15.0 → v0.15.1

## [v0.7.0] — 2026-10-18

### Added
- `urlguard` 子包：出站 SSRF 防护原语（[#11](https://github.com/oekit/httpx/issues/11)）
  - `IsBlockedURL` / `IsBlockedHost` / `IsBlockedIP`：URL/host 字面量校验（loopback、私网、链路本地、IPv4-mapped IPv6、`.internal`/`.local` 等）
  - `GuardAddr` / `SafeDialer`：拨号前解析并校验全部 IP（DNS rebinding）
  - `SetAllowPrivateForTest`：测试环境放行开关
- `SafeDialFunc(next)` / `WithSafeDial()`：与 `WithDialFunc` 一行组合的拨号层防护

## [v0.6.1] — 2026-10-18

### fix(roundtripper)：非默认端口丢失（[#9](https://github.com/oekit/httpx/issues/9)）
- Client 未配置端点/baseURL 时，桥接解析结果只是 path?query（无 scheme://host）——
  旧版直接拨号导致 http.Request 的 host:port 全部丢失、非 80 端点落默认 80；
  现按标准 RoundTripper 语义回退用请求自身的 scheme://host 寻址（显式端口原样保留）
- baseURL/Endpoints/NNS 模式不受影响（解析结果自带 scheme）；回归测试用
  httptest 随机端口复现 issue 现场（无任何寻址选项的 StdClient）

## [v0.6.0] — 2026-10-18

### feat：net/http 桥接层 `NewRoundTripper`/`StdClient`（[#8](https://github.com/oekit/httpx/issues/8)）
- 把熔断/重试/指标/链路透传装进标准库 `http.Client{Transport: httpx.NewRoundTripper(...)}`——
  存量 net/http 外呼（OAuth token 交换、webhook、RSS 抓取等低 QPS 控制面调用）只改一行
  Transport 即接入「纪律单源」，无需迁移 protocol.Request 体系
- 语义：目标寻址随 Client（Endpoints/NNS 模式忽略 http.Request host；BaseURL 模式同）；
  请求体/响应体全量缓冲（面向低 QPS 控制面，非流式）；**非 2xx 不是错误**（标准库语义，
  调用方判 StatusCode）；熔断拒绝透出裸 `ErrCircuitOpen`（errors.Is）；hop-by-hop 头与
  Host/Content-Length 不透传，其余头全量透传/回填；**重试缺省关、`WithRetryConfig`
  显式开**（防 OAuth POST 等非幂等重放；`WithRetryIfFunc` 可收紧）
- `StdClient(service, opts...)` 直接返回装好的 `*http.Client`（Timeout 留零，
  超时边界由 httpx 层管理）

## [v0.5.0] — 2026-10-18

- feat(loadbalance): `p2c` 策略（#6）——Power of Two Choices 自适应选择：随机取两候选选 EWMA 调用时长低者，EWMA 经 DoneFunc 回写建立（Err 计 1s 惩罚）；流量自动从慢/故障节点撬走，为 EWMA/P2C 类算法补齐数据闭环
- feat(codec): `form` 编解码器（#7）——application/x-www-form-urlencoded（stdlib 反射，flat-only；`form:"..."` 键名、`form:"-"` 跳过、slice 重复键）；protobuf 维持挂起（依赖树 + schema 形态不合通用 codec，见 #7 评论）

## [v0.4.2] — 2026-10-18

- fix(deps): metrics v0.1.7、tracing v0.2.2 地板对齐（零代码改动）

## [v0.4.1] — 2026-10-18

- fix(deps): logx 地板 v0.5.0 → v0.6.0、core 地板 v0.6.0 → v0.7.0、registry 地板 v0.4.0 → v0.4.1（零代码改动）

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
