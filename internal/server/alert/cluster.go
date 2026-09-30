package alert

import (
	"sort"
	"strconv"
	"strings"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/storage"
)

// ClusterMember 是一个集群成员实例，同时携带「集群是否健康」判定所需的全部信息。
//
// 之所以把判定输入集中到这一个结构（而不是各处各自查时序库、各自数主库个数）：
// 「集群状态损坏」既用于告警（Engine）也用于页面展示（API）。两处各写一份判定必然分叉——
// 曾经就出现过页面显示「运行正常」、告警却判「脑裂」的同一实例两种结论。
type ClusterMember struct {
	Node     string
	Instance string
	Group    string
	Role     string // primary/master、secondary/slave，空表示该类型无角色概念
	Topology string
	UP       bool
	Value    float64 // 存活指标最新值（用于告警事件记录）
	// SinglePrimary 仅 MySQL Group Replication 有意义：true=单主模式，false=多主模式，nil=未知。
	SinglePrimary *bool
	// View 是该成员作为观察者看到的组内成员集合（MySQL GR 的 replication_group_members）。
	// nil 表示无此项信息（非 GR 或旧版 Agent）。
	View []string
	// ViewStates 是该观察者看到的成员状态：MEMBER_HOST -> MEMBER_STATE（ONLINE/RECOVERING/...）。
	// 用于识别「组内成员未就绪」——成员可达（实例 up=1）但没真正在组里服务。
	ViewStates map[string]string
}

// 集群判定会用到的两个辅助指标，由 MySQL 采集器在 GR 实例上产出。
const (
	metricGRSinglePrimaryMode = "mysql_gr_single_primary_mode"
	metricGRViewMember        = "mysql_gr_view_member"
)

// clusterView 是一个观察者看到的成员集合。
type clusterView struct {
	instance string
	members  []string
}

// newestPoint 取序列中时间戳最大的样本（range 查询会返回窗口内多个样本点）。
func newestPoint(points []model.Point) model.Point {
	best := points[0]
	for _, p := range points[1:] {
		if p.Timestamp > best.Timestamp {
			best = p
		}
	}
	return best
}

// LoadClusterMembers 读取某中间件类型在给定节点上的全部集群成员（含 MySQL GR 的模式与组视图）。
//
// nodeNames 由调用方按自身可见范围（规则分组/范围或用户资源范围）过滤后传入：
// 判定结论只基于「调用方能看到的那部分节点」，避免越权推断。
func LoadClusterMembers(store storage.Storage, service string, nodeNames []string) []ClusterMember {
	metric := serviceMetric(service)
	labels := upLabelsFor(service)
	withGR := service == "mysql"

	var out []ClusterMember
	for _, node := range nodeNames {
		modes, views, states := map[string]*bool{}, map[string][]string{}, map[string]map[string]string{}
		if withGR {
			modes, views, states = grClusterMetaForNode(store, node)
		}
		for _, s := range latestRoleSamples(store, node, metric, labels) {
			out = append(out, ClusterMember{
				Node:          node,
				Instance:      s.instance,
				Group:         s.group,
				Role:          s.role,
				Topology:      s.topology,
				UP:            s.value > 0.5,
				Value:         s.value,
				SinglePrimary: modes[s.instance],
				View:          views[s.instance],
				ViewStates:    states[s.instance],
			})
		}
	}
	return out
}

// grClusterMetaForNode 读取某节点上各实例上报的 GR 模式与组视图。
// 返回的三个 map 均以 instance 标签（观察者）为键；同一实例存在多条序列时取数据点最新者。
func grClusterMetaForNode(store storage.Storage, node string) (map[string]*bool, map[string][]string, map[string]map[string]string) {
	modes := map[string]*bool{}
	views := map[string][]string{}
	states := map[string]map[string]string{}

	if series, err := store.QueryInstantWithLookback(node, metricGRSinglePrimaryMode, nil, freshSampleWindow); err == nil {
		latestTs := map[string]int64{}
		for _, s := range series {
			inst := s.Labels["instance"]
			if inst == "" || len(s.Points) == 0 {
				continue
			}
			last := newestPoint(s.Points)
			if prev, ok := latestTs[inst]; ok && last.Timestamp <= prev {
				continue
			}
			latestTs[inst] = last.Timestamp
			v := last.Value > 0.5
			modes[inst] = &v
		}
	}

	if series, err := store.QueryInstantWithLookback(node, metricGRViewMember, nil, freshSampleWindow); err == nil {
		// 同一观察者的一轮采集里，各成员序列的时间戳相同；而回看窗口内可能还留有上一轮的
		// 成员序列（如刚由多主切成各自为组，旧序列仍显示"看到 3 个成员"）。
		// 因此只采纳该观察者最新时间戳那一轮上报的成员，否则会把旧成员并进来、
		// 掩盖真实的分裂（这正是脑裂检测最需要灵敏的时刻）。
		// 必须用 range 查询（QueryInstantWithLookback）：VictoriaMetrics 即时查询返回的
		// 时间戳是求值时刻而非样本时间，那样所有序列时间戳相同、过滤会完全失效。
		memberTs := map[string]map[string]int64{} // 观察者 -> 成员 -> 最新时间戳
		latestTs := map[string]int64{}            // 观察者 -> 最新一轮时间戳
		memberState := map[string]map[string]string{}
		for _, s := range series {
			inst, member := s.Labels["instance"], s.Labels["member"]
			if inst == "" || member == "" || len(s.Points) == 0 {
				continue
			}
			last := newestPoint(s.Points)
			ts := last.Timestamp
			if memberTs[inst] == nil {
				memberTs[inst] = map[string]int64{}
				memberState[inst] = map[string]string{}
			}
			if ts > memberTs[inst][member] {
				memberTs[inst][member] = ts
				// 指标取值即成员状态编码（1=ONLINE / 0.5=RECOVERING / 0=其他）
				memberState[inst][member] = grStateText(last.Value)
			}
			if ts > latestTs[inst] {
				latestTs[inst] = ts
			}
		}
		for inst, byMember := range memberTs {
			for member, ts := range byMember {
				if ts != latestTs[inst] {
					continue // 上一轮遗留的成员，不属于最新视图
				}
				views[inst] = append(views[inst], member)
			}
			sort.Strings(views[inst])
			// 只保留最新一轮的成员状态，避免旧状态残留
			for member := range memberState[inst] {
				if memberTs[inst][member] != latestTs[inst] {
					delete(memberState[inst], member)
				}
			}
			states[inst] = memberState[inst]
		}
	}
	return modes, views, states
}

// grStateText 把组视图指标取值还原为成员状态文本（与 agent 侧编码约定一致）。
func grStateText(v float64) string {
	switch {
	case v >= 0.75:
		return "ONLINE"
	case v >= 0.25:
		return "RECOVERING"
	default:
		return "OFFLINE"
	}
}

// ClassifyClusterFault 判定同一分组（集群）是否存在故障，返回空字符串表示健康。
//
// 判定顺序与依据：
//  1. 组视图分裂：同一组内各成员「看到的成员集合」不一致 —— 单主/多主模式下都属于异常，
//     且是比数主库个数更直接的脑裂信号（全量重启时每个节点各自 bootstrap 成单成员组，
//     角色看都是 PRIMARY，但视图各不相同）。旧 Agent 无该指标时跳过此步。
//  2. 模式配置不一致：同组内既有单主又有多主配置，此时角色判定不可信。
//  3. 无主：在线成员里没有任何 PRIMARY/主库 —— 集群无法写入。
//  4. 多主：
//     - 单主模式（或模式未知的旧 Agent，保守沿用历史判定）→ 脑裂异常
//     - 多主模式（group_replication_single_primary_mode=OFF）→ 全部在线成员都是主库是**正常**形态；
//     只有部分成员是主库才异常（模式与角色不一致）
//
// 角色判定仅在该类型确有角色信息时生效：Kubernetes 等无主从概念的集群不会因此被判「无主」。
func ClassifyClusterFault(members []ClusterMember) string {
	alive, primaries := 0, 0
	hasRoleInfo := false
	modeSingle, modeMulti := 0, 0
	var views []clusterView

	for _, m := range members {
		if m.SinglePrimary != nil {
			if *m.SinglePrimary {
				modeSingle++
			} else {
				modeMulti++
			}
		}
		if m.View != nil {
			views = append(views, clusterView{instance: m.Instance, members: m.View})
		}
		if m.Role != "" {
			hasRoleInfo = true
		}
		if !m.UP {
			continue
		}
		alive++
		if isPrimaryRole(m.Role) {
			primaries++
		}
	}
	if alive == 0 {
		// 全部不可达：交由「服务离线」规则处理，避免同一故障双重告警。
		return ""
	}

	if msg := viewSplitMessage(views); msg != "" {
		return msg
	}
	if msg := unreadyMembersMessage(members); msg != "" {
		return msg
	}
	if msg := missingFromGroupMessage(members); msg != "" {
		return msg
	}
	if modeSingle > 0 && modeMulti > 0 {
		return "模式配置不一致（同类实例中同时存在单主与多主模式），角色判定不可信"
	}
	if !hasRoleInfo {
		// 该类型没有主从角色概念（如 Kubernetes），不做主库个数判定。
		return ""
	}
	if primaries == 0 {
		return "无主（缺少 PRIMARY/主库），集群无法写入"
	}
	if modeMulti > 0 {
		if primaries != alive {
			return "多主模式下的角色不一致（" + strconv.Itoa(primaries) + " 个主库 / " +
				strconv.Itoa(alive) + " 个在线成员），请检查 single_primary_mode 配置"
		}
		return ""
	}
	if primaries > 1 {
		return "多主（检测到 " + strconv.Itoa(primaries) + " 个 PRIMARY/主库），疑似脑裂（单主模式下仅允许 1 个主库）"
	}
	return ""
}

// viewSplitMessage 比较各观察者看到的成员集合，不一致时返回可读的判定说明。
func viewSplitMessage(views []clusterView) string {
	if len(views) < 2 {
		return ""
	}
	sorted := append([]clusterView(nil), views...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].instance < sorted[j].instance })

	base := sorted[0]
	for _, v := range sorted[1:] {
		if len(base.members) == len(v.members) && sameMembers(base.members, v.members) {
			continue
		}
		return "组视图分裂：各节点看到的成员集合不一致（" +
			base.instance + " 看到 " + strconv.Itoa(len(base.members)) + " 个成员，" +
			v.instance + " 看到 " + strconv.Itoa(len(v.members)) + " 个），疑似脑裂或重复引导"
	}
	return ""
}

// unreadyMembersMessage 找出「所有观察者一致认为未 ONLINE」的组内成员。
//
// 为什么需要：成员实例本身可以是可达的（实例 up=1），但在组里处于 RECOVERING/OFFLINE ——
// 它并没有真正参与复制、无法提供服务。只按角色个数判定会漏掉这种「集群实际降级但看板显示正常」，
// 而这类降级恰恰发生在模式切换、成员重启等运维动作之后。
// 仅在观察者意见一致时判定，避免个别节点的陈旧/局部视角造成误报。
func unreadyMembersMessage(members []ClusterMember) string {
	reported := map[string]int{}     // 成员 -> 被多少观察者看到
	notOnline := map[string]string{} // 成员 -> 被一致认定的非 ONLINE 状态
	seenOnline := map[string]bool{}  // 成员 -> 有任一观察者认为它 ONLINE
	for _, m := range members {
		for host, st := range m.ViewStates {
			reported[host]++
			if st == "ONLINE" {
				seenOnline[host] = true
				continue
			}
			notOnline[host] = st
		}
	}
	var items []string
	for host, st := range notOnline {
		if reported[host] == 0 || seenOnline[host] {
			continue
		}
		items = append(items, host+"="+st)
	}
	if len(items) == 0 {
		return ""
	}
	sort.Strings(items)
	total := len(items)
	if len(items) > 3 {
		items = append(items[:3], "…")
	}
	return "组内有 " + strconv.Itoa(total) + " 个成员未就绪（" + strings.Join(items, "、") +
		"），成员可达但未真正参与组复制"
}

// missingFromGroupMessage 识别「集群里有的实例在组内、有的完全不在组内」。
//
// 为什么需要：节点上的组复制被停掉后，MySQL 仍可正常连接，该实例的角色会退化为
// master/slave（非 GR 的 fallback 判定），于是「有几个成员是主库」这类计数看起来一切正常，
// 平台会误判为健康——而它其实已经不在组里、拿不到组内数据了（实测：容器重启把单主/多主
// 运行时设置重置后，两个成员就是这么掉出组、却被判为正常的）。
//
// 仅在「同组内确有实例上报了组视图」时才判定，避免非 GR 部署（如异步复制集群）被误报。
//
// 判定依据用「组视图」而不是「单主/多主模式」：模式变量只要 GR 插件加载就存在，
// 即便组复制已停止也会上报；而组视图序列存在 ⇔ 该实例确实出现在成员表里（真的在组内）。
func missingFromGroupMessage(members []ClusterMember) string {
	var reporting, silent []string
	for _, m := range members {
		if !strings.EqualFold(m.Topology, "cluster") {
			continue
		}
		if m.View != nil {
			reporting = append(reporting, m.Instance)
			continue
		}
		silent = append(silent, m.Instance)
	}
	if len(reporting) == 0 || len(silent) == 0 {
		return ""
	}
	sort.Strings(silent)
	total := len(silent)
	shown := silent
	if len(shown) > 3 {
		shown = append(shown[:3], "…")
	}
	return "有 " + strconv.Itoa(total) + " 个实例未在组复制中（" + strings.Join(shown, "、") +
		"），组内其余成员正常，疑似实例已掉出集群"
}

// sameMembers 判断两个成员集合是否等价（顺序无关）。
func sameMembers(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sa := append([]string(nil), a...)
	sb := append([]string(nil), b...)
	sort.Strings(sa)
	sort.Strings(sb)
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}
