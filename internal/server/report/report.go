package report

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/node"
	"github.com/nebula/monitor/internal/server/storage"
)

//go:embed template.html
var reportFS embed.FS

const oneHourMs = int64(3600 * 1000)

// ReportType 报告类型。
type ReportType string

const (
	ReportDaily   ReportType = "daily"
	ReportWeekly  ReportType = "weekly"
	ReportMonthly ReportType = "monthly"
)

// ReportMeta 报告元数据（用于历史列表）。
type ReportMeta struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Period    string `json:"period"`
	Generated int64  `json:"generatedAt"`
	Path      string `json:"path"`
}

// chartItem 一张概览图表（内联 SVG，以 template.HTML 类型避免被转义）。
type chartItem struct {
	Title string `json:"title"`
	SVG   template.HTML `json:"svg"`
	Kind  string `json:"kind"` // bar / line
}

// summaryStat 巡检概览指标。
type summaryStat struct {
	Total   int     `json:"total"`
	Online  int     `json:"online"`
	Offline int     `json:"offline"`
	CPUAvg  float64 `json:"cpuAvg"`
	CPUMax  float64 `json:"cpuMax"`
	MemAvg  float64 `json:"memAvg"`
	MemMax  float64 `json:"memMax"`
	DiskAvg float64 `json:"diskAvg"`
	DiskMax float64 `json:"diskMax"`
}

// hostTrend 单个主机的资源趋势（结构化，供前端绘制）。
type hostTrend struct {
	Node string      `json:"node"`
	CPU  []linePoint `json:"cpu"`
	Mem  []linePoint `json:"mem"`
	Disk []linePoint `json:"disk"`
}

// nodeStat 单主机巡检明细。
type nodeStat struct {
	Name        string  `json:"name"`
	IP          string  `json:"ip"`
	OS          string  `json:"os"`
	Group       string  `json:"group"`
	Status      string  `json:"status"` // online/offline
	Health      string  `json:"health"` // healthy/warning/critical
	HealthScore float64 `json:"healthScore"`
	CPUAvg      float64 `json:"cpuAvg"`
	CPUMax      float64 `json:"cpuMax"`
	MemAvg      float64 `json:"memAvg"`
	MemMax      float64 `json:"memMax"`
	DiskAvg     float64 `json:"diskAvg"`
	DiskMax     float64 `json:"diskMax"`
	LoadAvg     float64 `json:"loadAvg"`
	LastSeen    int64   `json:"lastSeen"`
}

// mwInstance 单个中间件实例巡检明细，覆盖「连接数 / 响应时间 / 内存使用率 / 命中率」。
type mwInstance struct {
	Type       string      `json:"type"`
	Node       string      `json:"node"`
	Instance   string      `json:"instance"`
	Role       string      `json:"role"`
	Topology   string      `json:"topology"`
	Version    string      `json:"version"`
	Up         bool        `json:"up"`
	ConnUsed   float64     `json:"connUsed"`
	ConnMax    float64     `json:"connMax"`
	ConnPct    float64     `json:"connPct"`
	RespTime   float64     `json:"respTime"`   // 平均响应时间/时延（ms）
	MemPct     float64     `json:"memPct"`     // 内存使用率 %
	MemUsedMB  float64     `json:"memUsedMB"`  // 内存用量（MB）
	HitRate    float64     `json:"hitRate"`    // 命中率 %
	Throughput float64     `json:"throughput"` // 吞吐（ops/s 或 qps）
	Extra      string      `json:"extra"`
	Status     string      `json:"status"`
	Trend      []linePoint `json:"trend"`
}

// finding 一条具体巡检发现：问题描述 + 影响范围 + 修复建议。
type finding struct {
	Severity   string `json:"severity"`
	Category   string `json:"category"`
	Resource   string `json:"resource"`
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	Impact     string `json:"impact"`
	Suggestion string `json:"suggestion"`
}

// reportData 是报告模板的数据模型。
type reportData struct {
	Period     string        `json:"period"`
	GeneratedAt int64        `json:"generatedAt"`
	Summary    summaryStat   `json:"summary"`
	Charts     []chartItem   `json:"charts"`
	Nodes      []nodeStat    `json:"nodes"`
	Middleware []mwInstance  `json:"middleware"`
	Findings   []finding     `json:"findings"`
}

// Generator 巡检报告生成服务。
type Generator struct {
	store   storage.Storage
	nodeMgr *node.Manager
	dir     string

	mu      sync.Mutex
	history []ReportMeta
}

// NewGenerator 构造报告生成器。dir 为报告 HTML 存储目录。
func NewGenerator(store storage.Storage, mgr *node.Manager, dir string) *Generator {
	if dir == "" {
		dir = "reports"
	}
	_ = os.MkdirAll(dir, 0o755)
	g := &Generator{store: store, nodeMgr: mgr, dir: dir}
	g.history = g.loadHistory()
	return g
}

// Generate 生成指定类型的报告，返回报告 ID。
func (g *Generator) Generate(rt ReportType) (string, error) {
	now := time.Now()
	var start, end time.Time
	switch rt {
	case ReportWeekly:
		end = now
		start = now.AddDate(0, 0, -7)
	case ReportMonthly:
		end = now
		start = now.AddDate(0, -1, 0)
	default:
		end = now
		start = now.AddDate(0, 0, -1)
	}
	data := g.collectData(start, end, string(rt))
	html := renderHTML(data)
	id := fmt.Sprintf("%s-%s", string(rt), now.Format("20060102-150405"))
	path := filepath.Join(g.dir, id+".html")
	if err := os.WriteFile(path, []byte(html), 0o644); err != nil {
		return "", err
	}
	meta := ReportMeta{
		ID:        id,
		Type:      string(rt),
		Period:    data.Period,
		Generated: data.GeneratedAt,
		Path:      path,
	}
	g.mu.Lock()
	g.history = append(g.history, meta)
	if len(g.history) > 50 {
		g.history = g.history[len(g.history)-50:]
	}
	g.persistHistoryLocked()
	g.mu.Unlock()
	return id, nil
}

// GetHTML 返回指定报告 ID 的 HTML 内容。
func (g *Generator) GetHTML(id string) (string, error) {
	path := filepath.Join(g.dir, id+".html")
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// History 返回报告历史列表（最新在前）。
func (g *Generator) History() []ReportMeta {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]ReportMeta, len(g.history))
	copy(out, g.history)
	// 倒序：最新在前
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (g *Generator) historyPath() string {
	return filepath.Join(g.dir, "history.json")
}

func (g *Generator) loadHistory() []ReportMeta {
	b, err := os.ReadFile(g.historyPath())
	if err != nil {
		return nil
	}
	var hs []ReportMeta
	if json.Unmarshal(b, &hs) != nil {
		return nil
	}
	return hs
}

func (g *Generator) persistHistoryLocked() {
	b, _ := json.MarshalIndent(g.history, "", "  ")
	_ = os.WriteFile(g.historyPath(), b, 0o644)
}

// ---- 数据收集 ----

func (g *Generator) collectData(start, end time.Time, period string) reportData {
	startMs := start.UnixMilli()
	endMs := end.UnixMilli()
	step := oneHourMs

	nodes := g.nodeMgr.ListNodes()
	online, offline := 0, 0
	for _, n := range nodes {
		if n.Status == "online" {
			online++
		} else {
			offline++
		}
	}

	nodeStats := make([]nodeStat, 0, len(nodes))
	trends := make([]hostTrend, 0, len(nodes))
	var sumCPUAvg, sumMemAvg, sumDiskAvg, sumCPUMax, sumMemMax, sumDiskMax float64

	for _, n := range nodes {
		ns := nodeStat{Name: n.Hostname, IP: n.IP, OS: n.OS, Group: n.Group, Status: n.Status, LastSeen: n.LastSeen}
		var cpuPts, memPts, diskPts []linePoint
		if s, err := g.store.QueryRange(n.Hostname, "cpu_usage", nil, startMs, endMs, step); err == nil {
			ns.CPUAvg, ns.CPUMax = avgMax(s)
			cpuPts = toPoints(s)
		}
		if s, err := g.store.QueryRange(n.Hostname, "mem_used_percent", nil, startMs, endMs, step); err == nil {
			ns.MemAvg, ns.MemMax = avgMax(s)
			memPts = toPoints(s)
		}
		if s, err := g.store.QueryRange(n.Hostname, "disk_used_percent", nil, startMs, endMs, step); err == nil {
			ns.DiskAvg, ns.DiskMax = avgMax(s)
			diskPts = toPoints(s)
		}
		if s, err := g.store.QueryRange(n.Hostname, "load1", nil, startMs, endMs, step); err == nil {
			if v, ok := lastValue(s); ok {
				ns.LoadAvg = v
			}
		}
		ns.Health, ns.HealthScore = evalHostHealth(ns)
		nodeStats = append(nodeStats, ns)
		if n.Status == "online" {
			sumCPUAvg += ns.CPUAvg
			sumMemAvg += ns.MemAvg
			sumDiskAvg += ns.DiskAvg
			sumCPUMax += ns.CPUMax
			sumMemMax += ns.MemMax
			sumDiskMax += ns.DiskMax
		}
		trends = append(trends, hostTrend{Node: n.Hostname, CPU: cpuPts, Mem: memPts, Disk: diskPts})
	}

	onlineN := online
	if onlineN == 0 {
		onlineN = 1
	}
	summary := summaryStat{
		Total:   len(nodes),
		Online:  online,
		Offline: offline,
		CPUAvg:  round1(sumCPUAvg / float64(onlineN)),
		CPUMax:  round1(sumCPUMax / float64(onlineN)),
		MemAvg:  round1(sumMemAvg / float64(onlineN)),
		MemMax:  round1(sumMemMax / float64(onlineN)),
		DiskAvg: round1(sumDiskAvg / float64(onlineN)),
		DiskMax: round1(sumDiskMax / float64(onlineN)),
	}

	mw := g.collectMiddleware(startMs, endMs, step)
	charts := buildCharts(nodeStats, trends)
	findings := buildFindings(nodeStats, mw)

	return reportData{
		Period:     fmt.Sprintf("%s ~ %s", start.Format("2006-01-02 15:04"), end.Format("2006-01-02 15:04")),
		GeneratedAt: time.Now().UnixMilli(),
		Summary:    summary,
		Charts:     charts,
		Nodes:      nodeStats,
		Middleware: mw,
		Findings:   findings,
	}
}

// ---- 中间件采集 ----

var mwDefs = []mwDef{
	{"redis", "redis_instance_up", "redis_connected_clients", []string{
		"redis_connected_clients", "redis_max_clients", "redis_used_memory_percent",
		"redis_used_memory_bytes", "redis_hit_rate", "redis_cmd_latency_ms", "redis_ops_per_sec",
		"redis_replication_lag_seconds", "redis_evicted_keys", "redis_rejected_connections",
	}},
	{"mysql", "mysql_instance_up", "mysql_threads_connected", []string{
		"mysql_threads_connected", "mysql_max_connections", "mysql_buffer_pool_hit_rate",
		"mysql_queries_per_sec", "mysql_seconds_behind_master", "mysql_slow_queries",
		"mysql_query_latency_ms",
	}},
	{"postgres", "postgres_instance_up", "postgres_numbackends", []string{
		"postgres_numbackends", "postgres_max_connections", "postgres_cache_hit_ratio",
		"postgres_replication_lag_bytes", "postgres_query_latency_ms",
	}},
	{"nginx", "nginx_instance_up", "nginx_active_connections", []string{
		"nginx_active_connections", "nginx_5xx",
	}},
}

type mwDef struct {
	typ        string
	up         string
	connMetric string
	metrics    []string
}

func (d mwDef) throughputMetric() string {
	switch d.typ {
	case "redis":
		return "redis_ops_per_sec"
	case "mysql":
		return "mysql_queries_per_sec"
	}
	return ""
}

func (g *Generator) collectMiddleware(startMs, endMs, step int64) []mwInstance {
	var out []mwInstance
	for _, d := range mwDefs {
		upSeries, err := g.store.QueryAllLatest(d.up, nil)
		if err != nil || len(upSeries) == 0 {
			continue
		}
		latest := map[string]map[string]float64{}
		for _, m := range d.metrics {
			latest[m] = latestByInstance(g.store, m)
		}
		tp := d.throughputMetric()
		for _, s := range upSeries {
			node := s.Labels["node"]
			inst := s.Labels["instance"]
			up := false
			if len(s.Points) > 0 {
				up = s.Points[len(s.Points)-1].Value > 0
			}
			key := node + "|" + inst
			mi := mwInstance{
				Type:     d.typ,
				Node:     node,
				Instance: inst,
				Role:     s.Labels["role"],
				Topology: s.Labels["topology"],
				Version:  s.Labels["version"],
				Up:       up,
			}
			lv := func(m string) float64 { return latest[m][key] }
			mi.ConnUsed = lv(d.connMetric)
			if tp != "" {
				mi.Throughput = lv(tp)
			}
			switch d.typ {
			case "redis":
				mi.ConnMax = lv("redis_max_clients")
				mi.MemPct = lv("redis_used_memory_percent")
				mi.MemUsedMB = lv("redis_used_memory_bytes") / 1e6
				mi.HitRate = lv("redis_hit_rate")
				mi.RespTime = lv("redis_cmd_latency_ms")
				if lag := lv("redis_replication_lag_seconds"); lag > 0 {
					mi.Extra = fmt.Sprintf("主从复制延迟 %.1fs", lag)
				}
		case "mysql":
			mi.ConnMax = lv("mysql_max_connections")
			mi.HitRate = lv("mysql_buffer_pool_hit_rate")
			mi.RespTime = lv("mysql_query_latency_ms")
			slaveLag := lv("mysql_seconds_behind_master")
			slow := lv("mysql_slow_queries")
			var parts []string
			if slaveLag > 0 {
				parts = append(parts, fmt.Sprintf("主从延迟 %.1fs", slaveLag))
			}
			if slow > 0 {
				parts = append(parts, fmt.Sprintf("慢查询 %d", int(slow)))
			}
			mi.Extra = strings.Join(parts, "；")
		case "postgres":
			mi.ConnMax = lv("postgres_max_connections")
			mi.HitRate = lv("postgres_cache_hit_ratio")
			mi.RespTime = lv("postgres_query_latency_ms")
			if lag := lv("postgres_replication_lag_bytes"); lag > 0 {
				mi.Extra = fmt.Sprintf("复制延迟 %.0fB", lag)
			}
			case "nginx":
				mi.ConnUsed = lv("nginx_active_connections")
				if c5 := lv("nginx_5xx"); c5 > 0 {
					mi.Extra = fmt.Sprintf("5xx 响应 %d", int(c5))
				}
			}
			if mi.ConnMax > 0 {
				mi.ConnPct = round1(mi.ConnUsed / mi.ConnMax * 100)
			}
			mi.Status = evalMwStatus(mi)
			if up && node != "" {
				mi.Trend = rangePoints(g.store, node, d.connMetric, inst, startMs, endMs, step)
			}
			out = append(out, mi)
		}
	}
	return out
}

// ---- 健康评估 ----

func evalHostHealth(ns nodeStat) (string, float64) {
	score := 100.0
	if ns.CPUMax >= cpuCrit {
		score -= 30
	} else if ns.CPUMax >= cpuWarn {
		score -= 15
	}
	if ns.MemMax >= memCrit {
		score -= 30
	} else if ns.MemMax >= memWarn {
		score -= 12
	}
	if ns.DiskMax >= diskCrit {
		score -= 25
	} else if ns.DiskMax >= diskWarn {
		score -= 10
	}
	if ns.LoadAvg >= 8 {
		score -= 20
	} else if ns.LoadAvg >= 4 {
		score -= 8
	}
	if ns.Status != "online" {
		score -= 40
	}
	if score < 0 {
		score = 0
	}
	switch {
	case score < 60:
		return "critical", round1(score)
	case score < 85:
		return "warning", round1(score)
	default:
		return "healthy", round1(score)
	}
}

func evalMwStatus(mi mwInstance) string {
	if !mi.Up {
		return "offline"
	}
	worst := "healthy"
	downgrade := func(to string) {
		if to == "critical" {
			worst = "critical"
		} else if to == "warning" && worst != "critical" {
			worst = "warning"
		}
	}
	switch mi.Type {
	case "redis":
		if mi.MemPct >= redisMemCrit {
			downgrade("critical")
		} else if mi.MemPct >= redisMemWarn {
			downgrade("warning")
		}
		if mi.HitRate > 0 && mi.HitRate < hitCrit {
			downgrade("critical")
		} else if mi.HitRate > 0 && mi.HitRate < hitWarn {
			downgrade("warning")
		}
		if mi.RespTime >= redisLatCrit {
			downgrade("critical")
		} else if mi.RespTime >= redisLatWarn {
			downgrade("warning")
		}
	case "mysql", "postgres":
		if mi.ConnPct >= connCrit {
			downgrade("critical")
		} else if mi.ConnPct >= connWarn {
			downgrade("warning")
		}
		if mi.HitRate > 0 && mi.HitRate < hitCrit {
			downgrade("critical")
		} else if mi.HitRate > 0 && mi.HitRate < hitWarn {
			downgrade("warning")
		}
		if mi.RespTime >= dbLatCrit {
			downgrade("critical")
		} else if mi.RespTime >= dbLatWarn {
			downgrade("warning")
		}
	}
	return worst
}

// ---- 图表构建 ----

func buildCharts(nodes []nodeStat, trends []hostTrend) []chartItem {
	var charts []chartItem
	{
		var items []barItem
		for _, n := range nodes {
			items = append(items, barItem{Label: n.Name, Value: n.CPUMax, Color: statusColor(n.Health)})
		}
		charts = append(charts, chartItem{
			Title: "各主机 CPU 峰值使用率（%）",
			Kind:  "bar",
			SVG:   template.HTML(barChart("各主机 CPU 峰值使用率（%）", items, "%", 560, 260)),
		})
	}
	{
		cnt := map[string]int{"healthy": 0, "warning": 0, "critical": 0, "offline": 0}
		for _, n := range nodes {
			if n.Status != "online" {
				cnt["offline"]++
				continue
			}
			cnt[n.Health]++
		}
		items := []barItem{
			{Label: "健康", Value: float64(cnt["healthy"]), Color: statusColor("healthy")},
			{Label: "关注", Value: float64(cnt["warning"]), Color: statusColor("warning")},
			{Label: "预警", Value: float64(cnt["critical"]), Color: statusColor("critical")},
			{Label: "离线", Value: float64(cnt["offline"]), Color: "#909399"},
		}
		charts = append(charts, chartItem{
			Title: "主机健康状态分布",
			Kind:  "bar",
			SVG:   template.HTML(barChart("主机健康状态分布", items, "台", 560, 240)),
		})
	}
	for _, t := range trends {
		if len(t.CPU) == 0 && len(t.Mem) == 0 && len(t.Disk) == 0 {
			continue
		}
		series := []lineSeries{
			{Name: "CPU", Color: "#409eff", Points: t.CPU},
			{Name: "内存", Color: "#e6a23c", Points: t.Mem},
			{Name: "磁盘", Color: "#f56c6c", Points: t.Disk},
		}
		charts = append(charts, chartItem{
			Title: "节点 " + t.Node + " 资源使用趋势（%）",
			Kind:  "line",
			SVG:   template.HTML(lineChart("节点 "+t.Node+" 资源使用趋势（%）", series, "%", 560, 260)),
		})
	}
	return charts
}

func statusColor(status string) string {
	switch status {
	case "healthy":
		return "#67c23a"
	case "warning":
		return "#e6a23c"
	case "critical":
		return "#f56c6c"
	case "offline":
		return "#909399"
	default:
		return "#409eff"
	}
}

// ---- 发现项构建 ----

func buildFindings(nodes []nodeStat, mw []mwInstance) []finding {
	var fs []finding
	sevRank := map[string]int{"critical": 0, "warning": 1, "info": 2}

	for _, n := range nodes {
		if n.Status != "online" {
			fs = append(fs, finding{
				Severity:   "critical",
				Category:   "主机资源",
				Resource:   n.Name,
				Title:      "主机离线",
				Detail:     fmt.Sprintf("主机 %s（%s）在巡检周期内状态为离线，最近一次上报时间为 %s，期间无监控数据回传。", n.Name, n.IP, relTime(n.LastSeen)),
				Impact:     "该主机上的所有业务与中间件指标不可见，发生故障时无法及时感知，存在监控盲区。",
				Suggestion: "检查该主机的 nebula-agent 进程是否存活（systemctl status nebula-agent）、主机网络连通性与到 Server 的链路；若已主动下线请确认维护窗口配置。",
			})
			continue
		}
		if n.CPUMax >= cpuWarn {
			sev := "warning"
			if n.CPUMax >= cpuCrit {
				sev = "critical"
			}
			fs = append(fs, finding{
				Severity: sev,
				Category: "主机资源",
				Resource: n.Name,
				Title:    "CPU 使用率偏高",
				Detail:   fmt.Sprintf("主机 %s 巡检周期内 CPU 平均 %.1f%%、峰值 %.1f%%，多个采样点处于高位。", n.Name, n.CPUAvg, n.CPUMax),
				Impact:   "CPU 持续高位会导致进程调度延迟、请求排队与响应变慢，极端情况下触发进程超时或连锁雪崩。",
				Suggestion: "登录该机使用 `top`/`pidstat -u 1` 定位高 CPU 进程，排查异常批处理或死循环；必要时垂直扩容 CPU 或水平扩容；建议对 cpu_usage 配置 70%%/85%% 阈值告警提前预警。",
			})
		}
		if n.MemMax >= memWarn {
			sev := "warning"
			if n.MemMax >= memCrit {
				sev = "critical"
			}
			fs = append(fs, finding{
				Severity: sev,
				Category: "主机资源",
				Resource: n.Name,
				Title:    "内存使用率偏高",
				Detail:   fmt.Sprintf("主机 %s 巡检周期内内存平均 %.1f%%、峰值 %.1f%%。", n.Name, n.MemAvg, n.MemMax),
				Impact:   "内存接近上限会触发系统 OOM Killer 随机终止进程，造成服务中断与数据不一致。",
				Suggestion: "使用 `free -h`/`smem` 排查内存占用最大的进程，确认是否存在内存泄漏；调优应用堆/JVM 参数或容器内存限制；必要时扩容内存并配置 mem_usage 告警。",
			})
		}
		if n.DiskMax >= diskWarn {
			sev := "warning"
			if n.DiskMax >= diskCrit {
				sev = "critical"
			}
			fs = append(fs, finding{
				Severity: sev,
				Category: "主机资源",
				Resource: n.Name,
				Title:    "磁盘使用率偏高",
				Detail:   fmt.Sprintf("主机 %s 巡检周期内磁盘使用率平均 %.1f%%、峰值 %.1f%%，剩余可用空间受限。", n.Name, n.DiskAvg, n.DiskMax),
				Impact:   "磁盘写满将导致日志/数据无法落盘、数据库写入失败、应用异常甚至宕机。",
				Suggestion: "使用 `df -h`/`du -sh` 定位大目录，清理过期日志与临时文件、归档冷数据；对核心挂载配置 disk_usage 阈值告警并规划磁盘扩容。",
			})
		}
		if n.LoadAvg >= 1 {
			fs = append(fs, finding{
				Severity: "warning",
				Category: "主机资源",
				Resource: n.Name,
				Title:    "系统负载偏高",
				Detail:   fmt.Sprintf("主机 %s 最近系统负载 load1 为 %.2f，处于较高水平。", n.Name, n.LoadAvg),
				Impact:   "高负载通常意味着 CPU 或 IO 资源出现瓶颈，系统吞吐下降、请求排队。",
				Suggestion: "结合 CPU/磁盘 IO 指标定位瓶颈来源（计算密集型或 IO 等待），针对性扩容或优化；负载持续高位时核查 top 中 D 状态（IO 等待）进程。",
			})
		}
	}

	for _, m := range mw {
		name := fmt.Sprintf("%s/%s", m.Type, m.Instance)
		if !m.Up {
			fs = append(fs, finding{
				Severity: "critical",
				Category: "中间件",
				Resource: name,
				Title:    "中间件实例离线",
				Detail:   fmt.Sprintf("中间件实例 %s（类型 %s，节点 %s）探活失败，当前处于离线状态。", m.Instance, m.Type, m.Node),
				Impact:   "依赖该实例的业务功能受影响，可能出现缓存/查询失败或降级。",
				Suggestion: "检查实例进程状态与端口监听、网络连通性与访问凭证；若为复制集群请确认主从同步状态；恢复后关注连接数、命中率是否回到正常水平。",
			})
			continue
		}
		switch m.Type {
		case "redis":
			if m.HitRate > 0 && m.HitRate < hitWarn {
				sev := "warning"
				if m.HitRate < hitCrit {
					sev = "critical"
				}
				fs = append(fs, finding{
					Severity: sev,
					Category: "中间件",
					Resource: name,
					Title:    "Redis 缓存命中率偏低",
					Detail:   fmt.Sprintf("Redis 实例 %s 命中率仅 %.1f%%，未命中请求将回源至后端存储。", m.Instance, m.HitRate),
					Impact:   "命中率下降会显著增加后端数据库压力，并使依赖缓存的接口响应变慢。",
					Suggestion: "检查 maxmemory 是否过小导致频繁驱逐；确认淘汰策略（建议 allkeys-lru）；排查大 key/热 key 与业务 key 设计；监控 redis_evicted_keys 与 rejected_connections。",
				})
			}
			if m.MemPct >= redisMemWarn {
				sev := "warning"
				if m.MemPct >= redisMemCrit {
					sev = "critical"
				}
				fs = append(fs, finding{
					Severity: sev,
					Category: "中间件",
					Resource: name,
					Title:    "Redis 内存使用率偏高",
					Detail:   fmt.Sprintf("Redis 实例 %s 内存使用率 %.1f%%（约 %.0fMB），接近 maxmemory 上限。", m.Instance, m.MemPct, m.MemUsedMB),
					Impact:   "内存接近上限会触发 key 驱逐甚至拒绝写入，命中率下降并可能出现写入失败。",
					Suggestion: "适当上调 maxmemory（确保宿主机内存余量充足）；清理无效/过期数据；设置合理淘汰策略；持续监控 used_memory 与 evicted_keys。",
				})
			}
			if m.RespTime >= redisLatWarn {
				sev := "warning"
				if m.RespTime >= redisLatCrit {
					sev = "critical"
				}
				fs = append(fs, finding{
					Severity: sev,
					Category: "中间件",
					Resource: name,
					Title:    "Redis 命令响应时间偏高",
					Detail:   fmt.Sprintf("Redis 实例 %s 命令平均响应时间 %.2fms，高于常态水平。", m.Instance, m.RespTime),
					Impact:   "命令时延升高会使调用方超时、链路整体变慢，影响上游业务 RT。",
					Suggestion: "排查慢命令（如 keys *、大 key、复杂 Lua）；检查网络与持久化阻塞（AOF/RDB fork）；对热 key 做本地/客户端缓存分散压力。",
				})
			}
		case "mysql":
			if m.ConnPct >= connWarn {
				sev := "warning"
				if m.ConnPct >= connCrit {
					sev = "critical"
				}
				fs = append(fs, finding{
					Severity: sev,
					Category: "中间件",
					Resource: name,
					Title:    "MySQL 连接数使用率偏高",
					Detail:   fmt.Sprintf("MySQL 实例 %s 连接数使用率 %.1f%%（%.0f/%.0f），接近 max_connections 上限。", m.Instance, m.ConnPct, m.ConnUsed, m.ConnMax),
					Impact:   "连接耗尽会导致新连接被拒绝（Too many connections），应用报错或无法建立数据库连接。",
					Suggestion: "排查并优化连接池配置与空闲连接回收；定位长期占用连接的事务/慢查询；必要时调大 max_connections（受系统 ulimit 与内存约束）；配置连接数阈值告警。",
				})
			}
			if m.HitRate > 0 && m.HitRate < hitWarn {
				sev := "warning"
				if m.HitRate < hitCrit {
					sev = "critical"
				}
				fs = append(fs, finding{
					Severity: sev,
					Category: "中间件",
					Resource: name,
					Title:    "MySQL InnoDB 缓冲池命中率偏低",
					Detail:   fmt.Sprintf("MySQL 实例 %s InnoDB 缓冲池命中率 %.1f%%，低于推荐值。", m.Instance, m.HitRate),
					Impact:   "缓冲池命中率下降会增加磁盘 IO，查询延迟上升，数据库整体吞吐受限。",
				Suggestion: "适当增大 innodb_buffer_pool_size（建议不超过物理内存的 75%）；排查全表扫描与大结果集查询；结合慢查询日志优化索引。",
			})
			}
			if m.RespTime >= dbLatWarn {
				sev := "warning"
				if m.RespTime >= dbLatCrit {
					sev = "critical"
				}
				fs = append(fs, finding{
					Severity: sev,
					Category: "中间件",
					Resource: name,
					Title:    "MySQL 平均语句响应时间偏高",
					Detail:   fmt.Sprintf("MySQL 实例 %s 平均语句响应时间 %.2fms，高于常态水平。", m.Instance, m.RespTime),
					Impact:   "SQL 时延升高会拖慢调用方 RT，高并发下引发请求堆积与超时，影响上游业务。",
					Suggestion: "结合 performance_schema.events_statements_summary_by_digest 定位高耗时 SQL 类型；优化索引与执行计划；排查锁等待、全表扫描与临时表落盘；必要时扩容或读写分离。",
				})
			}
		case "postgres":
			if m.ConnPct >= connWarn {
				sev := "warning"
				if m.ConnPct >= connCrit {
					sev = "critical"
				}
				fs = append(fs, finding{
					Severity: sev,
					Category: "中间件",
					Resource: name,
					Title:    "PostgreSQL 连接数使用率偏高",
					Detail:   fmt.Sprintf("PostgreSQL 实例 %s 连接数使用率 %.1f%%（%.0f/%.0f）。", m.Instance, m.ConnPct, m.ConnUsed, m.ConnMax),
					Impact:   "连接接近上限会造成新连接被拒绝，应用出现连接获取失败。",
					Suggestion: "优化连接池（如 pgbouncer）与空闲连接回收；排查长事务；必要时调大 max_connections；配置连接数阈值告警。",
				})
			}
			if m.HitRate > 0 && m.HitRate < hitWarn {
				sev := "warning"
				if m.HitRate < hitCrit {
					sev = "critical"
				}
				fs = append(fs, finding{
					Severity: sev,
					Category: "中间件",
					Resource: name,
					Title:    "PostgreSQL 缓存命中率偏低",
					Detail:   fmt.Sprintf("PostgreSQL 实例 %s 缓存命中率 %.1f%%。", m.Instance, m.HitRate),
					Impact:   "缓存命中率下降增加磁盘读取，查询性能下降。",
				Suggestion: "适当增大 shared_buffers；排查大表顺序扫描；结合 pg_stat_statements 优化高频 SQL 与索引。",
			})
			}
			if m.RespTime >= dbLatWarn {
				sev := "warning"
				if m.RespTime >= dbLatCrit {
					sev = "critical"
				}
				fs = append(fs, finding{
					Severity: sev,
					Category: "中间件",
					Resource: name,
					Title:    "PostgreSQL 平均语句响应时间偏高",
					Detail:   fmt.Sprintf("PostgreSQL 实例 %s 平均语句响应时间 %.2fms，高于常态水平。", m.Instance, m.RespTime),
					Impact:   "SQL 时延升高会拖慢调用方 RT，高并发下引发请求堆积与超时，影响上游业务。",
					Suggestion: "结合 pg_stat_statements 定位高耗时 SQL 与执行计划；优化索引、避免全表扫描与顺序扫描；排查锁等待与长事务；必要时扩容或读写分离。",
				})
			}
		}
		if strings.Contains(m.Extra, "主从延迟") || strings.Contains(m.Extra, "复制延迟") {
			fs = append(fs, finding{
				Severity: "warning",
				Category: "中间件",
				Resource: name,
				Title:    "复制延迟",
				Detail:   fmt.Sprintf("中间件实例 %s 存在复制延迟：%s。", m.Instance, m.Extra),
				Impact:   "读从库可能读到陈旧数据，存在数据一致性风险；延迟持续扩大可能引发复制中断。",
				Suggestion: "排查主库写入压力与从库硬件/IO 瓶颈，确认复制线程状态（SHOW REPLICA STATUS / pg_stat_replication）与网络带宽；对强一致读改走主库。",
			})
		}
	}

	sort.SliceStable(fs, func(i, j int) bool {
		return sevRank[fs[i].Severity] < sevRank[fs[j].Severity]
	})
	return fs
}

// ---- 工具函数 ----

func avgMax(series []model.Series) (avg, max float64) {
	var sum float64
	var cnt int
	for _, s := range series {
		for _, p := range s.Points {
			sum += p.Value
			cnt++
			if p.Value > max {
				max = p.Value
			}
		}
	}
	if cnt > 0 {
		avg = sum / float64(cnt)
	}
	return round1(avg), round1(max)
}

func lastValue(series []model.Series) (float64, bool) {
	for _, s := range series {
		if len(s.Points) > 0 {
			return s.Points[len(s.Points)-1].Value, true
		}
	}
	return 0, false
}

func toPoints(series []model.Series) []linePoint {
	if len(series) == 0 {
		return nil
	}
	pts := series[0].Points
	out := make([]linePoint, 0, len(pts))
	for _, p := range pts {
		out = append(out, linePoint{T: p.Timestamp, V: p.Value})
	}
	return out
}

func latestByInstance(store storage.Storage, metric string) map[string]float64 {
	out := map[string]float64{}
	series, err := store.QueryAllLatest(metric, nil)
	if err != nil {
		return out
	}
	for _, s := range series {
		node := s.Labels["node"]
		inst := s.Labels["instance"]
		if node == "" && inst == "" {
			continue
		}
		key := node + "|" + inst
		if len(s.Points) > 0 {
			out[key] = s.Points[len(s.Points)-1].Value
		}
	}
	return out
}

func rangePoints(store storage.Storage, node, metric, instance string, startMs, endMs, step int64) []linePoint {
	labels := map[string]string{}
	if instance != "" {
		labels["instance"] = instance
	}
	series, err := store.QueryRange(node, metric, labels, startMs, endMs, step)
	if err != nil || len(series) == 0 {
		return nil
	}
	return toPoints(series)
}

func relTime(ms int64) string {
	if ms == 0 {
		return "未知"
	}
	d := time.Now().UnixMilli() - ms
	if d < 0 {
		d = 0
	}
	switch {
	case d < 60*1000:
		return "刚刚"
	case d < 60*60*1000:
		return fmt.Sprintf("%.0f 分钟前", float64(d)/60000)
	case d < 24*60*60*1000:
		return fmt.Sprintf("%.0f 小时前", float64(d)/3600000)
	default:
		return fmt.Sprintf("%.0f 天前", float64(d)/86400000)
	}
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round2(v float64) float64 { return math.Round(v*100) / 100 }

// renderHTML 将报告数据渲染为完整 HTML。
func renderHTML(data reportData) string {
	funcs := template.FuncMap{
		"fmtTime": func(ms int64) string {
			if ms == 0 {
				return "-"
			}
			return time.UnixMilli(ms).Format("2006-01-02 15:04:05")
		},
		"statusLabel": func(s string) string {
			switch s {
			case "healthy":
				return "健康"
			case "warning":
				return "关注"
			case "critical":
				return "预警"
			case "offline":
				return "离线"
			default:
				return s
			}
		},
		"sevLabel": func(s string) string {
			switch s {
			case "critical":
				return "严重"
			case "warning":
				return "警告"
			case "info":
				return "提示"
			default:
				return s
			}
		},
		"f1": func(v float64) string { return formatNum(v) },
		"spark": func(pts []linePoint) template.HTML {
			if len(pts) == 0 {
				return template.HTML("")
			}
			return template.HTML(sparkline(pts, "#409eff", 120, 34))
		},
	}
	tmplBytes, err := reportFS.ReadFile("template.html")
	if err != nil {
		return fmt.Sprintf("<html><body><pre>模板读取失败: %v</pre></body></html>", err)
	}
	tmpl := template.Must(template.New("report").Funcs(funcs).Parse(string(tmplBytes)))
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Sprintf("<html><body><pre>报告渲染失败: %v</pre></body></html>", err)
	}
	return buf.String()
}

// ---- 阈值常量 ----

const (
	cpuWarn, cpuCrit         = 70.0, 85.0
	memWarn, memCrit         = 80.0, 90.0
	diskWarn, diskCrit       = 80.0, 90.0
	connWarn, connCrit       = 80.0, 90.0 // 连接数使用率 %
	hitWarn, hitCrit         = 90.0, 80.0 // 命中率低于该值告警（%）
	redisMemWarn, redisMemCrit = 80.0, 90.0
	redisLatWarn, redisLatCrit = 5.0, 20.0 // ms
	dbLatWarn, dbLatCrit     = 50.0, 200.0 // ms，关系型数据库平均语句时延
)
