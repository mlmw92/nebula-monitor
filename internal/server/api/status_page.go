package api

import (
	"net/http"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/dialtest"
)

// 对外状态页（C3）：**无需登录**的服务可用性页面，复用拨测数据。
//
// 三条设计原则（每一条都对应一个具体的风险）：
//
//  1. **默认不暴露任何东西**：只有显式勾选 `public: true` 的拨测任务才会出现
//     （见 dialtest.Task.Public）。状态页是给别人看的，默认曝光等于把内部服务清单公开。
//  2. **最小信息**：只返回「名称 / 类型 / 是否可用 / 延迟 / 可用率 / 最近检查时间」。
//     刻意**不含** `target`、节点名与错误原文——内部主机名、端口、错误细节
//     （常带内网 IP、路径、甚至凭据线索）都不该出现在对外页面里。
//  3. **一个页面不该 500**：时序库查询失败时按「未知」处理并照常返回 200。
//     否则外部访问者看到的「页面打不开」会被误读成我们挂了——恰恰相反。
//
// 数据来源：拨测结果本身就是指标（`dial_test_up` / `dial_test_latency`），
// 因此「当前状态」取最新样本、「可用率」取近 24 小时的平均值，不需要额外的存储。

// publicStatusUptimeWindow 是可用率的统计窗口。
const publicStatusUptimeWindow = 24 * time.Hour

// publicStatusStepMillis 是可用率查询的步长（1 小时）。
//
// 取 1 小时：可用率是「概览」量级，1 小时粒度足够，且点数少（24 点）——
// 对外页面不应把时序库的原始点数直接搬出去。
const publicStatusStepMillis = int64(time.Hour / time.Millisecond)

// publicUpItem 是对外状态页的一项。
//
// Status 用字符串而不是布尔：**「不知道」和「坏了」必须区分**。
// 时序库查询失败时若显示成 down，就把我们自己的故障说成了服务的故障——方向正好反了。
type publicUpItem struct {
	Name        string  `json:"name"`
	Type        string  `json:"type"`
	Status      string  `json:"status"` // up | down | unknown
	LatencyMs   float64 `json:"latencyMs"`
	Uptime      float64 `json:"uptime"`      // 近 24 小时可用率（0-100）；无数据为 -1
	LastCheckAt int64   `json:"lastCheckAt"` // 最近一次检查时间（毫秒）；无数据为 0
}

// publicStatus 是对外状态页的响应。
type publicStatus struct {
	UpdatedAt int64          `json:"updatedAt"`
	Overall   string         `json:"overall"` // up | partial | down | unknown
	Items     []publicUpItem `json:"items"`
}

// handlePublicStatus 处理 GET /api/v1/status（对外状态页，无需登录）。
func (a *API) handlePublicStatus(w http.ResponseWriter, r *http.Request) {
	out := publicStatus{UpdatedAt: model.NowMillis(), Overall: "unknown", Items: []publicUpItem{}}
	if a.dialtest == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}

	tasks := make([]dialtest.Task, 0, 8)
	for _, t := range a.dialtest.List() {
		// 只有「显式勾选对外」且「启用中」的任务出现：停用的任务没有新数据，
		// 显示在状态页上只会让人以为它刚刚坏了
		if t.Public && t.Enabled {
			tasks = append(tasks, t)
		}
	}
	if len(tasks) == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}

	// 当前状态：一次跨全部序列的即时查询（与既有拨测接口同一条路径）
	upBySeries := seriesLastValues(a.latestDialtest("dial_test_up"))
	latBySeries := seriesLastValues(a.latestDialtest("dial_test_latency"))

	up, known := 0, 0
	for _, t := range tasks {
		item := publicUpItem{Name: t.Name, Type: string(t.Type), Status: "unknown", Uptime: -1}
		if v, ts, ok := pickByName(upBySeries, t.Name); ok {
			if v > 0 {
				item.Status = "up"
				up++
			} else {
				item.Status = "down"
			}
			known++
			item.LastCheckAt = ts
		}
		if v, _, ok := pickByName(latBySeries, t.Name); ok {
			item.LatencyMs = v
		}
		if pct, ts, ok := a.dialtestUptime(t, out.UpdatedAt); ok {
			item.Uptime = pct
			if ts > item.LastCheckAt {
				item.LastCheckAt = ts
			}
		}
		out.Items = append(out.Items, item)
	}

	switch {
	case known == 0:
		// 一条数据都没读到（时序库不可用或任务还没跑过）：如实说「未知」，
		// 而不是让外部访问者以为服务全挂了
		out.Overall = "unknown"
	case up == known && known == len(out.Items):
		out.Overall = "up"
	case up == 0:
		out.Overall = "down"
	default:
		out.Overall = "partial"
	}
	writeJSON(w, http.StatusOK, out)
}

// latestDialtest 取某拨测指标的当前序列；失败时返回 nil（对外页面按「未知」处理）。
func (a *API) latestDialtest(metric string) []model.Series {
	if a.store == nil {
		return nil
	}
	series, err := a.store.QueryAllLatest(metric, nil)
	if err != nil {
		return nil
	}
	return series
}

// dialtestUptime 计算某任务近 24 小时的可用率（百分比）。
//
// 按**任务名**匹配：拨测指标只有 name/type/target 三个标签（没有任务 ID），
// 与既有「拨测最新结果」接口一致。同名任务会合并统计（取最差值），
// 因为对外页面关心的是「这个服务整体可用吗」。
func (a *API) dialtestUptime(t dialtest.Task, now int64) (float64, int64, bool) {
	if a.store == nil {
		return 0, 0, false
	}
	series, err := a.store.QueryRange("", "dial_test_up", map[string]string{"name": t.Name},
		now-int64(publicStatusUptimeWindow/time.Millisecond), now, publicStatusStepMillis)
	if err != nil {
		return 0, 0, false
	}
	worst := -1.0
	var lastTS int64
	for _, s := range series {
		if len(s.Points) == 0 {
			continue
		}
		sum, n := 0.0, 0
		for _, p := range s.Points {
			sum += p.Value
			n++
			if p.Timestamp > lastTS {
				lastTS = p.Timestamp
			}
		}
		if n == 0 {
			continue
		}
		pct := sum / float64(n) * 100
		if worst < 0 || pct < worst {
			worst = pct
		}
	}
	if worst < 0 {
		return 0, 0, false
	}
	return roundTo1(worst), lastTS, true
}

// seriesLastValues 把「即时查询的序列集合」摊平成「序列标签 → (最新值, 时间)」，
// 以 name 标签为键（拨测指标用它标识任务）。
func seriesLastValues(series []model.Series) map[string]struct {
	V  float64
	TS int64
} {
	out := make(map[string]struct {
		V  float64
		TS int64
	}, len(series))
	for _, s := range series {
		name := s.Labels["name"]
		if name == "" || len(s.Points) == 0 {
			continue
		}
		last := s.Points[len(s.Points)-1]
		// 同名多序列（不同 target）取**最差**：状态页上一个服务「部分可用」应当显示为异常
		if cur, ok := out[name]; ok && cur.V <= last.Value {
			continue
		}
		out[name] = struct {
			V  float64
			TS int64
		}{last.Value, last.Timestamp}
	}
	return out
}

// pickByName 取某名字的最新值与时间。
func pickByName(m map[string]struct {
	V  float64
	TS int64
}, name string) (float64, int64, bool) {
	v, ok := m[name]
	return v.V, v.TS, ok
}

// roundTo1 保留一位小数（对外展示用）。
func roundTo1(v float64) float64 {
	return float64(int(v*10+0.5)) / 10
}
