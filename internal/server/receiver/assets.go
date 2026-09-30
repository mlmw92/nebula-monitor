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
		if err := r.assets.Link(
			asset.Ref{TypeKey: instance.TypeKey, NaturalKey: instance.NaturalKey}, hostRef, asset.LinkRunsOn,
		); err != nil {
			// 主机资产写入失败时这里会报错：只影响关系，不影响实例本身，故不中断循环。
			slog.Warn("建立实例 → 主机关联失败", "type", ob.Type, "instance", ob.Addr, "err", err)
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
			Up: i.Up, HasUp: true}
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
