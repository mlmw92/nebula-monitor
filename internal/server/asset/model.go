// Package asset 实现「资产与配置」领域：资产类型、资产台账、关联关系、变更历史与配置快照。
//
// 设计依据：docs/superpowers/specs/2026-09-30-asset-cmdb-design.md
// 持久化决策：docs/adr/0001-cmdb-relational-store.md（内嵌 SQLite，纯 Go 驱动）
//
// 本包只负责「模型、关系、变更、快照」。资产的**采集**由既有 Agent 采集器产出
// （主机信息、中间件实例、容器清单），本包通过 Apply 消费这些观测值，不新增采集器。
package asset

import (
	"sort"
	"strings"
)

// Source 是属性值的来源。
//
// 采集值与人工值必须**并存、互不覆盖**：人工值只影响展示优先级，采集值始终保留，
// 两者的差异在资产详情里可见。这与 model.Node.DisplayName 的既有语义一致
// （「自定义显示名/别名（不修改 Agent 上报的真实主机名）」）。
type Source string

const (
	// SourceDiscovery 表示该值来自 Agent 采集。
	SourceDiscovery Source = "discovery"
	// SourceManual 表示该值由人工维护。
	SourceManual Source = "manual"
)

// Valid 判断来源是否可用。空来源按「采集值」处理——调用方绝大多数是采集路径，
// 强行要求显式声明只会让人忘写。
func (s Source) Valid() bool {
	return s == "" || s == SourceDiscovery || s == SourceManual
}

// normalized 返回落库用的来源值（空 → 采集值）。
func (s Source) normalized() Source {
	if s == "" {
		return SourceDiscovery
	}
	return s
}

// LinkKind 是资产关联的类型。
//
// 刻意只保留四类：关系宁少而准，避免一开始就做通用图模型（设计件 §3.2）。
type LinkKind string

const (
	LinkRunsOn    LinkKind = "runs_on"    // 服务 / 容器运行于主机
	LinkMemberOf  LinkKind = "member_of"  // 实例属于集群
	LinkDependsOn LinkKind = "depends_on" // 显式声明的依赖
	LinkExposes   LinkKind = "exposes"    // 服务暴露于端点
)

// Valid 判断关联类型是否受支持。
func (k LinkKind) Valid() bool {
	switch k {
	case LinkRunsOn, LinkMemberOf, LinkDependsOn, LinkExposes:
		return true
	}
	return false
}

// ChangeKind 区分「建档」与「变更」。
//
// 首次发现一个资产只写一条 initial 记录，不产生逐条变更噪声（设计件 §风险与取舍）。
type ChangeKind string

const (
	ChangeInitial ChangeKind = "initial"
	ChangeUpdate  ChangeKind = "update"
)

// 内置资产类型键。
const (
	TypeHost           = "host"                // 主机（自然键 = hostname，与 model.Node.Hostname 一致）
	TypeMiddlewareInst = "middleware-instance" // 中间件实例（自然键 = <类型>:<地址>）
	// TypePod 是 K8s Pod（自然键 = <集群>/<命名空间>/pod/<名称>）。
	//
	// 键名用 pod 而不是 container，这是刻意的：**Pod 不是容器**（一个 Pod 可有多个容器），
	// 而"容器"这个词在本平台已经指 Docker 容器——它们以 middleware-instance 落在台账里
	// （自然键 docker:<容器ID>）。用一个词指两个东西，以后一定会有人按"container 类型"
	// 去找 Docker 容器而找不到。
	TypePod = "pod"
	// TypeWorkload 是 K8s 工作负载（Deployment / StatefulSet / DaemonSet），
	// 自然键 = <集群>/<命名空间>/<kind>/<名称>。
	TypeWorkload = "workload"
)

// EphemeralTypes 是**运行时短命对象**的资产类型：它们天生高 churn（滚动更新、Job、扩缩容），
// 与台账"长期存在、需要有人负责的配置项"不是同一类东西。
//
// 因此它们默认不参与台账首页的默认视图与健康度摘要（总数 / 失联 / 无责任人）：
// 被替换掉的旧 Pod 停止上报后会被判为失联，计入之后页面顶部会出现"失联 200"，
// 把真实故障埋掉；而没人会为滚动更新掉的 Pod 指派责任人。
// 显式按类型筛选时仍然全量可见（含已消失的）。
func EphemeralTypes() []string { return []string{TypePod, TypeWorkload} }

// AssetType 是资产的定义：字段集合、是否内置、是否允许人工维护。
type AssetType struct {
	Key     string
	Title   string
	Builtin bool
	// Schema 是字段定义（JSON）。首期原样存取，不做解释——避免在没有消费方之前
	// 就固化一套 DSL。
	Schema string
}

// BuiltinTypes 返回内置资产类型，供 Store 初始化时播种。
//
// 这里新增类型**不需要**升 schemaVersion：类型是数据行、不是表结构，
// 而播种语句是 `INSERT … ON CONFLICT(key) DO UPDATE SET title`（store.go），
// 每次启动对齐一遍即可自愈。这与"给 asset_links 补 source 列"必须升版本 + 显式 ALTER 是两回事。
func BuiltinTypes() []AssetType {
	return []AssetType{
		{Key: TypeHost, Title: "主机", Builtin: true},
		{Key: TypeMiddlewareInst, Title: "中间件实例", Builtin: true},
		{Key: TypePod, Title: "容器（Pod）", Builtin: true},
		{Key: TypeWorkload, Title: "工作负载", Builtin: true},
	}
}

// PodNaturalKey 由集群（apiserver 地址）、命名空间与 Pod 名拼出自然键。
//
// 形态与 normalizeKey 注释里预留的 <集群>/<命名空间>/<kind>/<name> 一致。
// 集群取 apiserver 地址而不是别名：地址是唯一键，别名可变（K8sInstance 的注释已界定）。
func PodNaturalKey(cluster, namespace, name string) string {
	return podKeyPrefix(cluster, namespace) + "pod/" + strings.TrimSpace(name)
}

// WorkloadNaturalKey 同 PodNaturalKey，<kind> 取 deployment / statefulset / daemonset。
func WorkloadNaturalKey(cluster, namespace, kind, name string) string {
	return podKeyPrefix(cluster, namespace) + strings.TrimSpace(kind) + "/" + strings.TrimSpace(name)
}

func podKeyPrefix(cluster, namespace string) string {
	return strings.TrimSpace(cluster) + "/" + strings.TrimSpace(namespace) + "/"
}

// ContainerIdentity 是容器类资产（pod / workload）的自然键解出的身份。
type ContainerIdentity struct {
	Cluster   string
	Namespace string
	Kind      string
	Name      string
}

// ParseContainerKey 从 pod / workload 的自然键解出身份三元组。
//
// 为什么由服务端解：集群是 apiserver 地址，本身含 `://` 与可能的端口，键里因此有斜杠——
// 前端 split 一次就可能解错（把 https: 当成命名空间），而**解错的联动不会报错**，
// 只会跳到另一个 Pod 上。拼法与解析放在同一处，改的时候两边一起改。
//
// 形态：<cluster>/<namespace>/<kind>/<name>，其中 cluster 可含 `/`，因此从右往左取三段。
func ParseContainerKey(typeKey, naturalKey string) (ContainerIdentity, bool) {
	if typeKey != TypePod && typeKey != TypeWorkload {
		return ContainerIdentity{}, false
	}
	parts := strings.Split(strings.TrimSpace(naturalKey), "/")
	if len(parts) < 4 {
		return ContainerIdentity{}, false
	}
	ident := ContainerIdentity{
		Cluster:   strings.Join(parts[:len(parts)-3], "/"),
		Namespace: parts[len(parts)-3],
		Kind:      parts[len(parts)-2],
		Name:      parts[len(parts)-1],
	}
	if ident.Cluster == "" || ident.Namespace == "" || ident.Kind == "" || ident.Name == "" {
		return ContainerIdentity{}, false
	}
	return ident, true
}

// Attr 是一条资产属性。
//
// 同一 key 允许同时存在采集值与人工值（联合主键含 source），因此这里不能只看 value。
type Attr struct {
	Key       string
	Value     string
	Source    Source
	UpdatedAt int64
	UpdatedBy string
}

// Asset 是一个资产实例。
type Asset struct {
	ID         int64
	TypeKey    string
	NaturalKey string
	Name       string
	// Node 是该资产所属节点（hostname）。资源范围经它映射到既有节点分组，
	// 不做第二套范围判定（设计件 §3.2）。非主机类资产可为空。
	Node  string
	Attrs map[string]Attr
	// Labels 是资产的管理标签（key → value），与 Attrs **职责分开**：
	// Attrs 是采集值 / 人工值（含责任人 owner），参与巡检与变更语义；
	// Labels 是分类维度（业务系统 / 环境 / 机房…），用于展示与筛选，不参与巡检比对。
	Labels map[string]string
	// Ignored 为 true 表示该资产已从台账隐藏（忽略）。
	//
	// 它是「隐藏」而不是「删除」：采集仍会继续刷新其属性（因此恢复后看到的是最新状态），
	// 但列表 / 摘要 / 巡检默认都不再计入它。采集资产一旦删除会被下一轮上报重建，
	// 所以对它们正确的动作是忽略，而不是删除。
	Ignored      bool
	IgnoreReason string
	IgnoredBy    string
	IgnoredAt    int64
	// CreatedAt / UpdatedAt 单位为毫秒。
	CreatedAt int64
	UpdatedAt int64
	// SeenAt 是最近一次**采集上报**的时刻（毫秒），由 asset_seen 表维护；0 表示从未上报。
	// 与 UpdatedAt / 属性行的 updated_at 都不同：后两者只在"值发生变化"时前进。
	SeenAt int64
}

// Value 返回属性的**生效值**：人工值优先，其次采集值。
func (a Asset) Value(key string) (string, bool) {
	if v, ok := a.ValueFrom(key, SourceManual); ok {
		return v, true
	}
	return a.ValueFrom(key, SourceDiscovery)
}

// ValueFrom 返回指定来源的属性值。
func (a Asset) ValueFrom(key string, src Source) (string, bool) {
	attr, ok := a.Attrs[key+"@"+string(src)]
	if !ok {
		return "", false
	}
	return attr.Value, true
}

// OwnerKey 是「责任人」的约定属性键。
//
// 责任人是唯一被列表、摘要与筛选直接消费的管理属性，因此约定一个固定键；
// 其余管理属性（业务系统 / 环境 / 维保到期 / 资产编号…）保持自由键——
// 在字典与枚举能力上线前，不把一堆展示字段固化成领域概念。
const OwnerKey = "owner"

// Owner 返回人工维护的责任人（空串表示未指派）。
func (a Asset) Owner() string {
	if v, ok := a.ValueFrom(OwnerKey, SourceManual); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// SourceMix 统计人工值数量与「人工、采集并存」的字段数量。
//
// conflict 的语义是**同一字段**两种来源都有值——这是资产台账最需要人处理的一类数据：
// 生效值取人工值，但采集值仍在变，不处理就会一直"看着像对、其实已过期"。
func (a Asset) SourceMix() (manualCount, conflictCount int) {
	byKey := map[string]struct{ discovery, manual bool }{}
	for _, attr := range a.Attrs {
		entry := byKey[attr.Key]
		if attr.Source == SourceManual {
			entry.manual = true
			manualCount++
		} else {
			entry.discovery = true
		}
		byKey[attr.Key] = entry
	}
	for _, entry := range byKey {
		if entry.discovery && entry.manual {
			conflictCount++
		}
	}
	return manualCount, conflictCount
}

// ConflictKeys 返回同时存在采集值与人工值的字段名（按字典序，便于稳定展示与测试）。
func (a Asset) ConflictKeys() []string {
	byKey := map[string]struct{ discovery, manual bool }{}
	for _, attr := range a.Attrs {
		entry := byKey[attr.Key]
		if attr.Source == SourceManual {
			entry.manual = true
		} else {
			entry.discovery = true
		}
		byKey[attr.Key] = entry
	}
	out := make([]string, 0, len(byKey))
	for key, entry := range byKey {
		if entry.discovery && entry.manual {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// HasDiscovery 判断资产是否被采集过（纯人工建档的资产为 false）。
func (a Asset) HasDiscovery() bool {
	for _, attr := range a.Attrs {
		if attr.Source == SourceDiscovery {
			return true
		}
	}
	return false
}

// LastSeenAt 返回最近一次**采集上报**的时间（毫秒）；从未被采集时返回 0。
//
// 不能用 Asset.UpdatedAt：人工维护也会刷新它，于是"最近上报"会显示成人工改动的时间。
// 也**不能**用属性行的 updated_at：Apply 只在值真正变化时才写属性行（避免每轮制造无意义的
// 变更记录），而主机的 os/cpuCores、中间件的 version/topology 长期不变——这些资产的
// "最近上报"会永久冻结在最后一次变更时刻，超过阈值后整页资产被误判为失联。
// 因此单独维护 asset_seen：它只回答「最后一次见到它是什么时候」，与值是否变化无关。
func (a Asset) LastSeenAt() int64 { return a.SeenAt }

// attrID 是属性在 map 中的键：同一 key 的来源不同则是不同条目。
func attrID(key string, src Source) string {
	return key + "@" + string(src.normalized())
}

// Ref 是资产的引用（类型 + 自然键），用于关联关系等只需定位不需加载的场景。
type Ref struct {
	TypeKey    string
	NaturalKey string
}

// Link 是一条资产关联（有向）。
//
// Source 记录这条边由谁认领，复用属性那套「采集 / 人工」词汇——关系的来源与属性的来源
// 是同一个概念，没必要各造一套。
//
// **人工优先**：同一 (from,to,kind) 只存在一条边（见 asset_links 的唯一约束），
// 一旦被人工认领就一直是 manual；采集侧照常上报同一条边，但不把它降级回 discovery。
// 人工删除一条边则是**逻辑删除**（落 asset_link_suppressions），否则下一轮采集就把它建回来。
type Link struct {
	ID        int64
	From      Ref
	To        Ref
	Kind      LinkKind
	Source    Source
	CreatedAt int64
}

// SuppressedLink 是一条**被人工抑制**的关联（逻辑删除的结果）。
//
// 单独一个类型而不是复用 Link：抑制记录没有"来源"可言（它就是「人工说过这条关系不存在」），
// 但多带一个 created_by，用来回答「谁把它藏了」——这在多人协作的台账里比时间更有用。
type SuppressedLink struct {
	From      Ref
	To        Ref
	Kind      LinkKind
	CreatedAt int64
	CreatedBy string
}

// ChangeRecord 是一条字段级变更。
//
// 与审计的分工：审计回答「谁调了什么接口」，变更记录回答「哪个字段从什么变成什么」。
type ChangeRecord struct {
	ID      int64
	AssetID int64
	Field   string
	Old     string
	New     string
	Source  Source
	Actor   string
	Kind    ChangeKind
	At      int64
}

// Snapshot 是某资产在某一时刻被抽取的**关注字段集合**（用于前后比对与合规比对）。
type Snapshot struct {
	ID      int64
	AssetID int64
	TakenAt int64
	Fields  map[string]string
}

// FindingKind 是差异项的分类（对齐设计件「差异分级」的四种）。
type FindingKind string

const (
	// FindingAdded 新增：本次出现、上次快照里没有的字段。
	FindingAdded FindingKind = "added"
	// FindingChanged 变更：同一字段、值不同（运维最关心的那一类）。
	FindingChanged FindingKind = "changed"
	// FindingMissing 缺失：上次快照里有、本次没有的字段。
	FindingMissing FindingKind = "missing"
	// FindingDeviation 合规偏差：与「标杆资产」的期望值不一致（L3）。
	FindingDeviation FindingKind = "deviation"
)

// FindingLevel 是差异项的严重级别。
//
// 分级刻意区分「值变了」与「字段没了」：后者通常意味着采集退化或配置被删，
// 比前者更该被看见；而新增字段多数只是采集到了新项，属提示级。
type FindingLevel string

const (
	FindingInfo     FindingLevel = "info"
	FindingWarning  FindingLevel = "warning"
	FindingCritical FindingLevel = "critical"
)

// InspectRun 是一次差异巡检的执行记录。
type InspectRun struct {
	ID int64
	// Scope 是本次巡检的范围描述（如 all / type:host / node:web-01），供人读懂记录用途。
	Scope     string
	Actor     string
	StartedAt int64
	// Baselined 是「本次首次建立基线、因而无法比对」的资产数。
	// 单独计数是为了让界面能解释「为什么第一次巡检没有差异」——数据不足不等于不合规。
	Baselined int
	Assets    int
	Findings  int
	// Truncated 表示资产数超过单次巡检上限，本次只覆盖了前 N 个（绝不静默截断）。
	Truncated    bool
	PartialScope bool
}

// InspectRunMember 保存执行时归属；读取权限始终按当前资产与节点分组判断。
type InspectRunMember struct {
	AssetID   int64
	Node      string
	Baselined bool
	Findings  int
}

// InspectFinding 是一条差异项。
//
// 冗余存资产的身份字段（type/key/name/node）而不是只存 asset_id：
// 巡检记录是**证据**，资产后来被删除也不该让历史结论变得无法解读。
type InspectFinding struct {
	ID              int64
	RunID           int64
	AssetID         int64
	AssetType       string
	AssetKey        string
	AssetName       string
	Node            string
	Field           string
	Kind            FindingKind
	Level           FindingLevel
	Expected        string
	Actual          string
	BaselineAssetID int64
	At              int64
}

// Baseline 是「标杆资产」：以它的某次快照作为该资产类型的期望值（L3 合规比对）。
type Baseline struct {
	TypeKey    string
	AssetID    int64
	AssetKey   string
	SnapshotID int64
	SetBy      string
	SetAt      int64
}

// Observation 是一次提交给 Service.Apply 的观测：采集轮次或一次人工维护。
type Observation struct {
	TypeKey    string
	NaturalKey string
	Name       string
	Node       string
	Source     Source
	Actor      string
	Attrs      map[string]string
}

// normalizeValue 用于判断「值是否真的变化」：忽略首尾空白与大小写差异，
// 避免每轮上报都写一条无意义的变更记录。**展示值始终保留原样。**
func normalizeValue(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

// normalizeKey 规范化自然键：去首尾空白。
//
// 自然键是幂等 upsert 的唯一依据，必须由稳定标识构成（hostname / <类型>:<地址> /
// <集群>/<命名空间>/<kind>/<name>），因此不允许为空。
func normalizeKey(v string) string {
	return strings.TrimSpace(v)
}
