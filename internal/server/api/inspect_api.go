package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/nebula/monitor/internal/server/asset"
)

// assetPathInt 解析路径上的正整数参数（用于巡检记录 ID 这类没有资产上下文的 ID）。
func assetPathInt(w http.ResponseWriter, r *http.Request, name, errMsg string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue(name)), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": errMsg})
		return 0, false
	}
	return id, true
}

// 差异巡检（inspect）的对外接口。
//
// 巡检**只给结论、不改任何东西**（对齐既有「只读决策辅助」口径）：findings 是证据，
// 修复动作属于后续的配置下发能力。因此这里读写分离成两个权限点：
//   - inspect:read 看巡检记录与差异项
//   - inspect:run  触发一次巡检
//
// 「期望值（标杆）」来自某个资产的快照，属台账数据维护 → 用 assets:write。

type inspectRunView struct {
	ID        int64  `json:"id"`
	Scope     string `json:"scope"`
	Actor     string `json:"actor,omitempty"`
	StartedAt int64  `json:"startedAt"`
	Assets    int    `json:"assets"`
	// Baselined 是本次「首次建立基线、无法比对」的资产数：界面据此解释"为什么第一次没有差异"。
	Baselined    int  `json:"baselined"`
	Findings     int  `json:"findings"`
	Truncated    bool `json:"truncated"`
	PartialScope bool `json:"partialScope,omitempty"`
}

type inspectFindingView struct {
	ID        int64  `json:"id"`
	RunID     int64  `json:"runId"`
	AssetID   int64  `json:"assetId"`
	AssetType string `json:"assetType"`
	AssetKey  string `json:"assetKey"`
	AssetName string `json:"assetName,omitempty"`
	Node      string `json:"node,omitempty"`
	Field     string `json:"field"`
	Kind      string `json:"kind"`
	Level     string `json:"level"`
	Expected  string `json:"expected,omitempty"`
	Actual    string `json:"actual,omitempty"`
	At        int64  `json:"at"`
}

type baselineView struct {
	TypeKey  string `json:"typeKey"`
	AssetID  int64  `json:"assetId"`
	AssetKey string `json:"assetKey"`
	SetBy    string `json:"setBy,omitempty"`
	SetAt    int64  `json:"setAt"`
}

func toInspectRunView(r asset.InspectRun) inspectRunView {
	return inspectRunView{
		ID: r.ID, Scope: r.Scope, Actor: r.Actor, StartedAt: r.StartedAt,
		Assets: r.Assets, Baselined: r.Baselined, Findings: r.Findings, Truncated: r.Truncated,
		PartialScope: r.PartialScope,
	}
}

func toInspectFindingView(f asset.InspectFinding) inspectFindingView {
	return inspectFindingView{
		ID: f.ID, RunID: f.RunID, AssetID: f.AssetID, AssetType: f.AssetType, AssetKey: f.AssetKey,
		AssetName: f.AssetName, Node: f.Node, Field: f.Field,
		Kind: string(f.Kind), Level: string(f.Level), Expected: f.Expected, Actual: f.Actual, At: f.At,
	}
}

// inspectRunBody 是触发巡检的请求体；全字段可省（等价于"全部资产、默认关注字段"）。
type inspectRunBody struct {
	Type    string   `json:"type"`
	Node    string   `json:"node"`
	Keyword string   `json:"keyword"`
	Fields  []string `json:"fields"`
}

// handleInspectRunCreate 触发一次差异巡检。
//
// 范围复用台账筛选（含资源范围下推）：受限用户只能检到自己范围内的资产，
// 与列表页看到的范围一致——否则会出现"巡检说某台有问题、列表里却找不到它"。
func (a *API) handleInspectRunCreate(w http.ResponseWriter, r *http.Request) {
	if a.assets == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "资产台账未启用"})
		return
	}
	var body inspectRunBody
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
			return
		}
	}
	p := Principal(r)
	sc := asset.InspectScope{
		Filter: asset.ListFilter{
			TypeKey: strings.TrimSpace(body.Type),
			Node:    strings.TrimSpace(body.Node),
			Keyword: strings.TrimSpace(body.Keyword),
			Nodes:   a.assetAllowedNodes(p),
		},
		Fields: body.Fields,
	}
	run, err := a.assets.RunInspect(sc, assetActor(r))
	if err != nil {
		slog.Error("执行配置巡检失败", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "执行配置巡检失败"})
		return
	}
	writeJSON(w, http.StatusCreated, toInspectRunView(run))
}

// handleInspectRuns 列出巡检记录（时间倒序）。
func (a *API) handleInspectRuns(w http.ResponseWriter, r *http.Request) {
	if a.assets == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "资产台账未启用"})
		return
	}
	runs, err := a.assets.InspectRunsInNodes(assetIntParam(r.URL.Query().Get("limit"), 0), a.assetAllowedNodes(Principal(r)))
	if err != nil {
		slog.Error("查询巡检记录失败", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询巡检记录失败"})
		return
	}
	out := make([]inspectRunView, 0, len(runs))
	for _, run := range runs {
		out = append(out, toInspectRunView(run))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"runs": out})
}

// handleInspectFindings 返回某次巡检的差异项。
//
// 差异项里带着资产身份（type/key/name/node）：资产若已被删除，历史结论仍应可读。
func (a *API) handleInspectFindings(w http.ResponseWriter, r *http.Request) {
	if a.assets == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "资产台账未启用"})
		return
	}
	runID, ok := assetPathInt(w, r, "id", "巡检记录 ID 无效")
	if !ok {
		return
	}
	findings, visible, err := a.assets.InspectFindingsInNodes(runID, assetIntParam(r.URL.Query().Get("limit"), 0), a.assetAllowedNodes(Principal(r)))
	if err != nil {
		slog.Error("查询差异项失败", "run", runID, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询差异项失败"})
		return
	}
	if !visible {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "巡检记录不存在"})
		return
	}
	out := make([]inspectFindingView, 0, len(findings))
	for _, f := range findings {
		out = append(out, toInspectFindingView(f))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"findings": out})
}

// handleInspectBaselines 列出各资产类型当前的期望值来源（标杆资产）。
func (a *API) handleInspectBaselines(w http.ResponseWriter, r *http.Request) {
	if a.assets == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "资产台账未启用"})
		return
	}
	items, err := a.assets.BaselinesInNodes(a.assetAllowedNodes(Principal(r)))
	if err != nil {
		slog.Error("查询巡检标杆失败", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询巡检标杆失败"})
		return
	}
	out := make([]baselineView, 0, len(items))
	for _, b := range items {
		out = append(out, baselineView{
			TypeKey: b.TypeKey, AssetID: b.AssetID, AssetKey: b.AssetKey, SetBy: b.SetBy, SetAt: b.SetAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"baselines": out})
}

// handleAssetBaselineSet 把某资产的当前配置设为该资产类型的期望值（标杆）。
func (a *API) handleAssetBaselineSet(w http.ResponseWriter, r *http.Request) {
	item, ok := a.assetInScope(w, r)
	if !ok {
		return
	}
	current, hasBaseline, err := a.assets.BaselineForType(item.TypeKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询巡检标杆失败"})
		return
	}
	currentID := int64(0)
	if hasBaseline {
		owner, found, err := a.assets.GetByID(current.AssetID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询标杆资产失败"})
			return
		}
		if !found || !a.nodeInScope(Principal(r), owner.Node) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "资产不存在"})
			return
		}
		currentID = current.AssetID
	}
	b, err := a.assets.SetBaselineIfCurrent(asset.Ref{TypeKey: item.TypeKey, NaturalKey: item.NaturalKey}, assetActor(r), currentID, a.assetAllowedNodes(Principal(r)))
	if errors.Is(err, asset.ErrOutOfScope) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "资产不存在"})
		return
	}
	if errors.Is(err, asset.ErrBaselineChanged) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "标杆已变更，请刷新后重试"})
		return
	}
	if err != nil {
		slog.Error("设置巡检标杆失败", "asset", item.NaturalKey, "err", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, baselineView{
		TypeKey: b.TypeKey, AssetID: b.AssetID, AssetKey: b.AssetKey, SetBy: b.SetBy, SetAt: b.SetAt,
	})
}

// handleAssetBaselineDelete 清除该资产所属类型的标杆；本来没有也返回成功（幂等）。
func (a *API) handleAssetBaselineDelete(w http.ResponseWriter, r *http.Request) {
	item, ok := a.assetInScope(w, r)
	if !ok {
		return
	}
	current, hasBaseline, err := a.assets.BaselineForType(item.TypeKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询巡检标杆失败"})
		return
	}
	if hasBaseline {
		owner, found, err := a.assets.GetByID(current.AssetID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询标杆资产失败"})
			return
		}
		if !found || !a.nodeInScope(Principal(r), owner.Node) || current.AssetID != item.ID {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "资产不存在"})
			return
		}
		err = a.assets.ClearBaselineIfCurrent(item.TypeKey, current.AssetID, a.assetAllowedNodes(Principal(r)))
		switch {
		case errors.Is(err, asset.ErrOutOfScope):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "资产不存在"})
			return
		case errors.Is(err, asset.ErrBaselineChanged):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "标杆已变更，请刷新后重试"})
			return
		case err != nil:
			slog.Error("清除巡检标杆失败", "type", item.TypeKey, "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "清除巡检标杆失败"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"typeKey": item.TypeKey, "cleared": true})
}

// snapshotView 是配置快照头的对外形态（不含字段内容：差异项里已经给出 expected/actual）。
type snapshotView struct {
	ID      int64 `json:"id"`
	TakenAt int64 `json:"takenAt"`
}

// handleAssetSnapshots 列出某资产的配置快照（时间倒序）。
func (a *API) handleAssetSnapshots(w http.ResponseWriter, r *http.Request) {
	item, ok := a.assetInScope(w, r)
	if !ok {
		return
	}
	snaps, err := a.assets.Snapshots(asset.Ref{TypeKey: item.TypeKey, NaturalKey: item.NaturalKey},
		assetIntParam(r.URL.Query().Get("limit"), 0))
	if err != nil {
		slog.Error("查询配置快照失败", "asset", item.NaturalKey, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询配置快照失败"})
		return
	}
	out := make([]snapshotView, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, snapshotView{ID: s.ID, TakenAt: s.TakenAt})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"snapshots": out})
}
