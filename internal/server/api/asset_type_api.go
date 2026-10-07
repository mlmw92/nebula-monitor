package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/nebula/monitor/internal/server/asset"
	"github.com/nebula/monitor/internal/server/audit"
	"github.com/nebula/monitor/internal/server/auth"
)

// 配置项模型（设计件 docs/superpowers/specs/2026-10-07-asset-ci-model-design.md）。
//
// 这个端点的形状由一条口径决定：**它是"看模型"，不是"看数据"**。因此响应里有类型的字段清单、
// 每个字段的覆盖数与来源分布、运行态/关注字段、标杆与"疑似同义键"；**没有任何属性值**，
// 也没有资产名/自然键（值里可能有连接串、口令、内网地址）。
//
// 映射写成显式的 view 结构（而不是直接把服务层结构序列化出去），就是为了让"不泄露值"这件事
// 在**类型层面**成立：将来有人给 TypeAttrStat 加一个 Value 字段，这里也不会跟着泄露。

type assetAttrStatView struct {
	Key string `json:"key"`
	// Assets 是有这个键的资产数；Coverage 是它与该类型资产数的比值（0..1）。
	Assets    int     `json:"assets"`
	Coverage  float64 `json:"coverage"`
	Discovery int     `json:"discovery"`
	Manual    int     `json:"manual"`
}

type assetTypeSchemaView struct {
	RuntimeFields []string `json:"runtimeFields"`
	// RuntimeFieldsDefault 表示当前生效的是**内置默认**（界面要能看出"没配过"，
	// 否则"我明明没配运行态字段，为什么 up 被排除了"会变成一个疑问）。
	RuntimeFieldsDefault bool                      `json:"runtimeFieldsDefault"`
	FocusFields          []string                  `json:"focusFields"`
	AttrMeta             map[string]asset.AttrMeta `json:"attrMeta,omitempty"`
}

type assetTypeBaselineView struct {
	AssetID  int64  `json:"assetId"`
	AssetKey string `json:"assetKey"`
}

type assetTypeModelView struct {
	Key       string `json:"key"`
	Title     string `json:"title"`
	Builtin   bool   `json:"builtin"`
	Ephemeral bool   `json:"ephemeral"`
	// Assets 是当前可见范围内该类型的资产数（含短命对象）。
	Assets   int                   `json:"assets"`
	Schema   assetTypeSchemaView   `json:"schema"`
	Baseline *assetTypeBaselineView `json:"baseline,omitempty"`
	// Attrs 是该类型上出现过的字段；**不含任何值**（见本文件顶部说明）。
	Attrs          []assetAttrStatView `json:"attrs"`
	AttrsTruncated bool                `json:"attrsTruncated,omitempty"`
	SynonymGroups  [][]string          `json:"synonymGroups,omitempty"`
}

func toAssetTypeModelView(m asset.TypeModel) assetTypeModelView {
	attrs := make([]assetAttrStatView, 0, len(m.Attrs))
	for _, st := range m.Attrs {
		coverage := 0.0
		if m.Assets > 0 {
			coverage = float64(st.Assets) / float64(m.Assets)
		}
		attrs = append(attrs, assetAttrStatView{
			Key: st.Key, Assets: st.Assets, Coverage: coverage,
			Discovery: st.Discovery, Manual: st.Manual,
		})
	}
	out := assetTypeModelView{
		Key: m.Key, Title: m.Title, Builtin: m.Builtin, Ephemeral: m.Ephemeral,
		Assets: m.Assets,
		Schema: assetTypeSchemaView{
			RuntimeFields:        m.Schema.EffectiveRuntimeFields(),
			RuntimeFieldsDefault: m.Schema.UsesDefaultRuntimeFields(),
			FocusFields:          m.Schema.FocusFields,
			AttrMeta:             m.Schema.AttrMeta,
		},
		Attrs:          attrs,
		AttrsTruncated: m.AttrsTruncated,
		SynonymGroups:  m.SynonymGroups,
	}
	if m.Baseline != nil {
		out.Baseline = &assetTypeBaselineView{AssetID: m.Baseline.AssetID, AssetKey: m.Baseline.AssetKey}
	}
	return out
}

// assetModelScopeFilter 组装模型页的统计条件：**只有调用者的可见范围**这一个维度
// （节点 + 业务标签，与台账同一套口径）。
//
// 刻意**不带** ExcludeTypes：台账默认视图不含短命对象（Pod/工作负载），但模型页看的是**全量模型**，
// 把它们藏起来会让人以为平台不认识它们（设计件 D6）。
func (a *API) assetModelScopeFilter(p *auth.Principal) asset.ListFilter {
	return asset.ListFilter{
		Nodes:          a.assetAllowedNodes(p),
		LabelSelectors: a.assetScopeSelectors(p),
	}
}

// handleAssetTypes 返回全部类型的模型与字段画像。
func (a *API) handleAssetTypes(w http.ResponseWriter, r *http.Request) {
	if a.assets == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "资产台账未启用"})
		return
	}
	p := Principal(r)
	models, err := a.assets.TypeModels(a.assetModelScopeFilter(p), a.assetAllowedNodes(p))
	if err != nil {
		slog.Error("读取配置项模型失败", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "读取配置项模型失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"types": modelViews(models)})
}

// assetTypeModelBody 是保存模型的请求体：**只有这三项可写**。
//
// 范围之外的字段（类型键、标题、内置标记）由播种与路径决定，不接受客户端提交——
// 模型页能改的是"字段怎么看"，不是"有哪些类型"（自定义类型是本批明确不做的）。
type assetTypeModelBody struct {
	RuntimeFields []string                  `json:"runtimeFields"`
	FocusFields   []string                  `json:"focusFields"`
	AttrMeta      map[string]asset.AttrMeta `json:"attrMeta"`
}

// handleAssetTypeModelSave 保存某类型的模型（运行态字段 / 关注字段 / 属性说明）。
//
// 权限用 assets:write（与「标杆 = 台账数据维护」同一取向，不新增权限点）；
// 与单条资产写不同，这里**写审计**：模型改的是全平台的比对口径，而它没有变更历史可查。
func (a *API) handleAssetTypeModelSave(w http.ResponseWriter, r *http.Request) {
	if a.assets == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "资产台账未启用"})
		return
	}
	typeKey := r.PathValue("key")
	var body assetTypeModelBody
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
			return
		}
	}
	sch := asset.TypeSchema{
		RuntimeFields: body.RuntimeFields,
		FocusFields:   body.FocusFields,
		AttrMeta:      body.AttrMeta,
	}
	if err := a.assets.UpdateTypeModel(typeKey, sch); err != nil {
		// 区分"没这个类型"（404）与"提交的内容不合法"（400）：现场需要知道该改哪个方向
		if errors.Is(err, asset.ErrTypeNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	operator := AuthenticatedUser(r)
	if a.audit != nil {
		_ = a.audit.Record(audit.Event{
			User: operator, Method: r.Method, Path: r.URL.Path,
			Status: http.StatusOK, RemoteIP: audit.ClientIP(r), Succeeded: true,
			RequestID: RequestID(r),
			Category:  "assets", Action: "asset_model:update",
			Detail: fmt.Sprintf("类型 %s：运行态字段 %d 个、关注字段 %d 个、属性说明 %d 条",
				typeKey, len(sch.RuntimeFields), len(sch.FocusFields), len(sch.AttrMeta)),
		})
	}
	slog.Info("已保存配置项模型", "type", typeKey, "operator", operator)

	// 回整份模型：界面直接整体替换，省掉"局部更新拼错状态"的一类问题（类型只有几个）
	p := Principal(r)
	models, err := a.assets.TypeModels(a.assetModelScopeFilter(p), a.assetAllowedNodes(p))
	if err != nil {
		slog.Error("保存后读取配置项模型失败", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "保存成功但读取配置项模型失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"types": modelViews(models)})
}

func modelViews(models []asset.TypeModel) []assetTypeModelView {
	out := make([]assetTypeModelView, 0, len(models))
	for _, m := range models {
		out = append(out, toAssetTypeModelView(m))
	}
	return out
}
