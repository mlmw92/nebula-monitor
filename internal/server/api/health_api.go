package api

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/nebula/monitor/internal/server/selfmon"
	"github.com/nebula/monitor/internal/version"
)

// SetSelfMon 注入自监控收集器（可选）。需在 RegisterRoutes 之前调用。
func (a *API) SetSelfMon(mon *selfmon.Monitor) { a.selfmon = mon }

// metricsResponseWriter 记录响应状态码，供自监控统计错误率。
type metricsResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *metricsResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Hijack 透传底层连接的接管能力。
// 必须实现：/ws 的协议升级依赖 Hijack，若外层包装把它「吃掉」，
// 所有 WebSocket 连接都会握手失败（这是包装 ResponseWriter 最容易踩的坑之一）。
func (w *metricsResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("底层 ResponseWriter 不支持 Hijack")
	}
	return h.Hijack()
}

// Flush 透传刷新，避免流式响应的缓冲语义被改变。
func (w *metricsResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// MetricsMiddleware 统计全部 HTTP 请求数与错误数。
//
// 放在中间件链最外层：这样被鉴权拒绝的请求（401/403）也会被计入，
// 否则「大量 401」这种最需要被看见的自监控信号反而看不到。
func MetricsMiddleware(next http.Handler, mon *selfmon.Monitor) http.Handler {
	if mon == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &metricsResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		mon.AddHTTPRequest(rec.status)
	})
}

// healthResponse 是存活探针响应体。
type healthResponse struct {
	Status        string `json:"status"`
	Version       string `json:"version"`
	UptimeSeconds int64  `json:"uptimeSeconds"`
}

// handleHealthz 存活探针（公开）：只表明进程仍在服务，不做任何依赖检查。
//
// 存活探针的语义是「要不要重启本进程」，因此不检查依赖；
// 依赖故障由 /readyz 表达。两者都在公开白名单内（见 auth.isPublicPath），
// 因为 k8s / systemd / 反向代理通常无法携带登录令牌。
func (a *API) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{
		Status:        "ok",
		Version:       version.Version,
		UptimeSeconds: int64(time.Since(a.startedAt).Seconds()),
	})
}

// readyCheck 是单项就绪检查结果。
type readyCheck struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// handleReadyz 就绪探针（公开）：校验关键依赖，任一不满足返回 503。
func (a *API) handleReadyz(w http.ResponseWriter, r *http.Request) {
	checks := map[string]readyCheck{}
	ready := true
	markBad := func(name, detail string) {
		checks[name] = readyCheck{OK: false, Detail: detail}
		ready = false
	}

	// 时序库：做一次真实即时查询。比「配置已加载」有意义——TSDB 不可用时
	// Agent 上报会直接失败，这是硬依赖而非可选能力。
	switch {
	case a.store == nil:
		markBad("tsdb", "未配置时序库")
	default:
		if _, err := a.store.QueryAllLatest("self_uptime_seconds", nil); err != nil {
			markBad("tsdb", "查询失败: "+err.Error())
		} else {
			checks["tsdb"] = readyCheck{OK: true, Detail: a.store.Backend()}
		}
	}

	// 告警引擎：启用后评估循环不应停摆。未注入自监控或未启用告警时跳过该项。
	if mon := a.selfmon; mon != nil {
		if iv := mon.EvalInterval(); iv > 0 {
			s := mon.Snapshot()
			limit := int64(3*iv.Seconds()) + 5
			// 宽限期统一用本进程的运行时长（与响应里的 uptimeSeconds 同源），
			// 避免「启动后首次评估尚未完成」被误判为停摆。
			uptime := int64(time.Since(a.startedAt).Seconds())
			switch {
			case s.Alert.EvalTotal == 0 && uptime > limit:
				markBad("alertEngine", fmt.Sprintf("启动 %d 秒仍未完成首次评估", uptime))
			case s.Alert.EvalAgeSeconds > limit:
				markBad("alertEngine", fmt.Sprintf("评估已停摆 %d 秒", s.Alert.EvalAgeSeconds))
			default:
				checks["alertEngine"] = readyCheck{OK: true}
			}
		}
	}

	status := http.StatusOK
	label := "ok"
	if !ready {
		status = http.StatusServiceUnavailable
		label = "degraded"
	}
	writeJSON(w, status, map[string]interface{}{
		"status":        label,
		"checks":        checks,
		"version":       version.Version,
		"uptimeSeconds": int64(time.Since(a.startedAt).Seconds()),
	})
}

// handleSelfStatus 返回 Server 自监控快照（供前端「系统自监控」页与运维排查）。
// GET /api/v1/self/status
func (a *API) handleSelfStatus(w http.ResponseWriter, r *http.Request) {
	snap := a.selfmon.Snapshot() // 收集器为 nil 时返回零值快照，接口仍可用
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"metrics":      snap,
		"nodeLabel":    snap.Node,
		"collectedAt":  snap.CollectedAt,
		"metricPrefix": selfmon.MetricPrefix,
	})
}
