package collector

import (
	"github.com/shirou/gopsutil/v4/load"

	"github.com/nebula/monitor/internal/model"
)

// collectLoad 采集系统负载 load1/5/15。
func collectLoad() []model.Metric {
	avg, err := load.Avg()
	if err != nil {
		return nil
	}
	return []model.Metric{
		{Name: "load1", Value: round2(avg.Load1)},
		{Name: "load5", Value: round2(avg.Load5)},
		{Name: "load15", Value: round2(avg.Load15)},
	}
}
