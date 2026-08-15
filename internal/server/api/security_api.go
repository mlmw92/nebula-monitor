package api

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/audit"
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

// handleAuditEvents 返回管理操作审计记录，支持 limit/user/path/category 筛选和 CSV 导出。
func (a *API) handleAuditEvents(w http.ResponseWriter, r *http.Request) {
	if AuthenticatedUser(r) == "" && a.auth.Enabled {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未认证"})
		return
	}
	if a.audit == nil {
		if r.URL.Query().Get("format") == "csv" {
			writeAuditCSV(w, nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"events": []interface{}{}})
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	events := a.audit.ListFiltered(limit, q.Get("user"), q.Get("path"), q.Get("category"))
	if q.Get("format") == "csv" {
		writeAuditCSV(w, events)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"events": events})
}

func writeAuditCSV(w http.ResponseWriter, events []audit.Event) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"audit-events.csv\"")
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"time", "user", "method", "path", "status", "remote_ip", "succeeded", "category", "action", "detail"})
	for _, event := range events {
		_ = writer.Write([]string{
			event.Time.Format(time.RFC3339), event.User, event.Method, event.Path,
			strconv.Itoa(event.Status), event.RemoteIP, strconv.FormatBool(event.Succeeded),
			event.Category, event.Action, event.Detail,
		})
	}
	writer.Flush()
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
	// category / node 归一化：空串或各类「全部」占位值都视为不过滤，返回全部事件。
	// 兼容旧前端：el-select 未选中时 v-model 为 undefined，经 URLSearchParams
	// 序列化成字符串 "undefined"（非空），会使后端按非空 category 过滤而返回空。
	cat := normalizeSecurityFilter(q.Get("category"))
	node := normalizeSecurityFilter(q.Get("node"))
	events := a.security.Events(limit, cat, node)
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
		baselines[i].DisplayName = a.nodeDisplayName(baselines[i].Node)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"baselines": baselines, "enabled": true})
}

// normalizeSecurityFilter 将安全事件/基线查询参数归一化：
// 空串、空白、字符串 "undefined"（旧前端 el-select 未选中时经 URLSearchParams
// 序列化得到），以及各类「全部」占位值，均视为不过滤。
func normalizeSecurityFilter(v string) string {
	switch strings.TrimSpace(v) {
	case "", "undefined", "null", "all", "ALL", "all_categories", "全部", "所有", "*", "any", "none":
		return ""
	}
	return strings.TrimSpace(v)
}
