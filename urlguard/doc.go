// Package urlguard 提供出站 SSRF 防护原语：URL 字面量校验 + 解析后 IP 校验。
//
// 两层防护缺一不可：
//
//  1. IsBlockedURL / IsBlockedHost：拒绝 loopback、私网 IP、链路本地、
//     IPv4-mapped IPv6、.internal/.local 等保留域名——挡住低成本探测；
//  2. GuardAddr / SafeDialer：拨号前解析并校验目标 IP——挡住 DNS rebinding。
//
// 典型消费方是「出站目标由用户自配」的服务（webhook / 回调 / 第三方源）。
// 测试需打本机 mock 时用 SetAllowPrivateForTest(true)，进程级开关，用完复位。
package urlguard
