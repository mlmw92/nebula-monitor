package api

import (
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
	GetByID(id int64) (asset.Asset, bool, error)
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
