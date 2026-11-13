package httpx

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/cloudwego/hertz/pkg/protocol"
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
//   - 请求体由本 Transport 关闭——标准 RoundTripper 契约「must always close
//     the body, including on errors」（#186）：限流拒绝等错误路径同样关闭，
//     调用方按契约无需自行关闭（标准库 send() 在错误返回时不兜底）；
//   - 非 2xx 不是错误（标准库语义）：调用方自行判定 StatusCode；熔断拒绝返回
//     裸 ErrCircuitOpen（errors.Is 判定）；
//   - 重试缺省关闭，WithRetryConfig 显式开启；开启后的默认条件带幂等方法门
//     （非幂等请求不自动重放，#85），放开经 WithRetryIfFunc 显式声明；
//   - hop-by-hop 头（Connection/Keep-Alive/TE/Trailers/Transfer-Encoding/Upgrade/
//     Proxy-*）与 Host/Content-Length 不透传（由目标端点与 body 长度决定），
//     其余请求头全量透传；响应头全量回填。
type roundTripper struct {
	c *Client
}

// NewRoundTripper 把 httpx Client 装进标准 http.RoundTripper（#8）。
// service 与 opts 语义同 NewClient。
//
// 构造失败（含静态端点与动态 resolver 互斥等构造期冲突）直接 panic——
// 桥接层为「一行 Transport 接入」保留无错误签名，panic 发生在初始化期、
// 携带完整原因，不是请求路径上的运行期 panic。配置来自外部输入、需要
// 显式判错的调用方用 [NewRoundTripperE]。
func NewRoundTripper(service string, opts ...ClientOption) http.RoundTripper {
	return &roundTripper{c: MustNewClient(service, opts...)}
}

// NewRoundTripperE 是 [NewRoundTripper] 的显式错误版本（#83）：构造失败
// 返回 error 而非 panic。语义同 NewRoundTripper。
func NewRoundTripperE(service string, opts ...ClientOption) (http.RoundTripper, error) {
	c, err := NewClient(service, opts...)
	if err != nil {
		return nil, err
	}
	return &roundTripper{c: c}, nil
}

// StdClient 返回装好 Transport 的标准 http.Client（Timeout 留零——
// 超时边界由 httpx 层管理：缺省 dial/read 30s，可经 WithTimeout/WithHertzOption 调整）。
// 构造失败语义同 [NewRoundTripper]（panic）；需显式判错用 [NewRoundTripperE]。
func StdClient(service string, opts ...ClientOption) *http.Client {
	return &http.Client{Transport: NewRoundTripper(service, opts...)}
}

// Close 释放底层资源（EndpointManager 停止等）。经 StdClient 使用时无需调用
// （进程生命周期与 client 一致）；长生命周期场景自行管理。
func (rt *roundTripper) Close() {
	rt.c.Close()
}

// RoundTrip 实现 http.RoundTripper。
//
// 与 request.go 五个封装方法共享的骨架片段（priority 守卫 / Do+lbDone 回写 /
// 错误包装）经 guardPriority/doWithLBDone 收敛单源（#159/#162）；本方法
// 保留桥接层特有的段落：请求体全量缓冲、标准库凭证优先（#43）、
// hop-by-hop 头过滤与 scheme://host 补全（#106）、非 2xx 非错误语义。
func (rt *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	c := rt.c
	ctx := req.Context()

	// net/http RoundTripper 契约（#186）：「RoundTrip must always close the
	// body, including on errors」——标准库 send() 在 rt.RoundTrip 返回 error
	// 时不碰 req.Body，调用方按契约不兜底关（NewRoundTripper/StdClient 是
	// 「一行 Transport 接入」的公开 API，泄漏责任在实现侧）。收尾提到方法
	// 入口注册一次：guardPriority 拒绝等所有提前返回路径都关 body；读体
	// 路径复用已读结果、不再内联关（避免双关非幂等 Closer）。
	if req.Body != nil {
		defer req.Body.Close()
	}

	priority, err := c.guardPriority(ctx)
	if err != nil {
		return nil, err
	}
	defer c.releasePriority(priority)

	// 请求体全量缓冲（重试回放前提）
	var body []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("%s: read request body: %w", c.service, err)
		}
		body = b
	}

	fullURL, lbDone := c.resolveEndpoint(ctx, req.URL.RequestURI())
	// 标准 RoundTripper 语义（#9）：Client 未配置端点/baseURL 时，解析结果只是
	// path?query（无 scheme://host），目标 = http.Request 自身的 scheme://host
	// ——显式端口必须原样进入拨号寻址（旧版在此丢失 host:port，非 80/443 端点
	// 全部落默认 80）。baseURL/Endpoints/NNS 模式的解析结果自带 scheme，不受影响。
	//
	// 「是否已带 scheme://host」必须解析式判定（#106）：fullURL 来自
	// RequestURI（path?query），是调用方可控内容——?to=http://x 这类 query 里
	// 含字面量 "://" 的合法形态会让子串匹配误判为「已有 scheme」⇒ 请求既无
	// scheme 也无 host，被拨到默认 :80（Authorization 等凭证随错误寻址泄漏）。
	if !hasAbsoluteHost(fullURL) && req.URL.Host != "" {
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
	if err := c.doWithLBDone(ctx, hreq, hresp, lbDone, "request failed"); err != nil {
		return nil, err
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

// hasAbsoluteHost 解析式判定 s 是否已带 scheme://host（#106）：url.Parse 出
// Host 非空才算绝对 URL——子串匹配 "://" 会被 path/query 里的字面量
// （如 ?to=http://x）误触发，请求随之丢失 scheme/host 被拨向默认 :80。
func hasAbsoluteHost(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Host != ""
}
