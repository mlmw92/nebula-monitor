package alert

import (
	"strconv"
	"strings"
	"testing"
)

func grBoolPtr(v bool) *bool { return &v }

// grCluster 构造一个 MySQL Group Replication 集群的成员集合。
// mode: true=单主模式，false=多主模式；roles 按顺序对应 members。
func grCluster(mode bool, roles []string, views [][]string) []ClusterMember {
	out := make([]ClusterMember, 0, len(roles))
	for i, role := range roles {
		m := ClusterMember{
			Node: "node-1", Instance: "10.0.0.10:330" + strconv.Itoa(7+i),
			Group: "dev-mysql-gr", Role: role, Topology: "cluster", UP: true, Value: 1,
			SinglePrimary: boolPtr(mode),
		}
		if views != nil {
			m.View = views[i]
		}
		out = append(out, m)
	}
	return out
}

// 单主模式下出现多个 PRIMARY：脑裂，必须判异常（这是用户明确要的语义）。
func TestClassifyClusterFaultSinglePrimaryMultiMasterIsFault(t *testing.T) {
	members := grCluster(true, []string{"primary", "primary", "primary"}, nil)
	got := ClassifyClusterFault(members)
	if got == "" {
		t.Fatal("单主模式下 3 个 PRIMARY 应判为异常（脑裂）")
	}
	if !strings.Contains(got, "多主") {
		t.Fatalf("判定说明应包含「多主」，实际 %q", got)
	}
}

// 多主模式（group_replication_single_primary_mode=OFF）下全部成员都是主库是**正常**形态。
func TestClassifyClusterFaultMultiPrimaryModeAllMastersIsHealthy(t *testing.T) {
	members := grCluster(false, []string{"primary", "primary", "primary"}, nil)
	if got := ClassifyClusterFault(members); got != "" {
		t.Fatalf("多主模式下全主应为正常，实际判定 %q", got)
	}
}

// 多主模式下只有部分成员是主库：模式与角色不一致，属异常。
func TestClassifyClusterFaultMultiPrimaryModePartialMastersIsFault(t *testing.T) {
	members := grCluster(false, []string{"primary", "secondary", "secondary"}, nil)
	got := ClassifyClusterFault(members)
	if got == "" {
		t.Fatal("多主模式下部分主库应判为异常")
	}
	if !strings.Contains(got, "角色不一致") {
		t.Fatalf("判定说明应包含「角色不一致」，实际 %q", got)
	}
}

// 没有任何主库：无法写入。
func TestClassifyClusterFaultNoPrimary(t *testing.T) {
	members := grCluster(true, []string{"secondary", "secondary", "secondary"}, nil)
	got := ClassifyClusterFault(members)
	if !strings.Contains(got, "无主") {
		t.Fatalf("应判「无主」，实际 %q", got)
	}
}

// 组视图分裂：各节点看到的成员集合不一致（全量重启各自引导的典型症状），
// 即使角色看起来都正常也必须判异常。
func TestClassifyClusterFaultViewSplitDetected(t *testing.T) {
	members := grCluster(false, []string{"primary", "primary", "primary"}, [][]string{
		{"mysql-gr-1"}, // 只看到自己
		{"mysql-gr-2"},
		{"mysql-gr-3"},
	})
	got := ClassifyClusterFault(members)
	if !strings.Contains(got, "组视图分裂") {
		t.Fatalf("应判「组视图分裂」，实际 %q", got)
	}
}

// 单主模式下视图分裂同样要判出来（视图检测优先于角色判定）。
func TestClassifyClusterFaultViewSplitTakesPrecedence(t *testing.T) {
	members := grCluster(true, []string{"primary", "secondary", "secondary"}, [][]string{
		{"mysql-gr-1", "mysql-gr-2", "mysql-gr-3"},
		{"mysql-gr-2"},
		{"mysql-gr-3"},
	})
	got := ClassifyClusterFault(members)
	if !strings.Contains(got, "组视图分裂") {
		t.Fatalf("视图不一致应优先判定，实际 %q", got)
	}
}

// 视图一致的多主集群是健康的（不能因为视图检测引入误报）。
func TestClassifyClusterFaultConsistentViewIsHealthy(t *testing.T) {
	full := []string{"mysql-gr-1", "mysql-gr-2", "mysql-gr-3"}
	members := grCluster(false, []string{"primary", "primary", "primary"}, [][]string{full, full, full})
	if got := ClassifyClusterFault(members); got != "" {
		t.Fatalf("视图一致的多主集群应正常，实际 %q", got)
	}
}

// 同组内既有单主又有多主配置：角色判定不可信，直接判异常。
func TestClassifyClusterFaultInconsistentMode(t *testing.T) {
	members := grCluster(true, []string{"primary", "secondary"}, nil)
	members[1].SinglePrimary = boolPtr(false)
	got := ClassifyClusterFault(members)
	if !strings.Contains(got, "模式配置不一致") {
		t.Fatalf("应判「模式配置不一致」，实际 %q", got)
	}
}

// 模式未知（旧版 Agent 未上报）的退化行为：沿用历史判定，>1 主即异常。
func TestClassifyClusterFaultUnknownModeKeepsLegacyBehaviour(t *testing.T) {
	members := grCluster(true, []string{"primary", "primary"}, nil)
	for i := range members {
		members[i].SinglePrimary = nil
	}
	if got := ClassifyClusterFault(members); got == "" {
		t.Fatal("模式未知时 >1 主库应沿用历史判定（异常）")
	}
}

// 没有角色概念的集群（如 Kubernetes）不能被判「无主」——这是重构前存在的假告警：
// k8s_cluster_up 不带 role 标签，原判定会恒定报「无主」。
func TestClassifyClusterFaultSkipsRoleJudgementWithoutRoleInfo(t *testing.T) {
	members := []ClusterMember{
		{Node: "node-1", Instance: "k8s-1", Group: "k8s", Topology: "cluster", UP: true, Value: 1},
	}
	if got := ClassifyClusterFault(members); got != "" {
		t.Fatalf("无角色信息的集群不应判「无主」，实际 %q", got)
	}
}

// 全部成员不可达：交由「服务离线」规则处理，集群判定不重复告警。
func TestClassifyClusterFaultAllDownIsSilent(t *testing.T) {
	members := grCluster(true, []string{"primary", "secondary"}, nil)
	for i := range members {
		members[i].UP = false
		members[i].Value = 0
	}
	if got := ClassifyClusterFault(members); got != "" {
		t.Fatalf("全部离线时集群判定应保持沉默（由服务离线规则负责），实际 %q", got)
	}
}

// 单主模式一个主库 + 两个从库：正常。
func TestClassifyClusterFaultSinglePrimaryHealthy(t *testing.T) {
	members := grCluster(true, []string{"primary", "secondary", "secondary"}, nil)
	if got := ClassifyClusterFault(members); got != "" {
		t.Fatalf("单主模式下 1 主 2 从应正常，实际 %q", got)
	}
}

// 离线成员不参与角色计数：3 成员中 1 个离线、其余 1 主 1 从 → 仍属健康（离线由服务离线规则报）。
func TestClassifyClusterFaultOfflineMemberExcludedFromRoleCount(t *testing.T) {
	members := grCluster(true, []string{"primary", "secondary", "primary"}, nil)
	members[2].UP = false
	members[2].Value = 0
	if got := ClassifyClusterFault(members); got != "" {
		t.Fatalf("离线成员不应参与角色计数，实际 %q", got)
	}
}

func TestSameMembersOrderInsensitive(t *testing.T) {
	if !sameMembers([]string{"a", "b"}, []string{"b", "a"}) {
		t.Fatal("成员集合比较应与顺序无关")
	}
	if sameMembers([]string{"a", "b"}, []string{"a", "c"}) {
		t.Fatal("不同成员集合不应相等")
	}
	if sameMembers([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("长度不同不应相等")
	}
}

func TestClusterMemberViewNilSkipsSplitCheck(t *testing.T) {
	// 旧版 Agent 不上报组视图（View=nil）时不应误判分裂：1 主 2 从仍正常。
	members := grCluster(true, []string{"primary", "secondary", "secondary"}, nil)
	if got := ClassifyClusterFault(members); got != "" {
		t.Fatalf("无组视图信息时不应判分裂，实际 %q", got)
	}
}

// grClusterWithStates 构造视图一致、但成员状态可指定的集群（用于「成员未就绪」判定）。
func grClusterWithStates(mode bool, roles []string, states []map[string]string) []ClusterMember {
	full := []string{"mysql-gr-1", "mysql-gr-2", "mysql-gr-3"}
	members := grCluster(mode, roles, [][]string{full, full, full})
	for i := range members {
		members[i].ViewStates = states[i]
	}
	return members
}

// 成员实例可达（up=1），但所有观察者一致认为它不在 ONLINE（RECOVERING/OFFLINE）：
// 它并未真正参与组复制，看板不能显示"运行正常"。
// 依据：实测把集群切成多主后，两个成员在组里长期 RECOVERING，而旧判定仍显示健康。
func TestClassifyClusterFaultUnreadyMemberIsFault(t *testing.T) {
	states := []map[string]string{
		{"mysql-gr-1": "ONLINE", "mysql-gr-2": "RECOVERING", "mysql-gr-3": "ONLINE"},
		{"mysql-gr-1": "ONLINE", "mysql-gr-2": "RECOVERING", "mysql-gr-3": "ONLINE"},
		{"mysql-gr-1": "ONLINE", "mysql-gr-2": "RECOVERING", "mysql-gr-3": "ONLINE"},
	}
	members := grClusterWithStates(false, []string{"primary", "primary", "primary"}, states)
	got := ClassifyClusterFault(members)
	if !strings.Contains(got, "未就绪") {
		t.Fatalf("成员一致未就绪应判异常，实际 %q", got)
	}
	if !strings.Contains(got, "RECOVERING") {
		t.Fatalf("判定说明应带上成员状态，实际 %q", got)
	}
}

// 只有个别观察者认为某成员未就绪，其余认为 ONLINE：属局部/陈旧视角，不判故障（避免误报）。
func TestClassifyClusterFaultUnreadyNeedsConsensus(t *testing.T) {
	states := []map[string]string{
		{"mysql-gr-1": "ONLINE", "mysql-gr-2": "RECOVERING", "mysql-gr-3": "ONLINE"},
		{"mysql-gr-1": "ONLINE", "mysql-gr-2": "ONLINE", "mysql-gr-3": "ONLINE"},
		{"mysql-gr-1": "ONLINE", "mysql-gr-2": "ONLINE", "mysql-gr-3": "ONLINE"},
	}
	members := grClusterWithStates(false, []string{"primary", "primary", "primary"}, states)
	if got := ClassifyClusterFault(members); got != "" {
		t.Fatalf("观察者意见不一致时不应判未就绪，实际 %q", got)
	}
}

// 全部成员 ONLINE 的多主集群：正常。
func TestClassifyClusterFaultAllOnlineMultiPrimaryHealthy(t *testing.T) {
	all := map[string]string{"mysql-gr-1": "ONLINE", "mysql-gr-2": "ONLINE", "mysql-gr-3": "ONLINE"}
	members := grClusterWithStates(false, []string{"primary", "primary", "primary"},
		[]map[string]string{all, all, all})
	if got := ClassifyClusterFault(members); got != "" {
		t.Fatalf("全 ONLINE 的多主集群应正常，实际 %q", got)
	}
}

// 旧版 Agent 无成员状态信息（ViewStates=nil）时跳过就绪度判定，不影响既有结论。
func TestClassifyClusterFaultNoStatesSkipsUnreadyCheck(t *testing.T) {
	members := grCluster(true, []string{"primary", "secondary", "secondary"}, nil)
	if got := ClassifyClusterFault(members); got != "" {
		t.Fatalf("无成员状态信息时不应判未就绪，实际 %q", got)
	}
}

// 组内有的实例在组里、有的完全没上报 GR 信息（组复制被停掉后角色退化为 master，
// 计数看起来仍正常）→ 必须判异常，否则「实例已掉出集群」被当成健康。
func TestClassifyClusterFaultMemberMissingFromGroup(t *testing.T) {
	// 只有 gr-1 在组里；另两个实例 GR 已停、角色退化为 master（无 GR 信息）
	members := []ClusterMember{
		{Node: "n1", Instance: "10.0.0.10:3307", Group: "g", Role: "primary", Topology: "cluster",
			UP: true, Value: 1, SinglePrimary: grBoolPtr(false), View: []string{"mysql-gr-1"},
			ViewStates: map[string]string{"mysql-gr-1": "ONLINE"}},
		{Node: "n1", Instance: "10.0.0.10:3308", Group: "g", Role: "master", Topology: "cluster", UP: true, Value: 1},
		{Node: "n1", Instance: "10.0.0.10:3309", Group: "g", Role: "master", Topology: "cluster", UP: true, Value: 1},
	}
	got := ClassifyClusterFault(members)
	if !strings.Contains(got, "未在组复制中") {
		t.Fatalf("掉出集群的实例应判异常，实际 %q", got)
	}
}

// 组复制已停止的节点仍会上报「单主/多主模式」变量（插件已加载），但不再出现在成员表里
// （没有组视图序列）：缺席判定必须以组视图为依据，否则会漏报。
// 依据：实测容器重启把运行时设置重置后，两个成员正是这么掉出组、却仍上报着模式变量。
func TestClassifyClusterFaultModeWithoutViewStillCountsAsMissing(t *testing.T) {
	members := []ClusterMember{
		{Node: "n1", Instance: "10.0.0.10:3307", Group: "g", Role: "primary", Topology: "cluster",
			UP: true, Value: 1, SinglePrimary: grBoolPtr(true),
			View: []string{"mysql-gr-1"}, ViewStates: map[string]string{"mysql-gr-1": "ONLINE"}},
		{Node: "n1", Instance: "10.0.0.10:3308", Group: "g", Topology: "cluster",
			UP: true, Value: 1, SinglePrimary: grBoolPtr(true), Role: ""}, // 有模式、无组视图
	}
	got := ClassifyClusterFault(members)
	if !strings.Contains(got, "未在组复制中") {
		t.Fatalf("只有模式没有组视图的实例应判缺席，实际 %q", got)
	}
}

// 角色未知（GR 角色查询失败）不应被当成主库 → 不产生「多主」误报。
// 依据：实测节点刚重新入组时，agent 曾把组复制节点回退标成 master，导致瞬时误报脑裂。
func TestClassifyClusterFaultUnknownRoleNotCountedAsPrimary(t *testing.T) {
	members := []ClusterMember{
		{Node: "n1", Instance: "a", Group: "g", Role: "primary", Topology: "cluster", UP: true, Value: 1,
			SinglePrimary: grBoolPtr(true), View: []string{"mysql-gr-1", "mysql-gr-2", "mysql-gr-3"},
			ViewStates: map[string]string{"mysql-gr-1": "ONLINE", "mysql-gr-2": "ONLINE", "mysql-gr-3": "ONLINE"}},
		{Node: "n1", Instance: "b", Group: "g", Role: "", Topology: "cluster", UP: true, Value: 1,
			SinglePrimary: grBoolPtr(true), View: []string{"mysql-gr-1", "mysql-gr-2", "mysql-gr-3"},
			ViewStates: map[string]string{"mysql-gr-1": "ONLINE", "mysql-gr-2": "ONLINE", "mysql-gr-3": "ONLINE"}},
		{Node: "n1", Instance: "c", Group: "g", Role: "", Topology: "cluster", UP: true, Value: 1,
			SinglePrimary: grBoolPtr(true), View: []string{"mysql-gr-1", "mysql-gr-2", "mysql-gr-3"},
			ViewStates: map[string]string{"mysql-gr-1": "ONLINE", "mysql-gr-2": "ONLINE", "mysql-gr-3": "ONLINE"}},
	}
	if got := ClassifyClusterFault(members); got != "" {
		t.Fatalf("角色未知不应被当成主库（1 主 + 2 未知 属正常），实际 %q", got)
	}
}

// 同组实例都上报 GR 信息（无论单主多主）：不触发「未在组复制中」。
func TestClassifyClusterFaultAllInGroupNoMissingFault(t *testing.T) {
	members := grCluster(true, []string{"primary", "secondary", "secondary"}, nil)
	for i := range members {
		members[i].View = []string{"mysql-gr-1", "mysql-gr-2", "mysql-gr-3"}
	}
	if got := ClassifyClusterFault(members); got != "" {
		t.Fatalf("全部在组内不应判缺席，实际 %q", got)
	}
}

// 非 GR 部署（所有实例都没有 GR 信息，如异步主从）不应被判「未在组复制中」。
func TestClassifyClusterFaultNonGRClusterNotFlagged(t *testing.T) {
	members := []ClusterMember{
		{Node: "n1", Instance: "10.0.0.10:3306", Group: "g", Role: "master", Topology: "cluster", UP: true, Value: 1},
		{Node: "n1", Instance: "10.0.0.10:3307", Group: "g", Role: "slave", Topology: "cluster", UP: true, Value: 1},
	}
	if got := ClassifyClusterFault(members); got != "" {
		t.Fatalf("异步复制集群不应被判缺席组复制，实际 %q", got)
	}
}
