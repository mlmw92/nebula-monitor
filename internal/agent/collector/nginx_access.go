package collector

import (
	"bufio"
	"io"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"
)

const (
	// nginxAccessTopIPs 上报 Top-N 来源 IP，控制 Server 端聚合规模。
	nginxAccessTopIPs = 200
	// nginxAccessTopURIs 上报 Top-N URI。
	nginxAccessTopURIs = 10
	// nginxAccessMaxRows 单周期最多解析的日志行数，防止日志洪峰拖垮采集。
	nginxAccessMaxRows = 50000
	// nginxAccessMaxBytes 单周期最多读取的日志字节数（约 8MB），超限则下次继续。
	nginxAccessMaxBytes = 8 << 20

	// 支持的日志格式名称
	nginxLogFormatCombined      = "combined"
	nginxLogFormatCombinedTimed = "combined_timed"
)

// nginxLogReCombined 匹配标准 combined 格式，末尾可选 $request_time。
// combined: $remote_addr - $remote_user [$time_local] "$request" $status $body_bytes_sent "$http_referer" "$http_user_agent" [$request_time]
// 分组：1=addr 2=user 3=time_local 4=request 5=status 6=body_bytes 7=referer 8=ua 9=request_time
var nginxLogReCombined = regexp.MustCompile(`^(\S+) - (\S+) \[([^\]]+)\] "([^"]*)" (\d{3}) (\d+) "([^"]*)" "([^"]*)"(?:\s+([\d.]+))?$`)

// nginxLogReCombinedTimed 匹配 combined_timed 格式（无 referer/ua，末尾有 request_time）。
// combined_timed: $remote_addr - - [$time_local] "$request" $status $body_bytes_sent $request_time
// 分组：1=addr 2=time_local 3=request 4=status 5=body_bytes 6=request_time
var nginxLogReCombinedTimed = regexp.MustCompile(`^(\S+) - - \[([^\]]+)\] "([^"]*)" (\d{3}) (\d+)(?:\s+(\S+))?$`)

// nginxLogReFlex 兼容多种主流 Nginx 日志变体：combined、combined_timed，
// 以及 combined + 额外尾部字段（如 $http_x_forwarded_for，为空时记 "-"）。
// 只提取聚合所需字段；末尾可选的纯数字视为 $request_time（用于延迟统计）。
// 分组：1=addr 2=time_local 3=request 4=status 5=body_bytes 6=request_time(可选)
var nginxLogReFlex = regexp.MustCompile(`^(\S+) - - \[([^\]]+)\] "([^"]*)" (\d{3}) (\d+)(?:\s+"([^"]*)")*(?:\s+([\d.]+))?$`)

// nginxLogParser 封装特定格式的正则与解析逻辑。
type nginxLogParser struct {
	re *regexp.Regexp
	// parse 从匹配分组中提取字段并累加进聚合结构。
	parse func(m []string, ipStats map[string]*ipAgg, statusCount map[string]float64,
		uriCount map[string]float64, totalRequests, totalBytes *float64,
		latencySum, latencyN *float64)
}

// combinedParser 解析 standard combined 日志行。
// 分组：1=addr 2=user 3=time_local 4=request 5=status 6=body_bytes 7=referer 8=ua 9=request_time
func combinedParser(m []string, ipStats map[string]*ipAgg, statusCount map[string]float64,
	uriCount map[string]float64, totalRequests, totalBytes *float64,
	latencySum, latencyN *float64) {
	ip := strings.TrimPrefix(m[1], "::ffff:")
	status := m[5]
	bodyBytes, _ := parseFloatOK(m[6])
	statusCount[status]++
	*totalRequests++
	*totalBytes += bodyBytes
	agg := ipStats[ip]
	if agg == nil {
		agg = &ipAgg{}
		ipStats[ip] = agg
	}
	agg.requests++
	agg.bytes += bodyBytes
	if req := m[4]; req != "" && req != "-" {
		uriCount[extractURI(req)]++
	}
	if m[9] != "" {
		if lt, ok := parseFloatOK(m[9]); ok && lt >= 0 {
			*latencySum += lt
			*latencyN++
		}
	}
}

// combinedTimedParser 解析 combined_timed 日志行（无 referer/ua）。
// 分组：1=addr 2=time_local 3=request 4=status 5=body_bytes 6=request_time
func combinedTimedParser(m []string, ipStats map[string]*ipAgg, statusCount map[string]float64,
	uriCount map[string]float64, totalRequests, totalBytes *float64,
	latencySum, latencyN *float64) {
	ip := strings.TrimPrefix(m[1], "::ffff:")
	status := m[4]
	bodyBytes, _ := parseFloatOK(m[5])
	statusCount[status]++
	*totalRequests++
	*totalBytes += bodyBytes
	agg := ipStats[ip]
	if agg == nil {
		agg = &ipAgg{}
		ipStats[ip] = agg
	}
	agg.requests++
	agg.bytes += bodyBytes
	if req := m[3]; req != "" && req != "-" {
		uriCount[extractURI(req)]++
	}
	if m[6] != "" && m[6] != "-" {
		if lt, ok := parseFloatOK(m[6]); ok && lt >= 0 {
			*latencySum += lt
			*latencyN++
		}
	}
}

// combinedFlexParser 解析上述多种变体（combined / combined_timed / 带额外尾部字段）。
// 仅依赖固定前缀与可选尾部字段，末尾纯数字视为 $request_time。
// 分组：1=addr 2=time_local 3=request 4=status 5=body_bytes 6=request_time
func combinedFlexParser(m []string, ipStats map[string]*ipAgg, statusCount map[string]float64,
	uriCount map[string]float64, totalRequests, totalBytes *float64,
	latencySum, latencyN *float64) {
	ip := strings.TrimPrefix(m[1], "::ffff:")
	status := m[4]
	bodyBytes, _ := parseFloatOK(m[5])
	statusCount[status]++
	*totalRequests++
	*totalBytes += bodyBytes
	agg := ipStats[ip]
	if agg == nil {
		agg = &ipAgg{}
		ipStats[ip] = agg
	}
	agg.requests++
	agg.bytes += bodyBytes
	if req := m[3]; req != "" && req != "-" {
		uriCount[extractURI(req)]++
	}
	if m[6] != "" {
		if lt, ok := parseFloatOK(m[6]); ok && lt >= 0 {
			*latencySum += lt
			*latencyN++
		}
	}
}

// getParser 根据 LogFormat 名称返回对应的解析器。
// 默认回退到 combined（包含 referer/ua 的最常见格式）。
func getParser(format string) *nginxLogParser {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case nginxLogFormatCombinedTimed:
		return &nginxLogParser{re: nginxLogReCombinedTimed, parse: combinedTimedParser}
	default:
		return &nginxLogParser{re: nginxLogReCombined, parse: combinedParser}
	}
}

// nginxLogParserList 返回解析器列表，通吃格式优先，再按配置格式与其余格式兜底。
// 兼容同一 access.log 混合多种日志格式（combined / combined_timed / 带额外尾部字段，
// 常见于不同 server 块或片段使用不同 log_format 却写入同一文件）。
func nginxLogParserList(format string) []*nginxLogParser {
	primary := getParser(format)
	all := []*nginxLogParser{
		{re: nginxLogReFlex, parse: combinedFlexParser}, // 兼容多格式，优先尝试
		primary,
		{re: nginxLogReCombined, parse: combinedParser},
		{re: nginxLogReCombinedTimed, parse: combinedTimedParser},
	}
	seen := map[*regexp.Regexp]bool{}
	out := make([]*nginxLogParser, 0, len(all))
	for _, p := range all {
		if !seen[p.re] {
			seen[p.re] = true
			out = append(out, p)
		}
	}
	return out
}

// ipAgg 单个来源 IP 的周期内聚合。
type ipAgg struct {
	requests float64
	bytes    float64
}

// accessFileState 记录单个 access.log 的增量读取状态。
type accessFileState struct {
	path   string // 日志文件路径
	offset int64  // 上次读取到的字节偏移
}

// NginxAccessCollector 增量解析 Nginx access.log，按 IP/URI/状态码聚合统计。
// 数据不写入时序库（来源 IP 高基数），而是作为 NginxAccessStat 随上报体提交，
// 由 Server 端做地理聚合与低基数指标写入。
type NginxAccessCollector struct {
	node      string
	group     string
	instances []model.NginxInstanceConfig
	files     map[string]*accessFileState // key=normalizeRemoteAddr(addr, "")
	lastAt    time.Time                   // 上次采集时间，用于计算周期秒数
}

// NewNginxAccessCollector 创建 NginxAccessCollector。
// 仅纳管配置了 AccessLog 路径的实例；启动时从文件尾部开始（不回溯历史日志）。
func NewNginxAccessCollector(node, group string, instances []model.NginxInstanceConfig) *NginxAccessCollector {
	c := &NginxAccessCollector{
		node:  node,
		group: group,
		files: make(map[string]*accessFileState),
	}
	skipped := 0
	for _, cfg := range instances {
		if strings.TrimSpace(cfg.AccessLog) == "" {
			// 未配置日志路径的实例无法采集访问日志：显式告警，避免静默无数据难以排查。
			skipped++
			slog.Warn("Nginx 实例未配置 accessLog，跳过访问日志采集",
				"instance", cfg.Name, "addr", cfg.Addr,
				"hint", `在 agent.yaml 的 nginxInstances 下为该实例添加 accessLog: "/var/log/nginx/access.log"`)
			continue
		}
		key := normalizeRemoteAddr(cfg.Addr, "")
		state := &accessFileState{path: cfg.AccessLog}
		if f, err := os.Open(cfg.AccessLog); err == nil {
			if st, err := f.Stat(); err == nil {
				state.offset = st.Size() // 启动不回溯历史日志
			}
			_ = f.Close()
			slog.Info("Nginx 访问日志已纳管",
				"instance", cfg.Name, "path", cfg.AccessLog,
				"startOffset", state.offset, "logFormat", cfg.LogFormat)
		} else {
			slog.Warn("Nginx access.log 打开失败，等待文件出现", "path", cfg.AccessLog, "err", err)
		}
		c.files[key] = state
		c.instances = append(c.instances, cfg)
	}
	if len(c.files) == 0 {
		slog.Warn("collectors.nginxLog 已开启，但没有任何 Nginx 实例配置 accessLog，访问日志采集不会产生数据",
			"nginxInstances", len(instances), "skipped", skipped)
	}
	return c
}

// Collect 读取自上次采集以来的增量日志并聚合统计，返回每实例一条聚合结果。
func (c *NginxAccessCollector) Collect() []model.NginxAccessStat {
	if len(c.files) == 0 {
		return nil
	}
	now := time.Now()
	periodSec := now.Sub(c.lastAt).Seconds()
	c.lastAt = now
	if periodSec <= 0 {
		periodSec = 1
	}
	var stats []model.NginxAccessStat
	for _, cfg := range c.instances {
		key := normalizeRemoteAddr(cfg.Addr, "")
		state, ok := c.files[key]
		if !ok {
			continue
		}
		st := c.collectFile(cfg, state, periodSec)
		if st != nil {
			stats = append(stats, *st)
		}
	}
	return stats
}

// collectFile 读取单个日志文件的增量部分并聚合。
func (c *NginxAccessCollector) collectFile(cfg model.NginxInstanceConfig, state *accessFileState, periodSec float64) *model.NginxAccessStat {
	f, err := os.Open(state.path)
	if err != nil {
		slog.Debug("Nginx access.log 打开失败", "path", state.path, "err", err)
		return nil
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil
	}
	// 文件被轮转（变小）或重建：从头读取（有限制，防洪峰）
	if st.Size() < state.offset {
		state.offset = 0
	}
	if _, err := f.Seek(state.offset, io.SeekStart); err != nil {
		return nil
	}

	ipStats := make(map[string]*ipAgg)
	statusCount := make(map[string]float64)
	uriCount := make(map[string]float64)
	var totalRequests, totalBytes float64
	var latencySum, latencyN float64

	parsers := nginxLogParserList(cfg.LogFormat)
	r := bufio.NewReader(f)
	var readBytes int64
	rows := 0
	for {
		line, err := r.ReadString('\n')
		if err != nil && len(line) > 0 && line[len(line)-1] != '\n' {
			// 末尾残行（日志正在写入，尚无换行）：不解析，offset 不推进该行
			break
		}
		if len(line) == 0 {
			// 读到 EOF 且无内容：不计入行数，避免"无新日志"被误判为"解析失败"
			if err != nil {
				break
			}
			continue
		}
		readBytes += int64(len(line))
		if readBytes > nginxAccessMaxBytes {
			break
		}
		rows++
		if rows > nginxAccessMaxRows {
			break
		}
		// 去除行尾 \r\n 后再匹配：Go 的 regexp（RE2）引擎中 $ 锚点不匹配行尾换行符，
		// 若不裁剪，带 \n 的整行会全部解析失败（误报"日志格式不符"）。
		matchLine := strings.TrimRight(line, "\r\n")
		// 逐行尝试所有已知格式，兼容同一文件混合多种日志格式。
		for _, p := range parsers {
			if m := p.re.FindStringSubmatch(matchLine); m != nil {
				p.parse(m, ipStats, statusCount, uriCount, &totalRequests, &totalBytes, &latencySum, &latencyN)
				break
			}
		}
		if err != nil { // io.EOF 等正常读完
			break
		}
	}
	state.offset += readBytes // 推进到实际读取位置（超限时下次从断点续读）

	if totalRequests == 0 {
		if rows > 0 {
			// 读到了新行却一行都没解析出来：几乎可以断定 log_format 与内置格式不匹配。
			slog.Warn("Nginx access.log 读到新日志但全部解析失败，请检查 nginx log_format 是否为 combined 系列",
				"path", state.path, "rows", rows, "readBytes", readBytes, "logFormat", cfg.LogFormat)
		} else {
			slog.Debug("Nginx access.log 本周期无新增日志", "path", state.path, "offset", state.offset)
		}
		return nil
	}

	// Top IP
	ips := make([]model.IPCount, 0, len(ipStats))
	for ip, agg := range ipStats {
		ips = append(ips, model.IPCount{IP: ip, Requests: agg.requests, Bytes: agg.bytes})
	}
	sort.Slice(ips, func(i, j int) bool { return ips[i].Requests > ips[j].Requests })
	if len(ips) > nginxAccessTopIPs {
		ips = ips[:nginxAccessTopIPs]
	}

	// Top URI
	uris := make([]model.NameCount, 0, len(uriCount))
	for u, cnt := range uriCount {
		uris = append(uris, model.NameCount{Name: u, Count: cnt})
	}
	sort.Slice(uris, func(i, j int) bool { return uris[i].Count > uris[j].Count })
	if len(uris) > nginxAccessTopURIs {
		uris = uris[:nginxAccessTopURIs]
	}

	stat := &model.NginxAccessStat{
		Instance:    normalizeRemoteAddr(cfg.Addr, ""),
		Group:       c.group,
		PeriodSec:   round2(periodSec),
		Requests:    totalRequests,
		Bytes:       totalBytes,
		StatusCount: statusCount,
		TopURIs:     uris,
		TopIPs:      ips,
	}
	if latencyN > 0 {
		stat.AvgLatency = round2(latencySum / latencyN)
	}
	return stat
}

// extractURI 从 "GET /path?query HTTP/1.1" 提取不含查询参数的路径。
func extractURI(request string) string {
	parts := strings.Fields(request)
	if len(parts) < 2 {
		return request
	}
	u := parts[1]
	if idx := strings.Index(u, "?"); idx >= 0 {
		u = u[:idx]
	}
	if u == "" {
		return "/"
	}
	return u
}
