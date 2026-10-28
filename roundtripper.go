package httpx

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/cloudwego/hertz/pkg/protocol"

	"github.com/oekit/httpx/loadbalance"
)

// net/http 桥接层（#8）：把 httpx 的熔断/重试/指标/链路透传装进标准库
// http.Client——存量 net/http 外呼代码（OAuth token 交换、webhook 通知、RSS
// 抓取等低 QPS 控制面调用）只改一行 Transport 即接入「纪律单源」，无需迁移
// protocol.Request 体系。
//
// 用法:
//
//	client := &http.Client{Transport: httpx.NewRoundTripper("oauth",
//	    httpx.WithBaseURL("https://auth.example.com"),
//	    httpx.WithCircuitBreaker("oauth", gobreaker.Settings{}))}
//	// 或直接拿装好的：
//	client := httpx.StdClient("oauth", httpx.WithBaseURL(...))
//
// 语义说明：
//   - 目标寻址完全随 Client：WithEndpoints/WithNNS 服务模式忽略 http.Request
//     的 host（仅 path?query 生效）；WithBaseURL 直连模式同样以 baseURL 为准；
//   - 请求体全量缓冲（重试回放与熔断统计的前提）；响应体全量缓冲后返回——
//     桥接面向低 QPS 控制面调用，不适合大文件流式场景；
//   - 非 2xx 不是错误（标准库语义）：调用方自行判定 StatusCode；熔断拒绝返回
//     裸 ErrCircuitOpen（errors.Is 判定）；
//   - 重试缺省关闭，WithRetryConfig 显式开启（防 OAuth POST 等非幂等请求被
//     自动重放）；开启后对幂等语义的自评仍是调用方责任（WithRetryIfFunc 可收紧）；
//   - hop-by-hop 头（Connection/Keep-Alive/TE/Trailers/Transfer-Encoding/Upgrade/
//     Proxy-*）与 Host/Content-Length 不透传（由目标端点与 body 长度决定），
//     其余请求头全量透传；响应头全量回填。
type roundTripper struct {
	c *Client
}

// NewRoundTripper 把 httpx Client 装进标准 http.RoundTripper（#8）。
// service 与 opts 语义同 NewClient。
func NewRoundTripper(service string, opts ...ClientOption) http.RoundTripper {
	return &roundTripper{c: NewClient(service, opts...)}
}

// StdClient 返回装好 Transport 的标准 http.Client（Timeout 留零——
// 超时边界由 httpx 层管理：缺省 dial/read 30s，可经 WithTimeout/WithHertzOption 调整）。
func StdClient(service string, opts ...ClientOption) *http.Client {
	return &http.Client{Transport: NewRoundTripper(service, opts...)}
}

// Close 释放底层资源（EndpointManager 停止等）。经 StdClient 使用时无需调用
// （进程生命周期与 client 一致）；长生命周期场景自行管理。
func (rt *roundTripper) Close() {
	rt.c.Close()
}

// RoundTrip 实现 http.RoundTripper。
func (rt *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	c := rt.c
	ctx := req.Context()

	priority := GetPriority(ctx)
	if c.priorityQueue != nil {
		if !c.priorityQueue.Acquire(ctx, priority) {
			return nil, fmt.Errorf("%s: request cancelled due to priority queue", c.service)
		}
		defer c.priorityQueue.Release(priority)
	}

	// 请求体全量缓冲（重试回放前提）
	var body []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: read request body: %w", c.service, err)
		}
		body = b
	}

	fullURL, lbDone := c.resolveEndpoint(ctx, req.URL.RequestURI())
	// 标准 RoundTripper 语义（#9）：Client 未配置端点/baseURL 时，解析结果只是
	// path?query（无 scheme://host），目标 = http.Request 自身的 scheme://host
	// ——显式端口必须原样进入拨号寻址（旧版在此丢失 host:port，非 80/443 端点
	// 全部落默认 80）。baseURL/Endpoints/NNS 模式的解析结果自带 scheme，不受影响
	if !strings.Contains(fullURL, "://") && req.URL.Host != "" {
		scheme := req.URL.Scheme
		if scheme == "" {
			scheme = "http"
		}
		fullURL = scheme + "://" + req.URL.Host + fullURL
	}

	hreq, hresp := protocol.AcquireRequest(), protocol.AcquireResponse()
	defer protocol.ReleaseRequest(hreq)
	defer protocol.ReleaseResponse(hresp)

	hreq.SetMethod(req.Method)
	hreq.SetRequestURI(fullURL)
	// 凭证来源**唯一化**（#43）：调用方显式带了 Authorization 就用它，
	// 否则才用客户端级凭证（WithBearerToken / WithBasicAuth）。
	//
	// 旧实现是 applyAuth(Set) + 透传(Add) ⇒ 两个 Authorization 值并存 ⇒
	// 线上发出两行 Authorization（违反 RFC 7235 §3.1「客户端 MUST NOT 生成
	// 多于一个 Authorization 头」），而各服务端对重复头的处理归属不确定
	// （取首个 ⇒ 调用方的请求级凭证被静默忽略；取末个 ⇒ 客户端凭证被静默覆盖）。
	// Authorization 不是 hertz 的 special header ⇒ Add 不会被降级为覆盖。
	if auth := req.Header.Values("Authorization"); len(auth) > 0 {
		hreq.Header.Set("Authorization", auth[0])
	} else {
		c.applyAuth(hreq)
	}
	for key, values := range req.Header {
		if skipBridgeHeader(key) {
			continue
		}
		if strings.EqualFold(key, "Authorization") {
			continue // 已在上方按「调用方优先」单独处理
		}
		for _, v := range values {
			hreq.Header.Add(key, v)
		}
	}
	if len(body) > 0 {
		hreq.SetBody(body)
	}

	// 熔断/重试/指标/链路透传全部经 Client 中间件链生效
	err := c.client.Do(ctx, hreq, hresp)
	if lbDone != nil {
		lbDone(ctx, loadbalance.DoneInfo{Err: err})
	}
	if err != nil {
		return nil, fmt.Errorf("%s: request failed: %w", c.service, err)
	}

	// 非 2xx 不是错误（标准库语义），handleResponse 的 ServiceError 转换不适用
	raw := append([]byte(nil), hresp.Body()...)
	out := &http.Response{
		StatusCode:    hresp.StatusCode(),
		Status:        fmt.Sprintf("%d %s", hresp.StatusCode(), http.StatusText(hresp.StatusCode())),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        make(http.Header),
		Body:          io.NopCloser(bytes.NewReader(raw)),
		ContentLength: int64(len(raw)),
		Request:       req,
	}
	hresp.Header.VisitAll(func(key, value []byte) {
		out.Header.Add(string(key), string(value))
	})
	return out, nil
}

// skipBridgeHeader 判定桥接时不透传的头：hop-by-hop（RFC 7230 §6.1）+
// 由目标端点/body 决定的 Host 与 Content-Length。
func skipBridgeHeader(key string) bool {
	switch strings.ToLower(key) {
	case "host", "content-length",
		"connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailers", "transfer-encoding", "upgrade":
		return true
	}
	return false
}
