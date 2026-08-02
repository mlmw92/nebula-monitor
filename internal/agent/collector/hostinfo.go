package collector

import (
	"net"
	"runtime"
	"strings"
)

// hostArch 返回 CPU 架构字符串（如 amd64 / arm64 / arm）。
func hostArch() string {
	return runtime.GOARCH
}

// primaryIP 返回本机"真实" IPv4 地址：
// 优先取物理网卡地址，排除 docker0/veth/vmnet/virbr 等虚拟网卡；
// 若无物理网卡地址，回退到第一个非回环地址（含虚拟网卡）。
func primaryIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	fallback := ""
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.IsLoopback() {
				continue
			}
			v4 := ipnet.IP.To4()
			if v4 == nil {
				continue
			}
			if isVirtualIface(iface.Name) {
				if fallback == "" {
					fallback = v4.String()
				}
				continue
			}
			return v4.String()
		}
	}
	return fallback
}

// isVirtualIface 判断网卡名是否为常见的虚拟/容器网卡（docker0、veth*、vmnet*、virbr* 等）。
func isVirtualIface(name string) bool {
	name = strings.ToLower(name)
	prefixes := []string{
		"docker", "veth", "vmnet", "virbr", "br-", "cni", "flannel", "calico",
		"cbr", "kube", "weave", "tun", "tap", "tailscale", "utun", "ppp",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
