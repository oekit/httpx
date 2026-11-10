## [v0.10.0] — 2026-11-11

- **BREAKING** refactor(ssrf): 跟随 pkg#355 禁裸 Option 改名——公开 API 的 `pkgurlguard.Option` 引用改为 `pkgurlguard.GuardOption`（SafeDialFuncWith / WithSafeDial 签名），地板同步抬至 pkg v0.43.0。仅**显式命名该类型**的调用方需机械改名（`WithSafeDial(pkgurlguard.WithAllowPrivate())` 这类不命名类型的调用不受影响）；改名动机是多包 co-import 冲突（pkg 仓规「函数选项模式 XxxOption」），全矩阵同批收口。

## [v0.9.4] — 2026-11-11

- fix(dialer): tlsAwareDialer 保留 hertz 层拨号预算——WithTimeout/WithDialTimeout 在自定义拨号器（含 WithSafeDial）形态下真实生效（#151）。修复前 tlsAwareDialer.DialConnection 接住 hertz 传入的 timeout 却只用于拨号后的 TLS 握手，拨号动作本身无从约束；WithSafeDial 自带拨号器更是硬编 30s——黑洞目标下单请求占住连接与 goroutine 30s（调用方声明 1s/2s），显式补 WithHertzOption(client.WithDialTimeout(d)) 的逃生舱同样被丢弃。修法：WithSafeDial 装配面收编为带预算通道的内部形态（guarded），hertz 传入的 timeout 经 ctx deadline 单通道透传——pkg 判定（含 DNS 解析）与底层 standard 拨号器都在预算内，TLS 握手只占剩余预算（「拨号上限不倒挂」承诺在自定义拨号器形态下重新成立，#92）；裸 network.DialFunc 形态（WithDialFunc 直配）签名装不下预算，自带拨号器以 defaultDialBudget（30s）兜底并在文档写明边界。现代特性：min（握手剩余预算钳制，时钟抖动下不反超声明上限）、errors.Is（回归测试的钩子拒绝哨兵在拨号错误链上可识别）
- fix(ssrf): guardHertzDial 判定层收编为 pkg/urlguard Guard.DialFunc 直接装配，删掉手写漂移面（#150）。修复前 httpx 手写重写 pkg 判定层且比 pkg 少三道闸：① allowDomains 豁免失效——预检走无 host 上下文的 CheckIP，写面放行的内网 webhook 目标在拨号层被拒（pkg#296 修复未跟上，写面/拨号面结论相反）；② 消费方注入的自定义 host 钩子（WithCheckHostFunc）在拨号层从不触发（pkg#91 契约落空，SSRF 防护最强一档静默失效）；③ exotic IPv4 字面量闸门缺失——字面量直接推给系统解析器，WithAllowPrivate 下 2852039166→169.254.169.254（云 metadata）穿透到拨号（pkg#236 闸门未跟上）。修法：next 适配成 Guard.DialFunc 的 net 形态交 pkg 包装，host 字面量判定、exotic 闸门、allowDomains 豁免、自定义钩子、IP 预检与端点复核全部单源在 pkg，httpx 只保留形态适配（hertz network.Conn ↔ net.Conn、拨号预算通道 #151、TLS 补握手 #74）——pkg 侧后续修复随版本升级自动到达。回归测试钉住三处豁免/钩子/闸门行为与预算透传（修复前失败、修复后通过）

关联问题单：httpx#150、httpx#151。

## [v0.9.3] — 2026-11-10

- - build: 依赖地板对齐（floor sync，无代码变更）

## [v0.9.2] — 2026-11-10

- - build: 依赖地板对齐（第二轮发版后 floor sync，无代码变更）

## [v0.9.1] — 2026-11-10

- - build: 依赖地板对齐（floor sync，无代码变更）

## [v0.9.0] — 2026-11-10

- - feat: WithHeaders/WithHeader 静态头选项——引擎预置请求前置中间件，全请求路径生效，多次调用合并后写者赢（#128）
- - fix: idempotentForRetry 删除无 body POST 的幂等兜底——非幂等副作用请求不再被自动重放（#129）

关联问题单：httpx#128、httpx#129。

## [v0.8.16] — 2026-11-08

- fix(bridge): RoundTrip 的「fullURL 是否自带 scheme://host」改解析式判定——path?query 里含字面量 "://"（如 `?to=http://x`）不再被误判为已有 scheme，请求不再丢失 scheme/host 被拨到默认 :80（Authorization 等凭证随错误寻址泄漏的风险面一并关闭）（#106）
- fix(options): WithCircuitBreaker 只管放行/统计，5xx 不再转成普通 error 外抛——引擎层 *ServiceError（含状态码与响应体）路径恢复可达（doc.go 明写的 `errors.As` 写法在开熔断后不再恒 false），桥接层「非 2xx 非错误」标准库语义恢复；5xx 仍计入熔断失败统计（gobreaker IsSuccessful 钩子照常生效、可否决计数）（#107）
- fix(loadbalance): p2c 采样起点随 Select 返回的 done 闭包走（每请求独立 time.Time）——修复前起点按「地址」存共享 map，同址并发请求互相覆盖时间戳，EWMA 学不到真实延迟、慢/故障节点永不摘流；候选只剩 1 个的早退分支同样建立采样（修复前整段丢弃）（#108）
- fix(codec): form 编解码整数按目标字段位宽解析、越界报错——修复前 64 位解析后 `SetInt/SetUint` 静默截断（服务端返回 300 写进 int8 字段变 44 且 err 为 nil），与 encoding/json 越界报错语义对齐（#109）
- fix(codec): form 解码实现逗号分隔切片（`k=a,b,c` → `[a b c]`，与重复键形态并存 `k=1,2&k=3` → `[1 2 3]`，切分后 TrimSpace）——类型注释承诺的解码形态此前不存在，逗号串被整体塞进单个元素（#110）
- fix(codec): form `form:",omitempty"` 空名 tag 回退字段名（编码/解码两侧对称）——修复前键名变空串：编码出 `=v` 无名参数、解码侧空键查值恒零值（#111）
- fix(endpoint): resolveEndpoint 拼接前归一分隔符（base 去尾 `/`、path 补前导 `/`）——path 缺前导 `/` 不再把 host 吃进 path（`http://h:8080` + `api/x` ⇒ `http://h:8080api/x`，拨号报误导性 unknown port），baseURL/静态端点/选点失败降级三分支同修（#112）
- fix(middleware): cookie jar 注入跳过调用方已设的同名 cookie——`SetCookie` 同键替换曾静默改写调用方值，对齐 base 中间件「不覆盖调用方已有设置」的注释承诺（无冲突的 jar cookie 照常注入）（#113）
- fix(request): DoCodec 的 codec 名解析改 Get 语义，未注册返回 error——修复前 `MustGetCodec` 在请求路径上 panic（codec 名来自配置时字符串即可触发进程死亡，#83「失败显式返回」同族契约）（#114）

关联问题单：httpx#106、httpx#107、httpx#108、httpx#109、httpx#110、httpx#111、httpx#112、httpx#113、httpx#114。

## [v0.8.15] — 2026-11-08

- build: 依赖地板对齐——本版把 oekit 依赖地板抬到各依赖当前最新已发布版本（matrix-tools/scripts/align_floors.py 维护轮）

## [v0.8.14] — 2026-11-08

- docs(request): #102 收口——该单为 #86 的重报（UploadFile 不读文件恒传空 part、filename 泄漏完整本地路径），修复已随 v0.8.12（PR #93）落地（`os.ReadFile` + `filepath.Base`，含内容/文件名/读失败三方回归测试）；本版把 request.go 与回归测试的注释标注对齐重报单号，无行为变更

关联问题单：httpx#86、httpx#102。

## [v0.8.13] — 2026-11-07

- build: 依赖地板对齐 logx v0.8.6

## [v0.8.12] — 2026-11-07

- feat(client): NewClient 构造失败显式返回 (*Client, error) + MustNewClient——与家族「错误显式返回」契约对齐（BREAKING，PR #93）（#83）
- fix(client): Close 关闭底层 Hertz client 空闲连接池——短生命周期场景不再每次泄漏一套连接/协程（#84）
- fix(retry): 默认重试谓词加幂等门——非幂等请求（含 POST）不再被无条件 5xx 重放，旧语义经 WithRetryIfFunc 显式放开（#85）
- fix(request): UploadFile 真正读取本地文件——此前实际上传空文件，filename 取 Base 不泄漏目录（#86）
- feat(client): WithStream 流式预设（关读超时保留拨号超时）+ 超时语义族文档（README 速查表）（#87）
- fix(loadbalance): NewPriorityQueue 构造期校验 capacity——0 不再全量阻塞、负数不再裸 panic（#88）
- fix(options): WithNNS/WithServiceResolver 替换旧 resolver 前 Stop——组合 option 的刷新 goroutine 泄漏与静态端点被 NNS 覆盖双向关闭（#89）
- fix(options): WithTimeout 同时覆盖拨号+读（拨号上限不倒挂）；NNS/静态实例地址 scheme 经 MetadataKeyScheme 可表达 https，不再硬编码 http://（#90）
- feat(doonce): DoOnce 低阶单发原语——client 可注入、无重试/熔断复合（#91）
- fix(options): WithTimeout 语义补全（拨号+读双边界）与单项逃生舱显式声明——与 #87/#90 同根因的超时语义族整体梳理（#92）

关联问题单：httpx#83、httpx#84、httpx#85、httpx#86、httpx#87、httpx#88、httpx#89、httpx#90、httpx#91、httpx#92。

## [v0.8.11] — 2026-11-06

- build: 矩阵依赖地板对齐（维护轮 #233）

## [v0.8.10] — 2026-11-06

- build: 矩阵依赖地板对齐（维护轮 #233）

## [v0.8.9] — 2026-11-05

- fix(bridge): net/http 桥接层 https 全线打通（#74）——两个叠加根因：①hertz `customDialer` 在有 dialFunc 时完全丢弃 tlsConfig（`DialConnection` 直接 `return m.dialFunc(address)`），且 http1 客户端的 AddTLS 只在代理路径生效 ⇒ WithSafeDial 形态把**明文请求静默打到 443**（复现实测：服务端 400 Bad Request「Client sent an HTTP request to an HTTPS server.」，handler 从未执行、bridge err=nil，看起来像服务端问题，极难排障——凭证头明文上线是安全敏感面）；②hertz `client.WithTLSConfig` **无条件覆盖 `o.Dialer = standard.NewDialer()`** ⇒ `WithTLSSkipVerify` 排在 WithDialFunc 之后时自定义拨号函数连同 SSRF 防护被整个静默丢弃（https 反而「能用」，防护悄悄失效）。修法：`WithDialFunc` 不再透传 hertz 选项，改装配 TLS 感知拨号器 `tlsAwareDialer`——dialFunc 拨通裸连接后按 tlsConfig 是否为 nil（https 目标必非 nil）补 TLS 握手（形态对齐 hertz 代理路径的 AddTLS；握手固定走 standard 拨号器——netpoll 构建下 DefaultDialer 是 netpoll，连接不支持 TLS 包装，会在这一步得到诚实报错而非明文降级），握手受拨号超时约束（先设 deadline、握手后清除）；装配时机改 `NewClient` **末位兜底** append——选项按序生效、末位覆盖，WithDialFunc/WithSafeDial 的装配面从此不被 TLS 选项清掉；`SafeDialFuncWith` 缺省 next 从 DefaultDialer() 改 standard 拨号器（裸拨语义不变，但连接是 net.Conn 背书、可被 TLS 包装）。补回归测试：https + WithSafeDial 必须真走 TLS 且防护在链路上（计数 next 断言）、任意自定义 dialFunc 同获 TLS 语义、明文 http 行为不变。netpoll 构建下**缺省（无 dialFunc）https 仍报 not support tls**（hertz netpoll 拨号器不支持 TLS，fail-fast，与 issue 判定一致）——桥接层走 WithSafeDial 或任意 WithDialFunc 即可 https

关联问题单：httpx#74。

## [v0.8.8] — 2026-11-02

- build: 矩阵内依赖地板 `pkg` v0.31.0 → v0.31.1（维护轮全量对齐；本版不含 API 变更）

## [v0.8.7] — 2026-11-01

- fix(httpx): base 中间件拼接相对 URI 时漏 ? 分隔查询串（v0.8.6 回归，带查询串的 path 粘连 404）

## [v0.8.6] — 2026-11-01

- fix(httpx): WithCookieJar 真生效 + base 中间件统一裸 Do 的 baseURL/auth/cookie（裸 Hertz().Do 此前三样全缺，下游 5 处生产 bug）

## [v0.8.5] — 2026-10-30

- build: 矩阵内依赖地板 `tracing` v0.3.1 → v0.3.2（维护轮全量对齐；本版不含 API 变更）

## [v0.8.4] — 2026-10-28

- fix(retry): 显式重试谓词永远赢，与 option 顺序无关（#41）——POST 不再被自动重放

`WithRetryIfFunc` 与 `WithRetryConfig` 写**同一个字段**（`c.retryIf`）且都是无条件赋值，而 option 按传入顺序依次 apply ⇒ **后写者赢**。而 `WithRetryConfig` 注入的默认谓词**完全不看 HTTP 方法**（5xx + network error 一律重试）⇒ 把「只重试 GET」写在 `WithRetryConfig` **之前**时，用户谓词被**无声丢弃**，退回默认谓词 ⇒ **POST 被自动重放**。

实测 A/B 对照（下游固定返回 500）：谓词在前 ⇒ 服务端收到 **3** 次请求；谓词在后 ⇒ **1** 次 ⇒ 同一个客户端、同一份代码意图，只因 option 书写顺序不同，POST 就从「不重试」变成「重试 3 次」。

为什么难发现：① 不报错、不 panic；② Go 里 option 顺序是「无所谓」的直觉，写错不会有任何提示；③ 只有下游返回 5xx 时才显形（测试环境常返回 200 ⇒ 全部测试通过）。且 hertz 自带的幂等方法检查在自定义谓词路径下**被完全跳过**（`isDefaultRetryFunc = false`）⇒ 唯一的幂等保护就是这个被丢掉的谓词。

后果形态最坏：下单 / 支付 / 发 webhook 这类 POST 收到下游 500 时被重复执行 ⇒ 重复订单、重复扣款、重复通知；现象与「下游真的失败」难以区分，排障方向会被带偏。

修法：`Client` 增 `retryIfExplicit` 标记，`WithRetryIfFunc` 置位；`WithRetryConfig` 仅在用户**未显式设置**谓词时才注入默认谓词 ⇒ 显式谓词永远赢、与顺序无关。既有口径不变：未设显式谓词时默认谓词行为完全照旧。补 3 条用例：两种顺序下显式谓词都生效、默认谓词仍重试 POST（口径护栏）、显式谓词返回 true 确实触发重试（#41）

- fix(lb): 一致性哈希的「同一视图」判定只比节点地址、不比权重（#42）——注册中心调权后地址不变 ⇒ 哈希环被永久缓存，权重折算出的虚拟副本数沿用旧值 ⇒ 调权静默失效（降权重想摘流摘不动）；判定同步比较权重（权重取自注册中心同一份实例数据、刷新之间逐字节稳定 ⇒ 不存在浮点抖动导致的反复重建），并更正原注释「单点调权需等下一轮刷新生效」的错误口径（下一轮刷新的地址列表依旧相同 ⇒ 永远不会重建）；补 2 条用例：只改权重 ⇒ 环必须重建（以环长度观测副本数变化）、地址与权重均未变 ⇒ 环必须复用
- fix(bridge): RoundTripper 桥接层发出两个 Authorization 头（#43）——违反 RFC 7235 §3.1「客户端 MUST NOT 生成多于一个 Authorization 头」；成因是先 applyAuth(Set，写客户端级凭证) 再透传(Add，追加调用方自带同名头)，而 Authorization 不是 hertz 的 special header ⇒ Add 不会被降级为覆盖 ⇒ 线上发出两行 Authorization；各服务端取首个/取末个的归属不确定（取首个 ⇒ 调用方的请求级凭证被静默忽略，取末个 ⇒ 客户端级凭证被静默覆盖 ⇒ 跨主机时客户端级凭证随请求外发，部分网关直接 400）；改为凭证来源唯一化：调用方显式带了 Authorization 就用它，否则才用客户端级凭证，透传循环按大小写不敏感跳过该头，其余多值头仍按 Add 原样透传（不被压扁）；补 3 条用例：只发一个头且调用方优先、调用方没带时客户端凭证仍生效、其余多值头不被压扁

## [v0.8.3] — 2026-10-28

- fix(httpx): 同名 collector **类型不符**时的 panic 不可定位（无检查的类型断言 `are.ExistingCollector.(T)`）——改为有检查的断言 + 人能读懂的错误（既有类型 vs 请求类型 + 怎么改）；同型修法见 oekit/metrics#31

## [v0.8.2] — 2026-10-20

- fix(metrics): 熔断状态改为类型化传递——`options.go` 的 `ObserveTransition(…, int(from), int(to))` 改为 `ObserveTransitionTyped(…, resilience.CircuitState(from), resilience.CircuitState(to))`，状态不再以裸 int 跨模块流转。同时加**编译期钉子**：`gobreaker.State` 与 `resilience.CircuitState` 数值必须一致（差值非 0 即编译失败）——谁改动 resilience 侧数值，本仓立刻编译红，而不是运行期把熔断状态标签悄悄记反（熔断打开被记成 `half_open`）。指标产出**逐字节不变**（标签仍是 `closed`/`half_open`/`open` 字符串）。
- build: 矩阵内依赖地板 `metrics` v0.1.7 → v0.1.9（`core` 间接随之 v0.7.0 → v0.9.2）。

## [v0.8.1] — 2026-10-19

- build: 矩阵内依赖地板对齐（align_floors 不动点）：concurrency v0.5.4 → v0.17.0、logx v0.6.0 → v0.7.0、pkg v0.15.0 → v0.15.1

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
- `WithDialFunc` / `WithHertzOption` — 拨号层拦截与 Hertz 原生选项透传（SSRF 防护）（#5）
- `loadbalance` — RoundRobin / Random / Weighted

### 迁移（从旧路径）

| 旧 | 新 |
|---|---|
| `github.com/oekit/httpx/httpx` | `github.com/oekit/httpx` |
| `github.com/oekit/httpx/loadbalance` | 不变 |
