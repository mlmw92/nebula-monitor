package api

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/server/config"
	"github.com/nebula/monitor/internal/server/nginxaccess"
)

// detectServerProvince 自动探测 server 所在的省级行政区。
// 优先使用本机网卡上直接绑定的公网 IPv4（云主机常见，离线可用）；
// 其次尝试通过公网出口 IP 识别服务（适配 NAT 后仅暴露内网网卡的场景）；
// 经内置 ip2region 库解析出省份，命中省级行政区则返回，否则回退默认值。
func detectServerProvince() string {
	geo := nginxaccess.NewGeo()
	for _, ip := range candidateServerIPs() {
		_, _, province, _ := geo.Search(ip)
		if config.IsValidDeployLocation(province) {
			slog.Info("自动探测到服务器所在地", "ip", ip, "province", province)
			return province
		}
	}
	slog.Warn("无法自动探测服务器所在地，使用默认值", "default", config.DefaultDeployLocation)
	return config.DefaultDeployLocation
}

// candidateServerIPs 返回用于定位 server 的候选 IP 列表。
func candidateServerIPs() []string {
	var ips []string
	// 1) 本机网卡上直接绑定的公网 IPv4（云主机常见），离线可用。
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP
			if ip.To4() == nil || ip.IsLoopback() || ip.IsPrivate() || !ip.IsGlobalUnicast() {
				continue
			}
			ips = append(ips, ip.String())
		}
	}
	// 2) 公网出口 IP（经外部识别服务，适配 NAT 场景）。
	ips = append(ips, publicEgressIPs()...)
	return ips
}

// publicEgressIPs 通过外部识别服务获取本机公网出口 IP。
func publicEgressIPs() []string {
	const timeout = 2 * time.Second
	urls := []string{
		"https://api.ipify.org",
		"https://ip.sb",
		"https://ifconfig.me/ip",
	}
	client := &http.Client{Timeout: timeout}
	var out []string
	for _, u := range urls {
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		func() {
			defer resp.Body.Close()
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
			s := strings.TrimSpace(string(b))
			if net.ParseIP(s) != nil {
				out = append(out, s)
			}
		}()
	}
	return out
}
