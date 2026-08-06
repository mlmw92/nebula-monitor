package api

import (
	"net/http"

	"github.com/nebula/monitor/internal/server/metrics"
)

// handleMetricsCatalog 返回按分类分组的指标目录（指标自动发现主入口）。
// GET /api/v1/metrics/catalog
func (a *API) handleMetricsCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	catalog := metrics.ListByCategory()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"catalog":   catalog,
		"categories": metrics.SortedCategories(),
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
	node := q.Get("node")

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
		if a.store != nil {
			var labels map[string]string
			if node != "" {
				labels = map[string]string{"node": node}
			}
			if s, err := a.store.QueryInstant(node, m.Name, labels); err == nil && len(s) > 0 {
				active = true
			}
		}
		items = append(items, item{Name: m.Name, Active: active})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items})
}
