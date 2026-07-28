package collector

import (
	"sync"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"

	"github.com/nebula/monitor/internal/model"
)

// CPUCollector 采集 CPU 使用率（基于 /proc/stat 两次采样增量计算）。
type CPUCollector struct {
	mu        sync.Mutex
	prevTotal float64
	prevIdle  float64
	prevOk    bool
}

// NewCPUCollector 创建 CPUCollector。
func NewCPUCollector() *CPUCollector {
	return &CPUCollector{}
}

// Collect 返回 cpu_usage（总体百分比）、cpu_cores（核数）。
func (c *CPUCollector) Collect() []model.Metric {
	times, err := cpu.Times(false)
	if err != nil || len(times) == 0 {
		return nil
	}
	t := times[0]
	total := t.User + t.System + t.Idle + t.Nice + t.Iowait + t.Irq + t.Softirq + t.Steal
	idle := t.Idle + t.Iowait

	c.mu.Lock()
	defer c.mu.Unlock()

	var usage float64
	if c.prevOk && c.prevTotal > 0 {
		totalDelta := total - c.prevTotal
		idleDelta := idle - c.prevIdle
		if totalDelta > 0 {
			usage = (1 - idleDelta/totalDelta) * 100
			if usage < 0 {
				usage = 0
			}
			if usage > 100 {
				usage = 100
			}
		}
	}
	c.prevTotal, c.prevIdle, c.prevOk = total, idle, true

	cores, _ := cpu.Counts(true)
	return []model.Metric{
		{Name: "cpu_usage", Value: round2(usage)},
		{Name: "cpu_cores", Value: float64(cores)},
	}
}

// CollectHostInfo 采集主机系统与硬件信息。
func CollectHostInfo() model.HostInfo {
	info := model.HostInfo{Disks: CollectDiskStats()}
	if cpuInfo, err := cpu.Info(); err == nil && len(cpuInfo) > 0 {
		info.CPUModel = cpuInfo[0].ModelName
	}
	if cores, err := cpu.Counts(true); err == nil {
		info.CPUCores = cores
	}
	if vm := collectVirtualMemory(); vm != nil {
		info.MemoryTotal = vm.Total
	}
	for _, d := range info.Disks {
		info.DiskTotal += d.Total
		info.DiskUsed += d.Used
	}
	if hi, err := host.Info(); err == nil {
		info.BootTime = int64(hi.BootTime)
	}
	info.OnlineUsers = CollectUsers()
	return info
}

// round2 保留两位小数。
func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}
