// retention_api.go 提供数据保留策略接口：查看现状、保存策略、立即清理。
//
// 权限：数据保留属系统配置类（与品牌 / 大屏 / 地理库同级），读写均需 system:config。
package api

import (
	"encoding/json"
	"net/http"

	"github.com/nebula/monitor/internal/server/retention"
)

// SetRetention 注入数据保留策略管理器（可选；未注入时接口仍可用，只是展示默认策略）。
func (a *API) SetRetention(m *retention.Manager) { a.retention = m }

// handleRetentionGet 返回保留策略与各类数据的现状（含时序库保留只读探测）。
// GET /api/v1/system/retention
func (a *API) handleRetentionGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.retention.Status())
}

// handleRetentionPut 保存保留策略（落盘并热生效：周期清理在下一轮生效，手动清理立即按新策略执行）。
// PUT /api/v1/system/retention
func (a *API) handleRetentionPut(w http.ResponseWriter, r *http.Request) {
	if a.retention == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "数据保留策略未启用"})
		return
	}
	var cfg retention.Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体解析失败: " + err.Error()})
		return
	}
	if err := a.retention.Save(cfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "保存失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, a.retention.Status())
}

// handleRetentionCleanup 按当前策略立即清理一次，返回各项删除数量。
// POST /api/v1/system/retention/cleanup
func (a *API) handleRetentionCleanup(w http.ResponseWriter, r *http.Request) {
	if a.retention == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "数据保留策略未启用"})
		return
	}
	writeJSON(w, http.StatusOK, a.retention.CleanupNow())
}
