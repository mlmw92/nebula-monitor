package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/nebula/monitor/internal/server/asset"
	"github.com/nebula/monitor/internal/server/auth"
)

// 周期化巡检的接口（设计件 docs/superpowers/specs/2026-10-07-inspect-schedule-design.md）。
//
// 与 `POST /api/v1/inspect/runs` 的分工：那个是"现在跑一次"，这里是"以后每隔一段时间自动跑一次"。
// 手动"立即执行"也走调度器的 RunNow，因此"上次运行"的口径一致——否则界面上会出现
// "我明明刚跑过，调度却说到点该跑了"（报告调度定过的同一条）。

// InspectScheduleProvider 提供周期化巡检调度（可空；未注入时接口返回 503）。
type InspectScheduleProvider interface {
	Config() asset.InspectScheduleConfig
	Save(asset.InspectScheduleConfig, string) error
	Status() asset.InspectScheduleStatus
	RunNow(by string, manual bool) asset.InspectScheduleRun
}

// SetInspectScheduler 注入周期化巡检调度器（未注入时相关接口返回 503，与"该能力未启用"一致）。
func (a *API) SetInspectScheduler(s InspectScheduleProvider) { a.inspectSched = s }

// InspectScopeSnapshot 按用户名折算范围快照——「身份 → 巡检范围」的唯一出口。
//
// 它同时是**保存配置时**的取值来源与**执行时**的收窄依据：保存者被降权后，下一次执行会按新的
// 更窄范围跑（交集只朝收窄方向变，不会反过来放大）。
//
// 三种情形要分清：
//   - 未启用登录认证：没有身份与范围的概念，业务接口一律放行（与 auth 的兼容策略同取向）
//     → 返回"两个维度都不限"；
//   - 启用认证但用户名解析不到（账号被删/被停用）→ ok=false，由调度器**拒绝执行**并写明原因；
//   - 受限身份：受限的维度写 limited + 清单（清单为空即恒不匹配，fail-closed）。
//
// 受限维度的判定用 `IsGlobal()` / `LimitsAssets()` 而不是"清单空不空"：清单为空在受限与不限
// 两种语义下都合法，靠它猜方向会把"受限但一个都看不到"读成"全部可见"。
func (a *API) InspectScopeSnapshot(user string) (asset.ScopeSnapshot, bool) {
	if a.authStore == nil {
		return asset.ScopeSnapshot{NodeMode: asset.ScopeModeAll, AssetMode: asset.ScopeModeAll}, true
	}
	// GetPrincipal 返回 nil 表示这个身份已经解析不到（账号被删/被停用）——**不能**当成"不限"。
	p := a.authStore.GetPrincipal(strings.TrimSpace(user))
	if p == nil {
		return asset.ScopeSnapshot{}, false
	}
	snap := asset.ScopeSnapshot{NodeMode: asset.ScopeModeAll, AssetMode: asset.ScopeModeAll}
	if !p.Scope.IsGlobal() {
		snap.NodeMode = asset.ScopeModeLimited
		// nil = 全局（不会走到这里）；受限但没有任何节点 = 空切片（fail-closed）
		snap.Nodes = a.assetAllowedNodes(p)
	}
	if p.Scope.LimitsAssets() {
		snap.AssetMode = asset.ScopeModeLimited
		if d := auth.ResolveAssetScope(p); d.Selectors != nil {
			for _, sel := range d.Selectors {
				snap.Labels = append(snap.Labels, asset.ScopeLabel{Key: sel.Key, Value: sel.Value})
			}
		}
	}
	return snap, true
}

// handleInspectScheduleGet 返回周期化巡检的配置与上次运行结果。
func (a *API) handleInspectScheduleGet(w http.ResponseWriter, r *http.Request) {
	if a.inspectSched == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "周期化巡检未启用"})
		return
	}
	writeJSON(w, http.StatusOK, a.inspectSched.Status())
}

// handleInspectScheduleSave 保存配置（热生效）。
//
// 权限用 `inspect:run`（不新增权限点）：它决定"以后会自动跑巡检"，与手动触发是同一类动作，
// 与报告页"读 report:read、改与跑 report:export"同构。
func (a *API) handleInspectScheduleSave(w http.ResponseWriter, r *http.Request) {
	if a.inspectSched == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "周期化巡检未启用"})
		return
	}
	var cfg asset.InspectScheduleConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体解析失败"})
		return
	}
	// 范围**只能由服务端按身份折算**：请求体里带的 scope 一律覆盖掉。
	// 否则一个受限用户只要提交一个更宽的范围，就能让定时巡检跑到自己看不到的资产上去。
	snapshot, ok := a.InspectScopeSnapshot(assetActor(r))
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "无法折算当前身份的范围，请重新登录后再保存"})
		return
	}
	cfg.Scope = snapshot
	if err := a.inspectSched.Save(cfg, assetActor(r)); err != nil {
		slog.Error("保存周期化巡检配置失败", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "保存周期化巡检配置失败"})
		return
	}
	writeJSON(w, http.StatusOK, a.inspectSched.Status())
}

// handleInspectScheduleRun 立即执行一次巡检（会更新"上次运行"，与调度到点执行同一个入口）。
//
// 与自动执行的区别只有一处、且是**收窄**方向：手动执行会再按**点击者**的当前范围取交集——
// 配置里的快照可能是管理员保存的"全部"，若只按快照跑，一个范围很窄的人点一下就能拿到
// 范围外资产的差异项。
func (a *API) handleInspectScheduleRun(w http.ResponseWriter, r *http.Request) {
	if a.inspectSched == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "周期化巡检未启用"})
		return
	}
	run := a.inspectSched.RunNow(assetActor(r), true)
	if run.Error != "" {
		// 执行失败/本轮未执行都不是"请求写错了"：502 + 结果体，让前端与运维都看清原因
		// （与报告调度的"立即生成"同一取向）。
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{"run": run, "error": run.Error})
		return
	}
	writeJSON(w, http.StatusOK, run)
}
