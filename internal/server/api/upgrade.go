package api

import (
	"net/http"
)

// 升级包最大 500 MB
const upgradeMaxBytes = 500 << 20

// handleSystemUpgradeUpload 接收 multipart 上传的 upgrade tar.gz。
// 表单字段：
//   - file: tar.gz 文件（必填）
func (a *API) handleSystemUpgradeUpload(w http.ResponseWriter, r *http.Request) {
	if a.upgrader == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "升级功能未启用"})
		return
	}
	task, err := a.upgrader.Upload(r, upgradeMaxBytes)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, task)
}

// handleSystemUpgradeCurrent 返回当前已上传待应用的升级任务（pending）。
func (a *API) handleSystemUpgradeCurrent(w http.ResponseWriter, r *http.Request) {
	if a.upgrader == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"current": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"current": a.upgrader.Current()})
}

// handleSystemUpgradeApply 立即应用升级：备份→替换 server→替换 web→复制新 agent 到 agentBinDir→重启 server。
func (a *API) handleSystemUpgradeApply(w http.ResponseWriter, r *http.Request) {
	if a.upgrader == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "升级功能未启用"})
		return
	}
	operator := r.URL.Query().Get("operator")
	if operator == "" {
		operator = "web"
	}
	task, err := a.upgrader.Apply(operator)
	if err != nil {
		// 服务端即将重启或刚重启，连接可能断开；客户端需轮询 /current 或刷新页面
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"task": task, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, task)
}

// handleSystemUpgradeRollback 回滚到最近一次备份。
func (a *API) handleSystemUpgradeRollback(w http.ResponseWriter, r *http.Request) {
	if a.upgrader == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "升级功能未启用"})
		return
	}
	operator := r.URL.Query().Get("operator")
	if err := a.upgrader.Rollback(operator); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleSystemUpgradeHistory 返回升级历史记录（最新在前）。
func (a *API) handleSystemUpgradeHistory(w http.ResponseWriter, r *http.Request) {
	if a.upgrader == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"history": []string{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"history": a.upgrader.History()})
}