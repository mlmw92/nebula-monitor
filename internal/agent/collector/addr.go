package collector

import (
	"net"
	"net/url"
	"strings"
)

// normalizeRemoteAddr 将中间件实例地址规范化后用于 instance 标签/字段展示：
//   - unix:// 本地 socket → Agent 本机真实 IP
//   - 回环地址（127.0.0.1 / localhost / ::1）→ Agent 本机真实 IP（物理网卡优先）
//   - 非回环地址（用户配置的真实 IP / 域名）原样保留（含协议头与路径）
//
// defaultPort 为解析不出端口时的兜底端口（MySQL 传 "3306"，其它中间件传 ""）。
func normalizeRemoteAddr(addr, defaultPort string) string {
	if addr == "" {
		return addr
	}
	// 1. 本地 socket：无地址可提取，直接使用 Agent 本机真实 IP
	if strings.HasPrefix(addr, "unix://") {
		if ip := primaryIP(); ip != "" {
			return ip
		}
		return addr
	}
	// 2. 带协议头（tcp://、http://、https:// 等），可能含路径（如 nginx stub_status URL）
	if i := strings.Index(addr, "://"); i >= 0 {
		scheme := addr[:i]
		u, err := url.Parse(addr)
		if err != nil || u.Host == "" {
			return addr
		}
		host := u.Hostname()
		port := u.Port()
		if port == "" {
			port = defaultPort
		}
		if isLoopbackHost(host) {
			if ip := primaryIP(); ip != "" {
				host = ip
			}
		}
		authority := host
		if port != "" {
			authority = net.JoinHostPort(host, port)
		}
		rest := u.RequestURI()
		if rest == "" {
			rest = "/"
		}
		return scheme + "://" + authority + rest
	}
	// 3. 纯 host[:port]
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host, port = addr, defaultPort
	}
	if isLoopbackHost(host) {
		if ip := primaryIP(); ip != "" {
			host = ip
		}
	}
	if port != "" {
		return net.JoinHostPort(host, port)
	}
	return host
}

// isLoopbackHost 判断 host 是否为回环地址（localhost 或回环 IP）。
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
