package urlguard

import (
	"net"
	"net/netip"
	"strings"
	"testing"
)

func TestIsBlockedIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1",
		"169.254.169.254", "0.0.0.0", "255.255.255.255",
		"::1", "fc00::1", "fe80::1", "::",
		"::ffff:10.0.0.1", "::ffff:127.0.0.1", // IPv4-mapped
	}
	for _, s := range blocked {
		ip := netip.MustParseAddr(s)
		if err := IsBlockedIP(ip); err == nil {
			t.Errorf("IsBlockedIP(%s): expected block", s)
		}
	}
	ok := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2001:4860:4860::8888"}
	for _, s := range ok {
		ip := netip.MustParseAddr(s)
		if err := IsBlockedIP(ip); err != nil {
			t.Errorf("IsBlockedIP(%s): expected pass, got %v", s, err)
		}
	}
}

func TestIsBlockedIP_AllowPrivate(t *testing.T) {
	SetAllowPrivateForTest(true)
	defer SetAllowPrivateForTest(false)
	if err := IsBlockedIP(netip.MustParseAddr("127.0.0.1")); err != nil {
		t.Errorf("allowPrivate should pass loopback, got %v", err)
	}
}

func TestIsBlockedHost(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "10.0.0.1:8080", "[::1]:443",
		"localhost", "foo.internal", "svc.local", "a.localhost",
		"",
	}
	for _, h := range blocked {
		if err := IsBlockedHost(h); err == nil {
			t.Errorf("IsBlockedHost(%q): expected block", h)
		}
	}
	for _, h := range []string{"example.com", "api.github.com", "8.8.8.8", "8.8.8.8:53"} {
		if err := IsBlockedHost(h); err != nil {
			t.Errorf("IsBlockedHost(%q): expected pass, got %v", h, err)
		}
	}
}

func TestIsBlockedURL(t *testing.T) {
	blocked := []string{
		"http://127.0.0.1/hook",
		"https://169.254.169.254/latest/meta-data",
		"http://metadata.google.internal/computeMetadata/v1/",
		"file:///etc/passwd",
		"ftp://example.com/x",
		"",
		"not a url",
	}
	for _, u := range blocked {
		if err := IsBlockedURL(u); err == nil {
			t.Errorf("IsBlockedURL(%q): expected block", u)
		}
	}
	for _, u := range []string{"https://example.com/hook", "http://api.github.com"} {
		if err := IsBlockedURL(u); err != nil {
			t.Errorf("IsBlockedURL(%q): expected pass, got %v", u, err)
		}
	}
}

func TestGuardAddr_LiteralPrivate(t *testing.T) {
	if _, err := GuardAddr("127.0.0.1:80"); err == nil {
		t.Fatal("GuardAddr loopback should fail")
	}
	if _, err := GuardAddr("10.0.0.1:443"); err == nil {
		t.Fatal("GuardAddr private should fail")
	}
}

func TestSafeDialer_RejectsBeforeNext(t *testing.T) {
	called := false
	d := SafeDialer(func(network, addr string) (net.Conn, error) {
		called = true
		return nil, nil
	})
	if _, err := d("tcp", "127.0.0.1:80"); err == nil {
		t.Fatal("expected error")
	}
	if called {
		t.Fatal("next dialer must not be called for blocked addr")
	}
}

func TestSafeDialer_AllowPrivatePassesThrough(t *testing.T) {
	SetAllowPrivateForTest(true)
	defer SetAllowPrivateForTest(false)
	called := false
	d := SafeDialer(func(network, addr string) (net.Conn, error) {
		called = true
		if addr != "127.0.0.1:80" {
			t.Errorf("addr = %q", addr)
		}
		return nil, nil
	})
	_, _ = d("tcp", "127.0.0.1:80")
	if !called {
		t.Fatal("next dialer should be called")
	}
}

func TestIsBlockedHost_IPv4Mapped(t *testing.T) {
	if err := IsBlockedHost("[::ffff:192.168.0.1]"); err == nil {
		t.Fatal("IPv4-mapped private should block")
	}
	if !strings.Contains(fmtErr(IsBlockedHost("169.254.0.1")), "blocked") {
		t.Log("ok")
	}
}

func fmtErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
