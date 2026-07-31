package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/nebula/monitor/internal/server/config"
)

// handleScreenGet 返回当前数据大屏模块显隐配置。
func (a *API) handleScreenGet(w http.ResponseWriter, r *http.Request) {
	if a.screenMgr == nil {
		writeJSON(w, http.StatusOK, config.DefaultScreenConfig())
		return
	}
	writeJSON(w, http.StatusOK, a.screenMgr.Get())
}

// handleScreenPut 保存数据大屏模块显隐配置。
func (a *API) handleScreenPut(w http.ResponseWriter, r *http.Request) {
	if a.screenMgr == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "大屏配置管理未初始化"})
		return
	}
	var incoming config.ScreenConfig
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体解析失败: " + err.Error()})
		return
	}
	if err := a.screenMgr.Save(incoming); err != nil {
		slog.Error("保存大屏配置失败", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "保存失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "已保存"})
}
