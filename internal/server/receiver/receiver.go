// Package receiver 实现 Server 的上报接收 HTTP 接口。
package receiver

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/config"
	"github.com/nebula/monitor/internal/server/node"
	"github.com/nebula/monitor/internal/server/storage"
)

// itoa 整型转字符串。
func itoa(v int) string { return strconv.Itoa(v) }

// Receiver 接收 Agent 上报并写入存储、更新节点索引。
type Receiver struct {
	storage storage.Storage
	nodeMgr *node.Manager
	auth    config.AgentAuthConfig
}

// New 创建 Receiver。auth 为 Agent 接入授权配置（参考哪吒探针密钥机制）。
func New(s storage.Storage, mgr *node.Manager, auth config.AgentAuthConfig) *Receiver {
	return &Receiver{storage: s, nodeMgr: mgr, auth: auth}
}

// HandleReport 处理 POST /api/v1/report。
func (r *Receiver) HandleReport(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// 接入授权校验：启用后，必须携带与配置一致的 X-Agent-Secret 头（常量时间比较，防时序侧信道）
	if r.auth.Enabled {
		got := req.Header.Get("X-Agent-Secret")
		want := r.auth.Secret
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	var payload model.ReportPayload
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if payload.Node == "" {
		http.Error(w, "node is required", http.StatusBadRequest)
		return
	}

	// 更新节点索引与心跳
	r.nodeMgr.Register(&payload)

	// 补充分组标签后写入 VM
	metrics := make([]model.Metric, 0, len(payload.Metrics)+len(payload.Processes)*2+len(payload.RedisInstances))
	for _, m := range payload.Metrics {
		if m.Node == "" {
			m.Node = payload.Node
		}
		if m.Labels == nil {
			m.Labels = map[string]string{}
		}
		m.Labels["group"] = payload.Group
		metrics = append(metrics, m)
	}
	// 进程 TOP 榜写入 VM（带 pid/comm 标签），便于跨副本查询
	for _, p := range payload.Processes {
		labels := map[string]string{"group": payload.Group, "pid": itoa(int(p.PID)), "comm": p.Name}
		metrics = append(metrics,
			model.Metric{Node: payload.Node, Name: "proc_cpu", Labels: labels, Value: p.CPU, Timestamp: payload.ReportAt},
			model.Metric{Node: payload.Node, Name: "proc_mem", Labels: labels, Value: p.Mem, Timestamp: payload.ReportAt},
		)
	}
	// Redis 实例元信息转为 redis_instance_up 指标写入 VM，供前端聚合查询
	for _, ri := range payload.RedisInstances {
		upVal := 0.0
		if ri.Up {
			upVal = 1
		}
		labels := map[string]string{
			"group":     payload.Group,
			"instance":  ri.Instance,
			"name":      ri.Name,
			"role":      ri.Role,
			"topology":  ri.Topology,
			"version":   ri.Version,
		}
		metrics = append(metrics, model.Metric{
			Node: payload.Node, Name: "redis_instance_up", Labels: labels, Value: upVal, Timestamp: payload.ReportAt,
		})
	}

	if err := r.storage.Write(metrics); err != nil {
		slog.Error("写入 VM 失败", "node", payload.Node, "err", err)
		http.Error(w, "write storage failed", http.StatusInternalServerError)
		return
	}

	// 响应：若节点仍需升级（agent 版本未达标），持续下发 upgrade 指令
	resp := map[string]interface{}{"status": "ok"}
	if r.nodeMgr.ConsumeUpgrade(payload.Node, payload.Version) {
		resp["command"] = "upgrade"
		slog.Info("已下发升级指令", "node", payload.Node, "agentVersion", payload.Version)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
