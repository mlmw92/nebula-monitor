package collector

import (
	"strings"
	"sync"

	"github.com/shirou/gopsutil/v4/disk"

	"github.com/nebula/monitor/internal/model"
)

// DiskCollector 采集磁盘分区使用率与设备 I/O 速率。
type DiskCollector struct {
	mu     sync.Mutex
	prevIO map[string]disk.IOCountersStat
	prevTs int64
}

// NewDiskCollector 创建 DiskCollector。
func NewDiskCollector() *DiskCollector {
	return &DiskCollector{prevIO: map[string]disk.IOCountersStat{}}
}

// Collect 返回各挂载点使用率与各设备读写速率（字节/秒）。
func (c *DiskCollector) Collect() []model.Metric {
	var out []model.Metric
	for _, d := range CollectDiskStats() {
		labels := map[string]string{"mountpoint": d.Mountpoint, "device": d.Device, "fstype": d.Fstype}
		out = append(out,
			model.Metric{Name: "disk_used_percent", Value: round2(d.UsedPercent), Labels: labels},
			model.Metric{Name: "disk_total", Value: float64(d.Total), Labels: labels},
			model.Metric{Name: "disk_used", Value: float64(d.Used), Labels: labels},
		)
	}

	// 设备 I/O 速率
	ioCounters, err := disk.IOCounters()
	if err == nil {
		now := model.NowMillis()
		c.mu.Lock()
		if c.prevTs > 0 && now > c.prevTs {
			dt := float64(now-c.prevTs) / 1000.0
			for dev, cur := range ioCounters {
				prev, ok := c.prevIO[dev]
				if !ok {
					continue
				}
				readRate := float64(cur.ReadBytes-prev.ReadBytes) / dt
				writeRate := float64(cur.WriteBytes-prev.WriteBytes) / dt
				out = append(out,
					model.Metric{Name: "disk_read_rate", Value: round2(readRate), Labels: map[string]string{"device": dev}},
					model.Metric{Name: "disk_write_rate", Value: round2(writeRate), Labels: map[string]string{"device": dev}},
				)
			}
		}
		c.prevIO = ioCounters
		c.prevTs = now
		c.mu.Unlock()
	}
	return out
}

// CollectDiskStats 返回真实文件系统的容量与使用率，过滤 tmpfs、overlay 等虚拟挂载。
func CollectDiskStats() []model.DiskStat {
	parts, err := disk.Partitions(false)
	if err != nil {
		return nil
	}
	disks := make([]model.DiskStat, 0, len(parts))
	for _, p := range parts {
		if skipPartition(p) {
			continue
		}
		u, e := disk.Usage(p.Mountpoint)
		if e != nil || u.Total == 0 {
			continue
		}
		disks = append(disks, model.DiskStat{
			Mountpoint:  p.Mountpoint,
			Device:      p.Device,
			Fstype:      p.Fstype,
			Total:       u.Total,
			Used:        u.Used,
			Free:        u.Free,
			UsedPercent: round2(u.UsedPercent),
		})
	}
	return disks
}

func skipPartition(p disk.PartitionStat) bool {
	fstype := strings.ToLower(p.Fstype)
	if fstype == "" || skippedFSTypes[fstype] {
		return true
	}
	for _, prefix := range skippedMountPrefixes {
		if p.Mountpoint == prefix || strings.HasPrefix(p.Mountpoint, prefix+"/") {
			return true
		}
	}
	return false
}

var skippedFSTypes = map[string]bool{
	"autofs":      true,
	"binfmt_misc": true,
	"bpf":         true,
	"cgroup":      true,
	"cgroup2":     true,
	"configfs":    true,
	"debugfs":     true,
	"devpts":      true,
	"devtmpfs":    true,
	"efivarfs":    true,
	"fusectl":     true,
	"hugetlbfs":   true,
	"mqueue":      true,
	"nsfs":        true,
	"overlay":     true,
	"proc":        true,
	"pstore":      true,
	"ramfs":       true,
	"securityfs":  true,
	"selinuxfs":   true,
	"squashfs":    true,
	"sysfs":       true,
	"tmpfs":       true,
	"tracefs":     true,
}

var skippedMountPrefixes = []string{
	"/dev",
	"/proc",
	"/run",
	"/sys",
	"/var/lib/docker",
	"/var/lib/kubelet",
}
