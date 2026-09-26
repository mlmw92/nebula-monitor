package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/logstore"
)

// 集中日志检索（C2 子批次 C）：GET /api/v1/logs?from&to&q&regex&nodes&sources&limit&cursor
//
// 权限：logs:read + 节点分组范围。日志内容比指标敏感，因此范围过滤**在扫描前**收窄节点集合，
// 而不是「查完再过滤结果」——后者既浪费扫描预算，也会通过「明明有 N 条却只显示 M 条」泄露别人的日志量。

// SetLogStore 注入集中日志存储（未注入时检索接口返回 503，与管理端「该能力未启用」一致）。
func (a *API) SetLogStore(s *logstore.Store) { a.logs = s }

// handleLogsQuery 处理集中日志检索。
func (a *API) handleLogsQuery(w http.ResponseWriter, r *http.Request) {
	if a.logs == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "集中日志未启用（server.yaml 的 logDir）"})
		return
	}
	q, err := parseLogQuery(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	cursor, err := logstore.DecodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// 资源范围：非全局用户只能看自己分组内的节点。
	if p := Principal(r); p != nil && !p.Scope.IsGlobal() {
		allowed := map[string]bool{}
		for _, n := range a.visibleNodes(a.nodeMgr.ListNodes(), p) {
			allowed[n.Hostname] = true
		}
		nodes := q.Nodes
		if len(nodes) == 0 {
			// 未指定节点：限定为可见节点全集（空 = 无可见节点 → 直接空结果，绝不退化成「不限」）
			for name := range allowed {
				nodes = append(nodes, name)
			}
			if len(nodes) == 0 {
				writeJSON(w, http.StatusOK, model.LogQueryResult{Lines: []model.LogHit{}})
				return
			}
		} else {
			nodes = nodes[:0]
			for _, n := range q.Nodes {
				if allowed[n] {
					nodes = append(nodes, n)
				}
			}
			// 请求的节点全在范围外 → 空结果（而不是报错：报错会暴露「这些节点存在且我没权限」）
			if len(nodes) == 0 {
				writeJSON(w, http.StatusOK, model.LogQueryResult{Lines: []model.LogHit{}})
				return
			}
		}
		q.Nodes = nodes
	}

	res, err := a.logs.Query(q, cursor)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// parseLogQuery 解析并校验查询参数。
//
// 默认时间范围取最近 1 小时：日志量远大于指标，不设默认范围等于让用户「一进来就全表扫」。
func parseLogQuery(r *http.Request) (model.LogQuery, error) {
	qv := r.URL.Query()
	now := time.Now().UnixMilli()
	q := model.LogQuery{To: now, From: now - int64(time.Hour/time.Millisecond)}
	if v := strings.TrimSpace(qv.Get("to")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return q, fmt.Errorf("to 需为毫秒时间戳")
		}
		q.To = n
	}
	if v := strings.TrimSpace(qv.Get("from")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return q, fmt.Errorf("from 需为毫秒时间戳")
		}
		q.From = n
	}
	if q.From > q.To {
		return q, fmt.Errorf("from 不能晚于 to")
	}

	q.Keyword = qv.Get("q")
	if len(q.Keyword) > logstore.MaxKeywordLen {
		return q, fmt.Errorf("关键词过长（上限 %d 字符）", logstore.MaxKeywordLen)
	}
	q.Regex = qv.Get("regex")
	if len(q.Regex) > logstore.MaxRegexLen {
		// RE2 无回溯风险，但扫描代价随模式长度增长，而模式来自外部输入
		return q, fmt.Errorf("正则过长（上限 %d 字符）", logstore.MaxRegexLen)
	}
	if q.Regex != "" {
		if _, err := regexp.Compile(q.Regex); err != nil {
			return q, fmt.Errorf("正则非法：%v", err)
		}
	}

	q.Nodes = splitCSV(qv.Get("nodes"))
	q.Sources = splitCSV(qv.Get("sources"))

	q.Limit = logstore.DefaultLimit
	if v := strings.TrimSpace(qv.Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return q, fmt.Errorf("limit 需为整数")
		}
		// 夹紧而不是报错：前端传个奇怪的值不该让查询失败
		if n <= 0 {
			n = logstore.DefaultLimit
		}
		if n > logstore.MaxLimit {
			n = logstore.MaxLimit
		}
		q.Limit = n
	}
	return q, nil
}

// splitCSV 解析逗号分隔的多值参数（去空、去重前的简单形态）。
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
