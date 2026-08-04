package collector

import (
	"sync"

	"github.com/shirou/gopsutil/v4/net"

	"github.com/nebula/monitor/internal/model"
)

// NetworkCollector 采集网络接口收发速率（字节/秒）、丢包速率与 TCP 重传速率。
type NetworkCollector struct {
	mu          sync.Mutex
	prevIO      map[string]net.IOCountersStat
	prevTs      int64
	prevRetrans int64
}

// NewNetworkCollector 创建 NetworkCollector。
func NewNetworkCollector() *NetworkCollector {
	return &NetworkCollector{prevIO: map[string]net.IOCountersStat{}}
}

// Collect 返回各网络接口收发速率（字节/秒）、丢包速率，以及系统级 TCP 重传速率。
func (c *NetworkCollector) Collect() []model.Metric {
	counters, err := net.IOCounters(true)
	if err != nil {
		return nil
	}
	// 转成以接口名为键的 map，便于与上一次采样做差分
	curMap := make(map[string]net.IOCountersStat, len(counters))
	for _, s := range counters {
		curMap[s.Name] = s
	}
	now := model.NowMillis()
	var out []model.Metric
	c.mu.Lock()
	defer c.mu.Unlock()

	// 累计字节（用于「总发送 / 总接收」展示；新接口首周期即可上报，无需 prev）
	for iface, cur := range curMap {
		if iface == "lo" {
			continue
		}
		out = append(out,
			model.Metric{Name: "network_recv_total", Value: float64(cur.BytesRecv), Labels: map[string]string{"iface": iface}},
			model.Metric{Name: "network_sent_total", Value: float64(cur.BytesSent), Labels: map[string]string{"iface": iface}},
		)
	}

	if c.prevTs > 0 && now > c.prevTs {
		dt := float64(now-c.prevTs) / 1000.0
		for iface, cur := range curMap {
			// 跳过回环
			if iface == "lo" {
				continue
			}
			prev, ok := c.prevIO[iface]
			if !ok {
				continue
			}
			recvRate := float64(cur.BytesRecv-prev.BytesRecv) / dt
			sentRate := float64(cur.BytesSent-prev.BytesSent) / dt
			dropRate := float64(int64(cur.Dropin+cur.Dropout)-int64(prev.Dropin+prev.Dropout)) / dt
			if dropRate < 0 {
				dropRate = 0
			}
			out = append(out,
				model.Metric{Name: "network_recv_rate", Value: round2(recvRate), Labels: map[string]string{"iface": iface}},
				model.Metric{Name: "network_sent_rate", Value: round2(sentRate), Labels: map[string]string{"iface": iface}},
				model.Metric{Name: "network_drop_rate", Value: round2(dropRate), Labels: map[string]string{"iface": iface}},
			)
		}

		// 系统级 TCP 重传速率（RetransSegs 差分 / 时间）
		if proto, perr := net.ProtoCounters([]string{"tcp"}); perr == nil && len(proto) > 0 {
			if v, ok := proto[0].Stats["RetransSegs"]; ok {
				if c.prevRetrans > 0 {
					retransRate := float64(v-c.prevRetrans) / dt
					if retransRate < 0 {
						retransRate = 0
					}
					out = append(out, model.Metric{Name: "tcp_retransmit_rate", Value: round2(retransRate)})
				}
				c.prevRetrans = v
			}
		}
	}
	c.prevIO = curMap
	c.prevTs = now
	return out
}
