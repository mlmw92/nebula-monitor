package api

import (
	"encoding/json"
	"net/http"

	"github.com/nebula/monitor/internal/server/uicfg"
)

// handleUIGet GET /api/v1/ui/settings -> {name, logo}
// 允许匿名访问：登录页与未登录场景也需要展示系统名称与 Logo。
func (a *API) handleUIGet(w http.ResponseWriter, r *http.Request) {
	cfg := a.uiMgr.Get()
	if cfg.Name == "" {
		cfg.Name = "NebulaEye"
	}
	writeJSON(w, http.StatusOK, cfg)
}

// handleUIPut PUT /api/v1/ui/settings {name, logo} -> 持久化品牌配置。
// 写操作受登录鉴权保护（AuthMiddleware 对匿名 GET 放行、PUT 仍要求 token）。
func (a *API) handleUIPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		Logo string `json:"logo"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式错误"})
		return
	}
	if err := a.uiMgr.Save(uicfg.UIConfig{Name: body.Name, Logo: body.Logo}); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": a.uiMgr.Get()})
}
