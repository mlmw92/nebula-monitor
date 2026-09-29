package collector

import (
	"net/url"
	"strings"
)

// stringsSplitLines 按换行拆分（兼容 \r\n）。
func stringsSplitLines(s string) []string {
	return strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
}

// stringsHasPrefix 薄封装，便于集中替换前缀判断实现。
func stringsHasPrefix(s, prefix string) bool { return strings.HasPrefix(s, prefix) }

// urlHostPort 从 URL（或 host:port）提取 host:port 作为实例标识。
// 解析失败时原样返回。
func urlHostPort(addr string) string {
	if u, err := url.Parse(addr); err == nil && u.Host != "" {
		return u.Host
	}
	return addr
}
