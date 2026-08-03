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
	for _, cfg := range instances {
		if strings.TrimSpace(cfg.AccessLog) == "" {
			continue
		}
		key := normalizeRemoteAddr(cfg.Addr, "")
		state := &accessFileState{path: cfg.AccessLog}
		if f, err := os.Open(cfg.AccessLog); err == nil {
			if st, err := f.Stat(); err == nil {
				state.offset = st.Size() // 启动不回溯历史日志
			}
			_ = f.Close()
		} else {
			slog.Warn("Nginx access.log 打开失败，等待文件出现", "path", cfg.AccessLog, "err", err)
		}
		c.files[key] = state
		c.instances = append(c.instances, cfg)
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

	parser := getParser(cfg.LogFormat)
	r := bufio.NewReader(f)
	var readBytes int64
	rows := 0
	for {
		line, err := r.ReadString('\n')
		if err != nil && len(line) > 0 && line[len(line)-1] != '\n' {
			// 末尾残行（日志正在写入，尚无换行）：不解析，offset 不推进该行
			break
		}
		readBytes += int64(len(line))
		if readBytes > nginxAccessMaxBytes {
			break
		}
		rows++
		if rows > nginxAccessMaxRows {
			break
		}
		// 按实例配置的日志格式解析
		if m := parser.re.FindStringSubmatch(line); m != nil {
			parser.parse(m, ipStats, statusCount, uriCount, &totalRequests, &totalBytes, &latencySum, &latencyN)
		}
		if err != nil { // io.EOF 等正常读完
			break
		}
	}
	state.offset += readBytes // 推进到实际读取位置（超限时下次从断点续读）

	if totalRequests == 0 {
		slog.Debug("Nginx access.log 解析结果为空", "path", state.path, "rows", rows, "readBytes", readBytes)
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
