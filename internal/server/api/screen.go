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
	// 刷新间隔校验：未传(0)则沿用默认值；传入则必须是预设档位之一
	if incoming.RefreshInterval != 0 && !config.IsValidScreenRefreshInterval(incoming.RefreshInterval) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "刷新间隔仅支持 10/20/30/60 秒"})
		return
	}
	// 服务器所在地校验：留空表示由 server 自动探测；显式填写则必须是省级行政区
	if incoming.DeployLocation != "" && !config.IsValidDeployLocation(incoming.DeployLocation) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "服务器所在地需为省级行政区名称"})
		return
	}
	if err := a.screenMgr.Save(incoming); err != nil {
		slog.Error("保存大屏配置失败", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "保存失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "已保存"})
}
