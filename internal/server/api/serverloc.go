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

// egressService 公网出口 IP/地理识别服务。
// geoFrom 为从响应中提取"地理直出"省份的正则：命中即采用（比二次解析 IP 更准确，
// 可规避云厂商公网 IP 段注册地与物理机房不符导致的误判）；留空表示仅提供 IP，
// 交由 ip2region 二次解析。
type egressService struct {
	url     string
	geoFrom *regexp.Regexp
}

// egressServices 出口识别服务列表，并行请求取首个成功结果。
// myip.ipip.net 会直接给出 "来自于：上海市 电信" 等机房地理信息，定位通常比
// ip2region 对 IP 段的注册地判定更贴近真实物理机房，故排在最前。
var egressServices = []egressService{
	{url: "https://myip.ipip.net", geoFrom: regexp.MustCompile(`来自于[:：][^\n]*?(\p{Han}+省|\p{Han}+市|[\p{Han}]*自治区)`)},
	{url: "https://ip.3322.net"},
	{url: "https://ip.sb"},
	{url: "https://api.ipify.org"},
	{url: "https://ifconfig.me/ip"},
}

// detectServerProvince 自动探测 server 所在的省级行政区。
// 优先级：① 公网出口服务"地理直出"（最准，规避云 IP 段误判）；
// ② 公网出口 IP 经内置 ip2region 解析；③ 本机网卡公网 IP（离线兜底）；
// 全部失败回退默认「北京」。
func detectServerProvince() string {
	geo := nginxaccess.NewGeo()

	// 1) 公网出口：优先地理直出；否则收集出口 IP 后解析
	province, ips := fetchEgress(geo)
	if province != "" {
		slog.Info("自动探测到服务器所在地(服务直出)", "province", province)
		return province
	}
	for _, ip := range ips {
		if p := resolveProvince(geo, ip); p != "" {
			slog.Info("自动探测到服务器所在地(出口IP解析)", "ip", ip, "province", p)
			return p
		}
	}

	// 2) 本机网卡上直接绑定的公网 IPv4（云主机内网 NAT 场景通常为空，离线兜底用）
	for _, ip := range localPublicIPs() {
		if p := resolveProvince(geo, ip); p != "" {
			slog.Info("自动探测到服务器所在地(本机网卡)", "ip", ip, "province", p)
			return p
		}
	}

	slog.Warn("无法自动探测服务器所在地，已回退默认；如需指定请在 screen.yaml 设置 deployLocation",
		"default", config.DefaultDeployLocation)
	return config.DefaultDeployLocation
}

// fetchEgress 并行请求出口识别服务，返回（地理直出省份, 可用出口 IP 列表）。
// 任意服务一旦给出有效地理直出即立即返回该省份；否则汇总所有成功提取到的 IP。
func fetchEgress(geo *nginxaccess.Geo) (string, []string) {
	client := &http.Client{Timeout: 3 * time.Second}
	type result struct {
		province string
		ip       string
	}
	ch := make(chan result, len(egressServices))
	for _, svc := range egressServices {
		go func(svc egressService) {
			body := fetchText(svc.url, client)
			if body == "" {
				ch <- result{}
				return
			}
			if svc.geoFrom != nil {
				if m := svc.geoFrom.FindStringSubmatch(body); m != nil {
					if p := normalizeProvince(m[1]); config.IsValidDeployLocation(p) {
						ch <- result{province: p}
						return
					}
				}
			}
			ch <- result{ip: extractIP(geo, body)}
		}(svc)
	}
	timeout := time.After(4 * time.Second)
	var ips []string
	for i := 0; i < len(egressServices); i++ {
		select {
		case r := <-ch:
			if r.province != "" {
				return r.province, ips
			}
			if r.ip != "" {
				ips = append(ips, r.ip)
			}
		case <-timeout:
			return "", ips
		}
	}
	return "", ips
}

// localPublicIPs 返回本机网卡上直接绑定的公网 IPv4（非回环、非私网、全球单播）。
func localPublicIPs() []string {
	var ips []string
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
	return ips
}

// resolveProvince 经 ip2region 解析 IP 得到省级行政区，命中预设列表则返回。
func resolveProvince(geo *nginxaccess.Geo, ip string) string {
	if ip == "" {
		return ""
	}
	_, _, province, _ := geo.Search(ip)
	if config.IsValidDeployLocation(province) {
		return province
	}
	return ""
}

// normalizeProvince 将 "上海市" / "广东省" 等完整写法归一为 "上海" / "广东"。
func normalizeProvince(s string) string {
	s = strings.TrimSpace(s)
	canonical := map[string]string{
		"北京市": "北京", "上海市": "上海", "天津市": "天津", "重庆市": "重庆",
		"河北省": "河北", "山西省": "山西", "辽宁省": "辽宁", "吉林省": "吉林",
		"黑龙江省": "黑龙江", "江苏省": "江苏", "浙江省": "浙江", "安徽省": "安徽",
		"福建省": "福建", "江西省": "江西", "山东省": "山东", "河南省": "河南",
		"湖北省": "湖北", "湖南省": "湖南", "广东省": "广东", "海南省": "海南",
		"四川省": "四川", "贵州省": "贵州", "云南省": "云南", "陕西省": "陕西",
		"甘肃省": "甘肃", "青海省": "青海", "台湾省": "台湾",
		"内蒙古自治区": "内蒙古", "广西壮族自治区": "广西", "宁夏回族自治区": "宁夏",
		"新疆维吾尔自治区": "新疆", "西藏自治区": "西藏",
		"香港": "香港", "澳门": "澳门",
	}
	if v, ok := canonical[s]; ok {
		return v
	}
	// 兜底：直接剥离省/市/自治区后缀
	return strings.NewReplacer("省", "", "市", "", "自治区", "").Replace(s)
}

// fetchText 请求识别服务并返回响应文本（截断 1KB）。
func fetchText(u string, client *http.Client) string {
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
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return string(b)
}

// extractIP 从响应文本中尽力提取首个 IPv4 地址。
func extractIP(geo *nginxaccess.Geo, s string) string {
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
