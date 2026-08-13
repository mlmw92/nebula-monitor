package api

import (
	"net/http"
	"strconv"

	"github.com/nebula/monitor/internal/model"
)

// handleSecuritySummary 返回安全态势概览：合规评分均值、事件总数、风险主机数、FIM 变化数。
// GET /api/v1/security/summary
func (a *API) handleSecuritySummary(w http.ResponseWriter, r *http.Request) {
	if a.security == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"score": 0, "eventCount": 0, "riskNodes": 0, "fimChanges": 0, "baselineHosts": 0, "enabled": false,
		})
		return
	}
	s := a.security.Summary()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"score":         s.Score,
		"eventCount":    s.EventCount,
		"riskNodes":     s.RiskNodes,
		"fimChanges":    s.FIMChanges,
		"baselineHosts": s.BaselineHosts,
		"generatedAt":   s.GeneratedAt,
		"enabled":       true,
	})
}

// handleSecurityEvents 返回安全事件列表（按时间倒序），支持 limit/category/node 筛选。
// 返回前对缺 NodeIP 的事件按 e.Node 反查当前节点管理器补 IP，避免历史持久化数据或
// 代理转发场景下「节点」列只展示主机名、不展示服务器 IP。
// GET /api/v1/security/events?limit=50&category=ssh_bruteforce&node=host1
func (a *API) handleSecurityEvents(w http.ResponseWriter, r *http.Request) {
	if a.security == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"events": []model.SecurityEvent{}, "enabled": false})
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	events := a.security.Events(limit, q.Get("category"), q.Get("node"))
	if events == nil {
		events = []model.SecurityEvent{}
	}
	for i := range events {
		if events[i].NodeIP == "" && events[i].Node != "" {
			events[i].NodeIP = a.nodeIP(events[i].Node)
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"events": events, "enabled": true})
}

// handleSecurityBaselines 返回各主机安全基线评分与检查项明细。
// GET /api/v1/security/baselines
func (a *API) handleSecurityBaselines(w http.ResponseWriter, r *http.Request) {
	if a.security == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"baselines": []model.SecurityBaseline{}, "enabled": false})
		return
	}
	baselines := a.security.Baselines()
	if baselines == nil {
		baselines = []model.SecurityBaseline{}
	}
	for i := range baselines {
		if baselines[i].NodeIP == "" && baselines[i].Node != "" {
			baselines[i].NodeIP = a.nodeIP(baselines[i].Node)
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"baselines": baselines, "enabled": true})
}
