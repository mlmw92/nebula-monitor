package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/audit"
	"github.com/nebula/monitor/internal/server/nginxaccess"
)

// handleSecuritySummary 返回安全态势概览：合规评分均值、事件总数、风险主机数、FIM 变化数。
// GET /api/v1/security/summary
func (a *API) handleSecuritySummary(w http.ResponseWriter, r *http.Request) {
	if a.security == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"score": 0, "eventCount": 0, "riskNodes": 0, "fimChanges": 0, "baselineHosts": 0, "enabled": false,
		})
		return
	}
	s := a.security.Summary()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"score":         s.Score,
		"eventCount":    s.EventCount,
		"riskNodes":     s.RiskNodes,
		"fimChanges":    s.FIMChanges,
		"baselineHosts": s.BaselineHosts,
		"generatedAt":   s.GeneratedAt,
		"enabled":       true,
	})
}

// handleAuditEvents 返回管理操作审计记录（JSON），支持 limit/user/path/category/from/to/offset 筛选。
// 需 audit:read。兼容历史调用方：带 ?format=csv 时按导出处理并额外要求 audit:export；
// 新调用方请直接使用 GET /api/v1/audit/export。
func (a *API) handleAuditEvents(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("format") == "csv" {
		if !a.checkPerm(w, r, "audit:export") {
			return
		}
		a.auditExportCSV(w, r)
		return
	}
	if a.audit == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"events": []interface{}{}, "total": 0})
		return
	}
	events, total, err := a.auditQuery(auditFilter(r, 0))
	if err != nil {
		// **不降级成空列表**：审计查询失败却返回空，界面会显示"没有任何操作记录"——
		// 对审计来说这是一句谎话（等于说"没人干过事"），比报错糟得多。
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询审计事件失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"events": events, "total": total})
}

// handleAuditExport 导出管理操作审计记录为 CSV（需 audit:export，属高危权限点）。
// GET /api/v1/audit/export?limit=&user=&path=&category=&from=&to=
func (a *API) handleAuditExport(w http.ResponseWriter, r *http.Request) {
	a.auditExportCSV(w, r)
}

// auditFilter 解析审计查询参数（列表与导出共用）。
//
// from/to 是毫秒时间戳；offset 与资产列表同款——审计是低频写入的"翻页查阅"场景，
// 不存在集中日志那种"翻页途中文件增长"的漂移，用不上游标。
// defaultLimit 为 0 时按请求参数（缺省 100 条）；导出传 MaxEvents 作为上限。
func auditFilter(r *http.Request, defaultLimit int) audit.QueryFilter {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if defaultLimit > 0 && limit <= 0 {
		limit = defaultLimit
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	from, _ := strconv.ParseInt(q.Get("from"), 10, 64)
	to, _ := strconv.ParseInt(q.Get("to"), 10, 64)
	return audit.QueryFilter{
		User: q.Get("user"), Path: q.Get("path"), Category: q.Get("category"),
		// requestId 是"从一条资产变更跳到那次操作"的入口（精确匹配，见 auditWhere）。
		RequestID: strings.TrimSpace(q.Get("requestId")),
		From:      from, To: to, Limit: limit, Offset: offset,
	}
}

// auditQuery 按条件读取审计事件（含**同条件总数**）并补全来源 IP 属地。
func (a *API) auditQuery(f audit.QueryFilter) ([]audit.Event, int, error) {
	events, total, err := a.audit.Query(f)
	if err != nil {
		return nil, 0, err
	}
	// 补全来源 IP 属地（经已集成的 ip2region 库），旧数据或缺失字段同样实时补全。
	for i := range events {
		if events[i].SourceLocation == "" && events[i].RemoteIP != "" {
			events[i].SourceLocation = geoLocation(events[i].RemoteIP)
		}
	}
	return events, total, nil
}

// auditEvents 兼容既有调用方：只要事件列表，不要总数。查询失败返回 nil（调用方自行决定怎么报）。
func (a *API) auditEvents(r *http.Request) []audit.Event {
	if a.audit == nil {
		return nil
	}
	events, _, err := a.auditQuery(auditFilter(r, 0))
	if err != nil {
		return nil
	}
	return events
}

// auditExportCSV 以 CSV 形式写出审计事件（/audit/export 与 /audit/events?format=csv 共用）。
//
// 导出上限取 audit.MaxEvents（2000）：与入库前的内存上限同档，避免一次导出把库读爆。
// 列表的 limit/offset 不影响导出；要导出更多就按时间范围分批。
func (a *API) auditExportCSV(w http.ResponseWriter, r *http.Request) {
	if a.audit == nil {
		writeAuditCSV(w, nil)
		return
	}
	filter := auditFilter(r, audit.MaxEvents)
	filter.Limit = audit.MaxEvents
	filter.Offset = 0
	events, _, err := a.auditQuery(filter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询审计事件失败"})
		return
	}
	writeAuditCSV(w, events)
}

func writeAuditCSV(w http.ResponseWriter, events []audit.Event) {
	// 统一走 csvDownload：这里此前**漏了 BOM**（Excel 打开中文列名乱码），
	// 也顺带补上公式注入消毒（审计的 detail 里可能带用户可控内容）。
	csvDownload(w, "audit-events.csv",
		[]string{"time", "user", "method", "path", "status", "remote_ip", "source_location", "succeeded", "category", "action", "detail"},
		func(write func([]string)) {
			for _, event := range events {
				write([]string{
					event.Time.Format(time.RFC3339), event.User, event.Method, event.Path,
					strconv.Itoa(event.Status), event.RemoteIP, event.SourceLocation, strconv.FormatBool(event.Succeeded),
					event.Category, event.Action, event.Detail,
				})
			}
		})
}

// handleSecurityEvents 返回安全事件列表（按时间倒序），支持 limit/category/node 筛选。
// 返回前对缺 NodeIP 的事件按 e.Node 反查当前节点管理器补 IP，避免历史持久化数据或
// 代理转发场景下「节点」列只展示主机名、不展示服务器 IP。
// GET /api/v1/security/events?limit=50&category=ssh_bruteforce&node=host1
func (a *API) handleSecurityEvents(w http.ResponseWriter, r *http.Request) {
	if a.security == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"events": []model.SecurityEvent{}, "enabled": false})
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	// category / node 归一化：空串或各类「全部」占位值都视为不过滤，返回全部事件。
	// 兼容旧前端：el-select 未选中时 v-model 为 undefined，经 URLSearchParams
	// 序列化成字符串 "undefined"（非空），会使后端按非空 category 过滤而返回空。
	cat := normalizeSecurityFilter(q.Get("category"))
	node := normalizeSecurityFilter(q.Get("node"))
	events := a.security.Events(limit, cat, node)
	if events == nil {
		events = []model.SecurityEvent{}
	}
	// 资源范围：受限用户只能看到范围内节点的安全事件。
	events = filterByNodeScope(a, Principal(r), events, func(e model.SecurityEvent) string { return e.Node })
	for i := range events {
		if events[i].NodeIP == "" && events[i].Node != "" {
			events[i].NodeIP = a.nodeIP(events[i].Node)
		}
		// 来源 IP 属地：SSH 审计/暴力破解、sudo 审计等事件含 SourceIP，
		// 经已集成的 ip2region 库补全国家/省份/城市，便于研判攻击来源。
		if events[i].SourceLocation == "" && events[i].SourceIP != "" {
			events[i].SourceLocation = geoLocation(events[i].SourceIP)
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"events": events, "enabled": true})
}

// handleSecurityBaselines 返回各主机安全基线评分与检查项明细。
// GET /api/v1/security/baselines
func (a *API) handleSecurityBaselines(w http.ResponseWriter, r *http.Request) {
	baselines, enabled := a.securityBaselinesInScope(r)
	if baselines == nil {
		baselines = []model.SecurityBaseline{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"baselines": baselines, "enabled": enabled})
}

// securityBaselinesInScope 取"当前身份可见的安全基线"，并补全 IP 与节点别名。
//
// 列表（/security/baselines）与导出（/security/baselines/export）**必须共用这一份**：
// 两处各写一遍，迟早会出现"界面上看得见、导出里没有"或者更糟的"导出越权"——
// 导出是最容易漏掉资源范围的地方（资源范围这一课在本项目里已经上过几次）。
//
// 第二个返回值是"安全能力是否启用"：未注入 security store 时为 false，
// 调用方据此给出不同的提示（未启用 vs 有启用但还没数据）。
func (a *API) securityBaselinesInScope(r *http.Request) ([]model.SecurityBaseline, bool) {
	if a.security == nil {
		return nil, false
	}
	baselines := a.security.Baselines()
	if baselines == nil {
		baselines = []model.SecurityBaseline{}
	}
	// 资源范围：受限用户只能看到范围内节点的基线。
	baselines = filterByNodeScope(a, Principal(r), baselines, func(b model.SecurityBaseline) string { return b.Node })
	for i := range baselines {
		if baselines[i].NodeIP == "" && baselines[i].Node != "" {
			baselines[i].NodeIP = a.nodeIP(baselines[i].Node)
		}
		baselines[i].DisplayName = a.nodeDisplayName(baselines[i].Node)
	}
	return baselines, true
}

// normalizeSecurityFilter 将安全事件/基线查询参数归一化：
// 空串、空白、字符串 "undefined"（旧前端 el-select 未选中时经 URLSearchParams
// 序列化得到），以及各类「全部」占位值，均视为不过滤。
func normalizeSecurityFilter(v string) string {
	switch strings.TrimSpace(v) {
	case "", "undefined", "null", "all", "ALL", "all_categories", "全部", "所有", "*", "any", "none":
		return ""
	}
	return strings.TrimSpace(v)
}

// geoLocation 经已集成的 ip2region 库查询 IP 归属地，返回「国家 省份 城市」
// 的紧凑展示串；查询失败或内网/保留地址返回空串。
func geoLocation(ip string) string {
	country, _, province, city := nginxaccess.NewGeo().Search(ip)
	var parts []string
	if country != "" {
		parts = append(parts, country)
	}
	if province != "" && province != country {
		parts = append(parts, province)
	}
	if city != "" && city != province {
		parts = append(parts, city)
	}
	return strings.Join(parts, " ")
}
