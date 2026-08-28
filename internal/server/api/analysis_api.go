package api

import (
	"net/http"
	"strings"

	"github.com/nebula/monitor/internal/server/analysis"
)

func (a *API) handleAnalysisSummary(w http.ResponseWriter, r *http.Request) {
	if a.analysis == nil {
		http.Error(w, "analysis unavailable", http.StatusServiceUnavailable)
		return
	}
	refresh := r.URL.Query().Get("refresh") == "true"
	summary := a.analysis.Summary(refresh)
	if principal := Principal(r); principal != nil {
		visible := make([]analysis.HostResult, 0, len(summary.Hosts))
		for _, host := range summary.Hosts {
			if principal.CanAccessGroup(host.Group) {
				visible = append(visible, host)
			}
		}
		summary.Hosts = visible
		summary.NodeCount = len(visible)
		summary.RiskCount, summary.AnomalyCount, summary.UrgentCapacityCount = summarizeAnalysisHosts(visible)
	}
	writeJSON(w, http.StatusOK, summary)
}

func (a *API) handleAnalysisHost(w http.ResponseWriter, r *http.Request) {
	if a.analysis == nil {
		http.Error(w, "analysis unavailable", http.StatusServiceUnavailable)
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" || len(name) > 255 {
		http.Error(w, "invalid node name", http.StatusBadRequest)
		return
	}
	host, ok := a.analysis.Host(name, r.URL.Query().Get("refresh") == "true")
	if !ok {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	if principal := Principal(r); principal != nil && !principal.CanAccessGroup(host.Group) {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, host)
}

func summarizeAnalysisHosts(hosts []analysis.HostResult) (riskCount, anomalyCount, urgentCapacityCount int) {
	for _, host := range hosts {
		if host.Score > 0 {
			riskCount++
		}
		for _, baseline := range host.Baselines {
			if baseline.IsAnomalous {
				anomalyCount++
			}
		}
		for _, forecast := range host.Forecasts {
			if forecast.Status == "urgent" {
				urgentCapacityCount++
			}
		}
	}
	return
}
