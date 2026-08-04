package api

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/server/config"
	"github.com/nebula/monitor/internal/server/nginxaccess"
)

// ipv4Regex 用于从各种响应文本中提取首个 IPv4 地址。
var ipv4Regex = regexp.MustCompile(`\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`)

// detectServerProvince 自动探测 server 所在的省级行政区。
// 优先使用本机网卡上直接绑定的公网 IPv4（云主机常见，离线可用）；
// 其次尝试通过公网出口 IP 识别服务（适配 NAT 后仅暴露内网网卡的场景，采用国内可靠源并行请求）；
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
	slog.Warn("无法自动探测服务器所在地，已回退默认；如需指定请在 screen.yaml 设置 deployLocation",
		"default", config.DefaultDeployLocation)
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

// egressIPServices 公网出口 IP 识别服务列表。
// 优先国内可靠源（离线/内网环境可访问性更高），并兼容不同响应格式。
var egressIPServices = []string{
	"https://ip.3322.net",       // 国内，纯文本 IPv4
	"https://myip.ipip.net",     // 国内，返回 "当前 IP：1.2.3.4 来自于：..."
	"https://ip.sb",             // 纯文本 IPv4
	"https://api.ipify.org",     // 纯文本 IPv4
	"https://ifconfig.me/ip",    // 纯文本 IPv4
}

// publicEgressIPs 通过外部识别服务获取本机公网出口 IP，并行请求取首个成功结果。
func publicEgressIPs() []string {
	client := &http.Client{Timeout: 3 * time.Second}
	ch := make(chan string, len(egressIPServices))
	for _, u := range egressIPServices {
		go func(u string) {
			if ip := fetchTextIP(client, u); ip != "" {
				ch <- ip
			}
		}(u)
	}
	var out []string
	timeout := time.After(4 * time.Second)
	for i := 0; i < len(egressIPServices); i++ {
		select {
		case ip := <-ch:
			out = append(out, ip)
		case <-timeout:
			return out
		}
	}
	return out
}

// fetchTextIP 请求识别服务并尽力从响应中提取 IP 地址。
func fetchTextIP(client *http.Client, u string) string {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "nebula-monitor/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	s := string(b)
	// 兼容 myip.ipip.net 等带 "当前 IP：x.x.x.x" 前缀的响应
	if idx := strings.Index(s, "当前 IP："); idx >= 0 {
		s = s[idx+len("当前 IP："):]
	}
	s = strings.TrimSpace(s)
	// 优先取首个独立的 IP 片段
	for _, f := range strings.Fields(s) {
		f = strings.Trim(f, "：: \t")
		if net.ParseIP(f) != nil {
			return f
		}
	}
	// 回退：正则提取首个 IPv4
	if m := ipv4Regex.FindString(s); m != "" {
		return m
	}
	return ""
}
