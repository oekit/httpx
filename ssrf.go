package httpx

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/cloudwego/hertz/pkg/network"
	hzdialer "github.com/cloudwego/hertz/pkg/network/dialer"
	"github.com/cloudwego/hertz/pkg/network/standard"
	pkgurlguard "github.com/oekit/pkg/urlguard"
)

// 出站 SSRF 防护基于 pkg/urlguard 原语（单一事实源；v0.7.0 曾内置独立实现
// httpx/urlguard，与 pkg v0.15.0 重叠，v0.8.0 起收敛删除）。判定规则（host
// 字面量、exotic IPv4 字面量闸门、阻断网段、域名后缀、allowDomains 豁免、
// 自定义钩子）见 pkg/urlguard 文档；#150 起拨号层判定**整体收编**为
// Guard.DialFunc 的直接装配——httpx 不再复制判定面，pkg 侧修复（#296
// allowDomains 豁免、#236 exotic 闸门、#91 自定义钩子等）随版本升级自动到达。
// 本文件只保留形态适配：hertz network.Conn ↔ net.Conn、拨号预算通道（#151）、
// TLS 补握手（#74）——与 core/chainmw「内核单点、薄壳按框架」同一模式。
//
// 两阶段校验（Guard.DialFunc 内建）：拨号前先做 host 字面量判定，再对解析出的
// 每个 IP 预检（任一命中即不建连），建连成功后核对实际 RemoteAddr、违规立即
// 关闭——预检解析与拨号器真实解析之间的重解析窗口（TOCTOU）由端点核对闭合，
// Happy-Eyeballs 双栈竞争的胜出路径同样被覆盖。

// defaultDialBudget 是 network.DialFunc 形态（签名 func(addr)，装不下拨号
// 预算，#151）下自带拨号器的兜底预算。WithSafeDial 装配面有真实的预算通道
// （hertz 层 timeout 参数），不走本值。
const defaultDialBudget = 30 * time.Second

// SafeDialFunc 包装 network.DialFunc：出站 SSRF 防护（缺省配置，全阻断）。
// next 为 nil 时使用 standard 拨号器裸拨（#74：不用 DefaultDialer——
// netpoll 构建下它是 netpoll 拨号器，返回的连接不支持 TLS 后续包装，https
// 目标会在补握手一步 fail-fast）。需要豁免（测试打本机 mock、内网 webhook
// 端点）用 SafeDialFuncWith / WithSafeDial 传构造态 option。
//
// https 目标受支持：经 httpx.WithDialFunc/WithSafeDial 装配时拨号器是 TLS
// 感知的（见 tlsAwareDialer）——裸拨后按 tlsConfig 补握手。直接用 hertz 原生
// client.WithDialFunc 装配本函数会踩 hertz customDialer 丢 tlsConfig 的坑
// （明文打 443），应经本仓装配。
//
// #151：network.DialFunc 形态装不下调用方的拨号预算，next 为 nil 时的自带
// 拨号器以 defaultDialBudget 兜底；需要 WithTimeout/WithDialTimeout 的拨号
// 预算透传（拨号阶段真实受控）用 WithSafeDial。
func SafeDialFunc(next network.DialFunc) network.DialFunc {
	return SafeDialFuncWith(nil, next)
}

// SafeDialFuncWith 同 SafeDialFunc，但接受 pkg/urlguard 的构造态 Option：
//
//	httpx.SafeDialFuncWith([]urlguard.Option{urlguard.WithAllowPrivate()}, next) // 测试打本机 mock
//	httpx.SafeDialFuncWith([]urlguard.Option{urlguard.WithAllowCIDRs("10.20.0.0/16")}, next) // 内网端点豁免
//
// CIDR 文法非法时返回的拨号函数一律报错（fail-fast，不带病运行）。
//
// 判定规则单源在 pkg/urlguard（#150）：next（含 nil 时的自带拨号器）被适配成
// Guard.DialFunc 的 net 形态后交 pkg 包装，httpx 不再手写判定面。
func SafeDialFuncWith(opts []pkgurlguard.Option, next network.DialFunc) network.DialFunc {
	g, err := pkgurlguard.New(opts...)
	guarded := newGuardedDial(g, err, next, standard.NewDialer())
	return func(addr string) (network.Conn, error) {
		return guarded(context.Background(), "tcp", addr)
	}
}

// WithSafeDial 一步启用出站 SSRF 防护：装配 tlsAwareDialer，判定层收编为
// pkg Guard.DialFunc 的直接装配（#150），hertz 层拨号预算（WithTimeout /
// client.WithDialTimeout）经 DialConnection 的 timeout 参数全程透传到底层
// 拨号器（#151——WithTimeout 的拨号预算在本形态下真实生效）。URL 层字面量
// 校验请另调 pkg/urlguard 的 IsBlockedURL / Guard.CheckURL。无参调用即缺省
// 全阻断；豁免传 pkg/urlguard 的 Option（可变参，向后兼容无参形态）。
//
// https 目标受支持（#74）：拨通后按 tlsConfig 补握手——防护在 TLS 之前生效
// （先校验再建连），握手只占拨号预算的剩余部分（拨号上限不倒挂，#92）。
func WithSafeDial(opts ...pkgurlguard.Option) ClientOption {
	return func(c *Client) {
		c.dialer = newSafeDialer(opts)
	}
}

// newSafeDialer 装配 WithSafeDial 的拨号器。构造态 option 非法时拨号一律
// fail-fast（ClientOption 形态无法在装配期返回 error，与 SafeDialFuncWith
// 同口径）。
func newSafeDialer(opts []pkgurlguard.Option) *tlsAwareDialer {
	return newSafeDialerWithBase(opts, standard.NewDialer())
}

// newSafeDialerWithBase 供测试注入底层拨号器（mock 断言预算透传，#151）。
// 底层拨号器固定 standard 族而非 DefaultDialer：netpoll 构建下 netpoll 连接
// 不支持 deadline，TLS 无法包装（#74）。
func newSafeDialerWithBase(opts []pkgurlguard.Option, base network.Dialer) *tlsAwareDialer {
	g, err := pkgurlguard.New(opts...)
	d := &tlsAwareDialer{
		Dialer:    hzdialer.DefaultDialer(),
		tlsDialer: standard.NewDialer(),
	}
	d.guarded = newGuardedDial(g, err, nil, base)
	return d
}

// newGuardedDial 是拨号层判定内核（#150 收编）：把 hertz 形态的 next 适配成
// pkg Guard.DialFunc 的 next（func(ctx, network, addr) (net.Conn, error)），
// host 字面量判定、exotic IPv4 字面量闸门、allowDomains 豁免、自定义钩子、
// IP 预检与端点复核全部由 pkg 单源承担，httpx 不再复制判定规则。
//
// 拨号预算以 ctx deadline 单通道携带（#151）：Guard.DialFunc 的 next 只有
// ctx 可用，底层拨号器从 deadline 还原预算建连。next 非 nil 时拨号动作由
// next 全权负责（其签名 func(addr) 无预算通道）。gerr 非 nil（构造态 option
// 非法）时返回的拨号函数一律 fail-fast。base 仅在 next == nil 时用作底层
// 拨号器（WithSafeDial 传 standard 拨号器，#74；测试传 mock 断言预算透传）。
func newGuardedDial(g *pkgurlguard.Guard, gerr error, next network.DialFunc, base network.Dialer) func(ctx context.Context, ntw, addr string) (network.Conn, error) {
	if gerr != nil {
		return func(context.Context, string, string) (network.Conn, error) { return nil, gerr }
	}
	guarded := g.DialFunc(func(ctx context.Context, ntw, addr string) (net.Conn, error) {
		if next != nil {
			return next(addr) // network.Conn 内嵌 net.Conn，隐式满足 net 形态
		}
		return base.DialConnection(ntw, addr, dialBudget(ctx), nil)
	})
	return func(ctx context.Context, ntw, addr string) (network.Conn, error) {
		conn, err := guarded(ctx, ntw, addr)
		if err != nil {
			return nil, err
		}
		hc, ok := conn.(network.Conn)
		if !ok {
			_ = conn.Close()
			return nil, fmt.Errorf("urlguard: dial %q: 连接类型 %T 不是 hertz network.Conn", addr, conn)
		}
		return hc, nil
	}
}

// dialBudget 从 ctx deadline 还原拨号预算（#151：预算单通道）。无 deadline
// 时退回兜底预算（network.DialFunc 形态装不下调用方预算）；deadline 已过
// （≤0）原样透传——DialTimeout 以非正值立即失败，fail-fast。
func dialBudget(ctx context.Context) time.Duration {
	dl, ok := ctx.Deadline()
	if !ok {
		return defaultDialBudget
	}
	return time.Until(dl)
}

// tlsAwareDialer 把拨号装配面适配成完整 network.Dialer，并在适配中补上
// hertz customDialer 丢掉的 TLS 语义（#74）。
//
// 背景：WithDialFunc 旧实现透传 hertz client.WithDialFunc，其 customDialer 在
// 有 dialFunc 时完全忽略 tlsConfig（直接 dialFunc(address) 返回裸 TCP）；
// 而 hertz http1 客户端的 AddTLS 只在代理路径生效——非代理 https 目标
// （webhook 通知等出站调用的主形态）就此「缺省报 not support tls、
// WithSafeDial 明文打 443」。
//
// 修法：hertz 在拨号时对 https 目标传入非 nil tlsConfig（非代理路径），本拨号器
// 据此在拨通的裸连接上补 TLS 握手——形态对齐 hertz 代理路径的
// `dialer.AddTLS(conn, tlsConfig)`。握手固定经 **standard 拨号器**完成：
// base（DefaultDialer，netpoll 构建下是 netpoll）只负责裸拨语义，其 AddTLS
// 在 netpoll 下恒报 not support tls；standard 的 AddTLS 对 net.Conn 背书的
// 裸连接做 tls.Client + 握手（netpoll 连接不支持 deadline，TLS 无法包装，
// 会在这一步得到诚实报错而非明文降级）。
type tlsAwareDialer struct {
	network.Dialer // base：DialTimeout 等委托（缺省 DefaultDialer()）
	fn             network.DialFunc
	// guarded 是 WithSafeDial 装配面（#150/#151）：pkg 判定 + 拨号预算单点，
	// 与 fn 二选一、guarded 优先——fn（WithDialFunc 公开形态）的签名装不下
	// 拨号预算，走 fn 时 timeout 仅能约束拨号后的 TLS 握手段。
	guarded func(ctx context.Context, ntw, addr string) (network.Conn, error)
	// tlsDialer 固定 standard.NewDialer()：唯一支持把裸连接包成 TLS 的实现。
	tlsDialer network.Dialer
}

func (d *tlsAwareDialer) DialConnection(n, address string, timeout time.Duration, tlsConfig *tls.Config) (network.Conn, error) {
	if d.guarded != nil { // WithSafeDial 装配面：判定 + 拨号预算单点（#150/#151）
		return d.dialGuarded(n, address, timeout, tlsConfig)
	}
	if d.fn == nil { // 防呆：nil dialFunc 退化为 base 拨号器原生语义
		return d.Dialer.DialConnection(n, address, timeout, tlsConfig)
	}
	conn, err := d.fn(address)
	if err != nil || tlsConfig == nil {
		return conn, err
	}
	// network.DialFunc 形态装不下预算（#151 ①）：自定义拨号器的建连预算由
	// 其自身决定，timeout 仅约束拨号后的 TLS 握手段。
	return d.wrapTLS(conn, timeout, tlsConfig)
}

// dialGuarded 走 pkg 判定层内核（#150）+ 拨号预算透传（#151）：hertz 传入的
// timeout 设为 ctx deadline——pkg 判定（含 DNS 解析）与底层 standard 拨号器
// 都在预算内；TLS 握手只占剩余预算（拨号上限不倒挂，#92）。
func (d *tlsAwareDialer) dialGuarded(n, address string, timeout time.Duration, tlsConfig *tls.Config) (network.Conn, error) {
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	conn, err := d.guarded(ctx, n, address)
	if err != nil || tlsConfig == nil {
		return conn, err
	}
	budget := timeout
	if dl, ok := ctx.Deadline(); ok {
		// 拨号已消耗的预算不再重复给握手；min 钳住声明上限，时钟抖动下剩余
		// 时间不会反超调用方预算。剩余为负（预算耗尽）时 deadline 已在过去，
		// 握手立即失败（fail-fast）。
		budget = min(timeout, time.Until(dl))
	}
	return d.wrapTLS(conn, budget, tlsConfig)
}

// wrapTLS 在裸连接上补 TLS 握手（#74）：受预算约束——先设 deadline、握手后
// 清除，防悬死握手无上限阻塞；budget 为 0 表示无预算信息（不设 deadline），
// 为负表示预算已耗尽（deadline 在过去，握手立即失败）。
func (d *tlsAwareDialer) wrapTLS(conn network.Conn, budget time.Duration, tlsConfig *tls.Config) (network.Conn, error) {
	if budget != 0 {
		_ = conn.SetDeadline(time.Now().Add(budget))
		defer func() { _ = conn.SetDeadline(time.Time{}) }()
	}
	tlsConn, err := d.tlsDialer.AddTLS(conn, tlsConfig)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return tlsConn, nil
}
