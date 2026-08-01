package api

import (
	"encoding/json"
	"net/http"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/alert"
)

// handleInhibitGet 返回当前抑制规则列表。
func (a *API) handleInhibitGet(w http.ResponseWriter, r *http.Request) {
	if a.inhibit == nil {
		writeJSON(w, 200, map[string]any{"rules": []any{}})
		return
	}
	writeJSON(w, 200, map[string]any{"rules": a.inhibit.List()})
}

// handleInhibitPut 持久化抑制规则（热生效，无需重启）。
func (a *API) handleInhibitPut(w http.ResponseWriter, r *http.Request) {
	if a.inhibit == nil {
		http.Error(w, "inhibit disabled", http.StatusServiceUnavailable)
		return
	}
	var rules []alert.InhibitRule
	if err := json.NewDecoder(r.Body).Decode(&rules); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.inhibit.Save(rules); err != nil {
		http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, 200, map[string]any{"status": "ok", "count": len(rules)})
}

// handleGroupingGet 返回分组配置。
func (a *API) handleGroupingGet(w http.ResponseWriter, r *http.Request) {
	if a.grouping == nil {
		writeJSON(w, 200, map[string]any{"enabled": false})
		return
	}
	writeJSON(w, 200, a.grouping.Get())
}

// handleGroupingPut 持久化分组配置并热重建分组器（无需重启）。
func (a *API) handleGroupingPut(w http.ResponseWriter, r *http.Request) {
	if a.grouping == nil {
		http.Error(w, "grouping disabled", http.StatusServiceUnavailable)
		return
	}
	var cfg alert.GroupingConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.grouping.Save(cfg); err != nil {
		http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if a.engine != nil {
		a.engine.SetGrouping(cfg)
	}
	writeJSON(w, 200, map[string]any{"status": "ok", "config": a.grouping.Get()})
}

// handleAlertStats 返回告警统计看板数据：活跃数、抑制数、24h 状态分布、级别分布、Top 规则。
func (a *API) handleAlertStats(w http.ResponseWriter, r *http.Request) {
	active := a.alerts.Active()
	recent := a.alerts.Recent(200)

	bySeverity := map[string]int{"critical": 0, "warning": 0, "info": 0}
	byState := map[string]int{"firing": 0, "resolved": 0}
	firing := 0
	suppressed := 0
	ruleTotals := map[string]*ruleStat{}
	for _, e := range active {
		firing++
		bySeverity[string(e.Severity)]++
		if e.Suppressed {
			suppressed++
		}
	}
	for _, e := range recent {
		if e.State == model.AlertStateFiring {
			byState["firing"]++
		} else if e.State == model.AlertStateResolved {
			byState["resolved"]++
		}
		rs := ruleTotals[e.RuleID]
		if rs == nil {
			rs = &ruleStat{Name: e.RuleName, RuleID: e.RuleID}
			ruleTotals[e.RuleID] = rs
		}
		rs.Total++
		if e.State == model.AlertStateFiring {
			rs.Firing++
		}
	}
	// Top 规则（按总数降序，取前 10）
	top := make([]*ruleStat, 0, len(ruleTotals))
	for _, rs := range ruleTotals {
		top = append(top, rs)
	}
	for i := 0; i < len(top); i++ {
		for j := i + 1; j < len(top); j++ {
			if top[j].Total > top[i].Total {
				top[i], top[j] = top[j], top[i]
			}
		}
	}
	if len(top) > 10 {
		top = top[:10]
	}

	writeJSON(w, 200, map[string]any{
		"firing":     firing,
		"suppressed": suppressed,
		"total":      len(recent),
		"bySeverity": bySeverity,
		"byState":    byState,
		"topRules":   top,
	})
}

type ruleStat struct {
	RuleID string `json:"ruleId"`
	Name   string `json:"name"`
	Total  int    `json:"total"`
	Firing int    `json:"firing"`
}
