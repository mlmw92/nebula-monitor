package api

import (
	"encoding/json"
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

// handleSystemUpgradeHistory 返回升级历史记录（最新在前）。
func (a *API) handleSystemUpgradeHistory(w http.ResponseWriter, r *http.Request) {
	if a.upgrader == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"history": []string{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"history": a.upgrader.History()})
}

// handleSystemUpgradeArchive 返回已上传（已归档）的升级包列表，供回退到指定版本。
func (a *API) handleSystemUpgradeArchive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "方法不允许"})
		return
	}
	if a.upgrader == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"versions": []string{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"versions": a.upgrader.Archive()})
}

// handleSystemUpgradeRollbackTo 回退到指定的已归档版本。
func (a *API) handleSystemUpgradeRollbackTo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "方法不允许"})
		return
	}
	if a.upgrader == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "升级功能未启用"})
		return
	}
	var body struct {
		Version  string `json:"version"`
		Operator string `json:"operator"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体解析失败: " + err.Error()})
		return
	}
	if body.Version == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少 version 参数"})
		return
	}
	if body.Operator == "" {
		body.Operator = "web"
	}
	task, err := a.upgrader.RollbackTo(body.Version, body.Operator)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"task": task, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, task)
}