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
	"github.com/nebula/monitor/internal/server/security"
	"github.com/nebula/monitor/internal/server/storage"
)

//go:embed template.html
var reportFS embed.FS

// oneHourMs 一小时的毫秒数，用于报告时间窗口换算。
const oneHourMs = int64(3600 * 1000)

// ReportType 报告类型。
type ReportType string

const (
	// ReportDaily 日报。
	ReportDaily ReportType = "daily"
	// ReportWeekly 周报。
	ReportWeekly ReportType = "weekly"
	// ReportMonthly 月报。
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

	// 深化指标：网络流量、磁盘 IO、磁盘分区、Top 进程
	NetIfaces  []netIfaceStat `json:"netIfaces"`
	DiskIO     []diskIOStat   `json:"diskIO"`
	Partitions []diskPartStat `json:"partitions"`
	Processes  []procStat     `json:"processes"`
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
	Extra      string             `json:"extra"`
	Status     string             `json:"status"`
	Trend      []linePoint        `json:"trend"`
	Metrics    map[string]float64 `json:"metrics"`
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

	// 增强段：巡检结论/健康评级、环比、安全中心
	Conclusion Conclusion       `json:"conclusion"`
	Comparison *Comparison      `json:"comparison,omitempty"`
	Security   *SecuritySection `json:"security,omitempty"`
}

// netIfaceStat 单网卡流量统计。
type netIfaceStat struct {
	Name     string  `json:"name"`
	RecvMBps float64 `json:"recvMBps"`
	SentMBps float64 `json:"sentMBps"`
	DropPps  float64 `json:"dropPps"`
}

// diskIOStat 单磁盘设备 IO 统计。
type diskIOStat struct {
	Device    string  `json:"device"`
	ReadMBps  float64 `json:"readMBps"`
	WriteMBps float64 `json:"writeMBps"`
	ReadIOPS  float64 `json:"readIOPS"`
	WriteIOPS float64 `json:"writeIOPS"`
}

// diskPartStat 单挂载点使用情况。
type diskPartStat struct {
	Mount   string  `json:"mount"`
	Device  string  `json:"device"`
	Fstype  string  `json:"fstype"`
	UsedPct float64 `json:"usedPct"`
	UsedGB  float64 `json:"usedGB"`
	TotalGB float64 `json:"totalGB"`
}

// procStat 进程资源占用快照。
type procStat struct {
	PID    int32  `json:"pid"`
	Name   string  `json:"name"`
	CPUPct float64 `json:"cpuPct"`
	MemPct float64 `json:"memPct"`
}

// Conclusion 巡检结论与健康评级。
type Conclusion struct {
	Rating         string         `json:"rating"`       // 健康/关注/预警
	RatingClass    string         `json:"ratingClass"`  // healthy/warning/critical
	Score          float64        `json:"score"`        // 综合健康评分
	TotalFindings  int            `json:"totalFindings"`
	SeverityCounts map[string]int `json:"severityCounts"`
	TopRisks       []string       `json:"topRisks"`
	Summary        string         `json:"summary"`
}

// Comparison 与上一周期的环比对比。
type Comparison struct {
	OnlineRate     float64 `json:"onlineRate"`
	PrevOnlineRate float64 `json:"prevOnlineRate"`
	CPUAvg         float64 `json:"cpuAvg"`
	PrevCPUAvg     float64 `json:"prevCPUAvg"`
	MemAvg         float64 `json:"memAvg"`
	PrevMemAvg     float64 `json:"prevMemAvg"`
	DiskMax        float64 `json:"diskMax"`
	PrevDiskMax    float64 `json:"prevDiskMax"`
	HealthScore    float64 `json:"healthScore"`
	PrevHealthScore float64 `json:"prevHealthScore"`
}

// SecuritySection 安全中心巡检发现。
type SecuritySection struct {
	HasData       bool            `json:"hasData"`
	NodeCount     int             `json:"nodeCount"`
	AvgScore      float64         `json:"avgScore"`
	LowScoreNodes int             `json:"lowScoreNodes"`
	CriticalEvents int            `json:"criticalEvents"`
	WarningEvents int             `json:"warningEvents"`
	CVECount      int             `json:"cveCount"`
	RiskNodes     []string             `json:"riskNodes"`
	LowScores     map[string]float64   `json:"lowScores"`
	Events        []model.SecurityEvent `json:"events"`
}

// Generator 巡检报告生成服务。
type Generator struct {
	store    storage.Storage
	nodeMgr  *node.Manager
	secStore *security.Store
	dir      string

	mu      sync.Mutex
	history []ReportMeta
}

// NewGenerator 构造报告生成器。dir 为报告 HTML 存储目录。
func NewGenerator(store storage.Storage, mgr *node.Manager, secStore *security.Store, dir string) *Generator {
	if dir == "" {
		dir = "reports"
	}
	_ = os.MkdirAll(dir, 0o755)
	g := &Generator{store: store, nodeMgr: mgr, secStore: secStore, dir: dir}
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
		// 深化指标：网络流量、磁盘 IO、磁盘分区、Top 进程
		ns.NetIfaces = g.collectNetwork(n.Hostname, startMs, endMs, step)
		ns.DiskIO = g.collectDiskIO(n.Hostname, startMs, endMs, step)
		ns.Partitions = g.collectPartitions(n.Hostname, startMs, endMs)
		if pl := g.nodeMgr.LastPayload(n.Hostname); pl != nil {
			procs := make([]procStat, 0, len(pl.Processes))
			for _, p := range pl.Processes {
				procs = append(procs, procStat{PID: p.PID, Name: p.Name, CPUPct: p.CPU, MemPct: p.Mem})
			}
			sort.Slice(procs, func(i, j int) bool { return procs[i].CPUPct > procs[j].CPUPct })
			if len(procs) > 10 {
				procs = procs[:10]
			}
			ns.Processes = procs
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
	sec := g.collectSecurity(startMs, endMs)
	findings := buildFindings(nodeStats, mw)
	findings = append(findings, buildSecurityFindings(sec)...)
	sevOrder := map[string]int{"critical": 0, "warning": 1, "info": 2}
	sort.SliceStable(findings, func(i, j int) bool {
		return sevOrder[findings[i].Severity] < sevOrder[findings[j].Severity]
	})
	conclusion := g.buildConclusion(nodeStats, findings, sec)
	comparison := g.buildComparison(start, end, nodeStats, summary)

	return reportData{
		Period:     fmt.Sprintf("%s ~ %s", start.Format("2006-01-02 15:04"), end.Format("2006-01-02 15:04")),
		GeneratedAt: time.Now().UnixMilli(),
		Summary:    summary,
		Charts:     charts,
		Nodes:      nodeStats,
		Middleware: mw,
		Findings:   findings,
		Conclusion: conclusion,
		Comparison: comparison,
		Security:   sec,
	}
}

// ---- 主机深化指标采集 ----

// collectNetwork 聚合单主机各网卡的最新收发速率与丢包速率。
func (g *Generator) collectNetwork(node string, startMs, endMs, step int64) []netIfaceStat {
	recv, _ := g.store.QueryAllLatest("network_recv_rate", map[string]string{"node": node})
	sent, _ := g.store.QueryAllLatest("network_sent_rate", map[string]string{"node": node})
	drop, _ := g.store.QueryAllLatest("network_drop_rate", map[string]string{"node": node})
	byIface := map[string]*netIfaceStat{}
	get := func(iface string) *netIfaceStat {
		if iface == "" {
			iface = "eth0"
		}
		if byIface[iface] == nil {
			byIface[iface] = &netIfaceStat{Name: iface}
		}
		return byIface[iface]
	}
	add := func(series []model.Series, kind string) {
		for _, s := range series {
			iface := s.Labels["iface"]
			if iface == "" {
				iface = s.Labels["device"]
			}
			st := get(iface)
			if len(s.Points) > 0 {
				v := s.Points[len(s.Points)-1].Value
				switch kind {
				case "recv":
					st.RecvMBps = v / 1e6
				case "sent":
					st.SentMBps = v / 1e6
				case "drop":
					st.DropPps = v
				}
			}
		}
	}
	add(recv, "recv")
	add(sent, "sent")
	add(drop, "drop")
	out := make([]netIfaceStat, 0, len(byIface))
	for _, st := range byIface {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RecvMBps+out[i].SentMBps > out[j].RecvMBps+out[j].SentMBps
	})
	return out
}

// collectDiskIO 聚合单主机各磁盘设备的最新读写速率与 IOPS。
func (g *Generator) collectDiskIO(node string, startMs, endMs, step int64) []diskIOStat {
	read, _ := g.store.QueryAllLatest("disk_read_rate", map[string]string{"node": node})
	write, _ := g.store.QueryAllLatest("disk_write_rate", map[string]string{"node": node})
	riops, _ := g.store.QueryAllLatest("disk_read_iops", map[string]string{"node": node})
	wiops, _ := g.store.QueryAllLatest("disk_write_iops", map[string]string{"node": node})
	byDev := map[string]*diskIOStat{}
	get := func(dev string) *diskIOStat {
		if dev == "" {
			dev = "sda"
		}
		if byDev[dev] == nil {
			byDev[dev] = &diskIOStat{Device: dev}
		}
		return byDev[dev]
	}
	add := func(series []model.Series, kind string) {
		for _, s := range series {
			dev := s.Labels["device"]
			if dev == "" {
				dev = s.Labels["disk"]
			}
			st := get(dev)
			if len(s.Points) > 0 {
				v := s.Points[len(s.Points)-1].Value
				switch kind {
				case "read":
					st.ReadMBps = v / 1e6
				case "write":
					st.WriteMBps = v / 1e6
				case "riops":
					st.ReadIOPS = v
				case "wiops":
					st.WriteIOPS = v
				}
			}
		}
	}
	add(read, "read")
	add(write, "write")
	add(riops, "riops")
	add(wiops, "wiops")
	out := make([]diskIOStat, 0, len(byDev))
	for _, st := range byDev {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ReadMBps+out[i].WriteMBps > out[j].ReadMBps+out[j].WriteMBps
	})
	return out
}

// collectPartitions 聚合单主机各挂载点的磁盘使用率与容量（按使用率降序取 Top）。
func (g *Generator) collectPartitions(node string, startMs, endMs int64) []diskPartStat {
	pct, _ := g.store.QueryAllLatest("disk_used_percent", map[string]string{"node": node})
	used, _ := g.store.QueryAllLatest("disk_used", map[string]string{"node": node})
	total, _ := g.store.QueryAllLatest("disk_total", map[string]string{"node": node})
	byMount := map[string]*diskPartStat{}
	get := func(mount string) *diskPartStat {
		if mount == "" {
			mount = "/"
		}
		if byMount[mount] == nil {
			byMount[mount] = &diskPartStat{Mount: mount}
		}
		return byMount[mount]
	}
	for _, s := range pct {
		mount := s.Labels["mount"]
		if mount == "" {
			mount = s.Labels["device"]
		}
		st := get(mount)
		st.Device = s.Labels["device"]
		st.Fstype = s.Labels["fstype"]
		if len(s.Points) > 0 {
			st.UsedPct = s.Points[len(s.Points)-1].Value
		}
	}
	for _, s := range used {
		mount := s.Labels["mount"]
		if mount == "" {
			mount = s.Labels["device"]
		}
		st := get(mount)
		if len(s.Points) > 0 {
			st.UsedGB = s.Points[len(s.Points)-1].Value / 1e9
		}
	}
	for _, s := range total {
		mount := s.Labels["mount"]
		if mount == "" {
			mount = s.Labels["device"]
		}
		st := get(mount)
		if len(s.Points) > 0 {
			st.TotalGB = s.Points[len(s.Points)-1].Value / 1e9
		}
	}
	out := make([]diskPartStat, 0, len(byMount))
	for _, st := range byMount {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UsedPct > out[j].UsedPct })
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

// ---- 安全中心聚合 ----

// collectSecurity 汇总巡检周期内的安全基线评分与告警事件。
func (g *Generator) collectSecurity(startMs, endMs int64) *SecuritySection {
	if g.secStore == nil {
		return nil
	}
	baselines := g.secStore.Baselines()
	events := g.secStore.Events(0, "", "")
	sec := &SecuritySection{HasData: len(baselines) > 0 || len(events) > 0}
	var sumScore float64
	for _, b := range baselines {
		sumScore += b.Score
		if b.Score < 70 {
			sec.RiskNodes = append(sec.RiskNodes, b.Node)
			sec.LowScores[b.Node] = b.Score
		}
	}
	if len(baselines) > 0 {
		sec.NodeCount = len(baselines)
		sec.AvgScore = round1(sumScore / float64(len(baselines)))
	}
	sec.LowScoreNodes = len(sec.RiskNodes)
	for _, e := range events {
		if e.Timestamp < startMs || e.Timestamp > endMs {
			continue
		}
		switch e.Severity {
		case model.SeverityCritical:
			sec.CriticalEvents++
		case model.SeverityWarning:
			sec.WarningEvents++
		}
		if strings.Contains(strings.ToLower(e.Category), "cve") || strings.Contains(strings.ToUpper(e.Message), "CVE") {
			sec.CVECount++
		}
		sec.Events = append(sec.Events, e)
	}
	if len(sec.Events) > 20 {
		sec.Events = sec.Events[:20]
	}
	return sec
}

// buildSecurityFindings 将安全中心告警转化为报告发现项。
func buildSecurityFindings(sec *SecuritySection) []finding {
	if sec == nil {
		return nil
	}
	var fs []finding
	for _, e := range sec.Events {
		sev := "info"
		switch e.Severity {
		case model.SeverityCritical:
			sev = "critical"
		case model.SeverityWarning:
			sev = "warning"
		}
		fs = append(fs, finding{
			Severity:   sev,
			Category:   "安全中心",
			Resource:   e.Node,
			Title:      fmt.Sprintf("安全事件：%s", e.Category),
			Detail:     fmt.Sprintf("%s（来源：%s）", e.Message, e.SourceLocation),
			Suggestion: "结合安全中心对应事件的处理建议及时处置，必要时联动封禁或隔离。",
		})
	}
	for node, score := range sec.LowScores {
		fs = append(fs, finding{
			Severity:   "warning",
			Category:   "安全中心",
			Resource:   node,
			Title:      fmt.Sprintf("%s 安全基线评分偏低", node),
			Detail:     fmt.Sprintf("安全基线评分 %d，低于阈值 70，存在配置或漏洞风险。", int(score)),
			Suggestion: "进入安全中心查看基线检查明细，按项修复后重新评估评分。",
		})
	}
	return fs
}

// ---- 巡检结论与环比 ----

// buildConclusion 汇总整体健康评级、各级别发现数、Top 风险与一句话结论。
func (g *Generator) buildConclusion(nodes []nodeStat, findings []finding, sec *SecuritySection) Conclusion {
	c := Conclusion{
		SeverityCounts: map[string]int{"critical": 0, "warning": 0, "info": 0},
		TopRisks:       []string{},
	}
	for _, f := range findings {
		c.SeverityCounts[f.Severity]++
		c.TotalFindings++
	}
	var sumHost float64
	onlineCnt := 0
	for _, n := range nodes {
		if n.Status == "online" {
			sumHost += n.HealthScore
			onlineCnt++
		}
	}
	if onlineCnt > 0 {
		c.Score = round1(sumHost / float64(onlineCnt))
	}
	rating, cls := "健康", "healthy"
	if c.SeverityCounts["critical"] > 0 {
		rating, cls = "预警", "critical"
	} else if c.SeverityCounts["warning"] > 0 {
		rating, cls = "关注", "warning"
	}
	c.Rating, c.RatingClass = rating, cls
	for _, f := range findings {
		if f.Severity == "critical" || f.Severity == "warning" {
			c.TopRisks = append(c.TopRisks, fmt.Sprintf("[%s] %s", f.Resource, f.Title))
		}
		if len(c.TopRisks) >= 5 {
			break
		}
	}
	c.Summary = fmt.Sprintf("本周期共纳管 %d 台主机（在线 %d），发现 %d 项风险（严重 %d / 警告 %d / 提示 %d）。综合健康评分 %.0f 分，整体评级：%s。",
		len(nodes), onlineCnt, c.TotalFindings, c.SeverityCounts["critical"], c.SeverityCounts["warning"], c.SeverityCounts["info"], c.Score, rating)
	return c
}

// windowAgg 计算指定窗口内的主机聚合指标（用于环比）。
func (g *Generator) windowAgg(start, end time.Time) (cpuAvg, memAvg, diskMax, healthAvg, onlineRate float64) {
	nodes := g.nodeMgr.ListNodes()
	if len(nodes) == 0 {
		return
	}
	startMs, endMs, step := start.UnixMilli(), end.UnixMilli(), oneHourMs
	on := 0
	var sCPU, sMem, sDisk, sHealth float64
	for _, n := range nodes {
		ns := nodeStat{Status: n.Status}
		if s, err := g.store.QueryRange(n.Hostname, "cpu_usage", nil, startMs, endMs, step); err == nil {
			ns.CPUAvg, ns.CPUMax = avgMax(s)
		}
		if s, err := g.store.QueryRange(n.Hostname, "mem_used_percent", nil, startMs, endMs, step); err == nil {
			ns.MemAvg, ns.MemMax = avgMax(s)
		}
		if s, err := g.store.QueryRange(n.Hostname, "disk_used_percent", nil, startMs, endMs, step); err == nil {
			ns.DiskAvg, ns.DiskMax = avgMax(s)
		}
		ns.Health, ns.HealthScore = evalHostHealth(ns)
		if n.Status == "online" {
			on++
			sCPU += ns.CPUAvg
			sMem += ns.MemAvg
			sDisk += ns.DiskMax
			sHealth += ns.HealthScore
		}
	}
	onN := on
	if onN == 0 {
		onN = 1
	}
	return sCPU / float64(onN), sMem / float64(onN), sDisk / float64(onN), sHealth / float64(onN), float64(on) / float64(len(nodes)) * 100
}

// buildComparison 对比上一周期（等长窗口）的关键指标。
func (g *Generator) buildComparison(start, end time.Time, nodes []nodeStat, summary summaryStat) *Comparison {
	dur := end.Sub(start)
	prevCPU, prevMem, prevDisk, prevHealth, prevOnline := g.windowAgg(start.Add(-dur), start)
	var sumHealth float64
	online := 0
	for _, n := range nodes {
		if n.Status == "online" {
			sumHealth += n.HealthScore
			online++
		}
	}
	curHealth := 0.0
	if online > 0 {
		curHealth = round1(sumHealth / float64(online))
	}
	curOnline := 0.0
	if summary.Total > 0 {
		curOnline = round1(float64(summary.Online) / float64(summary.Total) * 100)
	}
	return &Comparison{
		OnlineRate:     curOnline,
		PrevOnlineRate: round1(prevOnline),
		CPUAvg:         summary.CPUAvg,
		PrevCPUAvg:     prevCPU,
		MemAvg:         summary.MemAvg,
		PrevMemAvg:     prevMem,
		DiskMax:        summary.DiskMax,
		PrevDiskMax:    prevDisk,
		HealthScore:    curHealth,
		PrevHealthScore: round1(prevHealth),
	}
}

// ---- 中间件采集 ----

// mwDefs 报告所需的中间件指标定义（类型、实例存活指标、负载指标、关键指标、展示名与图标）。
var mwDefs = []mwDef{
	{"redis", "redis_instance_up", "redis_connected_clients", []string{
		"redis_connected_clients", "redis_max_clients", "redis_used_memory_percent",
		"redis_used_memory_bytes", "redis_hit_rate", "redis_cmd_latency_ms", "redis_ops_per_sec",
		"redis_replication_lag_seconds", "redis_evicted_keys", "redis_rejected_connections",
	}, "Redis", "🗄️"},
	{"mysql", "mysql_instance_up", "mysql_threads_connected", []string{
		"mysql_threads_connected", "mysql_max_connections", "mysql_buffer_pool_hit_rate",
		"mysql_queries_per_sec", "mysql_seconds_behind_master", "mysql_slow_queries",
		"mysql_query_latency_ms",
	}, "MySQL", "🛢️"},
	{"postgres", "postgres_instance_up", "postgres_numbackends", []string{
		"postgres_numbackends", "postgres_max_connections", "postgres_cache_hit_ratio",
		"postgres_replication_lag_bytes", "postgres_query_latency_ms",
	}, "PostgreSQL", "🐘"},
	{"nginx", "nginx_instance_up", "nginx_active_connections", []string{
		"nginx_active_connections", "nginx_5xx",
	}, "Nginx", "🌐"},
	{"kafka", "kafka_instance_up", "", []string{
		"kafka_broker_count", "kafka_offline_partitions", "kafka_under_replicated_partitions",
		"kafka_consumer_lag", "kafka_active_controller_count", "kafka_topic_count",
	}, "Kafka", "📨"},
	{"rocketmq", "rocketmq_instance_up", "", []string{
		"rocketmq_broker_count", "rocketmq_message_accumulation", "rocketmq_consumer_lag",
		"rocketmq_topic_count",
	}, "RocketMQ", "🚀"},
	{"mongodb", "mongodb_up", "", []string{
		"mongodb_connections_current", "mongodb_connections_available",
		"mongodb_repl_lag", "mongodb_repl_health", "mongodb_mem_resident_bytes",
	}, "MongoDB", "🍃"},
	{"kubernetes", "k8s_cluster_up", "", []string{
		"k8s_nodes_total", "k8s_nodes_ready", "k8s_pods_running",
		"k8s_pods_pending", "k8s_pods_failed", "k8s_deployments_unhealthy",
	}, "Kubernetes", "☸️"},
	{"docker", "docker_containers_total", "", []string{
		"docker_containers_total", "docker_containers_running", "docker_containers_stopped",
		"docker_images_total",
	}, "Docker", "🐳"},
}

type mwDef struct {
	typ        string
	up         string
	connMetric string
	metrics    []string
	title      string
	icon       string
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
			if d.typ == "docker" {
				// docker 守护进程以容器总数指标存在与否判定存活，避免 0 容器误判离线
				up = true
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
			case "kafka":
				brokers := lv("kafka_broker_count")
				mi.ConnUsed, mi.ConnMax = brokers, brokers
				var kp []string
				if v := lv("kafka_offline_partitions"); v > 0 {
					kp = append(kp, fmt.Sprintf("离线分区 %d", int(v)))
				}
				if v := lv("kafka_under_replicated_partitions"); v > 0 {
					kp = append(kp, fmt.Sprintf("欠副本分区 %d", int(v)))
				}
				if v := lv("kafka_consumer_lag"); v > 0 {
					kp = append(kp, fmt.Sprintf("消费滞后 %.0f", v))
				}
				if v := lv("kafka_active_controller_count"); v != 1 {
					kp = append(kp, fmt.Sprintf("活跃控制器 %d（应为1）", int(v)))
				}
				mi.Extra = strings.Join(kp, "；")
			case "rocketmq":
				brokers := lv("rocketmq_broker_count")
				mi.ConnUsed, mi.ConnMax = brokers, brokers
				var rp []string
				if v := lv("rocketmq_message_accumulation"); v > 0 {
					rp = append(rp, fmt.Sprintf("消息堆积 %d", int(v)))
				}
				if v := lv("rocketmq_consumer_lag"); v > 0 {
					rp = append(rp, fmt.Sprintf("消费滞后 %.0f", v))
				}
				mi.Extra = strings.Join(rp, "；")
			case "mongodb":
				cur := lv("mongodb_connections_current")
				avail := lv("mongodb_connections_available")
				mi.ConnUsed, mi.ConnMax = cur, cur+avail
				var mp []string
				if h := lv("mongodb_repl_health"); h > 0 && h < 1 {
					mp = append(mp, fmt.Sprintf("复制健康 %.2f（异常）", h))
				}
				if v := lv("mongodb_repl_lag"); v > 0 {
					mp = append(mp, fmt.Sprintf("复制延迟 %.1fs", v))
				}
				mi.Extra = strings.Join(mp, "；")
			case "kubernetes":
				nt := lv("k8s_nodes_total")
				nr := lv("k8s_nodes_ready")
				mi.ConnUsed, mi.ConnMax = nr, nt
				var kdp []string
				if nt > 0 && nr < nt {
					kdp = append(kdp, fmt.Sprintf("未就绪节点 %d", int(nt-nr)))
				}
				if v := lv("k8s_pods_failed"); v > 0 {
					kdp = append(kdp, fmt.Sprintf("失败 Pod %d", int(v)))
				}
				if v := lv("k8s_pods_pending"); v > 0 {
					kdp = append(kdp, fmt.Sprintf("Pending Pod %d", int(v)))
				}
				if v := lv("k8s_deployments_unhealthy"); v > 0 {
					kdp = append(kdp, fmt.Sprintf("异常 Deployment %d", int(v)))
				}
				mi.Extra = strings.Join(kdp, "；")
			case "docker":
				total := lv("docker_containers_total")
				running := lv("docker_containers_running")
				mi.ConnUsed, mi.ConnMax = running, total
				var dp []string
				if v := lv("docker_containers_stopped"); v > 0 {
					dp = append(dp, fmt.Sprintf("已停止容器 %d", int(v)))
				}
				mi.Extra = strings.Join(dp, "；")
			}
			mi.Metrics = map[string]float64{}
			for _, m := range d.metrics {
				if v, ok := latest[m][key]; ok {
					mi.Metrics[m] = v
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
	case "kafka":
		if offline := m.Metrics["kafka_offline_partitions"]; offline > 0 {
			fs = append(fs, finding{Severity: "critical", Category: "中间件",
				Resource: name, Title: fmt.Sprintf("%s 存在离线分区", m.Instance),
				Detail: fmt.Sprintf("离线分区数 %d，可能导致数据不可用。", int(offline))})
		}
		if ur := m.Metrics["kafka_under_replicated_partitions"]; ur > 0 {
			fs = append(fs, finding{Severity: "warning", Category: "中间件",
				Resource: name, Title: fmt.Sprintf("%s 存在欠副本分区", m.Instance),
				Detail: fmt.Sprintf("欠副本分区数 %d，副本同步异常。", int(ur))})
		}
		if lag := m.Metrics["kafka_consumer_lag"]; lag > 100000 {
			fs = append(fs, finding{Severity: "warning", Category: "中间件",
				Resource: name, Title: fmt.Sprintf("%s 消费滞后偏高", m.Instance),
				Detail: fmt.Sprintf("消费滞后 %.0f 条，消费能力或下游处理存在瓶颈。", lag)})
		}
		if ctrl := m.Metrics["kafka_active_controller_count"]; ctrl != 1 {
			fs = append(fs, finding{Severity: "critical", Category: "中间件",
				Resource: name, Title: fmt.Sprintf("%s 控制器状态异常", m.Instance),
				Detail: fmt.Sprintf("活跃控制器数 %d（应为 1），集群选主异常。", int(ctrl))})
		}
	case "rocketmq":
		if acc := m.Metrics["rocketmq_message_accumulation"]; acc > 0 {
			fs = append(fs, finding{Severity: "warning", Category: "中间件",
				Resource: name, Title: fmt.Sprintf("%s 消息堆积", m.Instance),
				Detail: fmt.Sprintf("消息堆积 %d 条，消费速率不足。", int(acc))})
		}
		if lag := m.Metrics["rocketmq_consumer_lag"]; lag > 100000 {
			fs = append(fs, finding{Severity: "warning", Category: "中间件",
				Resource: name, Title: fmt.Sprintf("%s 消费滞后偏高", m.Instance),
				Detail: fmt.Sprintf("消费滞后 %.0f 条。", lag)})
		}
	case "mongodb":
		if h := m.Metrics["mongodb_repl_health"]; h > 0 && h < 1 {
			fs = append(fs, finding{Severity: "critical", Category: "中间件",
				Resource: name, Title: fmt.Sprintf("%s 复制健康异常", m.Instance),
				Detail: fmt.Sprintf("复制健康度 %.2f（正常为 1），副本集异常。", h)})
		}
		if lag := m.Metrics["mongodb_repl_lag"]; lag > 30 {
			fs = append(fs, finding{Severity: "warning", Category: "中间件",
				Resource: name, Title: fmt.Sprintf("%s 复制延迟偏高", m.Instance),
				Detail: fmt.Sprintf("复制延迟 %.1fs。", lag)})
		}
		if cur := m.Metrics["mongodb_connections_current"]; cur > 0 {
			avail := m.Metrics["mongodb_connections_available"]
			if cur+avail > 0 && cur/(cur+avail) > 0.9 {
				fs = append(fs, finding{Severity: "warning", Category: "中间件",
					Resource: name, Title: fmt.Sprintf("%s 连接使用率偏高", m.Instance),
					Detail: fmt.Sprintf("当前连接 %d，可用 %d，使用率 %.0f%%。", int(cur), int(avail), cur/(cur+avail)*100)})
			}
		}
	case "kubernetes":
		nt := m.Metrics["k8s_nodes_total"]
		nr := m.Metrics["k8s_nodes_ready"]
		if nt > 0 && nr < nt {
			fs = append(fs, finding{Severity: "warning", Category: "中间件",
				Resource: name, Title: fmt.Sprintf("%s 集群存在未就绪节点", m.Instance),
				Detail: fmt.Sprintf("节点总数 %d，就绪 %d。", int(nt), int(nr))})
		}
		if v := m.Metrics["k8s_pods_failed"]; v > 0 {
			fs = append(fs, finding{Severity: "warning", Category: "中间件",
				Resource: name, Title: fmt.Sprintf("%s 存在失败 Pod", m.Instance),
				Detail: fmt.Sprintf("失败 Pod 数 %d。", int(v))})
		}
		if v := m.Metrics["k8s_deployments_unhealthy"]; v > 0 {
			fs = append(fs, finding{Severity: "warning", Category: "中间件",
				Resource: name, Title: fmt.Sprintf("%s 存在异常 Deployment", m.Instance),
				Detail: fmt.Sprintf("异常 Deployment 数 %d。", int(v))})
		}
	case "docker":
		if v := m.Metrics["docker_containers_stopped"]; v > 0 {
			fs = append(fs, finding{Severity: "info", Category: "中间件",
				Resource: name, Title: fmt.Sprintf("%s 存在已停止容器", m.Instance),
				Detail: fmt.Sprintf("已停止容器 %d 个。", int(v))})
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
		"sevText": func(v interface{}) string {
			switch fmt.Sprintf("%v", v) {
			case "critical":
				return "严重"
			case "warning":
				return "警告"
			case "info":
				return "提示"
			default:
				return fmt.Sprintf("%v", v)
			}
		},
		"sevClass": func(v interface{}) string {
			switch fmt.Sprintf("%v", v) {
			case "critical":
				return "critical"
			case "warning":
				return "warning"
			default:
				return "info"
			}
		},
		"catLabel": func(cat string) string {
			m := map[string]string{
				"ssh_bruteforce":  "SSH 暴力破解",
				"ssh_audit":       "SSH 登录审计",
				"fim":             "文件完整性",
				"process_anomaly": "异常进程",
				"sudo_audit":      "sudo 审计",
				"cat_ban":         "fail2ban 封禁",
			}
			if s, ok := m[cat]; ok {
				return s
			}
			return cat
		},
		"cmpGood": func(cur, prev float64) string {
			d := cur - prev
			absd := d
			if absd < 0 {
				absd = -absd
			}
			if d > 0.0001 {
				return fmt.Sprintf(`<span class="cmp good">▲ %s</span>`, formatNum(absd))
			} else if d < -0.0001 {
				return fmt.Sprintf(`<span class="cmp bad">▼ %s</span>`, formatNum(absd))
			}
			return `<span class="cmp flat">—</span>`
		},
		"cmpBad": func(cur, prev float64) string {
			d := cur - prev
			absd := d
			if absd < 0 {
				absd = -absd
			}
			if d > 0.0001 {
				return fmt.Sprintf(`<span class="cmp bad">▲ %s</span>`, formatNum(absd))
			} else if d < -0.0001 {
				return fmt.Sprintf(`<span class="cmp good">▼ %s</span>`, formatNum(absd))
			}
			return `<span class="cmp flat">—</span>`
		},
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
	// cpuWarn/cpuCrit CPU 使用率告警阈值（警告/紧急，%）。
	cpuWarn, cpuCrit         = 70.0, 85.0
	// memWarn/memCrit 内存使用率告警阈值（警告/紧急，%）。
	memWarn, memCrit         = 80.0, 90.0
	// diskWarn/diskCrit 磁盘使用率告警阈值（警告/紧急，%）。
	diskWarn, diskCrit       = 80.0, 90.0
	// connWarn/connCrit TCP 连接数使用率告警阈值（警告/紧急，%）。
	connWarn, connCrit       = 80.0, 90.0 // 连接数使用率 %
	// hitWarn/hitCrit 缓存命中率低于该值即告警（%，警告/紧急）。
	hitWarn, hitCrit         = 90.0, 80.0 // 命中率低于该值告警（%）
	// redisMemWarn/redisMemCrit Redis 内存使用率告警阈值（警告/紧急，%）。
	redisMemWarn, redisMemCrit = 80.0, 90.0
	// redisLatWarn/redisLatCrit Redis 命令时延告警阈值（警告/紧急，ms）。
	redisLatWarn, redisLatCrit = 5.0, 20.0 // ms
	// dbLatWarn/dbLatCrit 关系型数据库平均语句时延告警阈值（警告/紧急，ms）。
	dbLatWarn, dbLatCrit     = 50.0, 200.0 // ms，关系型数据库平均语句时延
)
