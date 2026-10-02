package api

import (
	"net/http"
	"sort"

	"github.com/nebula/monitor/internal/server/instancereg"
)

// 容器只读管理面（见 docs/superpowers/specs/2026-09-30-container-observability-design.md §3）。
//
// 本文件只负责**集群清单**这一件事：工作负载 / Pod / 事件 / 对象详情都走既有的
// 统一下行操作通道（POST /api/v1/ops/tasks，kind=container.*）——因为 K8s 凭据只存
// Agent 本地，Server 无法直连 apiserver（ADR-0003 已定）。代价是指令延迟 = 一个上报周期，
// 所以那几条接口的语义是"异步任务"，不是"点开即返回"。

// GET /api/v1/container/k8s/clusters
//
// 只回答"有哪些 K8s 集群、由哪个节点上报、在不在线"，给容器管理面做集群选择器。
// 聚合指标（节点数 / Pod 数 / 工作负载健康度）**不在这里**，见 /api/v1/middleware/k8s/instances。
//
// 为什么不复用中间件那个接口：容器管理面的权限点是 container:read，不该要求运维为了看
// 工作负载额外拿到 middleware:read。两者的数据源同为 instancereg 的 K8sInstance 上报，
// 因此不存在"两份口径"的漂移风险。
//
// 凭据边界：返回体里**永远没有** kubeconfig / token——那两项只在 Agent 本地，
// Agent 上报用的 K8sInstance 结构本身就不含凭据字段。
func (a *API) handleContainerClusters(w http.ResponseWriter, r *http.Request) {
	p := Principal(r)

	// 在线状态取自 k8s_cluster_up 的最新样本。注意 VictoriaMetrics 的即时查询会返回**求值时刻**，
	// 所以这里用 QueryAllLatest（与中间件接口同一套）；Agent 离线超过 lookback 窗口后查不到样本，
	// 一律按"离线"处理——把"采不到"显示成"集群不存在"会让人以为配置丢了。
	online := map[string]bool{}
	if series, err := a.store.QueryAllLatest("k8s_cluster_up", nil); err == nil {
		for _, s := range series {
			if len(s.Points) == 0 {
				continue
			}
			online[s.Labels["node"]+"|"+s.Labels["instance"]] = s.Points[len(s.Points)-1].Value > 0
		}
	}

	type cluster struct {
		Node     string `json:"node"`
		Instance string `json:"instance"`
		// Name 是 agent.yaml 里 k8sInstances[].name，也是下发 container.* 动作时 cluster 参数的取值。
		Name    string `json:"name"`
		Version string `json:"version"`
		Group   string `json:"group"`
		// Up 为 false 既包含"集群不可达"，也包含"上报它的节点离线"——两种情况对使用者都是"现在用不了"。
		Up bool `json:"up"`
	}

	// 用非 nil 切片：JSON 里要出 `[]` 而不是 `null`，否则前端对空结果要多写一层判空。
	out := make([]cluster, 0)
	for _, ki := range instancereg.Default.K8sInstances() {
		// 资源范围：范围外的集群整条不下发。只给个名字也算暴露了"范围外有这么一台资产"。
		if !a.nodeInScope(p, ki.Node) {
			continue
		}
		out = append(out, cluster{
			Node:     ki.Node,
			Instance: ki.Instance,
			Name:     ki.Name,
			Version:  ki.Version,
			Group:    ki.Group,
			Up:       online[ki.Node+"|"+ki.Instance],
		})
	}
	// 稳定排序：选择器顺序每次刷新都变会让人点错。
	sort.Slice(out, func(i, j int) bool {
		if out[i].Node != out[j].Node {
			return out[i].Node < out[j].Node
		}
		return out[i].Instance < out[j].Instance
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{"clusters": out})
}
