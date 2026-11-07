package httpx

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// DoOnce 一次性低阶请求原语：发出请求 → 读尽响应体 → 返回
// (status, body, err)（#91）。
//
// 定位：服务里「共享 *http.Client（自定义 Transport：SSRF 拨号/链路透传）
// + 轻量请求骨架」的形态此前没有库级原语，各服务自收敛一份，收敛点又因
// client 不可注入被绕开（openclaw 实录：需要不挂 urlguard 的 loopback
// 客户端时整份 NewRequest/Do/ReadAll/状态码判定重新手搓）。
//
// 语义与边界：
//   - client 由调用方注入：hc 为 nil 时用 http.DefaultClient。挂不挂 SSRF
//     防护（Transport 包 urlguard）、什么连接池、什么超时，都是宿主决策——
//     本原语不做任何拨号/传输假设；
//   - 不做重试/熔断/负载均衡：弹性语义单点在 NewClient 引擎层，本原语与
//     引擎层组合会造成复合重试；需要弹性时直接用 Client；
//   - 非 2xx 不是错误（标准库语义）：调用方自行判定 status；body 仅在
//     err == nil 时有效（读 body 中途出错时 status 仍返回、body 为 nil）；
//   - ctx 非 nil 时覆盖 req 自带的 context（req.WithContext）；总时长
//     预算经 ctx 控制；
//   - 响应体总是读尽并关闭（body Close 后返回），适合 JSON/文本等可控
//     体量的响应；超大响应的流式消费不要经本原语缓冲。
func DoOnce(ctx context.Context, hc *http.Client, req *http.Request) (status int, body []byte, err error) {
	if req == nil {
		return 0, nil, fmt.Errorf("httpx: DoOnce: nil request")
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	if ctx != nil {
		req = req.WithContext(ctx)
	}

	// G704（taint：目标 URL 来自入参）是本原语的设计前提而非缺陷：client
	// 与请求目标都由宿主注入，SSRF 防护挂在注入 client 的 Transport 上
	// （如 WithSafeDial 的 urlguard 拨号拦截），库级不替宿主做拦截。
	resp, err := hc.Do(req) //nolint:gosec // G704: 目标由调用方注入，防护在注入的 client 上（见上）
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	status = resp.StatusCode
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return status, nil, fmt.Errorf("httpx: DoOnce: read response body (status %d): %w", status, err)
	}
	return status, body, nil
}
