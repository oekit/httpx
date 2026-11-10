package httpx

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/network"
	pkgurlguard "github.com/oekit/pkg/urlguard"
)

// fakeConn 仅实现两阶段校验用到的 RemoteAddr/Close；其余方法来自内嵌 nil 接口、不应被调用。
type fakeConn struct {
	network.Conn
	remote net.Addr
	closed *bool
}

func (f *fakeConn) RemoteAddr() net.Addr { return f.remote }
func (f *fakeConn) Close() error         { *f.closed = true; return nil }

func tcpAddr(ip string, port int) net.Addr {
	return &net.TCPAddr{IP: net.ParseIP(ip), Port: port}
}

// recordingDialer 记录型 mock 拨号器：观测 hertz 层拨号预算是否真实到达底层
// 建连（#151——修复前 WithSafeDial 形态的预算被 tlsAwareDialer 丢弃、拨号硬编
// 30s）。内嵌 nil 接口：AddTLS 等不应被调用。
type recordingDialer struct {
	network.Dialer
	nets    []string
	addrs   []string
	budgets []time.Duration
}

func (m *recordingDialer) DialConnection(n, addr string, timeout time.Duration, _ *tls.Config) (network.Conn, error) {
	m.nets = append(m.nets, n)
	m.addrs = append(m.addrs, addr)
	m.budgets = append(m.budgets, timeout)
	return &fakeConn{remote: tcpAddr("93.184.216.34", 443), closed: new(bool)}, nil
}

// errDeniedByPolicy 是自定义 host 钩子的拒绝哨兵：修复前 httpx 拨号层从不调用
// 自定义 host 钩子（pkg#91 契约），哨兵经 errors.Is 在拨号错误链上可识别——
// 钩子真实生效的可观测凭证（#150）。
var errDeniedByPolicy = errors.New("denied by policy")

// TestSafeDialFunc_BlocksBeforeNext 钉住层 2 预检：解析 IP 命中阻断段即不建连。
func TestSafeDialFunc_BlocksBeforeNext(t *testing.T) {
	called := false
	fn := SafeDialFunc(func(addr string) (network.Conn, error) {
		called = true
		return nil, nil
	})
	if _, err := fn("127.0.0.1:80"); err == nil {
		t.Fatal("expected block")
	}
	if called {
		t.Fatal("next must not be called")
	}
}

// TestSafeDialFuncWith_EndpointVerifyCloses 钉住 TOCTOU 闭合：预检解析为豁免网段、
// 实际建连端点违规（重解析窗口）——连接立即关闭并报错。
func TestSafeDialFuncWith_EndpointVerifyCloses(t *testing.T) {
	called := false
	closed := false
	fn := SafeDialFuncWith([]pkgurlguard.GuardOption{pkgurlguard.WithAllowCIDRs("10.0.0.0/8")},
		func(addr string) (network.Conn, error) {
			called = true
			return &fakeConn{remote: tcpAddr("127.0.0.1", 9000), closed: &closed}, nil
		})
	_, err := fn("10.0.0.5:9000")
	if err == nil || !strings.Contains(err.Error(), "connected endpoint") {
		t.Fatalf("实际端点违规应报错，got %v", err)
	}
	if !called {
		t.Fatal("预检通过后应调用 next")
	}
	if !closed {
		t.Fatal("违规连接必须被关闭")
	}
	_ = fn
}

// TestSafeDialFuncWith_HappyPath：解析与端点均合规 → next 收到原样 addr。
func TestSafeDialFuncWith_HappyPath(t *testing.T) {
	var gotAddr string
	fn := SafeDialFuncWith([]pkgurlguard.GuardOption{pkgurlguard.WithAllowCIDRs("93.0.0.0/8")},
		func(addr string) (network.Conn, error) {
			gotAddr = addr
			return &fakeConn{remote: tcpAddr("93.184.216.34", 443), closed: new(bool)}, nil
		})
	conn, err := fn("93.184.216.34:443")
	if err != nil {
		t.Fatalf("合规目标应放行，got %v", err)
	}
	if gotAddr != "93.184.216.34:443" {
		t.Fatalf("next 应收到原样 addr，got %q", gotAddr)
	}
	_ = conn.Close()
}

// TestSafeDialFuncWith_AllowDomainsExemption 钉住 #150 ①：allowDomains 命中域的
// 私网解析结果在拨号层放行（pkg#296 的豁免语义经 Guard.DialFunc 收编到达
// httpx）——修复前拨号层走无 host 上下文的 CheckIP，写面（CheckURL）放行的
// 内网 webhook 目标在 httpx 拨号层被拒，写面/拨号面结论相反。
func TestSafeDialFuncWith_AllowDomainsExemption(t *testing.T) {
	called := false
	fn := SafeDialFuncWith([]pkgurlguard.GuardOption{pkgurlguard.WithAllowDomains("localhost")},
		func(addr string) (network.Conn, error) {
			called = true
			return &fakeConn{remote: tcpAddr("127.0.0.1", 8080), closed: new(bool)}, nil
		})
	conn, err := fn("localhost:8080")
	if err != nil {
		t.Fatalf("allowDomains 豁免应在拨号层生效（#150），got %v", err)
	}
	if !called {
		t.Fatal("豁免目标应真实建连")
	}
	_ = conn.Close()
}

// TestSafeDialFuncWith_HostHookInvoked 钉住 #150 ②：消费方注入的自定义 host
// 钩子在拨号层真实生效（判定顺序内置在前、自定义在后，pkg#91）——修复前拨号
// 层走 CheckIP（IP 维度），host 维度钩子一次都不触发，SSRF 防护最强一档静默
// 失效。哨兵经 errors.Is 在拨号错误链上可识别。
func TestSafeDialFuncWith_HostHookInvoked(t *testing.T) {
	var hookHosts []string
	nextCalled := false
	fn := SafeDialFuncWith([]pkgurlguard.GuardOption{
		pkgurlguard.WithAllowDomains("localhost"),
		pkgurlguard.WithCheckHostFunc(func(h string) error {
			hookHosts = append(hookHosts, h)
			return errDeniedByPolicy
		}),
	}, func(addr string) (network.Conn, error) {
		nextCalled = true
		return &fakeConn{remote: tcpAddr("127.0.0.1", 8080), closed: new(bool)}, nil
	})
	_, err := fn("localhost:8080")
	if !errors.Is(err, errDeniedByPolicy) {
		t.Fatalf("拨号错误链应携带钩子拒绝（#150），got %v", err)
	}
	if len(hookHosts) == 0 || hookHosts[0] != "localhost" {
		t.Fatalf("自定义 host 钩子应在拨号层被调用，got %v", hookHosts)
	}
	if nextCalled {
		t.Fatal("钩子拒绝后不应建连")
	}
}

// TestSafeDialFuncWith_ExoticLiteralBlocked 钉住 #150 ③：exotic IPv4 字面量
// （标准库不认、宽松解析器会当地址）在拨号层无条件拒绝、不受 WithAllowPrivate
// 豁免（pkg#236 闸门经收编到达 httpx）——修复前 httpx 把字面量直接推给系统
// 解析器（cgo resolver 的 inet_aton 语义），allowPrivate 一开即穿透到拨号
// （2852039166 = 169.254.169.254 云 metadata）。
func TestSafeDialFuncWith_ExoticLiteralBlocked(t *testing.T) {
	for _, host := range []string{"2852039166", "127.1", "0x7f000001"} {
		fn := SafeDialFuncWith([]pkgurlguard.GuardOption{pkgurlguard.WithAllowPrivate()},
			func(addr string) (network.Conn, error) {
				return &fakeConn{remote: tcpAddr("169.254.169.254", 443), closed: new(bool)}, nil
			})
		_, err := fn(host + ":443")
		if err == nil {
			t.Fatalf("exotic IPv4 字面量 %q 应无条件拒绝（#150）", host)
		}
		if !strings.Contains(err.Error(), "exotic IPv4 literal") {
			t.Fatalf("exotic 字面量 %q 应报字面量形态闸门，got %v", host, err)
		}
	}
}

// TestSafeDialFuncWith_InvalidCIDRFailFast：构造态 CIDR 文法非法 → 拨号一律报错。
func TestSafeDialFuncWith_InvalidCIDRFailFast(t *testing.T) {
	fn := SafeDialFuncWith([]pkgurlguard.GuardOption{pkgurlguard.WithAllowCIDRs("not-a-cidr")}, nil)
	if _, err := fn("93.184.216.34:443"); err == nil {
		t.Fatal("非法配置的拨号函数应一律报错")
	}
}

// TestWithSafeDial_OptionPresent：无参形态向后兼容（v0.7.0 调用方零改动）。
// #74 起装配面从 hzOpts 挪到 c.dialer（NewClient 末位兜底，不被
// client.WithTLSConfig 的拨号器覆盖行为清掉）。
func TestWithSafeDial_OptionPresent(t *testing.T) {
	c := &Client{}
	WithSafeDial()(c)
	if c.dialer == nil {
		t.Fatal("WithSafeDial should install TLS-aware dialer")
	}
	if _, ok := c.dialer.(*tlsAwareDialer); !ok {
		t.Fatalf("dialer 应为 *tlsAwareDialer，got %T", c.dialer)
	}
}

// TestWithSafeDial_TimeoutPassthrough 钉住 #151：WithSafeDial 装配面下 hertz
// 传入的拨号预算必须真实到底层建连——修复前 tlsAwareDialer 丢掉 timeout 参数、
// 自带拨号器硬编 30s，WithTimeout（同时设 client.WithDialTimeout）在 WithSafeDial
// 形态下完全失效。装配路径与 newSafeDialer 相同，仅底层拨号器换成 mock 以观测
// 预算（判定层经 pkg Guard.DialFunc，mock 返回豁免网段内的端点）。
func TestWithSafeDial_TimeoutPassthrough(t *testing.T) {
	mock := &recordingDialer{}
	d := newSafeDialerWithBase([]pkgurlguard.GuardOption{pkgurlguard.WithAllowCIDRs("93.0.0.0/8")}, mock)
	conn, err := d.DialConnection("tcp", "93.184.216.34:443", 2*time.Second, nil)
	if err != nil {
		t.Fatalf("豁免网段内目标应放行，got %v", err)
	}
	_ = conn.Close()
	if len(mock.budgets) != 1 {
		t.Fatalf("底层拨号器应恰好被调用一次，got %d", len(mock.budgets))
	}
	if b := mock.budgets[0]; b <= 0 || b > 2*time.Second {
		t.Fatalf("拨号预算应等于 hertz 传入的 2s（修复前硬编 30s，#151），got %v", b)
	}
	if mock.nets[0] != "tcp" || mock.addrs[0] != "93.184.216.34:443" {
		t.Fatalf("底层拨号应收到原样 network/addr，got %s %q", mock.nets[0], mock.addrs[0])
	}
}

// TestSafeDialFuncWith_DefaultBudgetFallback 钉住 #151 的兜底口径：裸
// network.DialFunc 形态（签名 func(addr)，无预算通道）下自带拨号器以
// defaultDialBudget 兜底——修复前是内联硬编 30s，现固化为命名常量并有预算
// 通道形态（WithSafeDial）可越过。
func TestSafeDialFuncWith_DefaultBudgetFallback(t *testing.T) {
	mock := &recordingDialer{}
	g, err := pkgurlguard.New(pkgurlguard.WithAllowCIDRs("93.0.0.0/8"))
	if err != nil {
		t.Fatal(err)
	}
	guarded := newGuardedDial(g, nil, nil, mock)
	conn, err := guarded(context.Background(), "tcp", "93.184.216.34:443")
	if err != nil {
		t.Fatalf("豁免网段内目标应放行，got %v", err)
	}
	_ = conn.Close()
	if len(mock.budgets) != 1 || mock.budgets[0] != defaultDialBudget {
		t.Fatalf("无预算通道形态应以 defaultDialBudget 兜底，got %v", mock.budgets)
	}
}
