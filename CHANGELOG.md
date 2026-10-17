# Changelog — oekit/httpx

所有 notable 变更记录如下。格式参考 [Keep a Changelog](https://keepachangelog.com/)。

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
