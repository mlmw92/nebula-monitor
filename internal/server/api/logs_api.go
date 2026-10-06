package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/logparse"
	"github.com/nebula/monitor/internal/server/logstore"
)

// 集中日志检索（C2 子批次 C）：GET /api/v1/logs?from&to&q&regex&nodes&sources&limit&cursor
//
// 权限：logs:read + 节点分组范围。日志内容比指标敏感，因此范围过滤**在扫描前**收窄节点集合，
// 而不是「查完再过滤结果」——后者既浪费扫描预算，也会通过「明明有 N 条却只显示 M 条」泄露别人的日志量。

// SetLogStore 注入集中日志存储（未注入时检索接口返回 503，与管理端「该能力未启用」一致）。
//
// 参数是接口：调用方传 nil **具体类型**（例如未配置目录的 *Store）会得到"非 nil 接口"，
// 上面的 503 判断随即失效——注入前必须显式判空（见 cmd/server 的写法）。
func (a *API) SetLogStore(s logstore.LogStore) { a.logs = s }

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
		// 外部后端不可用要回 502：运维看到 400 会去改查询条件，
		// 而真实情况是"日志后端连不上"——错误码指错方向比不给错误码更费时间。
		if errors.Is(err, logstore.ErrBackendUnavailable) {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
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

	fields, err := parseLogFieldFilters(qv["field"])
	if err != nil {
		return q, err
	}
	q.Fields = fields

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

// MaxLogFieldFilters 是一次查询允许携带的字段过滤条件数上限。
// 字段过滤是**逐行**判断的，条件越多每条扫描行的代价越大；8 个足够表达常见的排障条件。
const MaxLogFieldFilters = 8

// parseLogFieldFilters 解析结构化字段过滤（`field=key:value`，可重复出现）。
//
// 形态与台账的标签筛选（`label=key:value`）一致：同一个平台不该有两套写法。
// 键走 model.IsValidLogFieldName（与写入侧同一条规则）；值有长度上限——
// 字段值在落盘时已被截断到该长度，更长的查询值永远不可能命中，必须明确报错，
// 否则用户会得到一个"查不到但也不报错"的结果。
func parseLogFieldFilters(raw []string) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		key, value, ok := strings.Cut(item, ":")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || key == "" || value == "" {
			return nil, fmt.Errorf("field 需为 key:value 形态（如 field=status:500）")
		}
		if !model.IsValidLogFieldName(key) {
			return nil, fmt.Errorf("字段名 %q 不合法", key)
		}
		if len(value) > logparse.MaxFieldValueBytes {
			return nil, fmt.Errorf("字段值过长（上限 %d 字节；字段值在落盘时已按此上限截断）", logparse.MaxFieldValueBytes)
		}
		if len(out) >= MaxLogFieldFilters {
			return nil, fmt.Errorf("字段过滤条件过多（上限 %d 个）", MaxLogFieldFilters)
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// handleLogFields 返回各来源已见过的结构化字段名（供界面做筛选候选）。
//
// 只列服务端真的见过、且还在基数上限内的名字：列"可能存在的字段"会让用户
// 按一个永远查不到的名字去筛，然后怀疑功能坏了。
func (a *API) handleLogFields(w http.ResponseWriter, r *http.Request) {
	if a.logs == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "集中日志未启用（server.yaml 的 logDir）"})
		return
	}
	sources := splitCSV(r.URL.Query().Get("sources"))
	if len(sources) == 0 {
		sources = a.logs.Sources()
	}
	out := map[string][]string{}
	for _, src := range sources {
		if !model.IsValidLogSourceName(src) {
			continue
		}
		names := a.logs.FieldNames(src)
		if len(names) > 0 {
			out[src] = names
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"fields": out})
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
