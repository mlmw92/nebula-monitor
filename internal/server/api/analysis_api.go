package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/server/analysis"
)

func (a *API) handleAnalysisSummary(w http.ResponseWriter, r *http.Request) {
	if a.analysis == nil {
		http.Error(w, "analysis unavailable", http.StatusServiceUnavailable)
		return
	}
	refresh := r.URL.Query().Get("refresh") == "true"
	window, err := analysisWindow(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	summary := a.analysis.Summary(window, refresh)
	if principal := Principal(r); principal != nil {
		visible := make([]analysis.HostResult, 0, len(summary.Hosts))
		for _, host := range summary.Hosts {
			if principal.CanAccessGroup(host.Group) {
				visible = append(visible, host)
			}
		}
		summary.Hosts = visible
		summary.NodeCount = len(visible)
		summary.ReadyNodeCount, summary.InsufficientNodeCount, summary.RiskCount, summary.AnomalyCount, summary.UrgentCapacityCount = summarizeAnalysisHosts(visible)
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
	window, err := analysisWindow(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	host, ok := a.analysis.Host(name, window, r.URL.Query().Get("refresh") == "true")
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

func analysisWindow(r *http.Request) (time.Duration, error) {
	value := r.URL.Query().Get("windowHours")
	if value == "" {
		return 7 * 24 * time.Hour, nil
	}
	hours, err := strconv.Atoi(value)
	if err != nil || (hours != 24 && hours != 168 && hours != 720) {
		return 0, fmt.Errorf("windowHours must be one of 24, 168, 720")
	}
	return time.Duration(hours) * time.Hour, nil
}

func summarizeAnalysisHosts(hosts []analysis.HostResult) (readyNodeCount, insufficientNodeCount, riskCount, anomalyCount, urgentCapacityCount int) {
	for _, host := range hosts {
		if host.Coverage.Ready {
			readyNodeCount++
		} else {
			insufficientNodeCount++
		}
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
