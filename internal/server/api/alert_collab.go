// alert_collab.go 提供告警协作处置接口：认领 / 指派 / 关闭 / 重新打开 / 评论。
//
// 处置状态只影响「谁在处理」与待处理视图，不改动监控条件的真实 firing 状态，
// 以免影响引擎重启后的状态恢复（见 unacknowledgedAlerts）。
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/server/alert"
)

// alertActionRequest 是各处置接口的公共请求体。
type alertActionRequest struct {
	Rule     string `json:"rule"`
	Host     string `json:"host"`
	Instance string `json:"instance"`
	StartsAt int64  `json:"startsAt"`

	Assignee string `json:"assignee,omitempty"` // 指派给谁（认领时可选）
	Reason   string `json:"reason,omitempty"`   // 关闭原因
	Comment  string `json:"comment,omitempty"`  // 附带评论
}

// decodeAlertAction 解析并校验请求体，同时完成资源范围校验。
// 返回 ok=false 表示已写出响应，调用方应直接返回。
func (a *API) decodeAlertAction(w http.ResponseWriter, r *http.Request) (alertActionRequest, string, bool) {
	var body alertActionRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return body, "", false
	}
	if body.Rule == "" || body.Host == "" || body.StartsAt <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "rule、host、startsAt 为必填项"})
		return body, "", false
	}
	if a.acks == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "告警处置存储未启用"})
		return body, "", false
	}
	// 资源范围：不允许处置范围外节点的告警
	if p := Principal(r); !a.nodeInScope(p, body.Host) {
		a.denyScope(w, r, "alerts:write", a.nodeGroup(body.Host))
		return body, "", false
	}
	user := AuthenticatedUser(r)
	if user == "" {
		user = "anonymous"
	}
	return body, user, true
}

func writeAlertActionError(w http.ResponseWriter, err error) {
	if errors.Is(err, alert.ErrEmptyComment) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "保存告警处置失败"})
}

// respondAlertAction 回写处置结果（含最新记录，前端无需再拉一次列表）。
func respondAlertAction(w http.ResponseWriter, info alert.AckInfo) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "ack": info})
}

// finishAlertAction 处理「可选附带评论」并回写结果。
func (a *API) finishAlertAction(w http.ResponseWriter, body alertActionRequest, user string,
	fn func(*alert.AckInfo)) {
	info, err := a.acks.Apply(body.Rule, body.Host, body.Instance, body.StartsAt, user, fn, body.Comment)
	if err != nil {
		writeAlertActionError(w, err)
		return
	}
	respondAlertAction(w, info)
}

// handleAlertAck 认领一条告警；可同时指派处理人并追加一条评论。
// POST /api/v1/alerts/ack
func (a *API) handleAlertAck(w http.ResponseWriter, r *http.Request) {
	body, user, ok := a.decodeAlertAction(w, r)
	if !ok {
		return
	}
	a.finishAlertAction(w, body, user, func(info *alert.AckInfo) {
		info.Status = alert.StatusAck
		if strings.TrimSpace(body.Assignee) != "" {
			info.Assignee = strings.TrimSpace(body.Assignee)
		} else {
			info.Assignee = user
		}
		if info.AckTime == 0 {
			info.AckTime = time.Now().UnixMilli()
		}
		// 认领把记录从「已关闭」拉回处理中：必须清掉上一条的关闭原因/时间，
		// 否则界面会显示「已认领」却带着旧关闭原因与关闭时间的自相矛盾记录。
		info.CloseReason = ""
		info.CloseTime = 0
	})
}

// handleAlertClose 关闭告警：处置完成或判定为误报，可记录原因。
// POST /api/v1/alerts/close
func (a *API) handleAlertClose(w http.ResponseWriter, r *http.Request) {
	body, user, ok := a.decodeAlertAction(w, r)
	if !ok {
		return
	}
	a.finishAlertAction(w, body, user, func(info *alert.AckInfo) {
		info.Status = alert.StatusClosed
		info.CloseReason = strings.TrimSpace(body.Reason)
		info.CloseTime = time.Now().UnixMilli()
	})
}

// handleAlertReopen 重新打开告警：回到待处理，重新出现在待处理列表与统计中。
// POST /api/v1/alerts/reopen
func (a *API) handleAlertReopen(w http.ResponseWriter, r *http.Request) {
	body, user, ok := a.decodeAlertAction(w, r)
	if !ok {
		return
	}
	a.finishAlertAction(w, body, user, func(info *alert.AckInfo) {
		info.Status = alert.StatusPending
		info.Assignee = ""
		info.CloseReason = ""
		info.CloseTime = 0
	})
}

// handleAlertComment 追加一条协作评论（不改变处置状态）。
// POST /api/v1/alerts/comment
func (a *API) handleAlertComment(w http.ResponseWriter, r *http.Request) {
	body, user, ok := a.decodeAlertAction(w, r)
	if !ok {
		return
	}
	info, err := a.acks.Comment(body.Rule, body.Host, body.Instance, body.StartsAt, user, body.Comment)
	if err != nil {
		writeAlertActionError(w, err)
		return
	}
	respondAlertAction(w, info)
}
