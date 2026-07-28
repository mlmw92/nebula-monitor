package collector

import (
	"net"
	"runtime"
)

// hostArch 返回 CPU 架构字符串（如 amd64 / arm64 / arm）。
func hostArch() string {
	return runtime.GOARCH
}

// primaryIP 返回第一个非回环 IPv4 地址。
func primaryIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		if v4 := ipnet.IP.To4(); v4 != nil {
			return v4.String()
		}
	}
	return ""
}
