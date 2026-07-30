package report

import (
	_ "embed"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/node"
	"github.com/nebula/monitor/internal/server/storage"
)

//go:embed template.html
var reportTemplate string

// ReportType 报告类型。
type ReportType string

const (
	ReportTypeDaily   ReportType = "daily"
	ReportTypeWeekly  ReportType = "weekly"
	ReportTypeMonthly ReportType = "monthly"
)

// ReportMeta 报告元信息。
type ReportMeta struct {
	ID        string     `json:"id"`
	Type      ReportType `json:"type"`
	GeneratedAt int64    `json:"generatedAt"`
	Period    string     `json:"period"`
}

// Generator 报告生成器。
type Generator struct {
	store   storage.Storage
	nodeMgr *node.Manager
	mu      sync.Mutex
	dir     string
	history []ReportMeta
}

// NewGenerator 创建报告生成器。
func NewGenerator(store storage.Storage, mgr *node.Manager, dir string) *Generator {
	if dir == "" {
		dir = "/var/lib/monitor-server/reports"
	}
	_ = os.MkdirAll(dir, 0o755)
	g := &Generator{store: store, nodeMgr: mgr, dir: dir}
	g.loadHistory()
	return g
}

// Generate 生成报告，返回报告 ID。
func (g *Generator) Generate(rt ReportType) (string, error) {
	now := time.Now()
	var start, end time.Time
	var period string
	switch rt {
	case ReportTypeDaily:
		end = time.Date(now.Year(), now.Month(), now.Day()-1, 23, 59, 59, 0, now.Location())
		start = time.Date(now.Year(), now.Month(), now.Day()-1, 0, 0, 0, 0, now.Location())
		period = start.Format("2006-01-02")
	case ReportTypeWeekly:
		end = now.AddDate(0, 0, -1)
		start = end.AddDate(0, 0, -6)
		period = start.Format("01-02") + " ~ " + end.Format("01-02")
	case ReportTypeMonthly:
		end = now.AddDate(0, 0, -1)
		start = end.AddDate(0, -1, 0)
		period = start.Format("2006-01")
	default:
		return "", fmt.Errorf("unsupported report type: %s", rt)
	}

	data := g.collectData(start, end, period)
	html, err := g.renderHTML(data)
	if err != nil {
		return "", err
	}

	id := "rpt-" + strconv.FormatInt(now.UnixNano(), 36)
	path := filepath.Join(g.dir, id+".html")
	if err := os.WriteFile(path, []byte(html), 0o644); err != nil {
		return "", err
	}

	meta := ReportMeta{
		ID:        id,
		Type:      rt,
		GeneratedAt: model.NowMillis(),
		Period:    period,
	}
	g.mu.Lock()
	g.history = append(g.history, meta)
	g.persistHistoryLocked()
	g.mu.Unlock()

	return id, nil
}

// GetHTML 返回报告 HTML 内容。
func (g *Generator) GetHTML(id string) (string, error) {
	path := filepath.Join(g.dir, id+".html")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// History 返回报告历史列表。
func (g *Generator) History() []ReportMeta {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]ReportMeta, len(g.history))
	copy(out, g.history)
	// 按生成时间倒序
	sort.Slice(out, func(i, j int) bool {
		return out[i].GeneratedAt > out[j].GeneratedAt
	})
	return out
}

// reportData 报告数据。
type reportData struct {
	Period     string
	GeneratedAt string
	Nodes      []nodeStat
	Alerts     alertSummary
}

type nodeStat struct {
	Name      string
	IP        string
	OS        string
	Group     string
	Status    string
	CPUAvg    float64
	CPUMax    float64
	MemAvg    float64
	MemMax    float64
	DiskAvg   float64
}

type alertSummary struct {
	Total    int
	Firing   int
	Resolved int
}

// collectData 聚合报告数据。
func (g *Generator) collectData(start, end time.Time, period string) reportData {
	data := reportData{
		Period:     period,
		GeneratedAt: time.Now().Format("2006-01-02 15:04:05"),
	}
	nodes := g.nodeMgr.ListNodes()
	startMs := start.UnixMilli()
	endMs := end.UnixMilli()
	step := int64(3600 * 1000) // 1 小时步长

	for _, n := range nodes {
		ns := nodeStat{
			Name:   n.Hostname,
			IP:     n.IP,
			OS:     n.OS,
			Group:  n.Group,
			Status: n.Status,
		}
		if series, err := g.store.QueryRange(n.Hostname, "cpu_usage", nil, startMs, endMs, step); err == nil {
			ns.CPUAvg, ns.CPUMax = avgMax(series)
		}
		if series, err := g.store.QueryRange(n.Hostname, "mem_used_percent", nil, startMs, endMs, step); err == nil {
			ns.MemAvg, ns.MemMax = avgMax(series)
		}
		if series, err := g.store.QueryRange(n.Hostname, "disk_used_percent", nil, startMs, endMs, step); err == nil {
			ns.DiskAvg, _ = avgMax(series)
		}
		data.Nodes = append(data.Nodes, ns)
	}
	return data
}

// avgMax 计算序列的平均值和最大值。
func avgMax(series []model.Series) (float64, float64) {
	var sum, max float64
	count := 0
	for _, s := range series {
		for _, p := range s.Points {
			sum += p.Value
			if p.Value > max {
				max = p.Value
			}
			count++
		}
	}
	if count == 0 {
		return 0, 0
	}
	return round2(sum / float64(count)), round2(max)
}

// renderHTML 渲染报告 HTML。
func (g *Generator) renderHTML(data reportData) (string, error) {
	tmpl, err := template.New("report").Parse(reportTemplate)
	if err != nil {
		return "", err
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (g *Generator) loadHistory() {
	// 简化：从文件列表推断历史
	entries, err := os.ReadDir(g.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) < 6 || name[len(name)-5:] != ".html" {
			continue
		}
		id := name[:len(name)-5]
		info, err := e.Info()
		if err != nil {
			continue
		}
		g.history = append(g.history, ReportMeta{
			ID:        id,
			GeneratedAt: info.ModTime().UnixMilli(),
		})
	}
}

func (g *Generator) persistHistoryLocked() {
	// 历史记录通过文件系统推断，无需额外持久化
}

func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}
