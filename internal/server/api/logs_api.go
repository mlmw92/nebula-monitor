package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/auth"
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
	writeJSON(w, http.StatusOK, logQueryResponse{
		LogQueryResult: res,
		Assets:         a.logsAssetMap(Principal(r), res.Lines),
	})
}

// logQueryResponse 是检索响应：检索结果 + 「来源 → 资产」映射（全景表 8-6 的联动侧）。
//
// 映射按 (source, node) 给出而不是塞进每一行：一页里的行大量共享同一组合，
// 逐行重复既费流量，也会让人以为"每行都是不同的资产"。
type logQueryResponse struct {
	model.LogQueryResult
	// Assets 的键是 `<source>|<node>`，值是该组合对应的资产（可能多条：同名来源
	// 在多台机器上都可能配置）。前端据此把日志行标到资产上，并提供跳转。
	Assets map[string][]logAssetRef `json:"assets,omitempty"`
}

// logAssetRef 是日志行指向的资产（只给跳转与展示所需的最小字段）。
type logAssetRef struct {
	ID         int64  `json:"id"`
	TypeKey    string `json:"typeKey"`
	TypeTitle  string `json:"typeTitle"`
	NaturalKey string `json:"naturalKey"`
	Name       string `json:"name,omitempty"`
	Node       string `json:"node"`
}

// logsAssetMap 解析本页日志涉及的 (来源, 节点) → 资产。
//
// 只解析**本页命中行**出现过的组合（数量受页大小约束），而不是把整个台账拉出来做映射：
// 后者在台账大时会把一次检索变成一次全表扫描，而收益只是"页面上用不到的那些映射"。
//
// 资产是补充信息：任何一步失败都只降级为"这一页没有映射"，绝不让
// "日志查到了"变成"页面报错"。
func (a *API) logsAssetMap(p *auth.Principal, lines []model.LogHit) map[string][]logAssetRef {
	if a.assets == nil || len(lines) == 0 {
		return nil
	}
	// 单页最多解析这么多组合：再多说明"这一页来自很多机器"，逐个反查的价值已经很低。
	const maxPairs = 20
	titles := assetTypeTitles()
	seen := map[string]bool{}
	out := map[string][]logAssetRef{}
	for _, hit := range lines {
		key := hit.Source + "|" + hit.Node
		if seen[key] {
			continue
		}
		if len(seen) >= maxPairs {
			break
		}
		seen[key] = true
		items, err := a.assets.AssetsByLogSource(hit.Source, hit.Node)
		if err != nil {
			slog.Warn("日志关联资产失败", "source", hit.Source, "node", hit.Node, "err", err)
			continue
		}
		refs := make([]logAssetRef, 0, len(items))
		for _, item := range items {
			// 资源范围与其它入口同一口径：范围外的资产不出现在映射里
			if !a.nodeInScope(p, item.Node) {
				continue
			}
			title := titles[item.TypeKey]
			if title == "" {
				title = item.TypeKey
			}
			refs = append(refs, logAssetRef{
				ID: item.ID, TypeKey: item.TypeKey, TypeTitle: title,
				NaturalKey: item.NaturalKey, Name: item.Name, Node: item.Node,
			})
		}
		if len(refs) > 0 {
			out[key] = refs
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
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

// handleLogRuleTemplate 返回「用某个日志模式建阈值规则」的规则模板（全景表 9-05）。
//
// 为什么由服务端给模板而不是让前端拼指标名：日志模式的指标名是
// `<来源>_log_<模式>_total`，**拼错的症状是"规则配好了却永远没有数据"**——
// 没有任何一处会报错。服务端拼（并在模式名非法时明确拒绝）才能把这个坑堵在配置期。
//
// 不新增规则类型、不改告警引擎：底层就是既有的日志指标 + 阈值规则，
// 这个接口只负责把"检索页看到的那一行"翻译成一条可直接保存的规则。
func (a *API) handleLogRuleTemplate(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	source := strings.TrimSpace(q.Get("source"))
	pattern := strings.TrimSpace(q.Get("pattern"))
	if !model.IsValidLogSourceName(source) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "来源名不合法（需匹配 " + model.LogSourceNamePattern.String() + "）",
		})
		return
	}
	if !model.IsValidLogPatternName(pattern) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "模式名不合法（会拼进指标名，需匹配 " + model.LogPatternNamePattern.String() + "）",
		})
		return
	}
	rule := model.AlertRule{
		Name:      "日志 " + pattern + "（" + source + "）",
		Metric:    model.LogPatternMetricName(source, pattern),
		Operator:  ">",
		Threshold: 0,
		For:       "5m",
		Severity:  model.SeverityWarning,
		Scope:     "all",
		Enabled:   true,
		Desc: "指标由 Agent 的 logSources[].patterns[].name 产出（每个模式一个独立指标名）。" +
			"模式名改了指标名也会变，规则需同步更新；阈值 0 表示「只要出现该模式就告警」，" +
			"高频日志请按实际速率调高。",
	}
	// 从检索页建规则时通常盯着某一台机器的日志：带上 node 就把范围收到那台，
	// 避免"另一台机器上的同名来源"触发同一条规则。
	if node := strings.TrimSpace(q.Get("node")); node != "" {
		rule.Scope = "specified"
		rule.Nodes = []string{node}
	}
	writeJSON(w, http.StatusOK, rule)
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
