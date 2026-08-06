package api

import (
	"encoding/json"
	"net/http"

	"github.com/nebula/monitor/internal/server/dashboard"
)

// handleDashboardsList 返回全部自定义看板。
// GET /api/v1/dashboards
func (a *API) handleDashboardsList(w http.ResponseWriter, r *http.Request) {
	if a.dashMgr == nil {
		writeJSON(w, 200, map[string]interface{}{"dashboards": []dashboard.Dashboard{}})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"dashboards": a.dashMgr.List()})
}

// handleDashboardGet 返回单块看板。
// GET /api/v1/dashboards/{id}
func (a *API) handleDashboardGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d, ok := a.dashMgr.Get(id)
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "看板不存在"})
		return
	}
	writeJSON(w, 200, d)
}

// dashboardReq 新建/更新看板请求体。
type dashboardReq struct {
	Name   string          `json:"name"`
	Panels []dashboard.Panel `json:"panels"`
}

// handleDashboardCreate 新建看板。
// POST /api/v1/dashboards
func (a *API) handleDashboardCreate(w http.ResponseWriter, r *http.Request) {
	var req dashboardReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "请求体解析失败: " + err.Error()})
		return
	}
	d, err := a.dashMgr.Create(req.Name, req.Panels)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 201, d)
}

// handleDashboardUpdate 更新看板。
// PUT /api/v1/dashboards/{id}
func (a *API) handleDashboardUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req dashboardReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "请求体解析失败: " + err.Error()})
		return
	}
	if err := a.dashMgr.Update(id, req.Name, req.Panels); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleDashboardDelete 删除看板。
// DELETE /api/v1/dashboards/{id}
func (a *API) handleDashboardDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.dashMgr.Delete(id); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
