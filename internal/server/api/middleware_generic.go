package api

import (
	"log/slog"
	"net/http"
	"sort"

	"github.com/nebula/monitor/internal/server/instancereg"
)

// 本文件是 RabbitMQ / Elasticsearch / ClickHouse / Nacos / ZooKeeper 五类
// 「轻采集」内置中间件的通用实例接口：它们的采集都是「存活 + 少量核心指标」，
// 展示形态一致（实例表 + 指标列），因此共用一个表驱动的 handler，
// 而不是像 Redis/Kafka 那样每类各写一份专属 handler + 专属前端 Tab。
//
// 与内置专属 handler 的行为对齐：
//   - 以 *_instance_up 的最新样本为基准枚举实例；
//   - 注册表（instancereg）补充「已配置但 agent 离线」的实例；
//   - 各核心指标最新值按 node|instance 关联到实例；
//   - 资源范围过滤（受限用户只见范围内节点）。

// mwGenericMetricDef 描述实例上的一个核心指标（展示名与单位）。
type mwGenericMetricDef struct {
	Name  string
	Label string
	Unit  string
}

// mwGenericRegistryRef 是注册表条目的统一投影（避免为每类写一套转换）。
type mwGenericRegistryRef struct {
	Node, Instance, Name, Group, Role, Version string
	Up                                         bool
}

// mwGenericSpec 是一类的完整描述。
type mwGenericSpec struct {
	Type     string // 类型键（rabbitmq / elasticsearch / ...）
	Label    string // 展示名
	UpMetric string
	// Registry 返回注册表中全部实例的统一投影。
	Registry func() []mwGenericRegistryRef
	// Metrics 实例上的核心指标定义（顺序即前端列顺序）。
	Metrics []mwGenericMetricDef
}

// mwGenericInstanceInfo 是通用实例接口的响应行。
type mwGenericInstanceInfo struct {
	Node     string             `json:"node"`
	Instance string             `json:"instance"`
	Name     string             `json:"name"`
	Group    string             `json:"group"`
	Role     string             `json:"role,omitempty"`
	Version  string             `json:"version,omitempty"`
	Up       bool               `json:"up"`
	Metrics  []mwGenericMetricT `json:"metrics,omitempty"`
}

// mwGenericMetricT 是实例上的一个指标值。
type mwGenericMetricT struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit,omitempty"`
}

// handleGenericMWInstances 返回指定类型的实例列表（含核心指标最新值）。
func (a *API) handleGenericMWInstances(spec mwGenericSpec) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		upSeries, err := a.store.QueryAllLatest(spec.UpMetric, nil)
		if err != nil {
			slog.Error("查询中间件实例失败", "type", spec.Type, "err", err)
			http.Error(w, "query failed", http.StatusInternalServerError)
			return
		}

		type state struct {
			info mwGenericInstanceInfo
			key  string
		}
		instances := map[string]*state{}
		var keys []string

		// latestTs 记录每个实例已采纳样本的数据点时间戳。
		// 同一 node|instance 可能存在多条存活序列（指标带 version/role 等
		// 「仅采集成功时才写入」的标签时，up=0 与 up=1 会落在不同序列上），
		// 离线期间写入的 up=0 在恢复后仍留在即时查询回看窗口内；只采纳时间戳
		// 最新的一条，避免已恢复的实例仍被判为离线。
		latestTs := map[string]int64{}
		add := func(key string, ts int64, info mwGenericInstanceInfo) {
			prev, ok := instances[key]
			if !ok {
				instances[key] = &state{info: info, key: key}
				keys = append(keys, key)
				latestTs[key] = ts
				return
			}
			if ts <= latestTs[key] {
				return
			}
			// 采纳更新的样本：存活状态以新样本为准，元信息保留非空值
			// （role/version 只在采集成功时才存在）。
			if info.Role == "" {
				info.Role = prev.info.Role
			}
			if info.Version == "" {
				info.Version = prev.info.Version
			}
			prev.info = info
			latestTs[key] = ts
		}

		for _, s := range upSeries {
			node := s.Labels["node"]
			instance := s.Labels["instance"]
			if node == "" || instance == "" || len(s.Points) == 0 {
				continue
			}
			name := s.Labels["name"]
			if name == "" {
				name = s.Labels["group"]
			}
			last := s.Points[len(s.Points)-1]
			add(node+"|"+instance, last.Timestamp, mwGenericInstanceInfo{
				Node:     node,
				Instance: instance,
				Name:     name,
				Group:    s.Labels["group"],
				Role:     s.Labels["role"],
				Version:  s.Labels["version"],
				Up:       last.Value > 0,
			})
		}

		// 注册表补充：agent 离线导致 up 指标 stale 时，实例仍以离线状态列出
		for _, ref := range spec.Registry() {
			add(ref.Node+"|"+ref.Instance, 0, mwGenericInstanceInfo{
				Node:     ref.Node,
				Instance: ref.Instance,
				Name:     ref.Name,
				Group:    ref.Group,
				Role:     ref.Role,
				Version:  ref.Version,
				Up:       false,
			})
		}

		// 核心指标最新值关联到实例
		for _, def := range spec.Metrics {
			series, err := a.store.QueryAllLatest(def.Name, nil)
			if err != nil {
				slog.Warn("聚合中间件指标查询失败", "type", spec.Type, "metric", def.Name, "err", err)
				continue
			}
			for _, s := range series {
				node := s.Labels["node"]
				instance := s.Labels["instance"]
				if node == "" || instance == "" || len(s.Points) == 0 {
					continue
				}
				st, ok := instances[node+"|"+instance]
				if !ok {
					continue
				}
				st.info.Metrics = append(st.info.Metrics, mwGenericMetricT{
					Key:   def.Name,
					Label: def.Label,
					Value: round2(s.Points[len(s.Points)-1].Value),
					Unit:  def.Unit,
				})
			}
		}

		keys = filterByNodeScope(a, Principal(r), keys, nodeOfKey)
		out := make([]mwGenericInstanceInfo, 0, len(keys))
		for _, k := range keys {
			out = append(out, instances[k].info)
		}
		// 稳定输出顺序：按 instance 排序，前端展示不跳动
		sort.Slice(out, func(i, j int) bool { return out[i].Instance < out[j].Instance })
		writeJSON(w, 200, map[string]interface{}{"type": spec.Type, "instances": out})
	}
}

// handleRabbitMQInstances RabbitMQ 实例列表。
func (a *API) handleRabbitMQInstances(w http.ResponseWriter, r *http.Request) {
	refs := make([]mwGenericRegistryRef, 0)
	for _, ri := range instancereg.Default.RabbitMQInstances() {
		refs = append(refs, mwGenericRegistryRef{Node: ri.Node, Instance: ri.Instance, Name: ri.Name, Group: ri.Group, Version: ri.Version})
	}
	a.handleGenericMWInstances(mwGenericSpec{
		Type:     "rabbitmq",
		Label:    "RabbitMQ",
		UpMetric: "rabbitmq_instance_up",
		Registry: func() []mwGenericRegistryRef { return refs },
		Metrics: []mwGenericMetricDef{
			{Name: "rabbitmq_connections", Label: "连接数"},
			{Name: "rabbitmq_queues", Label: "队列数"},
			{Name: "rabbitmq_queue_messages", Label: "消息总数"},
			{Name: "rabbitmq_consumers", Label: "消费者数"},
			{Name: "rabbitmq_publishers", Label: "发布者数"},
			{Name: "rabbitmq_process_memory_bytes", Label: "进程内存", Unit: "B"},
		},
	})(w, r)
}

// handleElasticsearchInstances Elasticsearch 实例列表。
func (a *API) handleElasticsearchInstances(w http.ResponseWriter, r *http.Request) {
	refs := make([]mwGenericRegistryRef, 0)
	for _, ei := range instancereg.Default.ElasticsearchInstances() {
		refs = append(refs, mwGenericRegistryRef{Node: ei.Node, Instance: ei.Instance, Name: ei.Name, Group: ei.Group, Version: ei.Version})
	}
	a.handleGenericMWInstances(mwGenericSpec{
		Type:     "elasticsearch",
		Label:    "Elasticsearch",
		UpMetric: "es_instance_up",
		Registry: func() []mwGenericRegistryRef { return refs },
		Metrics: []mwGenericMetricDef{
			{Name: "es_cluster_status", Label: "集群状态(0绿/1黄/2红)"},
			{Name: "es_nodes", Label: "节点数"},
			{Name: "es_active_shards", Label: "活跃分片"},
			{Name: "es_unassigned_shards", Label: "未分配分片"},
		},
	})(w, r)
}

// handleClickHouseInstances ClickHouse 实例列表。
func (a *API) handleClickHouseInstances(w http.ResponseWriter, r *http.Request) {
	refs := make([]mwGenericRegistryRef, 0)
	for _, ci := range instancereg.Default.ClickHouseInstances() {
		refs = append(refs, mwGenericRegistryRef{Node: ci.Node, Instance: ci.Instance, Name: ci.Name, Group: ci.Group, Version: ci.Version})
	}
	a.handleGenericMWInstances(mwGenericSpec{
		Type:     "clickhouse",
		Label:    "ClickHouse",
		UpMetric: "clickhouse_instance_up",
		Registry: func() []mwGenericRegistryRef { return refs },
		Metrics: []mwGenericMetricDef{
			{Name: "clickhouse_tcp_connections", Label: "TCP连接"},
			{Name: "clickhouse_http_connections", Label: "HTTP连接"},
			{Name: "clickhouse_queries_running", Label: "运行中查询"},
			{Name: "clickhouse_merges_running", Label: "合并数"},
		},
	})(w, r)
}

// handleNacosInstances Nacos 实例列表。
func (a *API) handleNacosInstances(w http.ResponseWriter, r *http.Request) {
	refs := make([]mwGenericRegistryRef, 0)
	for _, ni := range instancereg.Default.NacosInstances() {
		refs = append(refs, mwGenericRegistryRef{Node: ni.Node, Instance: ni.Instance, Name: ni.Name, Group: ni.Group})
	}
	a.handleGenericMWInstances(mwGenericSpec{
		Type:     "nacos",
		Label:    "Nacos",
		UpMetric: "nacos_instance_up",
		Registry: func() []mwGenericRegistryRef { return refs },
	})(w, r)
}

// handleZooKeeperInstances ZooKeeper 实例列表。
func (a *API) handleZooKeeperInstances(w http.ResponseWriter, r *http.Request) {
	refs := make([]mwGenericRegistryRef, 0)
	for _, zi := range instancereg.Default.ZooKeeperInstances() {
		refs = append(refs, mwGenericRegistryRef{Node: zi.Node, Instance: zi.Instance, Name: zi.Name, Group: zi.Group, Role: zi.Role, Version: zi.Version})
	}
	a.handleGenericMWInstances(mwGenericSpec{
		Type:     "zookeeper",
		Label:    "ZooKeeper",
		UpMetric: "zookeeper_instance_up",
		Registry: func() []mwGenericRegistryRef { return refs },
		Metrics: []mwGenericMetricDef{
			{Name: "zookeeper_avg_latency", Label: "平均延迟", Unit: "ms"},
			{Name: "zookeeper_outstanding_requests", Label: "未处理请求"},
			{Name: "zookeeper_alive_connections", Label: "活跃连接"},
			{Name: "zookeeper_znode_count", Label: "Znode数"},
		},
	})(w, r)
}
