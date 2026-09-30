package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/server/asset"
	"github.com/nebula/monitor/internal/server/auth"
)

// AssetProvider 提供资产台账读取（由 asset 包实现）。
//
// 与 RulesProvider / ReportProvider 等既有依赖同一取向：API 只依赖自己真正用到的方法集合，
// 而不是把某个具体实现类型引进本包。
type AssetProvider interface {
	List(f asset.ListFilter) ([]asset.Asset, error)
	// Count 与 List 共用条件（含 Nodes 资源范围），用于给出分页所需的真实总数。
	Count(f asset.ListFilter) (int, error)
	// Stats 汇总台账健康度，同样与 List 共用条件，保证顶部数字能下钻到列表。
	Stats(f asset.ListFilter, changesSince int64) (asset.Stats, error)
	Get(ref asset.Ref) (asset.Asset, bool, error)
	GetByID(id int64) (asset.Asset, bool, error)
	Apply(ob asset.Observation) (asset.Asset, bool, error)
	// ResetManual 清除指定字段的人工值（原型里的「恢复采集值」）。
	ResetManual(ref asset.Ref, keys []string, actor string) (asset.Asset, error)
	History(ref asset.Ref, limit int) ([]asset.ChangeRecord, error)
	// Snapshots 列出资产的配置快照头（时间倒序）。
	Snapshots(ref asset.Ref, limit int) ([]asset.Snapshot, error)
	// 差异巡检（inspect）：读结论 + 维护标杆，都不改动资产本身。
	RunInspect(sc asset.InspectScope, actor string) (asset.InspectRun, error)
	InspectRuns(limit int) ([]asset.InspectRun, error)
	InspectFindings(runID int64, limit int) ([]asset.InspectFinding, error)
	Baselines() ([]asset.Baseline, error)
	SetBaseline(ref asset.Ref, actor string) (asset.Baseline, error)
	ClearBaseline(typeKey string) error
	// Links 返回资产的直接关联（出边与入边）。
	Links(ref asset.Ref) ([]asset.Link, error)
	// 忽略（隐藏）与彻底删除：忽略是管理动作（可恢复、不停止采集），
	// 彻底删除仅限纯人工建档资产（采集资产删了会被重建）。
	Ignore(ref asset.Ref, actor, reason string) (asset.Asset, error)
	Restore(ref asset.Ref, actor string) (asset.Asset, error)
	Purge(ref asset.Ref) (baselineCleared bool, err error)
	// SetLabels 写入/覆盖标签，并删除 remove 中列出的键。
	SetLabels(ref asset.Ref, labels map[string]string, remove []string, actor string) (asset.Asset, error)
}

// SetAssetService 注入资产台账服务（可选；未注入时资产接口返回 503，与其它可选能力一致）。
func (a *API) SetAssetService(svc AssetProvider) { a.assets = svc }

// assetAttrView 是属性的对外形态。
//
// 采集值与人工值都会出现在 attrs 里（互不覆盖），前端据此并排展示或标出差异；
// values 则给出**生效值**（人工值优先），避免每个调用方各自实现优先级。
type assetAttrView struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Source    string `json:"source"`
	UpdatedAt int64  `json:"updatedAt"`
	UpdatedBy string `json:"updatedBy,omitempty"`
}

type assetView struct {
	ID         int64  `json:"id"`
	DisplayID  string `json:"displayId"`
	TypeKey    string `json:"typeKey"`
	NaturalKey string `json:"naturalKey"`
	Name       string `json:"name"`
	Node       string `json:"node"`
	// Status 是派生状态：online / missing / archived（见 assetStatusOf）。
	Status string `json:"status"`
	// Source 是派生来源：auto / manual / mixed（见 assetSourceKind）。
	Source string `json:"source"`
	// Owner 是人工维护的责任人（属性键 owner），未指派为空串。
	Owner     string `json:"owner"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
	// LastSeenAt 是最近一次**采集**写入时间；人工维护不会刷新它（否则"最近上报"会骗人）。
	LastSeenAt    int64             `json:"lastSeenAt"`
	ManualCount   int               `json:"manualCount"`
	ConflictCount int               `json:"conflictCount"`
	ConflictKeys  []string          `json:"conflictKeys"`
	Values        map[string]string `json:"values"`
	Attrs         []assetAttrView   `json:"attrs"`
	// Labels 是管理标签（分类维度），与属性分开：属性是采集/人工的值，标签用于筛选与展示。
	Labels map[string]string `json:"labels"`
	// Ignored 表示该资产已从台账隐藏；列表默认不返回它们，但会返回 ignored 计数。
	Ignored      bool   `json:"ignored"`
	IgnoreReason string `json:"ignoreReason,omitempty"`
	IgnoredBy    string `json:"ignoredBy,omitempty"`
	IgnoredAt    int64  `json:"ignoredAt,omitempty"`
}

// assetLinkView 是关联关系的对外形态。
//
// 同时给出方向与两端：前端要按「本资产 → 对方」「对方 → 本资产」分两组展示，
// 只给一个无向的边会让「谁 runs_on 谁」在界面上说不清楚。
type assetLinkView struct {
	Kind      string `json:"kind"`
	Direction string `json:"direction"` // out=本资产指向对方；in=对方指向本资产
	FromType  string `json:"fromType"`
	FromKey   string `json:"fromKey"`
	ToType    string `json:"toType"`
	ToKey     string `json:"toKey"`
	PeerType  string `json:"peerType"`
	PeerKey   string `json:"peerKey"`
	CreatedAt int64  `json:"createdAt"`
}

// 失联判定阈值：超过它未再上报即视为 missing。
//
// 原型标注为「超 30 分钟未上报」；对服务器资产而言，30 分钟既能覆盖一次 Agent 短暂失联或
// 系统重启，又不会把真掉线藏太久。改动它会影响摘要「失联」与列表 status=missing 的口径，
// 列表与摘要必须传同一个 StaleBefore。
const assetStaleThreshold = 30 * time.Minute

// assetChangeWindow 是摘要「近 N 天变更」的窗口。
const assetChangeWindow = 7 * 24 * time.Hour

// assetStaleBefore 给出失联判定的分界时刻（毫秒）。
// 一次请求只取一次并向下传：否则列表与摘要各自取"现在"，门槛上的资产会在两处得到不同结论。
func assetStaleBefore(now time.Time) int64 {
	return now.Add(-assetStaleThreshold).UnixMilli()
}

// assetStatusOf 派生资产状态。
//
// 归档与失联必须分开：纯人工建档的资产（如台账里补录的机房设备）从来没有采集值，
// 把它算成"失联"会让运维每天追一批本就不该上报的资产。
func assetStatusOf(a asset.Asset, staleBefore int64) string {
	if !a.HasDiscovery() {
		return asset.StatusArchived
	}
	if a.LastSeenAt() < staleBefore {
		return asset.StatusMissing
	}
	return asset.StatusOnline
}

// assetSourceKind 派生来源：无人工值=自动；有冲突字段=混合；其余=人工。
func assetSourceKind(manual, conflict int) string {
	if manual == 0 {
		return asset.SourceFilterAuto
	}
	if conflict > 0 {
		return asset.SourceFilterMixed
	}
	return asset.SourceFilterManual
}

type assetChangeView struct {
	Field  string `json:"field"`
	Old    string `json:"old,omitempty"`
	New    string `json:"new,omitempty"`
	Source string `json:"source"`
	Actor  string `json:"actor,omitempty"`
	Kind   string `json:"kind"`
	At     int64  `json:"at"`
}

func toAssetView(a asset.Asset, staleBefore int64) assetView {
	manual, conflict := a.SourceMix()
	view := assetView{
		ID: a.ID, DisplayID: fmt.Sprintf("ast_%d", a.ID), TypeKey: a.TypeKey, NaturalKey: a.NaturalKey,
		Name: a.Name, Node: a.Node,
		Status:    assetStatusOf(a, staleBefore),
		Source:    assetSourceKind(manual, conflict),
		Owner:     a.Owner(),
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt, LastSeenAt: a.LastSeenAt(),
		ManualCount: manual, ConflictCount: conflict, ConflictKeys: a.ConflictKeys(),
		Values: map[string]string{}, Attrs: make([]assetAttrView, 0, len(a.Attrs)),
		Labels: map[string]string{},
		Ignored: a.Ignored, IgnoreReason: a.IgnoreReason, IgnoredBy: a.IgnoredBy, IgnoredAt: a.IgnoredAt,
	}
	for k, v := range a.Labels {
		view.Labels[k] = v
	}
	for _, attr := range a.Attrs {
		view.Attrs = append(view.Attrs, assetAttrView{
			Key: attr.Key, Value: attr.Value, Source: string(attr.Source),
			UpdatedAt: attr.UpdatedAt, UpdatedBy: attr.UpdatedBy,
		})
		if value, ok := a.Value(attr.Key); ok {
			view.Values[attr.Key] = value
		}
	}
	// 稳定排序：同一 key 的多条（采集/人工）按来源排列，前端不必自己排。
	sort.Slice(view.Attrs, func(i, j int) bool {
		if view.Attrs[i].Key == view.Attrs[j].Key {
			return view.Attrs[i].Source < view.Attrs[j].Source
		}
		return view.Attrs[i].Key < view.Attrs[j].Key
	})
	return view
}

// handleAssets 列出资产台账（支持分页）。
//
// 资源范围：受限用户只能看到所属节点在范围内的资产；资产 Node 为空（不可归属）时对受限用户不可见——
// 与 visibleMetricSeries / handleNodesLatest 的判定保持一致，避免台账成为越权旁路。
//
// 范围**下推到查询条件**（filter.Nodes），而不是取回一页再在内存里过滤：后者会同时坏掉两件事——
// 页内被过滤掉的空位不补人（翻页会看到忽多忽少），以及总数只能数到当前页（「共 N 条」永远等于页大小）。
// 下推后 List 与 Count 条件一致，页内容与总数自洽；下面仍保留一次 nodeInScope 兜底，
// 保证「即使节点集合算错也不会泄露范围外资产」。
func (a *API) handleAssets(w http.ResponseWriter, r *http.Request) {
	if a.assets == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "资产台账未启用"})
		return
	}
	filter, ok := a.assetListFilter(w, r)
	if !ok {
		return
	}
	items, err := a.assets.List(filter)
	if err != nil {
		slog.Error("查询资产台账失败", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询资产台账失败"})
		return
	}
	total, err := a.assets.Count(filter)
	if err != nil {
		slog.Error("统计资产数量失败", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询资产台账失败"})
		return
	}
	p := Principal(r)
	out := make([]assetView, 0, len(items))
	for _, item := range items {
		if !a.nodeInScope(p, item.Node) {
			continue
		}
		out = append(out, toAssetView(item, filter.StaleBefore))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"assets": out, "total": total})
}

// assetListFilter 解析台账的筛选参数。
//
// 列表与摘要必须走同一个解析函数：顶部数字与列表若各自解析一遍参数，
// 迟早会出现「摘要判 missing 用了一个口径、列表用另一个口径」的偏差，
// 而摘要的全部价值就在于「点进去看到的就是这个数」。
func (a *API) assetListFilter(w http.ResponseWriter, r *http.Request) (asset.ListFilter, bool) {
	q := r.URL.Query()
	filter := asset.ListFilter{
		TypeKey:      strings.TrimSpace(q.Get("type")),
		Node:         strings.TrimSpace(q.Get("node")),
		Keyword:      strings.TrimSpace(q.Get("keyword")),
		Nodes:        a.assetAllowedNodes(Principal(r)),
		StaleBefore:  assetStaleBefore(time.Now()),
		OwnerMissing: assetBoolParam(q.Get("ownerMissing")),
		HasConflict:  assetBoolParam(q.Get("conflict")),
		Limit:        assetIntParam(q.Get("limit"), 0),
		Offset:       assetIntParam(q.Get("offset"), 0),
	}
	status := strings.TrimSpace(q.Get("status"))
	if !asset.ValidStatusFilter(status) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "状态取值不支持（可选 online / missing / archived）"})
		return asset.ListFilter{}, false
	}
	filter.Status = status
	source := strings.TrimSpace(q.Get("source"))
	if !asset.ValidSourceFilter(source) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "来源取值不支持（可选 auto / manual / mixed）"})
		return asset.ListFilter{}, false
	}
	filter.Source = source
	// 已忽略资产的可见性：默认隐藏（台账是"该关心的东西"的清单）。
	ignored := strings.TrimSpace(q.Get("ignored"))
	if !asset.ValidIgnoredFilter(ignored) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ignored 取值不支持（可选 with / only）"})
		return asset.ListFilter{}, false
	}
	filter.Ignored = ignored
	// 标签筛选：`key` 或 `key:value`
	filter.Label = strings.TrimSpace(q.Get("label"))
	return filter, true
}

// assetBoolParam 解析布尔查询参数：1 / true / yes（大小写不敏感）为真，其余为假。
func assetBoolParam(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// handleAssetSummary 返回台账健康度摘要（总数 / 失联 / 无责任人 / 冲突 / 窗口内变更）。
func (a *API) handleAssetSummary(w http.ResponseWriter, r *http.Request) {
	if a.assets == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "资产台账未启用"})
		return
	}
	filter, ok := a.assetListFilter(w, r)
	if !ok {
		return
	}
	stats, err := a.assets.Stats(filter, time.Now().Add(-assetChangeWindow).UnixMilli())
	if err != nil {
		slog.Error("统计资产摘要失败", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "统计资产摘要失败"})
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// handleAssetLinks 返回资产的直接关联（出边 + 入边）。
//
// 关联的**对方**也要做资源范围判定：本模块的既定约束是「只看得到自己范围内的资产」，
// 一条边足以暴露范围外资产的名字（例如另一台主机上的实例），因此范围外的边直接不返回，
// 而不是返回一个打码的对端。
func (a *API) handleAssetLinks(w http.ResponseWriter, r *http.Request) {
	item, ok := a.assetInScope(w, r)
	if !ok {
		return
	}
	links, err := a.assets.Links(asset.Ref{TypeKey: item.TypeKey, NaturalKey: item.NaturalKey})
	if err != nil {
		slog.Error("查询资产关联失败", "asset", item.NaturalKey, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询资产关联失败"})
		return
	}
	p := Principal(r)
	out := make([]assetLinkView, 0, len(links))
	for _, l := range links {
		view := assetLinkView{
			Kind: string(l.Kind), Direction: "out",
			FromType: l.From.TypeKey, FromKey: l.From.NaturalKey,
			ToType: l.To.TypeKey, ToKey: l.To.NaturalKey,
			PeerType: l.To.TypeKey, PeerKey: l.To.NaturalKey,
			CreatedAt: l.CreatedAt,
		}
		if l.To.TypeKey == item.TypeKey && l.To.NaturalKey == item.NaturalKey {
			view.Direction = "in"
			view.PeerType, view.PeerKey = l.From.TypeKey, l.From.NaturalKey
		}
		peer, found, err := a.assets.Get(asset.Ref{TypeKey: view.PeerType, NaturalKey: view.PeerKey})
		if err != nil {
			slog.Error("查询关联对端资产失败", "asset", view.PeerKey, "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询资产关联失败"})
			return
		}
		if !found || !a.nodeInScope(p, peer.Node) {
			continue
		}
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"links": out})
}

// assetAllowedNodes 计算资源范围允许的归属节点集合，供资产查询下推。
//
//   - nil      ：未启用认证或全局范围，不做节点限制
//   - 非空切片 ：受限用户可见的节点名（只含已注册且分组在范围内的节点）
//   - 空切片   ：受限但没有任何可见节点；调用方据此得到空结果与 0 条总数
//
// 与 nodeInScope 的边界一致：未注册节点（nodeGroup 为空）对受限用户不可归属，因此不在此集合内。
func (a *API) assetAllowedNodes(p *auth.Principal) []string {
	if p == nil || p.Scope.IsGlobal() || a.nodeMgr == nil {
		return nil
	}
	nodes := a.visibleNodes(a.nodeMgr.ListHostNodes(), p)
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Hostname)
	}
	return out
}

// handleAssetDetail 返回单个资产详情。
//
// 范围外的资产一律按「不存在」返回（404），不区分 403——否则可以通过状态码差异探测范围外资源。
func (a *API) handleAssetDetail(w http.ResponseWriter, r *http.Request) {
	item, ok := a.assetInScope(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toAssetView(item, assetStaleBefore(time.Now())))
}

// handleAssetHistory 返回资产的字段级变更记录（时间倒序）。
func (a *API) handleAssetHistory(w http.ResponseWriter, r *http.Request) {
	item, ok := a.assetInScope(w, r)
	if !ok {
		return
	}
	limit := assetIntParam(r.URL.Query().Get("limit"), 0)
	records, err := a.assets.History(asset.Ref{TypeKey: item.TypeKey, NaturalKey: item.NaturalKey}, limit)
	if err != nil {
		slog.Error("查询资产变更历史失败", "asset", item.NaturalKey, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询资产变更历史失败"})
		return
	}
	out := make([]assetChangeView, 0, len(records))
	for _, rec := range records {
		out = append(out, assetChangeView{
			Field: rec.Field, Old: rec.Old, New: rec.New,
			Source: string(rec.Source), Actor: rec.Actor, Kind: string(rec.Kind), At: rec.At,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id": item.ID, "typeKey": item.TypeKey, "naturalKey": item.NaturalKey, "records": out,
	})
}

// assetInScope 解析路径上的资产 ID、校验资源范围，并就地把错误响应写出。
// 返回 ok=false 表示响应已写出，调用方应立即返回。
func (a *API) assetInScope(w http.ResponseWriter, r *http.Request) (asset.Asset, bool) {
	if a.assets == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "资产台账未启用"})
		return asset.Asset{}, false
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "资产 ID 不合法"})
		return asset.Asset{}, false
	}
	item, found, err := a.assets.GetByID(id)
	if err != nil {
		slog.Error("查询资产失败", "id", id, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询资产失败"})
		return asset.Asset{}, false
	}
	if !found || !a.nodeInScope(Principal(r), item.Node) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "资产不存在"})
		return asset.Asset{}, false
	}
	return item, true
}

// assetActor 取变更记录的操作人。
//
// 认证中间件写入的用户名是首选；再回退到 Principal（有些路径只注入身份、不写用户名），
// 都取不到时记 anonymous——与告警处置的既有约定一致，避免变更历史里出现空操作人。
func assetActor(r *http.Request) string {
	if user := AuthenticatedUser(r); user != "" {
		return user
	}
	if p := Principal(r); p != nil && p.Username != "" {
		return p.Username
	}
	return "anonymous"
}

// assetIntParam 解析非负整数查询参数；非法值按默认值处理（不因一个坏参数把整个列表打成 400）。
func assetIntParam(raw string, def int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return def
	}
	return n
}

// assetWriteBody 是人工维护请求体。
//
// 关于 node：**新建**时必须给出归属节点（范围锚点，受限用户尤其不能漏）；
// **更新**时不允许改节点——否则等于允许把资产移出/移入别人的可见范围，
// 影响现有的分组与范围语义。要变更归属请用节点侧既有能力。
type assetWriteBody struct {
	TypeKey    string            `json:"typeKey"`
	NaturalKey string            `json:"naturalKey"`
	Name       string            `json:"name"`
	Node       string            `json:"node"`
	Attrs      map[string]string `json:"attrs"`
	// ResetAttrs 列出要**恢复采集值**的字段（清掉这些字段的人工值）。
	// 与 Attrs 是两个方向的意图（写 / 撤），故意不做成「attrs 里给 null」——
	// 那会让空字符串与"删除"在 JSON 里难以区分，而两者语义完全不同。
	ResetAttrs []string `json:"resetAttrs"`
}

// handleAssetCreate 手工新建资产（以人工来源提交，不会覆盖任何采集值）。
//
// 与更新接口的分工：本接口只负责建档；对已存在的（类型 + 自然键）返回 409，
// 避免「以为是新建，实际覆盖了既有台账」。
func (a *API) handleAssetCreate(w http.ResponseWriter, r *http.Request) {
	if a.assets == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "资产台账未启用"})
		return
	}
	var body assetWriteBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return
	}
	body.TypeKey = strings.TrimSpace(body.TypeKey)
	body.NaturalKey = strings.TrimSpace(body.NaturalKey)
	if body.TypeKey == "" || body.NaturalKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "typeKey、naturalKey 为必填项"})
		return
	}

	ref := asset.Ref{TypeKey: body.TypeKey, NaturalKey: body.NaturalKey}
	if _, exists, err := a.assets.Get(ref); err != nil {
		slog.Error("查询资产失败", "asset", body.NaturalKey, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询资产失败"})
		return
	} else if exists {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "该资产已存在，请改用更新接口"})
		return
	}

	node := strings.TrimSpace(body.Node)
	p := Principal(r)
	if node == "" {
		// 无归属资产的资源范围判定为不可归属，创建者自己也会看不见——与其造脏数据，不如直接拒绝。
		if p != nil && !p.Scope.IsGlobal() {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "受限用户必须为资产指定归属节点"})
			return
		}
	} else if !a.nodeInScope(p, node) {
		a.denyScope(w, r, "assets:write", a.nodeGroup(node))
		return
	}

	created, _, err := a.assets.Apply(asset.Observation{
		TypeKey: ref.TypeKey, NaturalKey: ref.NaturalKey, Name: strings.TrimSpace(body.Name), Node: node,
		Source: asset.SourceManual, Actor: assetActor(r), Attrs: cleanAssetAttrs(body.Attrs),
	})
	if err != nil {
		slog.Error("新建资产失败", "asset", ref.NaturalKey, "err", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, toAssetView(created, assetStaleBefore(time.Now())))
}

// handleAssetUpdate 维护已有资产的人工值（名称与属性）。
//
// 只写入 manual 来源：采集值原样保留，两者差异在详情里可见（与节点 DisplayName 的既有语义一致）。
func (a *API) handleAssetUpdate(w http.ResponseWriter, r *http.Request) {
	item, ok := a.assetInScope(w, r)
	if !ok {
		return
	}
	var body assetWriteBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return
	}
	if node := strings.TrimSpace(body.Node); node != "" && node != item.Node {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "不支持通过本接口变更资产归属节点"})
		return
	}

	name := item.Name
	if n := strings.TrimSpace(body.Name); n != "" {
		name = n
	}
	attrs := cleanAssetAttrs(body.Attrs)
	reset := cleanResetKeys(body.ResetAttrs)
	if len(attrs) == 0 && len(reset) == 0 && name == item.Name {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "没有需要更新的内容"})
		return
	}

	// 先恢复再写入：同一个字段同时出现在两者里时，最终留下的是本次提交的人工值（可预期）。
	if len(reset) > 0 {
		if _, err := a.assets.ResetManual(asset.Ref{TypeKey: item.TypeKey, NaturalKey: item.NaturalKey},
			reset, assetActor(r)); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	if len(attrs) == 0 && name == item.Name {
		// 本次只做了恢复：必须回读最新状态，否则返回的还是恢复前的对象，
		// 界面上的「生效值」会继续显示已删掉的人工值。
		updated, _, err := a.assets.GetByID(item.ID)
		if err != nil {
			slog.Error("回读资产失败", "asset", item.NaturalKey, "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询资产失败"})
			return
		}
		writeJSON(w, http.StatusOK, toAssetView(updated, assetStaleBefore(time.Now())))
		return
	}

	updated, _, err := a.assets.Apply(asset.Observation{
		TypeKey: item.TypeKey, NaturalKey: item.NaturalKey, Name: name, Node: item.Node,
		Source: asset.SourceManual, Actor: assetActor(r), Attrs: attrs,
	})
	if err != nil {
		slog.Error("更新资产失败", "asset", item.NaturalKey, "err", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, toAssetView(updated, assetStaleBefore(time.Now())))
}

// cleanResetKeys 规范化要恢复的字段名：去空白、去空项、去重（保持调用方给出的顺序）。
func cleanResetKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if key = strings.TrimSpace(key); key != "" && !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	return out
}

// cleanAssetAttrs 过滤掉空键与空白键：属性键会进库并成为查询维度，
// 放一个空键进去只会得到一条谁也查不到的脏数据。
func cleanAssetAttrs(attrs map[string]string) map[string]string {
	if len(attrs) == 0 {
		return nil
	}
	out := make(map[string]string, len(attrs))
	for key, value := range attrs {
		if key = strings.TrimSpace(key); key != "" {
			out[key] = value
		}
	}
	return out
}
