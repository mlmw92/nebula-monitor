package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/nebula/monitor/internal/server/asset"
)

// AssetProvider 提供资产台账读取（由 asset 包实现）。
//
// 与 RulesProvider / ReportProvider 等既有依赖同一取向：API 只依赖自己真正用到的方法集合，
// 而不是把某个具体实现类型引进本包。
type AssetProvider interface {
	List(f asset.ListFilter) ([]asset.Asset, error)
	Get(ref asset.Ref) (asset.Asset, bool, error)
	GetByID(id int64) (asset.Asset, bool, error)
	Apply(ob asset.Observation) (asset.Asset, bool, error)
	History(ref asset.Ref, limit int) ([]asset.ChangeRecord, error)
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
	ID         int64             `json:"id"`
	TypeKey    string            `json:"typeKey"`
	NaturalKey string            `json:"naturalKey"`
	Name       string            `json:"name"`
	Node       string            `json:"node"`
	CreatedAt  int64             `json:"createdAt"`
	UpdatedAt  int64             `json:"updatedAt"`
	Values     map[string]string `json:"values"`
	Attrs      []assetAttrView   `json:"attrs"`
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

func toAssetView(a asset.Asset) assetView {
	view := assetView{
		ID: a.ID, TypeKey: a.TypeKey, NaturalKey: a.NaturalKey, Name: a.Name, Node: a.Node,
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
		Values: map[string]string{}, Attrs: make([]assetAttrView, 0, len(a.Attrs)),
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

// handleAssets 列出资产台账。
//
// 资源范围：受限用户只能看到所属节点在范围内的资产；资产 Node 为空（不可归属）时对受限用户不可见——
// 与 visibleMetricSeries / handleNodesLatest 的判定保持一致，避免台账成为越权旁路。
// 注意**先过滤再计数**：否则「总数」会泄露范围外的资产量。
func (a *API) handleAssets(w http.ResponseWriter, r *http.Request) {
	if a.assets == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "资产台账未启用"})
		return
	}
	q := r.URL.Query()
	filter := asset.ListFilter{
		TypeKey: strings.TrimSpace(q.Get("type")),
		Node:    strings.TrimSpace(q.Get("node")),
		Keyword: strings.TrimSpace(q.Get("keyword")),
		Limit:   assetIntParam(q.Get("limit"), 0),
		Offset:  assetIntParam(q.Get("offset"), 0),
	}
	items, err := a.assets.List(filter)
	if err != nil {
		slog.Error("查询资产台账失败", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询资产台账失败"})
		return
	}
	p := Principal(r)
	out := make([]assetView, 0, len(items))
	for _, item := range items {
		if !a.nodeInScope(p, item.Node) {
			continue
		}
		out = append(out, toAssetView(item))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"assets": out, "total": len(out)})
}

// handleAssetDetail 返回单个资产详情。
//
// 范围外的资产一律按「不存在」返回（404），不区分 403——否则可以通过状态码差异探测范围外资源。
func (a *API) handleAssetDetail(w http.ResponseWriter, r *http.Request) {
	item, ok := a.assetInScope(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toAssetView(item))
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
	writeJSON(w, http.StatusCreated, toAssetView(created))
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
	if len(attrs) == 0 && name == item.Name {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "没有需要更新的内容"})
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
	writeJSON(w, http.StatusOK, toAssetView(updated))
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
