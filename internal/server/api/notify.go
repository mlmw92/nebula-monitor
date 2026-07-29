package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/nebula/monitor/internal/server/config"
)

// handleNotifyGet 返回当前通知配置（敏感字段脱敏，不返回明文密码/加签密钥）。
func (a *API) handleNotifyGet(w http.ResponseWriter, r *http.Request) {
	if a.notifyMgr == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "通知管理未初始化"})
		return
	}
	c := a.notifyMgr.Get()
	// 脱敏：不回显明文密码/加签密钥，前端未修改时留空由 PUT 侧保留原值。
	c.Email.Password = ""
	c.DingTalk.Secret = ""
	c.Feishu.Secret = ""
	writeJSON(w, http.StatusOK, c)
}

// handleNotifyPut 保存通知配置并热加载。空敏感字段沿用原值，避免误清空。
func (a *API) handleNotifyPut(w http.ResponseWriter, r *http.Request) {
	if a.notifyMgr == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "通知管理未初始化"})
		return
	}
	var incoming config.NotifyConfig
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体解析失败: " + err.Error()})
		return
	}
	cur := a.notifyMgr.Get()
	// 空敏感字段沿用原值（前端「不修改请留空」场景）。
	if incoming.Email.Password == "" {
		incoming.Email.Password = cur.Email.Password
	}
	if incoming.DingTalk.Secret == "" {
		incoming.DingTalk.Secret = cur.DingTalk.Secret
	}
	if incoming.Feishu.Secret == "" {
		incoming.Feishu.Secret = cur.Feishu.Secret
	}
	if err := a.notifyMgr.Save(incoming); err != nil {
		slog.Error("保存通知配置失败", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "保存失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "已保存并生效"})
}
