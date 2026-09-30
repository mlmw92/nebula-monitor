// Package asset 实现「资产与配置」领域：资产类型、资产台账、关联关系、变更历史与配置快照。
//
// 设计依据：docs/superpowers/specs/2026-09-30-asset-cmdb-design.md
// 持久化决策：docs/adr/0001-cmdb-relational-store.md（内嵌 SQLite，纯 Go 驱动）
//
// 本包只负责「模型、关系、变更、快照」。资产的**采集**由既有 Agent 采集器产出
// （主机信息、中间件实例、容器清单），本包通过 Apply 消费这些观测值，不新增采集器。
package asset

import "strings"

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
)

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
func BuiltinTypes() []AssetType {
	return []AssetType{
		{Key: TypeHost, Title: "主机", Builtin: true},
		{Key: TypeMiddlewareInst, Title: "中间件实例", Builtin: true},
	}
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
	// CreatedAt / UpdatedAt 单位为毫秒。
	CreatedAt int64
	UpdatedAt int64
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
type Link struct {
	ID        int64
	From      Ref
	To        Ref
	Kind      LinkKind
	CreatedAt int64
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
