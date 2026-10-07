package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/nebula/monitor/internal/server/asset"
)

// 资产生命周期与标签维护：忽略（隐藏）/ 恢复 / 彻底删除 / 标签。
//
// 为什么「忽略」与「删除」是两件事：采集资产由 Agent 每轮上报驱动，
// 删掉之后会被下一轮上报**重建**（表现为"删了又回来"）。因此：
//   - 忽略：从台账隐藏，采集继续（数据保持新鲜），可随时恢复 → 对采集资产是正确动作；
//   - 彻底删除：仅纯人工建档资产（不会被重建），不可恢复。

// assetRefOf 把已解析的资产转成服务层引用。
func assetRefOf(item asset.Asset) asset.Ref {
	return asset.Ref{TypeKey: item.TypeKey, NaturalKey: item.NaturalKey}
}

// assetViewAfter 是写操作后统一回填详情视图：写操作返回最新状态，
// 让前端不必再发一次 GET（也避免"列表已更新、抽屉还是旧的"这种不一致）。
func assetViewAfter(a *API, ref asset.Ref) (assetView, error) {
	item, ok, err := a.assets.Get(ref)
	if err != nil {
		return assetView{}, err
	}
	if !ok {
		return assetView{}, errors.New("资产不存在")
	}
	return toAssetView(item, assetStaleBefore(time.Now())), nil
}

type assetIgnoreBody struct {
	// Reason 是可选的忽略理由（界面上"为什么这条被我藏了"往往比"谁藏的"更重要）。
	Reason string `json:"reason"`
}

// handleAssetIgnore 忽略资产（从台账隐藏，可恢复）。
func (a *API) handleAssetIgnore(w http.ResponseWriter, r *http.Request) {
	item, ok := a.assetInScope(w, r)
	if !ok {
		return
	}
	var body assetIgnoreBody
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
			return
		}
	}
	updated, err := a.assets.Ignore(assetRefOf(item), assetActor(r), body.Reason)
	if err != nil {
		slog.Error("忽略资产失败", "asset", item.NaturalKey, "err", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, toAssetView(updated, assetStaleBefore(time.Now())))
}

// handleAssetRestore 解除忽略（幂等：未被忽略也返回成功）。
func (a *API) handleAssetRestore(w http.ResponseWriter, r *http.Request) {
	item, ok := a.assetInScope(w, r)
	if !ok {
		return
	}
	updated, err := a.assets.Restore(assetRefOf(item), assetActor(r))
	if err != nil {
		slog.Error("恢复资产失败", "asset", item.NaturalKey, "err", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, toAssetView(updated, assetStaleBefore(time.Now())))
}

// handleAssetPurge 彻底删除资产（不可恢复，仅限纯人工建档资产）。
//
// 采集资产返回 409：请求本身合法，但这个动作对这类资源不适用——
// 用 409 而不是 400，便于前端把提示写成"该用忽略，而不是删除"。
func (a *API) handleAssetPurge(w http.ResponseWriter, r *http.Request) {
	item, ok := a.assetInScope(w, r)
	if !ok {
		return
	}
	baselineCleared, err := a.assets.Purge(assetRefOf(item))
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"purged": true, "displayId": fmt.Sprintf("ast_%d", item.ID), "baselineCleared": baselineCleared,
	})
}

type assetLabelsBody struct {
	// Labels 是要写入/覆盖的标签（值可以是空串，表示"该标签无值/仅标记存在"）。
	Labels map[string]string `json:"labels"`
	// Remove 是要删除的标签键；与 Labels 的键冲突时按错误处理（避免"同时写入与删除"的歧义）。
	Remove []string `json:"remove"`
}

// handleAssetLabels 维护资产标签（分类维度，与属性分开）。
func (a *API) handleAssetLabels(w http.ResponseWriter, r *http.Request) {
	item, ok := a.assetInScope(w, r)
	if !ok {
		return
	}
	var body assetLabelsBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return
	}
	if _, err := a.assets.SetLabels(assetRefOf(item), body.Labels, body.Remove, assetActor(r), RequestID(r)); err != nil {
		slog.Error("维护资产标签失败", "asset", item.NaturalKey, "err", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	view, err := assetViewAfter(a, assetRefOf(item))
	if err != nil {
		slog.Error("回读资产失败", "asset", item.NaturalKey, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "回读资产失败"})
		return
	}
	writeJSON(w, http.StatusOK, view)
}
