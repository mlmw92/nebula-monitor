package api

import (
	"net/http"
)

// handleNotifyTest 触发一封测试邮件，立即返回 SMTP 错误详情，便于用户排查配置/链路问题。
func (a *API) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	if a.engine == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "告警引擎未初始化"})
		return
	}
	if err := a.engine.TestEmail(); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "message": "测试邮件发送成功，请检查收件箱（含垃圾邮件）"})
}

// handleAlertTest 手动触发一条测试告警事件，写入事件存储并广播到所有 WS 客户端。
// channel 非空时仅触发指定通知渠道；为空时按当前 notifiers 全渠道发送。
func (a *API) handleAlertTest(w http.ResponseWriter, r *http.Request) {
	if a.engine == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "告警引擎未初始化"})
		return
	}
	channel := r.URL.Query().Get("channel")
	ev, err := a.engine.TestAlert(channel)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error(), "event": ev})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "event": ev})
}
