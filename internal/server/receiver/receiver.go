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
	"github.com/nebula/monitor/internal/server/instancereg"
	"github.com/nebula/monitor/internal/server/nginxaccess"
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
	ngx     *nginxaccess.Window // Nginx access log 地理聚合窗口（可空）
}

// New 创建 Receiver。auth 为 Agent 接入授权配置（参考哪吒探针密钥机制）；
// ngx 为 Nginx access log 聚合窗口，可传 nil 关闭该能力。
func New(s storage.Storage, mgr *node.Manager, auth config.AgentAuthConfig, ngx *nginxaccess.Window) *Receiver {
	return &Receiver{storage: s, nodeMgr: mgr, auth: auth, ngx: ngx}
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
		// 中间件实例指标（mysql/kafka/postgres 等）已自带 group 标签（实例名/集群名），
		// 仅当缺失时才回退到节点分组，避免把实例分组覆盖成默认的 "default"。
		if m.Labels["group"] == "" {
			m.Labels["group"] = payload.Group
		}
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
		// group 优先取 Redis 实例自身分组（agent 配置的 name，如集群名），
		// 为空时回退到节点分组，避免展示成默认的 "default"。
		group := ri.Group
		if group == "" {
			group = payload.Group
		}
		labels := map[string]string{
			"group":     group,
			"instance":  ri.Instance,
			"name":      ri.Name,
			"role":      ri.Role,
			"topology":  ri.Topology,
			"version":   ri.Version,
		}
		if ri.ReplicaOf != "" {
			labels["replica_of"] = ri.ReplicaOf
		}
		metrics = append(metrics, model.Metric{
			Node: payload.Node, Name: "redis_instance_up", Labels: labels, Value: upVal, Timestamp: payload.ReportAt,
		})
	}
	// Kubernetes 集群元信息转为 k8s_cluster_up 指标写入 VM，保证集群存活与版本可聚合查询。
	for _, ki := range payload.K8sInstances {
		upVal := 0.0
		if ki.Up {
			upVal = 1
		}
		group := ki.Group
		if group == "" {
			group = payload.Group
		}
		labels := map[string]string{
			"group":    group,
			"instance": ki.Instance,
			"name":     ki.Name,
			"version":  ki.Version,
		}
		metrics = append(metrics, model.Metric{
			Node: payload.Node, Name: "k8s_cluster_up", Labels: labels, Value: upVal, Timestamp: payload.ReportAt,
		})
	}

	// 将本次上报的中间件实例配置写入注册表：即使 Agent 离线（时序指标 stale），
	// Web 仍可从注册表枚举到"已配置但离线"的实例，避免误判为"尚未配置 Xxx 监控"。
	instancereg.Default.SetMySQL(payload.Node, payload.MySQLInstances)
	instancereg.Default.SetRedis(payload.Node, payload.RedisInstances)
	instancereg.Default.SetPostgres(payload.Node, payload.PostgresInstances)
	instancereg.Default.SetNginx(payload.Node, payload.NginxInstances)
	instancereg.Default.SetKafka(payload.Node, payload.KafkaInstances)
	instancereg.Default.SetDocker(payload.Node, payload.DockerInstances)
	instancereg.Default.SetRocketMQ(payload.Node, payload.RocketMQInstances)
	instancereg.Default.SetK8s(payload.Node, payload.K8sInstances)

	if err := r.storage.Write(metrics); err != nil {
		slog.Error("写入 VM 失败", "node", payload.Node, "err", err)
		http.Error(w, "write storage failed", http.StatusInternalServerError)
		return
	}

	// Nginx access log 聚合统计：写低基数指标 + 送地理窗口（高基数 IP 不进时序库）
	if len(payload.NginxAccessStats) > 0 {
		accessMets := make([]model.Metric, 0, len(payload.NginxAccessStats)*5)
		for _, st := range payload.NginxAccessStats {
			group := st.Group
			if group == "" {
				group = payload.Group
			}
			base := map[string]string{"group": group, "instance": st.Instance}
			accessMets = append(accessMets,
				model.Metric{Node: payload.Node, Name: "nginx_access_requests", Labels: cloneLabels(base), Value: st.Requests, Timestamp: payload.ReportAt},
				model.Metric{Node: payload.Node, Name: "nginx_access_bytes", Labels: cloneLabels(base), Value: st.Bytes, Timestamp: payload.ReportAt},
			)
			// 派生指标：把每周期累计的 请求数/字节数 换算成 每秒速率，
			// 使其与 network_recv_rate / network_sent_rate 同量纲，便于大屏按 Nginx 拆分网络流量。
			if st.PeriodSec > 0 {
				accessMets = append(accessMets,
					model.Metric{Node: payload.Node, Name: "nginx_access_requests_rate", Labels: cloneLabels(base), Value: st.Requests / st.PeriodSec, Timestamp: payload.ReportAt},
					model.Metric{Node: payload.Node, Name: "nginx_access_bytes_rate", Labels: cloneLabels(base), Value: st.Bytes / st.PeriodSec, Timestamp: payload.ReportAt},
				)
			}
			if st.AvgLatency > 0 {
				accessMets = append(accessMets, model.Metric{Node: payload.Node, Name: "nginx_access_avg_latency", Labels: cloneLabels(base), Value: st.AvgLatency, Timestamp: payload.ReportAt})
			}
			for code, cnt := range st.StatusCount {
				lbs := cloneLabels(base)
				lbs["status"] = code
				accessMets = append(accessMets, model.Metric{Node: payload.Node, Name: "nginx_access_requests_by_status", Labels: lbs, Value: cnt, Timestamp: payload.ReportAt})
			}
		}
		if err := r.storage.Write(accessMets); err != nil {
			slog.Error("写入 Nginx access 指标失败", "node", payload.Node, "err", err)
		}
		if r.ngx != nil {
			r.ngx.Add(payload.NginxAccessStats)
		}
	}

	// 响应：若节点仍需升级（agent 版本未达标），持续下发 upgrade 指令
	resp := map[string]interface{}{"status": "ok"}
	if r.nodeMgr.ConsumeUpgrade(payload.Node, payload.Version, payload.BinSHA256) {
		resp["command"] = "upgrade"
		slog.Info("已下发升级指令", "node", payload.Node, "agentVersion", payload.Version)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// cloneLabels 复制标签 map，避免多个指标共享同一 map 被并发修改。
func cloneLabels(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
