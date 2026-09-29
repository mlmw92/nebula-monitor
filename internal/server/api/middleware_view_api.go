package api

import (
	"encoding/json"
	"net/http"

	"github.com/nebula/monitor/internal/server/mwview"
)

// handleMiddlewareViewConfigGET 返回展示配置：
//   - available：类型注册表中的全部类型 key（内置 + 模板派生）
//   - enabled：当前启用清单（空 = 全部展示）
func (a *API) handleMiddlewareViewConfigGET(w http.ResponseWriter, r *http.Request) {
	available := make([]string, 0)
	for _, t := range a.middlewareRegistry().Types() {
		available = append(available, t.Key)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"available": available,
		"enabled":   mwview.Default.Enabled(),
	})
}

// handleMiddlewareViewConfigPUT 保存启用清单（system:config 权限，与品牌/大屏等展示配置同级）。
func (a *API) handleMiddlewareViewConfigPUT(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled []string `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体解析失败"})
		return
	}
	// 过滤掉未知类型，防止前端状态与注册表漂移后写入脏数据
	available := make([]string, 0)
	for _, t := range a.middlewareRegistry().Types() {
		available = append(available, t.Key)
	}
	valid := map[string]bool{}
	for _, k := range available {
		valid[k] = true
	}
	cleaned := make([]string, 0, len(body.Enabled))
	for _, k := range body.Enabled {
		if valid[k] {
			cleaned = append(cleaned, k)
		}
	}
	if err := mwview.Default.Save(cleaned); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "保存失败：" + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "enabled": cleaned})
}
