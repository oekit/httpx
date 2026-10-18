package httpx

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/cloudwego/hertz/pkg/network"
	"github.com/cloudwego/hertz/pkg/network/dialer"
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
// next 为 nil 时使用 Hertz 默认 dialer（30s 超时）。需要豁免（测试打本机
// mock、内网 webhook 端点）用 SafeDialFuncWith / WithSafeDial 传构造态 option。
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
		d := dialer.DefaultDialer()
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
func WithSafeDial(opts ...pkgurlguard.Option) ClientOption {
	return WithDialFunc(SafeDialFuncWith(opts, nil))
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
