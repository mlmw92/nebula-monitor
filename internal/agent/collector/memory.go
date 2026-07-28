package collector

import (
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/nebula/monitor/internal/model"
)

func collectVirtualMemory() *mem.VirtualMemoryStat {
	vm, err := mem.VirtualMemory()
	if err != nil {
		return nil
	}
	return vm
}

// collectMemory 采集内存与交换分区占用。
func collectMemory() []model.Metric {
	var out []model.Metric
	vm := collectVirtualMemory()
	if vm != nil {
		out = append(out,
			model.Metric{Name: "mem_used_percent", Value: round2(vm.UsedPercent)},
			model.Metric{Name: "mem_used_bytes", Value: float64(vm.Used)},
			model.Metric{Name: "mem_total_bytes", Value: float64(vm.Total)},
			model.Metric{Name: "mem_available_bytes", Value: float64(vm.Available)},
		)
	}
	sm, err := mem.SwapMemory()
	if err == nil {
		out = append(out,
			model.Metric{Name: "swap_used_percent", Value: round2(sm.UsedPercent)},
			model.Metric{Name: "swap_used_bytes", Value: float64(sm.Used)},
		)
	}
	return out
}
