package httpx

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/cloudwego/hertz/pkg/network"
	"github.com/cloudwego/hertz/pkg/network/standard"
	pkgurlguard "github.com/oekit/pkg/urlguard"
)

// 出站 SSRF 防护基于 pkg/urlguard 原语（单一事实源；v0.7.0 曾内置独立实现
// httpx/urlguard，与 pkg v0.15.0 重叠，v0.8.0 起收敛删除）。判定规则（阻断网段、
// 域名后缀、IPv4-mapped 归一、豁免语义）见 pkg/urlguard 文档；本文件只是 hertz
// network.DialFunc 形态的胶水——与 core/chainmw「内核单点、薄壳按框架」同一模式。
//
// 两阶段校验：拨号前对解析出的每个 IP 预检（任一命中即不建连），建连成功后核对
// 实际 RemoteAddr、违规立即关闭——预检解析与拨号器真实解析之间的重解析窗口
// （TOCTOU）由端点核对闭合，Happy-Eyeballs 双栈竞争的胜出路径同样被覆盖。

// SafeDialFunc 包装 network.DialFunc：出站 SSRF 防护（缺省配置，全阻断）。
// next 为 nil 时使用 standard 拨号器裸拨（30s 超时；#74：不用 DefaultDialer——
// netpoll 构建下它是 netpoll 拨号器，返回的连接不支持 TLS 后续包装，https
// 目标会在补握手一步 fail-fast）。需要豁免（测试打本机 mock、内网 webhook
// 端点）用 SafeDialFuncWith / WithSafeDial 传构造态 option。
//
// https 目标受支持：经 httpx.WithDialFunc/WithSafeDial 装配时拨号器是 TLS
// 感知的（见 tlsAwareDialer）——裸拨后按 tlsConfig 补握手。直接用 hertz 原生
// client.WithDialFunc 装配本函数会踩 hertz customDialer 丢 tlsConfig 的坑
// （明文打 443），应经本仓装配。
func SafeDialFunc(next network.DialFunc) network.DialFunc {
	return SafeDialFuncWith(nil, next)
}

// SafeDialFuncWith 同 SafeDialFunc，但接受 pkg/urlguard 的构造态 Option：
//
//	httpx.SafeDialFuncWith([]urlguard.Option{urlguard.WithAllowPrivate()}, next) // 测试打本机 mock
//	httpx.SafeDialFuncWith([]urlguard.Option{urlguard.WithAllowCIDRs("10.20.0.0/16")}, next) // 内网端点豁免
//
// CIDR 文法非法时返回的拨号函数一律报错（fail-fast，不带病运行）。
func SafeDialFuncWith(opts []pkgurlguard.Option, next network.DialFunc) network.DialFunc {
	g, err := pkgurlguard.New(opts...)
	if err != nil {
		return func(string) (network.Conn, error) { return nil, err }
	}
	if next == nil {
		d := standard.NewDialer() // 裸拨：tlsConfig=nil 即裸 TCP，但连接可被 TLS 包装（#74）
		next = func(addr string) (network.Conn, error) {
			return d.DialConnection("tcp", addr, 30*time.Second, nil)
		}
	}
	return func(addr string) (network.Conn, error) {
		return guardHertzDial(g, next, addr)
	}
}

// WithSafeDial 一步启用出站 SSRF 防护，等价 WithDialFunc(SafeDialFuncWith(opts, nil))。
// URL 层字面量校验请另调 pkg/urlguard 的 IsBlockedURL / Guard.CheckURL。
// 无参调用即缺省全阻断；豁免传 pkg/urlguard 的 Option（可变参，向后兼容无参形态）。
//
// https 目标受支持（#74）：WithDialFunc 装配的是 TLS 感知拨号器，拨通后按
// tlsConfig 补握手——防护在 TLS 之前生效（先校验解析 IP 再建连），握手在受
// 拨号超时约束下完成。
func WithSafeDial(opts ...pkgurlguard.Option) ClientOption {
	return WithDialFunc(SafeDialFuncWith(opts, nil))
}

// tlsAwareDialer 把 network.DialFunc 适配成完整 network.Dialer，并在适配中补上
// hertz customDialer 丢掉的 TLS 语义（#74）。
//
// 背景：WithDialFunc 旧实现透传 hertz client.WithDialFunc，其 customDialer 在
// 有 dialFunc 时完全忽略 tlsConfig（直接 dialFunc(address) 返回裸 TCP）；
// 而 hertz http1 客户端的 AddTLS 只在代理路径生效——非代理 https 目标
// （webhook 通知等出站调用的主形态）就此「缺省报 not support tls、
// WithSafeDial 明文打 443」。
//
// 修法：hertz 在拨号时对 https 目标传入非 nil tlsConfig（非代理路径），本拨号器
// 据此在 dialFunc 拨通的裸连接上补 TLS 握手——形态对齐 hertz 代理路径的
// `dialer.AddTLS(conn, tlsConfig)`。握手固定经 **standard 拨号器**完成：
// base（DefaultDialer，netpoll 构建下是 netpoll）只负责裸拨语义，其 AddTLS
// 在 netpoll 下恒报 not support tls；standard 的 AddTLS 对 net.Conn 背书的
// 裸连接做 tls.Client + 握手（netpoll 连接不支持 deadline，TLS 无法包装，
// 会在这一步得到诚实报错而非明文降级）。握手受拨号超时约束（先设 deadline、
// 握手后清除，防悬死握手无上限阻塞）。
type tlsAwareDialer struct {
	network.Dialer // base：DialTimeout 等委托（缺省 DefaultDialer()）
	fn             network.DialFunc
	// tlsDialer 固定 standard.NewDialer()：唯一支持把裸连接包成 TLS 的实现。
	tlsDialer network.Dialer
}

func (d *tlsAwareDialer) DialConnection(n, address string, timeout time.Duration, tlsConfig *tls.Config) (network.Conn, error) {
	if d.fn == nil { // 防呆：nil dialFunc 退化为 base 拨号器原生语义
		return d.Dialer.DialConnection(n, address, timeout, tlsConfig)
	}
	conn, err := d.fn(address)
	if err != nil || tlsConfig == nil {
		return conn, err
	}
	// 握手受拨号超时约束：先给裸连接设 deadline，握手完成后清除。
	// hertz network.Conn 内嵌 net.Conn，SetDeadline 直接可用；不支持 deadline
	// 的连接（netpoll）会在 tls 握手侧得到诚实错误。
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
		defer func() { _ = conn.SetDeadline(time.Time{}) }()
	}
	tlsConn, err := d.tlsDialer.AddTLS(conn, tlsConfig)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return tlsConn, nil
}

// guardHertzDial 是两阶段校验的 hertz 形态实现：pkg Guard 判定 + hertz
// network.Conn 的实际端点核对。校验规则单源在 pkg/urlguard。
func guardHertzDial(g *pkgurlguard.Guard, next network.DialFunc, addr string) (network.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("urlguard: split %q: %w", addr, err)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(context.Background(), host)
	if err != nil {
		return nil, fmt.Errorf("urlguard: resolve %q: %w", host, err)
	}
	for _, a := range ips {
		if cerr := g.CheckIP(a.IP); cerr != nil {
			return nil, fmt.Errorf("urlguard: dial %q blocked: %v", addr, cerr)
		}
	}
	conn, err := next(addr)
	if err != nil {
		return nil, err
	}
	if ra := conn.RemoteAddr(); ra != nil {
		if tcp, ok := ra.(*net.TCPAddr); ok {
			if cerr := g.CheckIP(tcp.IP); cerr != nil {
				_ = conn.Close()
				return nil, fmt.Errorf("urlguard: dial %q blocked: connected endpoint %s violates guard: %v", addr, tcp.IP, cerr)
			}
		}
	}
	return conn, nil
}
