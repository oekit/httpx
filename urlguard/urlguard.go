package urlguard

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
)

var allowPrivate atomic.Bool

// SetAllowPrivateForTest 放行私网/loopback（仅测试环境）。默认 false。
func SetAllowPrivateForTest(enabled bool) { allowPrivate.Store(enabled) }

// protectedPrefixes 是必须拒绝的网段（含 IPv4-mapped IPv6 的底层 v4）。
var protectedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

var blockedHostSuffixes = []string{
	".internal",
	".local",
	".localhost",
	".localdomain",
}

// IsBlockedIP 判断 IP 是否落在受保护网段。IPv4-mapped IPv6 先还原再判。
// 测试放行开关打开时恒返回 nil。
func IsBlockedIP(ip netip.Addr) error {
	if !ip.IsValid() {
		return fmt.Errorf("urlguard: invalid IP")
	}
	ip = ip.Unmap()
	if allowPrivate.Load() {
		return nil
	}
	for _, p := range protectedPrefixes {
		if p.Contains(ip) {
			return fmt.Errorf("urlguard: IP %s is in blocked range %s", ip, p)
		}
	}
	return nil
}

// IsBlockedHost 校验主机名字面量：IP 直接过 IsBlockedIP；
// 域名检查保留后缀与 localhost。不解析 DNS。
func IsBlockedHost(host string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return fmt.Errorf("urlguard: empty host")
	}
	h := stripHostPort(host)
	if ip, err := netip.ParseAddr(h); err == nil {
		return IsBlockedIP(ip)
	}
	lower := strings.ToLower(strings.TrimSuffix(h, "."))
	if allowPrivate.Load() {
		return nil
	}
	if lower == "localhost" {
		return fmt.Errorf("urlguard: host %q is blocked", host)
	}
	for _, suf := range blockedHostSuffixes {
		if strings.HasSuffix(lower, suf) {
			return fmt.Errorf("urlguard: host %q is blocked (suffix %q)", host, suf)
		}
	}
	return nil
}

// IsBlockedURL 校验出站 URL 字面量：scheme 必须 http/https，host 过
// IsBlockedHost。不解析 DNS。
func IsBlockedURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("urlguard: empty URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("urlguard: parse URL: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	case "":
		return fmt.Errorf("urlguard: URL %q missing scheme", raw)
	default:
		return fmt.Errorf("urlguard: URL scheme %q not allowed", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("urlguard: URL %q missing host", raw)
	}
	return IsBlockedHost(u.Host)
}

// GuardAddr 解析 addr（host:port）并校验全部解析结果；任一 IP 落在受保护
// 网段即拒绝。返回校验后的第一个地址（ip:port），供下游拨号，缩小 DNS
// rebinding 窗口。测试放行时跳过解析，原样返回。
func GuardAddr(addr string) (string, error) {
	host, port := splitHostPortLoose(addr)
	if err := IsBlockedHost(host); err != nil {
		return "", err
	}
	if allowPrivate.Load() {
		return addr, nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return "", fmt.Errorf("urlguard: resolve %q: %w", host, err)
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("urlguard: resolve %q: no addresses", host)
	}
	var first netip.Addr
	for _, ip := range ips {
		a, ok := netip.AddrFromSlice(ip)
		if !ok {
			return "", fmt.Errorf("urlguard: resolve %q: bad IP", host)
		}
		if err := IsBlockedIP(a); err != nil {
			return "", err
		}
		if !first.IsValid() {
			first = a.Unmap()
		}
	}
	if port == "" {
		return first.String(), nil
	}
	return net.JoinHostPort(first.String(), port), nil
}

// SafeDialer 包装拨号函数：拨号前 GuardAddr，用校验后的 IP:port 建连。
// next 为 nil 时使用 net.Dial。
func SafeDialer(next func(network, addr string) (net.Conn, error)) func(network, addr string) (net.Conn, error) {
	if next == nil {
		next = net.Dial
	}
	return func(network, addr string) (net.Conn, error) {
		guarded, err := GuardAddr(addr)
		if err != nil {
			return nil, err
		}
		return next(network, guarded)
	}
}

func stripHostPort(host string) string {
	if strings.HasPrefix(host, "[") {
		if i := strings.Index(host, "]"); i > 0 {
			return host[1:i]
		}
		return host
	}
	// host:port —— 仅当冒号后是纯数字才当端口剥掉（避免砍掉 IPv6）
	if i := strings.LastIndex(host, ":"); i > 0 {
		if isDigits(host[i+1:]) {
			return host[:i]
		}
	}
	return host
}

func splitHostPortLoose(addr string) (host, port string) {
	if h, p, err := net.SplitHostPort(addr); err == nil {
		return h, p
	}
	return addr, ""
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
