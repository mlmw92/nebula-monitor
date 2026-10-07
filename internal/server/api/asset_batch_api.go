package api

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/server/asset"
	"github.com/nebula/monitor/internal/server/audit"
)

// 本文件是资产台账的**批量维护**与**清单导出**。
//
// 为什么单独一个文件：asset_api.go 已经承担列表/详情/摘要/历史/新建/更新，
// 而这两件事自成体系——批量要「逐条结论 + 范围逐条判定」，导出要「全量 + 独立权限点」，
// 塞进原文件只会让它继续变长。

// maxBatchAssets 是单次批量维护的资产数上限。
//
// 与 ops 的 200 台同理：超限**显式报错**而不是静默截断——静默截断会让用户以为
// 全都改了，而实际只改了前 200 台（这类"没改的还以为改了"最伤台账可信度）。
const maxBatchAssets = 200

// maxExportAssets 是单次导出的行数上限。超过则明确拒绝并要求缩小筛选，
// 而不是导出半个表（那种错误没有任何提示，而用户会拿它去做盘点）。
const maxExportAssets = 20000

// assetBatchOps 是批量维护支持的动作（键为 API 取值，值为界面文案）。
var assetBatchOps = map[string]string{
	"owner":      "转派责任人",
	"ownerClear": "清除责任人",
	"labels":     "打标签",
	"ignore":     "忽略（从台账隐藏）",
	"restore":    "恢复（重新纳入台账）",
}

// assetBatchBody 是批量维护请求体。
type assetBatchBody struct {
	// IDs 是目标资产主键（列表里的 id）；允许重复，服务端去重。
	IDs []int64 `json:"ids"`
	// Op 见 assetBatchOps。
	Op string `json:"op"`
	// Owner 仅 op=owner 使用；为空会报错——清空请显式用 ownerClear，
	// 否则"输入框忘了填"就会静默清掉一整批责任人。
	Owner string `json:"owner"`
	// Labels / Remove 仅 op=labels 使用（写入/覆盖 + 删除）。
	Labels map[string]string `json:"labels"`
	Remove []string          `json:"remove"`
	// Reason 仅 op=ignore 使用。
	Reason string `json:"reason"`
}

// assetBatchItem 是一条逐条结论。
//
// 带上名称与节点：结果面板要能让用户认出"这条说的是哪台"，
// 只回一个 id 等于让用户自己回去对号入座。
type assetBatchItem struct {
	ID    int64  `json:"id"`
	Name  string `json:"name,omitempty"`
	Node  string `json:"node,omitempty"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// handleAssetsBatch 批量维护资产。
//
// 与 ops 批量下发同一套语义（照搬既有约定，不另造一套）：
//   - **逐条给结论**，部分成功是常态，不因为某一条失败就整批回滚；
//   - 资源范围在 API 层**逐条**判定，范围外一律按「资产不存在」
//     （与 assetInScope 的既有约定一致，不给范围探测留信息）；
//   - 单批上限显式 400；**一条都没成功才 409**，否则调用方会把"全失败"读成"部分成功"；
//   - 整批只写一条审计（每条的字段级变更历史由单条写方法各自记录）。
func (a *API) handleAssetsBatch(w http.ResponseWriter, r *http.Request) {
	if a.assets == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "资产台账未启用"})
		return
	}
	var body assetBatchBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return
	}
	op := strings.TrimSpace(body.Op)
	opLabel, known := assetBatchOps[op]
	if !known {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "不支持的批量动作（可选 owner / ownerClear / labels / ignore / restore）",
		})
		return
	}
	ids := dedupeAssetIDs(body.IDs)
	if len(ids) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "必须指定至少一条资产"})
		return
	}
	if len(ids) > maxBatchAssets {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("一次最多维护 %d 条资产，当前 %d 条：请分批处理", maxBatchAssets, len(ids)),
		})
		return
	}
	if err := validateAssetBatchOp(op, body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	p := Principal(r)
	actor := assetActor(r)
	items := make([]assetBatchItem, 0, len(ids))
	okCount := 0

	for _, id := range ids {
		item := assetBatchItem{ID: id}
		cur, found, err := a.assets.GetByID(id)
		if err != nil {
			item.Error = "查询资产失败"
			items = append(items, item)
			continue
		}
		if !found || !a.assetVisible(p, cur) {
			item.Error = "资产不存在"
			items = append(items, item)
			continue
		}
		item.Name = assetDisplayName(cur)
		item.Node = cur.Node
		if err := a.applyAssetBatchOp(op, cur, body, actor); err != nil {
			// 失败原因原样透出：服务端给的说明（如"标签键不能为空"）就是下一步该做什么
			item.Error = err.Error()
		} else {
			item.OK = true
			okCount++
		}
		items = append(items, item)
	}

	total := len(items)
	failed := total - okCount
	operator := AuthenticatedUser(r)
	operatorIP := audit.ClientIP(r)
	if a.audit != nil {
		_ = a.audit.Record(audit.Event{
			User: operator, Method: r.Method, Path: r.URL.Path,
			Status: http.StatusOK, RemoteIP: operatorIP, Succeeded: true,
			Category: "assets", Action: "batch:" + op,
			Detail: fmt.Sprintf("批量%s：目标 %d 条，成功 %d、失败 %d%s",
				opLabel, total, okCount, failed, assetReasonSuffix(body.Reason)),
		})
	}
	slog.Info("已执行资产批量维护", "op", op, "total", total, "ok", okCount, "failed", failed, "operator", operator)

	status := http.StatusOK
	if okCount == 0 {
		// 一条都没成功（全部范围外/全部被拒）：明确 409，
		// 别让调用方把一个失败都没有的批次当成"部分成功"。
		status = http.StatusConflict
	}
	payload := map[string]interface{}{
		"batch": map[string]interface{}{
			"op": op, "opLabel": opLabel,
			"total": total, "ok": okCount, "failed": failed, "items": items,
		},
	}
	if okCount == 0 {
		payload["error"] = "没有任何一条资产被更新（原因见 items）"
	}
	writeJSON(w, status, payload)
}

// applyAssetBatchOp 执行单条批量动作。
//
// 全部复用**单条写方法**（Apply / ResetManual / SetLabels / Ignore / Restore）：
// 批量不该有"另一条写路径"——否则字段级变更历史、审计与校验迟早出现两套口径。
func (a *API) applyAssetBatchOp(op string, cur asset.Asset, body assetBatchBody, actor string) error {
	ref := asset.Ref{TypeKey: cur.TypeKey, NaturalKey: cur.NaturalKey}
	switch op {
	case "owner":
		_, _, err := a.assets.Apply(asset.Observation{
			TypeKey: ref.TypeKey, NaturalKey: ref.NaturalKey, Name: cur.Name, Node: cur.Node,
			Source: asset.SourceManual, Actor: actor,
			Attrs: map[string]string{asset.OwnerKey: strings.TrimSpace(body.Owner)},
		})
		return err
	case "ownerClear":
		// 本来就没有责任人 = 已经处在目标状态，按成功计（幂等）。
		// ResetManual 在"没有人工值可恢复"时会报错，那是给**单条**操作用的提示；
		// 批量里把"本来就是对的"报成失败，会让结果面板全是失败、用户以为整批没生效
		// ——而且一整批里本来就有一部分资产没有责任人，这是常态。
		if strings.TrimSpace(cur.Owner()) == "" {
			return nil
		}
		_, err := a.assets.ResetManual(ref, []string{asset.OwnerKey}, actor)
		return err
	case "labels":
		_, err := a.assets.SetLabels(ref, body.Labels, body.Remove, actor)
		return err
	case "ignore":
		_, err := a.assets.Ignore(ref, actor, strings.TrimSpace(body.Reason))
		return err
	case "restore":
		_, err := a.assets.Restore(ref, actor)
		return err
	}
	return fmt.Errorf("不支持的批量动作 %s", op)
}

// validateAssetBatchOp 做动作级的参数校验（在逐条执行之前一次性拦下）。
//
// 放在批量入口而不是逐条执行里：参数不合法是**整批**的问题，逐条报同一个错
// 只会让结果面板被 N 条一模一样的失败刷屏。
func validateAssetBatchOp(op string, body assetBatchBody) error {
	switch op {
	case "owner":
		if strings.TrimSpace(body.Owner) == "" {
			return errors.New("转派责任人必须填写责任人；要清空责任人请选「清除责任人」")
		}
	case "labels":
		if len(body.Labels) == 0 && len(body.Remove) == 0 {
			return errors.New("打标签需要至少一个标签键（写入或删除）")
		}
		for k := range body.Labels {
			if strings.TrimSpace(k) == "" {
				return errors.New("标签键不能为空")
			}
		}
		for _, k := range body.Remove {
			if strings.TrimSpace(k) == "" {
				return errors.New("要删除的标签键不能为空")
			}
			if _, dup := body.Labels[strings.TrimSpace(k)]; dup {
				return fmt.Errorf("标签 %s 同时出现在写入与删除中", strings.TrimSpace(k))
			}
		}
	}
	return nil
}

// dedupeAssetIDs 去重并丢弃非法 ID（保持调用方给出的顺序）。
func dedupeAssetIDs(ids []int64) []int64 {
	seen := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// assetDisplayName 取资产的展示名（无名称时退回自然键），与列表口径一致。
func assetDisplayName(a asset.Asset) string {
	if strings.TrimSpace(a.Name) != "" {
		return a.Name
	}
	return a.NaturalKey
}

// assetReasonSuffix 把忽略理由拼进审计详情（没有就不加）。
func assetReasonSuffix(reason string) string {
	if r := strings.TrimSpace(reason); r != "" {
		return "（理由：" + r + "）"
	}
	return ""
}

// handleAssetExport 导出资产清单 CSV。
//
// 与列表**同一个筛选解析函数 + 同一套资源范围下推**，但绕过分页：
// 导出必须与"筛出来的那个集合"逐条一致，否则用户会拿一份少了一半的表去盘点。
// 权限点独立（assets:export）：一次把整份台账（含 IP、责任人、标签）落盘，
// 与「逐页翻看」不是一个量级的动作。
func (a *API) handleAssetExport(w http.ResponseWriter, r *http.Request) {
	if a.assets == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "资产台账未启用"})
		return
	}
	filter, ok := a.assetListFilter(w, r)
	if !ok {
		return
	}
	total, err := a.assets.Count(filter)
	if err != nil {
		slog.Error("统计待导出资产失败", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "统计待导出资产失败"})
		return
	}
	if total > maxExportAssets {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("命中 %d 条，超过单次导出上限 %d 条：请先按类型 / 节点 / 标签缩小范围",
				total, maxExportAssets),
		})
		return
	}
	rows, err := a.assets.ListAll(filter)
	if err != nil {
		slog.Error("导出资产清单失败", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "导出资产清单失败"})
		return
	}

	// 一次请求只取一次"现在"，与列表/摘要同一口径（否则门槛上的资产两处结论不同）
	staleBefore := assetStaleBefore(time.Now())
	fname := fmt.Sprintf("assets-%s.csv", time.Now().Format("20060102-150405"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+fname)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("\xEF\xBB\xBF")) // BOM：Excel 打开中文列名不乱码

	cw := csv.NewWriter(w)
	_ = cw.Write([]string{
		"资产ID", "名称", "类型", "自然键", "归属节点", "上报状态", "来源", "责任人",
		"标签", "已忽略", "忽略理由", "人工值", "首次发现", "最近上报",
	})
	for _, it := range rows {
		manual, conflict := it.SourceMix()
		_ = cw.Write([]string{
			fmt.Sprintf("ast_%d", it.ID),
			it.Name,
			it.TypeKey,
			it.NaturalKey,
			it.Node,
			assetStatusLabelCSV(assetStatusOf(it, staleBefore)),
			assetSourceLabelCSV(assetSourceKind(manual, conflict)),
			it.Owner(),
			formatAssetLabels(it.Labels),
			boolLabelCSV(it.Ignored),
			it.IgnoreReason,
			formatManualAttrs(it),
			formatCSVTime(it.CreatedAt),
			formatCSVTime(it.LastSeenAt()),
		})
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		// 响应头已发出，无法再改状态码；如实记日志，避免"导出到一半失败却没人知道"
		slog.Error("写资产 CSV 失败", "err", err)
	}
	if a.audit != nil {
		_ = a.audit.Record(audit.Event{
			User: AuthenticatedUser(r), Method: r.Method, Path: r.URL.Path,
			Status: http.StatusOK, RemoteIP: audit.ClientIP(r), Succeeded: true,
			Category: "assets", Action: "export",
			Detail: fmt.Sprintf("导出资产清单 %d 条%s", len(rows), assetFilterSuffix(filter)),
		})
	}
	slog.Info("已导出资产清单", "rows", len(rows), "operator", AuthenticatedUser(r))
}

// formatAssetLabels 把标签拼成 `k=v; k=v`（键排序：同一份数据两次导出必须一致，
// 否则"这两份 CSV 哪里不同"会变成一个没法回答的问题）。
func formatAssetLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		if v := labels[k]; v != "" {
			parts = append(parts, k+"="+v)
			continue
		}
		parts = append(parts, k)
	}
	return strings.Join(parts, "; ")
}

// formatManualAttrs 只导出**人工值**（含责任人以外的补充信息）。
//
// 刻意不导出采集值：那是逐台几十项的时序侧数据，混进清单会让表无法阅读，
// 而清单要回答的是"这台是谁的、归哪、打了什么标签"。
func formatManualAttrs(a asset.Asset) string {
	parts := []string{}
	for _, attr := range a.Attrs {
		if attr.Source != asset.SourceManual || attr.Key == asset.OwnerKey {
			continue
		}
		parts = append(parts, attr.Key+"="+attr.Value)
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}

func formatCSVTime(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).Format("2006-01-02 15:04:05")
}

func boolLabelCSV(v bool) string {
	if v {
		return "是"
	}
	return "否"
}

// assetStatusLabelCSV / assetSourceLabelCSV 把内部取值翻成中文：
// 导出的表是给人（与 Excel）看的，写 online/missing 等于让用户自己去猜。
func assetStatusLabelCSV(status string) string {
	switch status {
	case asset.StatusOnline:
		return "在线"
	case asset.StatusMissing:
		return "失联"
	case asset.StatusArchived:
		return "归档"
	}
	return status
}

func assetSourceLabelCSV(src string) string {
	switch src {
	case asset.SourceFilterAuto:
		return "采集"
	case asset.SourceFilterManual:
		return "人工"
	case asset.SourceFilterMixed:
		return "混合"
	}
	return src
}

// assetFilterSuffix 把生效的筛选条件写进审计（"导出了什么"必须可回看）。
func assetFilterSuffix(f asset.ListFilter) string {
	conds := []string{}
	if f.TypeKey != "" {
		conds = append(conds, "类型="+f.TypeKey)
	}
	if f.Node != "" {
		conds = append(conds, "节点="+f.Node)
	}
	if f.Status != "" {
		conds = append(conds, "状态="+f.Status)
	}
	if f.Source != "" {
		conds = append(conds, "来源="+f.Source)
	}
	if f.Keyword != "" {
		conds = append(conds, "关键词="+f.Keyword)
	}
	if f.Label != "" {
		conds = append(conds, "标签="+f.Label)
	}
	if f.Ignored != "" {
		conds = append(conds, "已忽略="+f.Ignored)
	}
	if len(conds) == 0 {
		return "（全部）"
	}
	return "（" + strings.Join(conds, "、") + "）"
}
