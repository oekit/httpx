package httpx

import (
	"net"
	"testing"

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
	fn := SafeDialFuncWith([]pkgurlguard.Option{pkgurlguard.WithAllowCIDRs("10.0.0.0/8")},
		func(addr string) (network.Conn, error) {
			called = true
			return &fakeConn{remote: tcpAddr("127.0.0.1", 9000), closed: &closed}, nil
		})
	_, err := fn("10.0.0.5:9000")
	if err == nil || !contains(err.Error(), "connected endpoint") {
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
	fn := SafeDialFuncWith([]pkgurlguard.Option{pkgurlguard.WithAllowCIDRs("93.0.0.0/8")},
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

// TestSafeDialFuncWith_InvalidCIDRFailFast：构造态 CIDR 文法非法 → 拨号一律报错。
func TestSafeDialFuncWith_InvalidCIDRFailFast(t *testing.T) {
	fn := SafeDialFuncWith([]pkgurlguard.Option{pkgurlguard.WithAllowCIDRs("not-a-cidr")}, nil)
	if _, err := fn("93.184.216.34:443"); err == nil {
		t.Fatal("非法配置的拨号函数应一律报错")
	}
}

// TestWithSafeDial_OptionPresent：无参形态向后兼容（v0.7.0 调用方零改动）。
func TestWithSafeDial_OptionPresent(t *testing.T) {
	c := &Client{}
	WithSafeDial()(c)
	if len(c.hzOpts) == 0 {
		t.Fatal("WithSafeDial should add hz option")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
