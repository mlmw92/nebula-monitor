package receiver

import (
	"log/slog"
	"net"
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

	// 中间件实例资产与关系。**分两趟走**：
	//   ① 先落全部实例资产；
	//   ② 再统一维护关系（宿主 runs_on + 主从依赖 depends_on）。
	//
	// 顺序在这里是**正确性**而非优化：副本可能先于主库出现在同一份清单里，而 depends_on
	// 要求两端资产都已存在——与 applyContainerInventory 里"先工作负载、后 Pod"同一类约束。
	// 实例清单每轮都上报，Apply 内部按自然键幂等、且只在值真变化时写变更记录，
	// 因此这里可以放心地每轮提交。
	instances := make([]instancePlan, 0, 8)
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
		// 主从关系也落成属性：关系图回答"有这条边"，台账要能回答"这条边为什么存在"。
		putIfNotEmpty(attrs, "replicaOf", ob.ReplicaOf)
		// 地址里的主机部分：宿主判定不成立时，它是唯一能解释"为什么没有 runs_on 边"的线索
		// （否则用户只能看到"这个实例没挂到任何机器上"，无从判断）。
		if host, ok := instanceHost(ob.Addr); ok {
			attrs["addrHost"] = host
		}
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
		instances = append(instances, instancePlan{
			ref:       asset.Ref{TypeKey: instance.TypeKey, NaturalKey: instance.NaturalKey},
			kind:      ob.Type,
			addr:      ob.Addr,
			replicaOf: ob.ReplicaOf,
		})
	}
	for _, it := range instances {
		r.linkInstance(it, hostRef, p)
	}

	// K8s 容器/工作负载清单。与上面同一套原则（只以 discovery 提交、幂等、失败只记日志），
	// 但多一条**顺序约束**，见其函数注释。
	r.applyContainerInventory(p)
}

// instancePlan 是一条待维护关系的实例（先落资产、再建边，顺序见 applyAssets）。
type instancePlan struct {
	ref       asset.Ref
	kind      string // 实例类型前缀（redis / mysql / …），用来拼主库的自然键
	addr      string
	replicaOf string
}

// linkInstance 维护一条实例资产的两组关系：宿主（runs_on）与主从依赖（depends_on）。
//
// 两条边都以**本轮上报**为准：不只是"该有的建上"，还包括"不该有的清掉"。边只增不减的话，
// 一次误判或一次主从切换会永久留在影响面里——那是"看着像事实"的错，比缺一条边糟得多。
func (r *Receiver) linkInstance(it instancePlan, hostRef asset.Ref, p *model.ReportPayload) {
	r.linkInstanceHost(it, hostRef, p)
	r.linkInstanceMaster(it)
}

// linkInstanceHost 维护「实例 runs_on 主机」。
//
// **只有地址里的主机确实是这台机器时才建边。** 旧口径一律挂上报节点，于是 exporter 模式、
// 或跨机采集（Agent 去读别处的实例）会把**采集机**记成宿主——这不是"少一条边"，而是影响面里
// 多一条假事实：真正跑该库的机器被漏掉、采集机被误标成它的宿主。
//
// 判定不出来时的取舍与 pod→host 一致：**不猜**。分两种情形：
//   - 地址不是 `主机:端口` 形态（docker 的容器 ID、k8s 带 scheme 的 URL 等）：这类实例本来
//     就跑在采集机上，按原样挂；
//   - 地址明确指向别的机器：不建边，并把旧版本建下的错边清掉（只清采集建的，人工边不动）。
func (r *Receiver) linkInstanceHost(it instancePlan, hostRef asset.Ref, p *model.ReportPayload) {
	host, isAddr := instanceHost(it.addr)
	if !isAddr || hostMatchesNode(host, p.Node, p.IP) {
		// 采集路径：走 LinkDiscovered —— 它会跳过被人工抑制过的边，
		// 也不会把人工认领过的同一条边降级回 discovery。
		if err := r.assets.LinkDiscovered(it.ref, hostRef, asset.LinkRunsOn); err != nil {
			// 主机资产写入失败时这里会报错：只影响关系，不影响实例本身，故不中断循环。
			slog.Warn("建立实例 → 主机关联失败", "kind", it.kind, "instance", it.addr, "err", err)
		}
		return
	}
	removed, err := r.assets.UnlinkDiscovered(it.ref, hostRef, asset.LinkRunsOn)
	if err != nil {
		slog.Warn("撤销实例 → 主机关联失败", "kind", it.kind, "instance", it.addr, "err", err)
		return
	}
	if removed {
		slog.Info("实例地址不属于本机，已撤销旧的宿主关联",
			"kind", it.kind, "instance", it.addr, "addrHost", host, "node", p.Node, "nodeIP", p.IP)
	}
}

// linkInstanceMaster 维护「副本 depends_on 主库」。
//
// 主库不在台账（外部库、尚未纳管）时**不建边、也不造占位资产**：编一个我们不认识的资产，
// 只会让台账里多出一条没有采集、没有责任人的记录，还会污染清单与告警匹配。
// 反过来，副本被提升（ReplicaOf 变空）或改指别处时，旧的依赖必须清掉——否则影响面会一直
// 显示"它还依赖那台早已不是它主库的机器"。
func (r *Receiver) linkInstanceMaster(it instancePlan) {
	want := ""
	if isHostPort(it.replicaOf) {
		// 主库的自然键与实例同构（`<类型>:<地址>`），用的是同一套地址归一化——
		// 采集侧保证 ReplicaOf 与 Instance 同形，所以这里能精确对上，不需要任何猜测。
		want = it.kind + ":" + it.replicaOf
	}
	for _, l := range r.instanceMasterLinks(it.ref) {
		if want != "" && l.To.NaturalKey == want {
			continue // 本轮应有的那条，留着
		}
		if _, err := r.assets.UnlinkDiscovered(it.ref, l.To, asset.LinkDependsOn); err != nil {
			slog.Warn("清理过期的主从依赖失败", "instance", it.addr, "master", l.To.NaturalKey, "err", err)
		}
	}
	if want == "" {
		return
	}
	masterRef := asset.Ref{TypeKey: asset.TypeMiddlewareInst, NaturalKey: want}
	if _, ok, err := r.assets.Get(masterRef); err != nil {
		slog.Warn("查询主库资产失败", "master", want, "err", err)
		return
	} else if !ok {
		return // 主库不在台账：不建边（查不到不是错误，不刷日志）
	}
	if err := r.assets.LinkDiscovered(it.ref, masterRef, asset.LinkDependsOn); err != nil {
		slog.Warn("建立副本 → 主库关联失败", "instance", it.addr, "master", want, "err", err)
	}
}

// instanceMasterLinks 取该实例**出边**里由采集建立的 depends_on（人工边不在此列）。
func (r *Receiver) instanceMasterLinks(ref asset.Ref) []asset.Link {
	links, err := r.assets.Links(ref)
	if err != nil {
		slog.Warn("查询实例关联失败", "instance", ref.NaturalKey, "err", err)
		return nil
	}
	out := make([]asset.Link, 0, 2)
	for _, l := range links {
		if l.Kind == asset.LinkDependsOn && l.From == ref && l.Source != asset.SourceManual {
			out = append(out, l)
		}
	}
	return out
}

// instanceHost 从实例地址里取出"主机部分"；取不出（或根本不是地址形态）时 ok=false。
//
// 上报里真实存在三种形态，必须分开对待：
//   - `host:port`（redis / mysql 等）→ 可取；
//   - 带 scheme 的 URL（k8s 的 `https://127.0.0.1:6443`）→ 去掉 scheme 后再取；
//   - 根本不是地址（docker 的容器 ID `abc123def456`、实例别名等）→ 取不出，
//     这类实例本来就跑在采集机上，"取不出"不等于"不是本机"。
func instanceHost(addr string) (string, bool) {
	a := strings.TrimSpace(addr)
	if a == "" {
		return "", false
	}
	if i := strings.Index(a, "://"); i >= 0 {
		a = a[i+3:]
	}
	host, _, err := net.SplitHostPort(a)
	if err != nil {
		return "", false
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return "", false
	}
	return host, true
}

// hostMatchesNode 判断实例地址的主机部分是否就是"这台机器"：
//   - 回环地址（127.0.0.1 / ::1 / localhost）算本机——本机实例就该挂在本机上；
//   - 与上报 IP 相同算本机；
//   - 与上报主机名相同（忽略大小写、忽略 FQDN 后缀）算本机。后缀这条与 Pod→主机那条边的
//     口径一致：K8s 名字按 RFC 1123 一律小写、系统主机名保留原样，严格比对会把同一台机器
//     判成两台（见 applyContainerInventory 的 hostNameOf）。
//
// 其余一律**不算**：宁可少一条边，也不把采集机记成宿主（设计件 §2.3）。
func hostMatchesNode(host, node, nodeIP string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return false
	}
	switch h {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	if ip := strings.ToLower(strings.TrimSpace(nodeIP)); ip != "" && h == ip {
		return true
	}
	n := strings.ToLower(strings.TrimSpace(node))
	if n == "" {
		return false
	}
	return h == n || shortHostName(h) == shortHostName(n)
}

// shortHostName 取主机名的第一段（FQDN → 短名），用于宽松比对。
func shortHostName(name string) string {
	if i := strings.Index(name, "."); i > 0 {
		return name[:i]
	}
	return name
}

// isHostPort 判断一个字符串是不是 `主机:端口` 形态的地址（端口必须是数字）。
//
// 挡一道是为了防一类具体错误：中间件查询接口会给哨兵发现的 master 写 `sentinel:<名字>`
// （那是给人看的标签、不是地址），拿它拼自然键会去查一个不存在的资产。虽然"查不到就不建边"
// 也能兜住，但那会把"我们根本没有这个地址"混进"这个主库还没纳管"里，日志会误导人。
func isHostPort(addr string) bool {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil || strings.TrimSpace(host) == "" {
		return false
	}
	_, err = strconv.Atoi(port)
	return err == nil
}

// applyContainerInventory 把本轮上报的 K8s 清单落成台账资产与关系。
//
// **顺序是硬约束：先工作负载、后 Pod。** member_of（Pod → 工作负载）要求两端资产都已存在
// （LinkDiscovered 的契约），顺序反了这批边就整条建不出来——这不是优化，是正确性。
//
// 三条边的口径见设计件 §关系设计：
//   - runs_on：仅在 spec.nodeName 对应**已存在的主机资产**时才建。建不成不报错、也**不造占位主机**
//     ——"我们不知道那台机器"比编一个主机资产更诚实，而且服务端本来就有一个写入失败的日志。
//   - member_of：Pod → 所属工作负载（采集侧已把 ReplicaSet 那一跳解析掉）。
//   - exposes：Service → 后端 Pod。后端是采集侧从 EndpointSlice 的 targetRef 取的
//     （不是 selector 命中：未就绪的 Pod 不在 Endpoints 里）。这也是 exposes 的**第一个自动来源**。
func (r *Receiver) applyContainerInventory(p *model.ReportPayload) {
	// 主机资产的**规范名**查询（记忆化）：Pod 的范围归属与 runs_on 都以它为前提，
	// 而成百上千个 Pod 往往只分布在少数几个节点上——每个节点只查一次。
	//
	// 返回**台账里的规范键**而不是传进来的原文，这一点是必须的：K8s 的节点名按 RFC 1123 一律小写
	// （vm-0-10-ubuntu），Agent 上报的 hostname 保留系统原样（VM-0-10-ubuntu），两者大小写可能不同。
	// 拿原文当归属节点，资源范围（按节点分组）会再一次对不上——那是本函数要修的问题，不能自己再犯。
	hostCanonical := map[string]string{}
	hostNameOf := func(name string) string {
		name = strings.TrimSpace(name)
		if name == "" {
			return ""
		}
		if v, ok := hostCanonical[name]; ok {
			return v
		}
		canonical := ""
		host, found, err := r.assets.GetHostByName(name)
		if err != nil {
			slog.Warn("查询主机资产失败", "host", name, "err", err)
		} else if found {
			canonical = host.NaturalKey
		}
		hostCanonical[name] = canonical
		return canonical
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
	//
	// podKeys 记下"这一轮真的落过的 Pod"：第 4 步的 service → pod 边只对它们尝试。
	// 这样既不会给不存在的 Pod 建边，也不会为"合法缺席"（被截断、落在系统命名空间）刷日志
	// ——与 member_of 用 workloadKeys 把关是同一取向。
	podKeys := map[string]bool{}
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
		// 大小写由 hostNameOf 折叠，且取的是台账里的规范键（否则资源范围会再一次对不上）。
		node := hostNameOf(pod.Node)
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
		podKeys[asset.TypePod+"|"+cur.NaturalKey] = true

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

	// 3. Service：先落资产。Service 跨节点、没有单一归属节点，范围与工作负载同口径挂
	//    **集群的上报主机**（落空串 = 受限用户一律不可见，那等于把它藏起来）。
	serviceKeys := map[string]bool{}
	for _, svc := range p.K8sServices {
		if strings.TrimSpace(svc.Cluster) == "" || strings.TrimSpace(svc.Name) == "" {
			continue
		}
		attrs := map[string]string{}
		putIfNotEmpty(attrs, "namespace", svc.Namespace)
		putIfNotEmpty(attrs, "type", svc.Type)
		putIfNotEmpty(attrs, "clusterIP", svc.ClusterIP)
		// 后端数写进属性：它是"这个服务背后有没有人"最直接的答案。
		attrs["backends"] = strconv.Itoa(len(svc.BackendPods))
		if svc.BackendsTruncated {
			// 只报了 200 个后端时必须让中心知道"还有更多"，否则 200 会被当成全部。
			attrs["backendsTruncated"] = "true"
		}
		key := asset.ServiceNaturalKey(svc.Cluster, svc.Namespace, svc.Name)
		cur, _, err := r.assets.Apply(asset.Observation{
			TypeKey: asset.TypeService, NaturalKey: key,
			Name:   svc.Name,
			Node:   p.Node,
			Source: asset.SourceDiscovery, Actor: "agent", Attrs: attrs,
		})
		if err != nil {
			slog.Warn("写入 K8s 服务资产失败", "service", svc.Name, "err", err)
			continue
		}
		serviceKeys[asset.TypeService+"|"+cur.NaturalKey] = true
	}

	// 4. Service → 后端 Pod（exposes）。放在最后是因为**两端必须先存在**（LinkDiscovered 的契约）：
	//    第 2 步已经落过 Pod，第 3 步落过 Service，到这里才谈得上建边。
	//
	//    后端 Pod 不在本轮清单里就跳过——被截断、或落在被排除的系统命名空间时它本来就不在台账里。
	//    这不是错误，不该刷日志；已建成的边是持久的，不会因为某一轮缺席而消失。
	for _, svc := range p.K8sServices {
		if strings.TrimSpace(svc.Cluster) == "" || strings.TrimSpace(svc.Name) == "" || len(svc.BackendPods) == 0 {
			continue
		}
		svcKey := asset.ServiceNaturalKey(svc.Cluster, svc.Namespace, svc.Name)
		if !serviceKeys[asset.TypeService+"|"+svcKey] {
			continue
		}
		for _, podName := range svc.BackendPods {
			podKey := asset.PodNaturalKey(svc.Cluster, svc.Namespace, podName)
			if !podKeys[asset.TypePod+"|"+podKey] {
				continue
			}
			if err := r.assets.LinkDiscovered(
				asset.Ref{TypeKey: asset.TypeService, NaturalKey: svcKey},
				asset.Ref{TypeKey: asset.TypePod, NaturalKey: podKey},
				asset.LinkExposes); err != nil {
				slog.Warn("建立服务 → 容器关联失败", "service", svc.Name, "pod", podName, "err", err)
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
	// ReplicaOf 是主从关系里"主库的地址"（只有 redis / mysql / postgres 报它）。
	//
	// 它一直存在于上报载荷里（`model.RedisInstance.ReplicaOf` 等），此前**没有进这个结构体**
	// ——于是采集侧辛苦取到的主从关系在映射这一步被丢掉，台账里从来没有 depends_on 边。
	// 采集侧保证它与 Instance 同形（同一套地址归一化），因此可以直接拼自然键：
	// 主库资产存在时精确命中，不存在时不建边（不造占位）。
	ReplicaOf string
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
			Topology: i.Topology, Version: i.Version, Up: i.Up, HasUp: true, ReplicaOf: i.ReplicaOf}
	})...)
	out = append(out, collectObs(p.MySQLInstances, func(i model.MySQLInstance) assetObs {
		return assetObs{Type: "mysql", Addr: i.Instance, Name: i.Name, Group: i.Group, Role: i.Role,
			Topology: i.Topology, Version: i.Version, Up: i.Up, HasUp: true, ReplicaOf: i.ReplicaOf}
	})...)
	out = append(out, collectObs(p.PostgresInstances, func(i model.PostgresInstance) assetObs {
		return assetObs{Type: "postgres", Addr: i.Instance, Name: i.Name, Group: i.Group, Role: i.Role,
			Topology: i.Topology, Version: i.Version, Up: i.Up, HasUp: true, ReplicaOf: i.ReplicaOf}
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
