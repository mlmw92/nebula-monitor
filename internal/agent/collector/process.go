package collector

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shirou/gopsutil/v4/process"

	"github.com/nebula/monitor/internal/model"
)

// processMaxCount 单次上报的最大进程数量，避免数据量过大。
const processMaxCount = 500

// collectProcessTop 采集所有进程的资源占用快照，按 CPU+内存综合排序后返回 Top N。
// 同时返回系统总进程数。
func collectProcessTop() ([]model.ProcessStat, int) {
	processes, err := process.Processes()
	if err != nil {
		return nil, 0
	}

	total := len(processes)
	var stats []model.ProcessStat

	for _, p := range processes {
		pid := p.Pid

		name, _ := p.Name()
		if name == "" {
			name = fmt.Sprintf("PID-%d", pid)
		}

		cpuPct, _ := p.CPUPercent()
		memPct, _ := p.MemoryPercent()
		memInfo, _ := p.MemoryInfo()
		var memBytes uint64
		if memInfo != nil {
			memBytes = memInfo.RSS
		}

		status, _ := p.Status()
		statusStr := "Unknown"
		if len(status) > 0 {
			statusStr = status[0]
		}

		numThreads, _ := p.NumThreads()

		var fds uint64
		if numFd, err := p.NumFDs(); err == nil {
			fds = uint64(numFd)
		}

		ioCounters, _ := p.IOCounters()
		var readBytes, writeBytes uint64
		if ioCounters != nil {
			readBytes = ioCounters.ReadBytes
			writeBytes = ioCounters.WriteBytes
		}

		cmdline, _ := p.Cmdline()
		if len(cmdline) > 256 {
			cmdline = cmdline[:256] + "..."
		}
		// 将空格分隔的 cmdline 转为可读格式
		cmdline = strings.ReplaceAll(cmdline, "\x00", " ")
		if len(cmdline) > 512 {
			cmdline = cmdline[:512] + "..."
		}

		username, _ := p.Username()
		createTime, _ := p.CreateTime() // 毫秒，转为秒

		stats = append(stats, model.ProcessStat{
			PID:        int32(pid),
			Name:       name,
			CPU:        cpuPct,
			Mem:        float64(memPct),
			MemBytes:   memBytes,
			Status:     statusStr,
			NumThreads: int32(numThreads),
			Fds:        fds,
			ReadBytes:  readBytes,
			WriteBytes: writeBytes,
			Cmdline:    cmdline,
			Username:   username,
			CreateTime: createTime / 1000,
		})
	}

	// 按 CPU + 内存 综合排序（CPU 权重更高）
	sort.Slice(stats, func(i, j int) bool {
		scoreI := stats[i].CPU*100 + stats[i].Mem
		scoreJ := stats[j].CPU*100 + stats[j].Mem
		return scoreI > scoreJ
	})

	// 截取前 N 个
	if len(stats) > processMaxCount {
		stats = stats[:processMaxCount]
	}

	return stats, total
}
