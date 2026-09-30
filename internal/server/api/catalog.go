package api

import (
	"net/http"

	"github.com/nebula/monitor/internal/server/metrics"
)

// handleMetricsCatalog 返回指标目录（指标自动发现主入口），同时给告警规则表单用。
// GET /api/v1/metrics/catalog
//
// 响应里有两份数据，服务的是两类界面：
//   - catalog / categories：按分类原始分组（「指标浏览」页用它逐项浏览与查询）；
//   - alertGroups：带**中文分类名**、且把"不适合设阈值"的指标排到组内最后的分组列表
//     （告警表单的指标选择器用它）。同一次请求返回两者，是为了让"告警里能选的指标"
//     与"指标浏览里看到的"永远是同一份数据——此前告警表单用的是前端硬编码的一张短表，
//     于是 MySQL/Nginx/Kafka/ES/拨测全都不在可选范围内，而没人发现。
func (a *API) handleMetricsCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	catalog := metrics.ListByCategory()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"catalog":     catalog,
		"categories":  metrics.SortedCategories(),
		"alertGroups": metrics.AlertCatalog(),
	})
}

// handleMetricsActive 返回当前「有数据上报」的指标，供前端标记在线状态。
// GET /api/v1/metrics/active?category=&node=
// 实现：遍历目录中该分类（或全部）指标，对每个指标做一次即时查询，
// 有返回序列则标记 active=true。用于区分「已注册但无数据」与「有数据」。
func (a *API) handleMetricsActive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	cat := q.Get("category")
	node, ok := a.metricTarget(w, r, "nodes:read", map[string]string{})
	if !ok {
		return
	}
	p := Principal(r)
	// 受限用户在未指定节点时若没有任何可见节点，直接跳过存储查询，全部标记 inactive。
	skipStorage := node == "" && !a.visibleMetricNodes(p)

	type item struct {
		Name   string `json:"name"`
		Active bool   `json:"active"`
	}
	var items []item
	for _, m := range metrics.List() {
		if cat != "" && string(m.Category) != cat {
			continue
		}
		active := false
		if a.store != nil && !skipStorage {
			// node 已作为 QueryInstant 的独立参数传入，不能再次放进 labels，
			// 否则会生成重复的 node matcher，部分 PromQL 后端会拒绝该查询。
			if s, err := a.store.QueryInstant(node, m.Name, nil); err == nil {
				active = len(a.visibleMetricSeries(p, node, s)) > 0
			}
		}
		items = append(items, item{Name: m.Name, Active: active})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items})
}
