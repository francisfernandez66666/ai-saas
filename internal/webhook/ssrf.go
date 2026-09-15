// Package webhook 的出站回调 URL 安全校验（P2-SSRF，2026-09-15）。
// 背景：租户可自填 webhook 接收 URL，服务端代为 POST——不加限制就是内网穿透跳板
// （打 169.254.169.254 拿云厂商凭证、扫内网端口）。
// 口径：生产（GIN_MODE=release）默认禁指向环回/私网/链路本地/组播/未指定地址；
// 开发/测试环境放行（httptest 假接收端与 smoke 的 127.0.0.1:9 死信用例依赖本机回环）。
// 已知边界：按"校验时 DNS 解析结果"判定，域名指向内网+DNS 重绑定属残余风险，
// 严格态需自定义 DialContext 在拨号时二次核对——已在 DEPLOY_CHECKLIST 口径内说明。
package webhook

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// privateTargetAllowed 非 release 环境允许回调打到本机/内网（测试基建依赖）。
func privateTargetAllowed() bool {
	return os.Getenv("GIN_MODE") != "release"
}

// isBlockedIP 判定云元数据/内网探测常用靶点：环回、私网段、链路本地(169.254/16)、组播、0.0.0.0。
func isBlockedIP(ip net.IP) bool {
	return ip == nil || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified()
}

// ValidateCallbackURL 校验租户自填的回调地址。返回 nil 表示允许投递。
func ValidateCallbackURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("URL 非法: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("仅支持 http/https 回调")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL 缺少主机名")
	}
	if privateTargetAllowed() {
		return nil // 开发/测试：httptest/smoke 本机回环属正常用法
	}
	// 字面 IP 直接判段
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("生产环境禁止回调到内网/环回地址")
		}
		return nil
	}
	// 域名：解析后任一记录落内网段即拒（localhost/内部 DNS 皆经此路径）
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("回调域名解析失败: %v", err)
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("生产环境禁止回调域名解析到内网/环回地址")
		}
	}
	return nil
}
