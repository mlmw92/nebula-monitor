// proxy_api.go 提供代理模式状态查询接口。
//
// 由于 Hub 对 Server 完全透明（转发请求与直连无异），Server 无法直接感知有哪些
// Edge 连接。本接口通过查询时序库中的 proxy_* 自监控指标来反映代理节点的运行状态：
// Edge/Hub 代理启动后周期上报 proxy_conn_active / proxy_forward_total 等指标，
// 这些指标带有 node 标签，Server 据此汇总出当前在线的代理节点列表。
package api

import (
	"net/http"
	"strings"
)

// proxyStatusItem 是单个代理节点的状态。
type proxyStatusItem struct {
	Node           string `json:"node"`
	Mode           string `json:"mode"`           // edge | hub
	ConnActive     int64  `json:"connActive"`     // 当前活跃隧道连接数
	ForwardTotal   int64  `json:"forwardTotal"`   // 累计转发请求数
	DroppedTotal   int64  `json:"droppedTotal"`   // 累计丢弃请求数
	ReconnectTotal int64  `json:"reconnectTotal"` // 累计重连次数
	BufferDepth    int64  `json:"bufferDepth"`    // 当前缓冲深度
}

// handleProxyStatus 返回所有上报过 proxy_* 指标的代理节点状态。
// 数据来源：时序库中 proxy_conn_active 等指标的即时查询结果。
func (a *API) handleProxyStatus(w http.ResponseWriter, r *http.Request) {
	// 查询 proxy_conn_active 即时值，获取代理节点列表
	series, err := a.store.QueryAllLatest("proxy_conn_active", nil)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"items":  []proxyStatusItem{},
			"total":  0,
			"error":  "查询代理状态失败: " + err.Error(),
		})
		return
	}

	itemByNode := make(map[string]*proxyStatusItem, len(series))
	items := make([]proxyStatusItem, 0, len(series))
	for _, s := range series {
		node := s.Labels["node"]
		if node == "" && len(s.Points) > 0 {
			// 标签无 node 时用 series 名兜底（一般不会发生）
			continue
		}
		var val int64
		if len(s.Points) > 0 {
			val = int64(s.Points[0].Value)
		}
		mode := s.Labels["mode"]
		if mode == "" {
			mode = "edge"
		}
		it := proxyStatusItem{Node: node, Mode: mode, ConnActive: val}
		items = append(items, it)
		itemByNode[node] = &items[len(items)-1]
	}

	// 补充其他 proxy_* 指标
	mergeSeries := func(metricName string, setter func(*proxyStatusItem, int64)) {
		ss, err := a.store.QueryAllLatest(metricName, nil)
		if err != nil {
			return
		}
		for _, s := range ss {
			node := s.Labels["node"]
			if it, ok := itemByNode[node]; ok && len(s.Points) > 0 {
				setter(it, int64(s.Points[0].Value))
			}
		}
	}
	mergeSeries("proxy_forward_total", func(it *proxyStatusItem, v int64) { it.ForwardTotal = v })
	mergeSeries("proxy_dropped_total", func(it *proxyStatusItem, v int64) { it.DroppedTotal = v })
	mergeSeries("proxy_reconnect_total", func(it *proxyStatusItem, v int64) { it.ReconnectTotal = v })
	mergeSeries("proxy_buffer_depth", func(it *proxyStatusItem, v int64) { it.BufferDepth = v })

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items":   items,
		"total":   len(items),
		"summary": summarizeProxy(items),
	})
}

// summarizeProxy 汇总代理状态。
func summarizeProxy(items []proxyStatusItem) map[string]int64 {
	var edgeActive, hubActive, totalForward, totalDropped, totalReconnect int64
	for _, it := range items {
		if strings.EqualFold(it.Mode, "hub") {
			hubActive++
		} else {
			edgeActive++
		}
		totalForward += it.ForwardTotal
		totalDropped += it.DroppedTotal
		totalReconnect += it.ReconnectTotal
	}
	return map[string]int64{
		"edgeActive":     edgeActive,
		"hubActive":      hubActive,
		"totalForward":   totalForward,
		"totalDropped":   totalDropped,
		"totalReconnect": totalReconnect,
	}
}
