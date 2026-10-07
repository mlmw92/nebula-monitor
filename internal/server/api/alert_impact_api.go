package api

import (
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/server/asset"
)

// 告警 ↔ 资产 ↔ 变更 ↔ 波及范围（全景表 1-05）。
//
// 告警事件只带 node / instance（都是指标标签），台账里的资产是另一套身份。
// 这个接口把两者对上，并一次给出排障真正需要的三件事：
//
//	① 这条告警落在哪条资产上（可能不止一条）；
//	② 该资产最近有什么变更（"刚改过什么"是最高频的根因）；
//	③ 波及范围（以它为中心的 N 跳关系邻域）。
//
// 为什么不做成"给每条告警都带上资产"：告警列表是**高频轮询**的接口，
// 逐条去台账解析会把列表变成 N 次查询；而资产联动是点开某一条时才需要的信息。

// alertImpactChangeView 是一条与告警相关的资产变更。
//
// 带上资产身份（而不只是变更内容）：命中多条资产时，界面必须说清"是哪条资产的变更"，
// 否则用户会把 A 机器的改动当成 B 机器的。
type alertImpactChangeView struct {
	AssetID    int64  `json:"assetId"`
	TypeKey    string `json:"typeKey"`
	NaturalKey string `json:"naturalKey"`
	Name       string `json:"name,omitempty"`
	Field      string `json:"field"`
	Old        string `json:"old"`
	New        string `json:"new"`
	Source     string `json:"source"`
	Actor      string `json:"actor,omitempty"`
	At         int64  `json:"at"`
}

// alertImpactMaxAssets 是参与"近期变更"汇总的资产数上限。
// 命中很多条时全部去查历史会把一次点开变成一串查询；前几条足够回答"刚改了什么"。
const alertImpactMaxAssets = 3

// alertImpactMaxChanges 是返回的变更条数上限（合并后按时间倒序取前 N 条）。
const alertImpactMaxChanges = 20

// handleAlertImpact 返回某条告警涉及的资产、近期变更与波及范围。
//
// 参数是**指标标签**（node / instance）而不是告警 ID：告警事件在恢复后就从活跃列表消失，
// 而排障往往发生在之后；用标签寻址，历史的告警详情页也能复用这个入口。
func (a *API) handleAlertImpact(w http.ResponseWriter, r *http.Request) {
	if a.assets == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "资产台账未启用"})
		return
	}
	q := r.URL.Query()
	node := strings.TrimSpace(q.Get("node"))
	instance := strings.TrimSpace(q.Get("instance"))
	if node == "" && instance == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "需要 node 或 instance"})
		return
	}
	depth, _ := assetTopologyBounds(q.Get("depth"), "")

	matched, err := a.alertImpactAssets(node, instance)
	if err != nil {
		slog.Error("查询告警关联资产失败", "node", node, "instance", instance, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询告警关联资产失败"})
		return
	}

	// 资源范围：范围外的资产一律不出现（与列表 / 详情同一口径）。
	// 这里**不是** 404：告警的影响面是"补充信息"，范围外或还没进台账都只是"没匹配到"，
	// 用 404 会让前端把一次正常的信息缺失显示成错误。
	p := Principal(r)
	staleBefore := assetStaleBefore(time.Now())
	visible := make([]asset.Asset, 0, len(matched))
	views := make([]assetView, 0, len(matched))
	for _, item := range matched {
		if !a.assetVisible(p, item) {
			continue
		}
		visible = append(visible, item)
		views = append(views, toAssetView(item, staleBefore))
	}

	changes, err := a.alertImpactChanges(visible)
	if err != nil {
		slog.Error("查询告警关联资产的变更失败", "node", node, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询资产变更失败"})
		return
	}

	out := map[string]interface{}{
		"node":     node,
		"instance": instance,
		"matched":  views,
		"changes":  changes,
		"note":     alertImpactNote(node, instance, len(visible)),
	}
	// 波及范围以**主机**为中心（没有主机资产时退到第一条命中）：
	// 主机才是"这台机器出事会影响谁"这个问题的起点。
	if len(visible) > 0 {
		root := visible[0]
		for _, item := range visible {
			if item.TypeKey == asset.TypeHost {
				root = item
				break
			}
		}
		topology, err := a.assetTopologyPayload(assetRefOf(root), depth, 0, p)
		if err == nil {
			out["topology"] = topology
		} else if !errors.Is(err, asset.ErrOutOfScope) {
			// 图画不出来不该让整个影响面失败：其余三项（命中资产、变更）仍然有用。
			slog.Warn("查询告警波及范围失败", "asset", root.NaturalKey, "err", err)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// alertImpactAssets 按指标标签解析涉及的资产。
//
// 主机与实例都查、都返回：一条告警的 node 是主机、instance 是实例地址，
// 两者都是"这条告警落在哪"的有效答案，而且排障时通常两个都想看
// （实例出问题要连它的宿主一起看）。
func (a *API) alertImpactAssets(node, instance string) ([]asset.Asset, error) {
	out := make([]asset.Asset, 0, 2)
	if node != "" {
		host, found, err := a.assets.GetHostByName(node)
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, host)
		}
	}
	if instance != "" {
		insts, err := a.assets.InstancesByAddr(instance)
		if err != nil {
			return nil, err
		}
		out = append(out, insts...)
	}
	return out, nil
}

// alertImpactChanges 汇总命中资产的近期变更（时间倒序，条数有上限）。
func (a *API) alertImpactChanges(assets []asset.Asset) ([]alertImpactChangeView, error) {
	out := make([]alertImpactChangeView, 0, alertImpactMaxChanges)
	for i, item := range assets {
		if i >= alertImpactMaxAssets {
			break
		}
		records, err := a.assets.History(assetRefOf(item), alertImpactMaxChanges)
		if err != nil {
			return nil, err
		}
		for _, rec := range records {
			out = append(out, alertImpactChangeView{
				AssetID: item.ID, TypeKey: item.TypeKey, NaturalKey: item.NaturalKey, Name: item.Name,
				Field: rec.Field, Old: rec.Old, New: rec.New,
				Source: string(rec.Source), Actor: rec.Actor, At: rec.At,
			})
		}
	}
	// 合并多个资产后必须重排：否则界面上会出现"A 的旧变更排在 B 的新变更前面"。
	sort.SliceStable(out, func(i, j int) bool { return out[i].At > out[j].At })
	if len(out) > alertImpactMaxChanges {
		out = out[:alertImpactMaxChanges]
	}
	return out, nil
}

// alertImpactNote 在没匹配到资产时给出**可操作**的说明。
//
// 空列表本身不解释原因，而"没匹配到"有三种完全不同的情况：这个节点还没上报资产、
// 实例地址与台账里的写法不一致、资产被隐藏了。把它们说清楚，
// 用户才知道该去配置采集还是去台账里找。
func alertImpactNote(node, instance string, matched int) string {
	if matched > 0 {
		return ""
	}
	switch {
	case instance != "" && node != "":
		return "台账里没有与「节点 " + node + " + 实例 " + instance + "」匹配的资产：资产由 Agent 上报自动建立，实例地址需与台账中的写法一致。"
	case instance != "":
		return "台账里没有地址为「" + instance + "」的中间件实例：实例资产的自然键是 <类型>:<地址>，请核对地址写法。"
	default:
		return "台账里没有名为「" + node + "」的主机资产：主机由 Agent 上报自动建立，若该节点尚未上报则不会出现在台账中。"
	}
}
