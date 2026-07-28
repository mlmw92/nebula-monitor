// Package collector 实现主机指标采集。各采集器独立、可开关，结果统一为 model.Metric。
package collector

import (
	"github.com/shirou/gopsutil/v4/host"

	"github.com/nebula/monitor/internal/agent/config"
	"github.com/nebula/monitor/internal/model"
)

// Collector 聚合各子采集器，按配置开关产出一批指标。
type Collector struct {
	node   string
	group  string
	labels map[string]string
	cfg    config.CollectorToggle

	cpu  *CPUCollector
	disk *DiskCollector
	net  *NetworkCollector
}

// New 创建 Collector。
func New(node, group string, labels map[string]string, cfg config.CollectorToggle) *Collector {
	return &Collector{
		node:   node,
		group:  group,
		labels: labels,
		cfg:    cfg,
		cpu:    NewCPUCollector(),
		disk:   NewDiskCollector(),
		net:    NewNetworkCollector(),
	}
}

// Collect 采集所有启用指标，并填充节点基础信息。
// 返回的 metrics 已带 node 标签；group 由 Server 在写入时补充。
func (c *Collector) Collect() ([]model.Metric, []model.ProcessStat) {
	now := model.NowMillis()
	var metrics []model.Metric
	add := func(name string, value float64, labels map[string]string) {
		m := model.Metric{Node: c.node, Name: name, Value: value, Timestamp: now}
		if labels != nil {
			m.Labels = labels
		}
		metrics = append(metrics, m)
	}

	if c.cfg.CPU {
		for _, m := range c.cpu.Collect() {
			add(m.Name, m.Value, m.Labels)
		}
	}
	if c.cfg.Memory {
		for _, m := range collectMemory() {
			add(m.Name, m.Value, m.Labels)
		}
	}
	if c.cfg.Load {
		for _, m := range collectLoad() {
			add(m.Name, m.Value, m.Labels)
		}
	}
	if c.cfg.Disk {
		for _, m := range c.disk.Collect() {
			add(m.Name, m.Value, m.Labels)
		}
	}
	if c.cfg.Network {
		for _, m := range c.net.Collect() {
			add(m.Name, m.Value, m.Labels)
		}
	}

	var procs []model.ProcessStat
	if c.cfg.Process {
		procs = collectProcessTop()
	}
	return metrics, procs
}

// HostInfo 返回主机静态信息（OS/Arch/IP），用于上报体。
func (c *Collector) HostInfo() (os, arch, ip string) {
	info, err := host.Info()
	if err == nil {
		os = info.OS + " " + info.Platform + " " + info.PlatformVersion
	}
	arch = hostArch()
	ip = primaryIP()
	return
}
