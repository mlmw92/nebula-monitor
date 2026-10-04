package receiver

import (
	"log/slog"
	"strconv"
	"strings"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/asset"
)

// SetAssetService 注入资产台账服务（可选；未注入时不写台账，行为与引入资产前完全一致）。
func (r *Receiver) SetAssetService(svc *asset.Service) { r.assets = svc }

// applyAssets 把本轮上报里的主机与中间件实例幂等写入资产台账。
//
// 三条约束（设计件 §自动发现）：
//  1. **台账是次要数据**：任何失败只记日志，绝不能让指标上报失败或阻断主链路；
//  2. **复用既有采集产物**：主机信息与实例清单本来就在 payload 里，不新增采集器；
//  3. **不覆盖人工值**：这里一律以 discovery（采集）来源提交，人工值由后续接口单独写入。
func (r *Receiver) applyAssets(p *model.ReportPayload) {
	if r.assets == nil || p == nil || strings.TrimSpace(p.Node) == "" {
		return
	}

	// 主机资产：自然键 = hostname，与 model.Node.Hostname 一致——不造第二套主机标识，
	// 资源范围（节点分组）才能原样生效。
	hostAttrs := map[string]string{}
	putIfNotEmpty(hostAttrs, "os", p.OS)
	putIfNotEmpty(hostAttrs, "arch", p.Arch)
	putIfNotEmpty(hostAttrs, "ip", p.IP)
	putIfNotEmpty(hostAttrs, "group", p.Group)
	putIfNotEmpty(hostAttrs, "agentVersion", p.Version)
	putIfNotEmpty(hostAttrs, "cpuModel", p.HostInfo.CPUModel)
	if p.HostInfo.CPUCores > 0 {
		hostAttrs["cpuCores"] = strconv.Itoa(p.HostInfo.CPUCores)
	}
	// 容量统一换算成 MB：台账里同时出现字节与 MB 两种量纲，迟早会被算错。
	if p.HostInfo.MemoryTotal > 0 {
		hostAttrs["memoryMB"] = strconv.FormatUint(p.HostInfo.MemoryTotal/(1<<20), 10)
	}
	if p.HostInfo.DiskTotal > 0 {
		hostAttrs["diskMB"] = strconv.FormatUint(p.HostInfo.DiskTotal/(1<<20), 10)
	}
	hostRef := asset.Ref{TypeKey: asset.TypeHost, NaturalKey: p.Node}
	if _, _, err := r.assets.Apply(asset.Observation{
		TypeKey: asset.TypeHost, NaturalKey: p.Node, Name: p.Node, Node: p.Node,
		Source: asset.SourceDiscovery, Actor: "agent", Attrs: hostAttrs,
	}); err != nil {
		slog.Warn("写入主机资产失败", "node", p.Node, "err", err)
	}

	// 中间件实例资产 + runs_on 关联。
	// 实例清单每轮都上报，Apply 内部按自然键幂等、且只在值真变化时写变更记录，
	// 因此这里可以放心地每轮提交。
	for _, ob := range instanceObservations(p) {
		if strings.TrimSpace(ob.Addr) == "" {
			continue
		}
		attrs := map[string]string{}
		putIfNotEmpty(attrs, "group", ob.Group)
		putIfNotEmpty(attrs, "role", ob.Role)
		putIfNotEmpty(attrs, "topology", ob.Topology)
		putIfNotEmpty(attrs, "version", ob.Version)
		// 容器/实例自身的运行状态与镜像：台账里"为什么不可达"要靠它们才说得清。
		putIfNotEmpty(attrs, "status", ob.Status)
		putIfNotEmpty(attrs, "image", ob.Image)
		if ob.HasUp {
			attrs["up"] = strconv.FormatBool(ob.Up)
		}
		instance, _, err := r.assets.Apply(asset.Observation{
			TypeKey: asset.TypeMiddlewareInst, NaturalKey: ob.Type + ":" + ob.Addr,
			Name: ob.Name, Node: p.Node, Source: asset.SourceDiscovery, Actor: "agent", Attrs: attrs,
		})
		if err != nil {
			slog.Warn("写入中间件实例资产失败", "type", ob.Type, "instance", ob.Addr, "err", err)
			continue
		}
		// 采集路径：走 LinkDiscovered —— 它会跳过被人工抑制过的边，
		// 也不会把人工认领过的同一条边降级回 discovery。
		if err := r.assets.LinkDiscovered(
			asset.Ref{TypeKey: instance.TypeKey, NaturalKey: instance.NaturalKey}, hostRef, asset.LinkRunsOn,
		); err != nil {
			// 主机资产写入失败时这里会报错：只影响关系，不影响实例本身，故不中断循环。
			slog.Warn("建立实例 → 主机关联失败", "type", ob.Type, "instance", ob.Addr, "err", err)
		}
	}

	// K8s 容器/工作负载清单。与上面同一套原则（只以 discovery 提交、幂等、失败只记日志），
	// 但多一条**顺序约束**，见其函数注释。
	r.applyContainerInventory(p)
}

// applyContainerInventory 把本轮上报的 K8s 清单落成台账资产与关系。
//
// **顺序是硬约束：先工作负载、后 Pod。** member_of（Pod → 工作负载）要求两端资产都已存在
// （LinkDiscovered 的契约），顺序反了这批边就整条建不出来——这不是优化，是正确性。
//
// 两条边的口径见设计件 §关系设计：
//   - runs_on：仅在 spec.nodeName 对应**已存在的主机资产**时才建。建不成不报错、也**不造占位主机**
//     ——"我们不知道那台机器"比编一个主机资产更诚实，而且服务端本来就有一个写入失败的日志。
//   - member_of：Pod → 所属工作负载（采集侧已把 ReplicaSet 那一跳解析掉）。
func (r *Receiver) applyContainerInventory(p *model.ReportPayload) {
	// 主机资产是否存在的**记忆化**查询：范围归属与 runs_on 都以它为前提，
	// 而成百上千个 Pod 往往只分布在少数几个节点上——每个节点只查一次。
	hostKnown := map[string]bool{}
	hostExists := func(name string) bool {
		name = strings.TrimSpace(name)
		if name == "" {
			return false
		}
		if v, ok := hostKnown[name]; ok {
			return v
		}
		_, found, err := r.assets.Get(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: name})
		if err != nil {
			slog.Warn("查询主机资产失败", "host", name, "err", err)
			found = false
		}
		hostKnown[name] = found
		return found
	}

	// 1. 工作负载（必须先于 Pod：它是 member_of 的对端）
	workloadKeys := map[string]bool{}
	for _, w := range p.K8sWorkloads {
		if strings.TrimSpace(w.Cluster) == "" || strings.TrimSpace(w.Name) == "" {
			continue
		}
		attrs := map[string]string{}
		putIfNotEmpty(attrs, "namespace", w.Namespace)
		putIfNotEmpty(attrs, "kind", w.Kind)
		putIfNotEmpty(attrs, "image", w.Image)
		// 0 也要写：它表达的是"副本数为 0"这个事实，而不是"没采到"。
		attrs["desired"] = strconv.Itoa(w.Desired)
		attrs["ready"] = strconv.Itoa(w.Ready)
		key := asset.WorkloadNaturalKey(w.Cluster, w.Namespace, w.Kind, w.Name)
		if _, _, err := r.assets.Apply(asset.Observation{
			TypeKey: asset.TypeWorkload, NaturalKey: key,
			Name: w.Name,
			// 工作负载跨节点、没有单一归属节点：范围挂在**集群的上报主机**上，
			// 与集群资产同口径（设计件 §资源范围）。不能落空串——空串 = 受限用户一律不可见，
			// 那等于对受限用户隐藏工作负载。
			Node:   p.Node,
			Source: asset.SourceDiscovery, Actor: "agent", Attrs: attrs,
		}); err != nil {
			slog.Warn("写入工作负载资产失败", "workload", w.Name, "err", err)
			continue
		}
		workloadKeys[asset.TypeWorkload+"|"+key] = true
	}

	// 2. Pod：先落资产，再建两条边
	for _, pod := range p.K8sPods {
		if strings.TrimSpace(pod.Cluster) == "" || strings.TrimSpace(pod.Name) == "" {
			continue
		}
		attrs := map[string]string{}
		putIfNotEmpty(attrs, "namespace", pod.Namespace)
		putIfNotEmpty(attrs, "phase", pod.Phase)
		putIfNotEmpty(attrs, "status", pod.Status)
		putIfNotEmpty(attrs, "image", pod.Image)
		// 仅展示用：范围归属取的是下面那个 node 变量，不是这个属性。
		putIfNotEmpty(attrs, "k8sNode", pod.Node)
		attrs["ready"] = strconv.Itoa(pod.Ready)
		attrs["total"] = strconv.Itoa(pod.Total)
		attrs["restarts"] = strconv.Itoa(pod.Restarts)
		if pod.StartedAt > 0 {
			attrs["startedAt"] = strconv.FormatInt(pod.StartedAt, 10)
		}

		// Pod 归**它实际所在的节点**（设计件 §资源范围）：范围是"你能管的机器"，
		// Pod 跑在哪台就归哪台，这是最不容易越权的口径。
		// 该节点没有对应主机资产时落空串 → 仅全局范围可见：不假装它属于某个分组。
		node := ""
		if hostExists(pod.Node) {
			node = strings.TrimSpace(pod.Node)
		}
		cur, _, err := r.assets.Apply(asset.Observation{
			TypeKey:    asset.TypePod,
			NaturalKey: asset.PodNaturalKey(pod.Cluster, pod.Namespace, pod.Name),
			Name:       pod.Name, Node: node,
			Source: asset.SourceDiscovery, Actor: "agent", Attrs: attrs,
		})
		if err != nil {
			slog.Warn("写入容器资产失败", "pod", pod.Name, "err", err)
			continue
		}
		podRef := asset.Ref{TypeKey: cur.TypeKey, NaturalKey: cur.NaturalKey}

		if node != "" {
			if err := r.assets.LinkDiscovered(podRef,
				asset.Ref{TypeKey: asset.TypeHost, NaturalKey: node}, asset.LinkRunsOn); err != nil {
				slog.Warn("建立容器 → 主机关联失败", "pod", pod.Name, "host", node, "err", err)
			}
		}

		// 归属边只在"本轮清单里确实有这个工作负载"时才尝试：上报被截断、或工作负载落在
		// 被排除的系统命名空间时，它本来就不在台账里。这类情况不是错误，不该刷日志。
		// （已建成的边是持久的，不会因为某一轮缺席而消失。）
		if pod.OwnerKind != "" && pod.OwnerName != "" {
			key := asset.WorkloadNaturalKey(pod.Cluster, pod.Namespace, pod.OwnerKind, pod.OwnerName)
			if workloadKeys[asset.TypeWorkload+"|"+key] {
				if err := r.assets.LinkDiscovered(podRef,
					asset.Ref{TypeKey: asset.TypeWorkload, NaturalKey: key}, asset.LinkMemberOf); err != nil {
					slog.Warn("建立容器 → 工作负载关联失败", "pod", pod.Name, "workload", pod.OwnerName, "err", err)
				}
			}
		}
	}
}

func putIfNotEmpty(m map[string]string, key, value string) {
	if v := strings.TrimSpace(value); v != "" {
		m[key] = v
	}
}

// assetObs 是「实例 → 资产观测」的中间形态。
//
// 各类型实例的字段并不一致（部分类型没有 Role/Topology；RabbitMQ/Elasticsearch/ClickHouse/ZooKeeper
// 连 Up 都没有），因此这里**显式逐类型转换、不引入反射**：字段改名时编译期就能发现，
// 而不是在运行期静默丢字段。
type assetObs struct {
	Type     string
	Addr     string
	Name     string
	Group    string
	Role     string
	Topology string
	Version  string
	Up       bool
	HasUp    bool
	// Status 是「服务自身的运行状态」原文（目前只有 Docker 容器用：running / exited / paused…）。
	// 单独带出来是为了让台账能说清"为什么不可达"：只给 up=false，用户只能看到「离线」，
	// 看不出是容器退出了还是一直没起来。
	Status string
	// Image 是容器镜像名（同样只有 Docker 用），用于台账里辨认这个实例到底是哪个镜像跑起来的。
	Image string
}

func collectObs[T any](items []T, pick func(T) assetObs) []assetObs {
	out := make([]assetObs, 0, len(items))
	for _, it := range items {
		out = append(out, pick(it))
	}
	return out
}

// instanceObservations 把上报载荷里的全部中间件实例摊平成统一的观测形态。
func instanceObservations(p *model.ReportPayload) []assetObs {
	var out []assetObs
	out = append(out, collectObs(p.RedisInstances, func(i model.RedisInstance) assetObs {
		return assetObs{Type: "redis", Addr: i.Instance, Name: i.Name, Group: i.Group, Role: i.Role,
			Topology: i.Topology, Version: i.Version, Up: i.Up, HasUp: true}
	})...)
	out = append(out, collectObs(p.MySQLInstances, func(i model.MySQLInstance) assetObs {
		return assetObs{Type: "mysql", Addr: i.Instance, Name: i.Name, Group: i.Group, Role: i.Role,
			Topology: i.Topology, Version: i.Version, Up: i.Up, HasUp: true}
	})...)
	out = append(out, collectObs(p.PostgresInstances, func(i model.PostgresInstance) assetObs {
		return assetObs{Type: "postgres", Addr: i.Instance, Name: i.Name, Group: i.Group, Role: i.Role,
			Topology: i.Topology, Version: i.Version, Up: i.Up, HasUp: true}
	})...)
	out = append(out, collectObs(p.NginxInstances, func(i model.NginxInstance) assetObs {
		return assetObs{Type: "nginx", Addr: i.Instance, Name: i.Name, Group: i.Group,
			Version: i.Version, Up: i.Up, HasUp: true}
	})...)
	out = append(out, collectObs(p.KafkaInstances, func(i model.KafkaInstance) assetObs {
		return assetObs{Type: "kafka", Addr: i.Instance, Name: i.Name, Group: i.Group, Role: i.Role,
			Version: i.Version, Up: i.Up, HasUp: true}
	})...)
	out = append(out, collectObs(p.DockerInstances, func(i model.DockerInstance) assetObs {
		return assetObs{Type: "docker", Addr: i.Instance, Name: i.Name, Group: i.Group,
			Up: i.Up, HasUp: true, Status: i.Status, Image: i.Image}
	})...)
	out = append(out, collectObs(p.RocketMQInstances, func(i model.RocketMQInstance) assetObs {
		return assetObs{Type: "rocketmq", Addr: i.Instance, Name: i.Name, Group: i.Group, Role: i.Role,
			Version: i.Version, Up: i.Up, HasUp: true}
	})...)
	out = append(out, collectObs(p.K8sInstances, func(i model.K8sInstance) assetObs {
		return assetObs{Type: "kubernetes", Addr: i.Instance, Name: i.Name, Group: i.Group,
			Version: i.Version, Up: i.Up, HasUp: true}
	})...)
	out = append(out, collectObs(p.MongoDBInstances, func(i model.MongoDBInstance) assetObs {
		return assetObs{Type: "mongodb", Addr: i.Instance, Name: i.Name, Group: i.Group, Role: i.Role,
			Topology: i.Topology, Version: i.Version, Up: i.Up, HasUp: true}
	})...)
	out = append(out, collectObs(p.FastDFSInstances, func(i model.FastDFSInstance) assetObs {
		return assetObs{Type: "fastdfs", Addr: i.Instance, Name: i.Name, Group: i.Group, Role: i.Role,
			Up: i.Up, HasUp: true}
	})...)
	out = append(out, collectObs(p.RabbitMQInstances, func(i model.RabbitMQInstance) assetObs {
		return assetObs{Type: "rabbitmq", Addr: i.Instance, Name: i.Name, Group: i.Group, Version: i.Version}
	})...)
	out = append(out, collectObs(p.ElasticsearchInstances, func(i model.ElasticsearchInstance) assetObs {
		return assetObs{Type: "elasticsearch", Addr: i.Instance, Name: i.Name, Group: i.Group,
			Role: i.Status, Version: i.Version}
	})...)
	out = append(out, collectObs(p.ClickHouseInstances, func(i model.ClickHouseInstance) assetObs {
		return assetObs{Type: "clickhouse", Addr: i.Instance, Name: i.Name, Group: i.Group, Version: i.Version}
	})...)
	out = append(out, collectObs(p.NacosInstances, func(i model.NacosInstance) assetObs {
		return assetObs{Type: "nacos", Addr: i.Instance, Name: i.Name, Group: i.Group, Up: i.Up, HasUp: true}
	})...)
	out = append(out, collectObs(p.ZooKeeperInstances, func(i model.ZooKeeperInstance) assetObs {
		return assetObs{Type: "zookeeper", Addr: i.Instance, Name: i.Name, Group: i.Group,
			Role: i.Role, Version: i.Version}
	})...)
	return out
}
