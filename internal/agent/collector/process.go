package collector

import (
	"sort"
	"time"

	"github.com/shirou/gopsutil/v4/process"

	"github.com/nebula/monitor/internal/model"
)

// topN 取资源占用最高的 N 个进程。
const topN = 10

// collectProcessTop 采集资源占用 Top 进程，并返回当前系统进程总数。
// total 为本机全部进程数量（不受 topN 截断影响），用于 process_total 指标。
func collectProcessTop() (top []model.ProcessStat, total int) {
	procs, err := process.Processes()
	if err != nil {
		return nil, 0
	}
	total = len(procs)
	// 预热一次 CPU 采样，使第二次 CPUPercent 能计算时间窗增量（否则首次恒为 0）
	for _, p := range procs {
		_, _ = p.CPUPercent()
	}
	time.Sleep(300 * time.Millisecond)

	stats := make([]model.ProcessStat, 0, len(procs))
	for _, p := range procs {
		name, err := p.Name()
		if err != nil || name == "" {
			name = "unknown"
		}
		cpuPct, _ := p.CPUPercent()
		memPct, _ := p.MemoryPercent()
		stats = append(stats, model.ProcessStat{
			PID:  p.Pid,
			Name: name,
			CPU:  round2(cpuPct),
			Mem:  round2(float64(memPct)),
		})
	}

	byCPU := make([]model.ProcessStat, len(stats))
	copy(byCPU, stats)
	sort.Slice(byCPU, func(i, j int) bool { return byCPU[i].CPU > byCPU[j].CPU })

	top = byCPU
	if len(top) > topN {
		top = top[:topN]
	}
	return top, total
}
