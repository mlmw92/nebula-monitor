package report

import (
	"strings"
	"testing"
)

func TestRenderHTMLSmoke(t *testing.T) {
	charts := buildCharts(
		[]nodeStat{
			{Name: "node-a", Status: "online", Health: "healthy", CPUMax: 42, HealthScore: 96},
			{Name: "node-b", Status: "online", Health: "warning", CPUMax: 78, HealthScore: 70},
		},
		[]hostTrend{
			{Node: "node-a", CPU: []linePoint{{T: 1, V: 10}, {T: 2, V: 20}}, Mem: []linePoint{{T: 1, V: 30}}},
		},
	)
	if len(charts) == 0 {
		t.Fatal("buildCharts returned nothing")
	}
	for _, c := range charts {
		if !strings.Contains(string(c.SVG), "<svg") {
			t.Fatalf("chart %q SVG missing <svg>", c.Title)
		}
	}

	data := reportData{
		Period:     "2026-08-02 00:00 ~ 2026-08-03 00:00",
		GeneratedAt: 1754179200000,
		Summary: summaryStat{Total: 2, Online: 2, Offline: 0, CPUAvg: 40, CPUMax: 60, MemAvg: 50, MemMax: 70, DiskAvg: 30, DiskMax: 55},
		Charts:     charts,
		Nodes: []nodeStat{
			{Name: "node-a", IP: "10.0.0.1", Group: "g1", Status: "online", Health: "healthy", HealthScore: 96, CPUAvg: 30, CPUMax: 42, MemAvg: 40, MemMax: 55, DiskAvg: 20, DiskMax: 30, LoadAvg: 1.2, LastSeen: 1754179200000},
			{Name: "node-b", IP: "10.0.0.2", Group: "g1", Status: "online", Health: "warning", HealthScore: 70, CPUAvg: 60, CPUMax: 78, MemAvg: 70, MemMax: 82, DiskAvg: 40, DiskMax: 60, LoadAvg: 5.1, LastSeen: 1754179200000},
		},
		Middleware: []mwInstance{
			{Type: "redis", Node: "node-a", Instance: "cache-1", Role: "master", Topology: "单机", Up: true, ConnUsed: 120, ConnMax: 1000, ConnPct: 12, RespTime: 0.8, MemPct: 45, MemUsedMB: 256, HitRate: 98.5, Throughput: 12000, Status: "healthy", Trend: []linePoint{{T: 1, V: 100}, {T: 2, V: 130}}},
			{Type: "redis", Node: "node-b", Instance: "cache-2", Up: true, ConnUsed: 900, ConnMax: 1000, ConnPct: 90, RespTime: 12, MemPct: 92, MemUsedMB: 900, HitRate: 75, Throughput: 5000, Status: "critical", Trend: []linePoint{{T: 1, V: 800}, {T: 2, V: 900}}},
		},
		Findings: []finding{
			{Severity: "critical", Category: "中间件", Resource: "redis/cache-2", Title: "Redis 内存使用率偏高", Detail: "实例 cache-2 内存使用率 92%。", Impact: "命中率下降。", Suggestion: "上调 maxmemory。"},
			{Severity: "warning", Category: "主机资源", Resource: "node-b", Title: "CPU 使用率偏高", Detail: "node-b 峰值 78%。", Impact: "响应变慢。", Suggestion: "排查高 CPU 进程。"},
		},
	}

	html := renderHTML(data)
	if !strings.Contains(html, "<svg") {
		t.Fatal("rendered HTML missing <svg>")
	}
	if !strings.Contains(html, "巡检发现") {
		t.Fatal("rendered HTML missing findings section")
	}
	if !strings.Contains(html, "中间件监控明细") {
		t.Fatal("rendered HTML missing middleware section")
	}
	if !strings.Contains(html, "cache-2") {
		t.Fatal("rendered HTML missing middleware instance")
	}
	if strings.Contains(html, "{{") || strings.Contains(html, "}}") {
		t.Fatal("rendered HTML contains unprocessed template tokens")
	}
	t.Logf("rendered html length=%d", len(html))
}
