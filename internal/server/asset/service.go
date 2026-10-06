package asset

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	// DefaultListLimit / MaxListLimit 是列表查询的分页边界。
	//
	// 上限的意义与集中日志的扫描预算相同：不让一次请求把整库拉进内存。
	DefaultListLimit = 50
	// MaxListLimit 是客户端可请求的最大页大小。
	MaxListLimit = 500

	defaultHistoryLimit  = 200
	defaultSnapshotLimit = 50

	// defaultInspectRunLimit / defaultInspectFindingLimit 是巡检记录与差异项的返回上限。
	defaultInspectRunLimit     = 50
	defaultInspectFindingLimit = 500
	// maxInspectAssets 是单次巡检覆盖的资产上限。
	//
	// 超限时**显式截断并在记录里标注**（`truncated`）：宁可在界面看到"本次只覆盖了前 N 个"，
	// 也不能让一次巡检把整库读进内存、或者悄悄只检了一部分却报告"已巡检"。
	maxInspectAssets = 5000
)

// ListFilter 是资产列表的查询条件。零值表示「全部资产」，但始终受默认分页约束。
type ListFilter struct {
	TypeKey string
	// ExcludeTypes 排除若干资产类型：用于把**运行时短命对象**（容器 / 工作负载，
	// 见 EphemeralTypes）挡在默认视图与健康度摘要之外。
	//
	// 由调用方（API 层）决定要不要设：用户**显式**按类型筛选时不该再排除——
	// 他明确要看容器，却被他自己的筛选条件排除掉，是自相矛盾的。
	ExcludeTypes []string
	Node         string
	// Keyword 匹配资产名称、自然键与**属性值**（原型：搜索资产名 / 自然键 / 属性值）。
	Keyword string
	// Nodes 限定归属节点集合，用于把调用方的资源范围下推到 SQL：
	//   nil      —— 不做节点限制（未启用认证或全局范围）
	//   非空切片 —— 只返回归属这些节点的资产
	//   空切片   —— 无任何可见节点，恒空结果（**不能**当作「不限制」，那是越权旁路）
	//
	// 之所以由调用方传入而不是在存储层过滤：资源范围是服务端的授权概念，
	// 资产库不该知道它；但过滤又必须发生在分页之前，所以以条件形式下推。
	Nodes []string
	// Source 过滤资产来源（SourceAuto / SourceManual / SourceMixed）；空串不过滤。
	Source string
	// Status 过滤资产状态（StatusOnline / StatusMissing / StatusArchived）；空串不过滤。
	// missing / online 需要 StaleBefore：晚于该时刻上报即为 online，早于即 missing。
	Status string
	// StaleBefore 是失联判定的分界时刻（毫秒），由 API 按「当前时间 - 阈值」传入。
	// 存储层不自己取当前时间：那样同一个查询的列表与总数可能落在不同瞬间，
	// 门槛上的资产会在两处得到不同结论（总数与列表对不上）。
	StaleBefore int64
	// OwnerMissing 只返回人工未指派责任人的资产（摘要「无责任人」下钻用）。
	OwnerMissing bool
	// HasConflict 只返回存在「同字段人工值与采集值并存」的资产（摘要「冲突」下钻用）。
	HasConflict bool
	// Ignored 控制**已忽略资产**的可见性（取值见 IgnoreFilter*）：
	//   ""（默认） 不返回已忽略资产——台账是"该关心的东西"的清单
	//   with      连同已忽略一起返回（界面上"含已忽略"开关）
	//   only      只返回已忽略（摘要「已忽略」下钻用）
	Ignored string
	// Label 按标签过滤：`key` 表示"存在该标签键"，`key:value` 表示精确匹配键值。
	Label  string
	Limit  int
	Offset int
}

// 已忽略资产的可见性取值。
const (
	IgnoreFilterWith = "with"
	IgnoreFilterOnly = "only"
)

// ValidIgnoredFilter 判断已忽略可见性取值是否受支持（空串表示默认隐藏）。
func ValidIgnoredFilter(v string) bool {
	switch v {
	case "", IgnoreFilterWith, IgnoreFilterOnly:
		return true
	}
	return false
}

// 来源过滤取值：与前端「来源」下拉、摘要下钻保持一致。
//
// 命名带 Filter 前缀以免与 SourceManual（属性来源）混淆——两者都是 "manual" 语义但不同层次：
// 属性来源说「这条值是人工写的」，来源过滤说「这个资产有人工介入」。
const (
	SourceFilterAuto   = "auto"   // 无任何人工值
	SourceFilterManual = "manual" // 有人工值且无冲突
	SourceFilterMixed  = "mixed"  // 存在同字段人工值与采集值并存
)

// 状态过滤取值。
//
// 状态是**派生**的（不落库）：归档 = 从无采集（纯人工建档）；missing = 最近采集早于失联阈值；
// online = 有采集且新鲜。刻意不把状态存成一列——它是时间与采集共同推导出来的结论，
// 存下来就必须有人定期刷新，反而会写出"字段说 online、现实已失联"的自相矛盾。
const (
	StatusOnline   = "online"
	StatusMissing  = "missing"
	StatusArchived = "archived"
)

// Stats 是台账摘要（供列表页顶部的健康度条使用）。
type Stats struct {
	Total int `json:"total"`
	// Missing 是「曾被采集、但已超过失联阈值未再上报」的资产数（不含纯人工归档资产）。
	Missing  int `json:"missing"`
	NoOwner  int `json:"noOwner"`
	Conflict int `json:"conflict"`
	// Changes 是窗口内发生变更的字段数（含采集与人工）。窗口由调用方给出（默认近 7 天）。
	Changes int `json:"changes"`
	// Ignored 是「已忽略」的资产数（同样按当前筛选条件统计）。
	// 单独给出它，是因为已忽略资产默认不出现在 Total 里——不显示这个数字，
	// 用户会以为自己忽略过的东西"找不回来了"。
	Ignored int `json:"ignored"`
}

// ValidSourceFilter 判断来源过滤取值是否受支持（空串表示不过滤）。
func ValidSourceFilter(v string) bool {
	switch v {
	case "", SourceFilterAuto, SourceFilterManual, SourceFilterMixed:
		return true
	}
	return false
}

// ValidStatusFilter 判断状态过滤取值是否受支持（空串表示不过滤）。
func ValidStatusFilter(v string) bool {
	switch v {
	case "", StatusOnline, StatusMissing, StatusArchived:
		return true
	}
	return false
}

func (f ListFilter) limit() int {
	if f.Limit <= 0 {
		return DefaultListLimit
	}
	if f.Limit > MaxListLimit {
		return MaxListLimit
	}
	return f.Limit
}

func (f ListFilter) offset() int {
	if f.Offset < 0 {
		return 0
	}
	return f.Offset
}

// Service 是资产领域对外的唯一接口。
//
// 这是本包的深模块接缝：调用方（API / 巡检 / 后续作业模块）只需知道这几个方法；
// 类型校验、采集值与人工值合并、变更 diff、关系一致性与分页边界都藏在实现里，
// 因此这些行为可以在 Service 这一层被测试，而不必穿透到 SQL。
type Service struct {
	store *Store
	now   func() int64
}

// NewService 创建资产服务。store 由 Open 得到，生命周期由调用方管理。
func NewService(store *Store) *Service {
	return &Service{store: store, now: func() int64 { return time.Now().UnixMilli() }}
}

// Apply 提交一次观测（一轮采集或一次人工维护），按「类型 + 自然键」幂等 upsert。
//
// 三条硬性语义（设计件 §3.2）：
//  1. 同键重复提交不会产生重复资产；
//  2. 采集值与人工值并存、互不覆盖（人工值只影响生效值）；
//  3. **只有值真的变化**才写变更记录，首次建档只写一条 initial。
//
// 返回值 created 表示本次是否新建了资产。
func (s *Service) Apply(ob Observation) (Asset, bool, error) {
	typeKey := strings.TrimSpace(ob.TypeKey)
	naturalKey := normalizeKey(ob.NaturalKey)
	if typeKey == "" {
		return Asset{}, false, errors.New("资产类型不能为空")
	}
	if naturalKey == "" {
		return Asset{}, false, errors.New("资产自然键不能为空：它是幂等去重的唯一依据")
	}
	if !ob.Source.Valid() {
		return Asset{}, false, fmt.Errorf("未知的属性来源: %q", ob.Source)
	}
	if ok, err := s.store.typeExists(typeKey); err != nil {
		return Asset{}, false, err
	} else if !ok {
		return Asset{}, false, fmt.Errorf("资产类型 %q 未注册", typeKey)
	}

	src := ob.Source.normalized()
	at := s.now()
	name, node := strings.TrimSpace(ob.Name), strings.TrimSpace(ob.Node)

	cur, found, err := s.store.assetByNatural(typeKey, naturalKey)
	if err != nil {
		return Asset{}, false, err
	}

	if !found {
		id, err := s.store.insertAsset(Asset{
			TypeKey: typeKey, NaturalKey: naturalKey, Name: name, Node: node,
			CreatedAt: at, UpdatedAt: at,
		})
		if err != nil {
			return Asset{}, false, err
		}
		for key, value := range ob.Attrs {
			if err := s.store.upsertAttr(id, newAttr(key, value, src, at, ob.Actor)); err != nil {
				return Asset{}, false, err
			}
		}
		if src == SourceDiscovery {
			// 采集上报也刷新「最近上报」：它回答的是"最后一次见到它"，与值是否变化无关。
			if err := s.store.markSeen(id, at); err != nil {
				return Asset{}, false, err
			}
		}
		// 建档记录只有一条：首次导入不能产生逐条变更噪声。
		if err := s.store.appendChange(ChangeRecord{
			AssetID: id, Field: "asset", New: naturalKey,
			Source: src, Actor: ob.Actor, Kind: ChangeInitial, At: at,
		}); err != nil {
			return Asset{}, false, err
		}
		created, _, err := s.store.assetByNatural(typeKey, naturalKey)
		return created, true, err
	}

	changed := false
	// 名称与所属节点也是字段，变化同样要留痕。
	for field, pair := range map[string][2]string{"name": {cur.Name, name}, "node": {cur.Node, node}} {
		if normalizeValue(pair[0]) == normalizeValue(pair[1]) {
			continue
		}
		if err := s.store.appendChange(ChangeRecord{
			AssetID: cur.ID, Field: field, Old: pair[0], New: pair[1],
			Source: src, Actor: ob.Actor, Kind: ChangeUpdate, At: at,
		}); err != nil {
			return Asset{}, false, err
		}
		changed = true
	}
	if changed {
		if err := s.store.touchAsset(cur.ID, name, node, at); err != nil {
			return Asset{}, false, err
		}
	}

	for key, value := range ob.Attrs {
		prev, ok := cur.Attrs[attrID(key, src)]
		if ok && normalizeValue(prev.Value) == normalizeValue(value) {
			continue // 值没变：不写属性、不写变更记录
		}
		if err := s.store.upsertAttr(cur.ID, newAttr(key, value, src, at, ob.Actor)); err != nil {
			return Asset{}, false, err
		}
		old := ""
		if ok {
			old = prev.Value
		}
		if err := s.store.appendChange(ChangeRecord{
			AssetID: cur.ID, Field: key, Old: old, New: value,
			Source: src, Actor: ob.Actor, Kind: ChangeUpdate, At: at,
		}); err != nil {
			return Asset{}, false, err
		}
		changed = true
	}
	if src == SourceDiscovery {
		// 关键：即使上面一个字段都没变（上面 `continue` 了），也必须在**每次上报**都刷新
		// 「最近上报」。资产属性长期不变是常态（主机的 os/cpuCores、中间件的 version/topology），
		// 若只在值变化时刷新，这些资产会在阈值后被整批误判为失联。
		if err := s.store.markSeen(cur.ID, at); err != nil {
			return Asset{}, false, err
		}
	}
	if changed {
		if err := s.store.touchAsset(cur.ID, name, node, at); err != nil {
			return Asset{}, false, err
		}
	}

	updated, _, err := s.store.assetByNatural(typeKey, naturalKey)
	return updated, false, err
}

// Get 按（类型 + 自然键）取资产。
func (s *Service) Get(ref Ref) (Asset, bool, error) {
	return s.store.assetByNatural(strings.TrimSpace(ref.TypeKey), normalizeKey(ref.NaturalKey))
}

// GetHostByName 按主机名找主机资产，**忽略大小写**；返回的是台账里的规范资产（NaturalKey 为规范写法）。
//
// 为什么需要它：主机名按 DNS 约定大小写不敏感，而两侧的写法由不同系统决定——K8s 的节点名按
// RFC 1123 一律小写（`vm-0-10-ubuntu`），Agent 上报的 hostname 保留系统原样（`VM-0-10-ubuntu`）。
// 用严格比对去关联这两侧，会把**同一台机器**判成两台：Pod 的 `runs_on` 建不出来，
// 而且它的归属节点为空——**按节点分组授权的受限用户会看不到自己机器上的 Pod**。
// 这两处失效都不报错，只表现为"看起来没有关系"，所以必须在关联处按约定折叠大小写。
//
// 调用方要用返回资产的 NaturalKey 作为归属节点：只有规范写法才能在资源范围里查到节点分组。
func (s *Service) GetHostByName(name string) (Asset, bool, error) {
	name = normalizeKey(name)
	if name == "" {
		return Asset{}, false, nil
	}
	return s.store.assetByNaturalFold(TypeHost, name)
}

// GetByID 按主键取资产。
func (s *Service) GetByID(id int64) (Asset, bool, error) { return s.store.assetByID(id) }

// List 按条件分页列出资产。
func (s *Service) List(f ListFilter) ([]Asset, error) { return s.store.listAssets(f) }

// Count 返回符合条件的资产总数（忽略 Limit/Offset），供列表接口做分页。
//
// 与 List 用同一套条件（含 Nodes 资源范围下推），保证「总数」与「能翻到的条数」一致。
func (s *Service) Count(f ListFilter) (int, error) { return s.store.countAssets(f) }

// ListAll 返回符合条件的**全部**资产（不分页，忽略入参里的 Limit/Offset）。
//
// 导出清单必须走它：分页版默认只取一页，导出就会"看起来像全量、其实只有 50 条"——
// 这种错误没有任何提示，而用户会拿导出的表去做盘点（巡检早就踩过同一个坑，
// 见 listAssetsAll 的注释）。
func (s *Service) ListAll(f ListFilter) ([]Asset, error) {
	f.Limit, f.Offset = 0, 0
	return s.store.listAssetsAll(f)
}

// Stats 汇总台账健康度：总数 / 失联 / 无责任人 / 冲突 / 窗口内变更。
//
// 五个数字必须来自**同一套筛选条件与同一时刻**，否则顶部数字点进列表会出现
// 「摘要说 12 个失联、列表却是 11 条」——这正是摘要最容易失信的地方。
func (s *Service) Stats(f ListFilter, changesSince int64) (Stats, error) {
	return s.store.assetStats(f, changesSince)
}

// ResetManual 清除指定字段的人工值，使字段回落到采集值（原型的「恢复采集值」）。
//
// 只删人工值：采集值一直没被动过，所以"恢复"是**删除**而不是写回——
// 写回会把当前采集值固化成一条人工值，此后采集再变反而显示成"人工值覆盖"，
// 等于用一个更隐蔽的错误替换了原来那个。
func (s *Service) ResetManual(ref Ref, keys []string, actor string) (Asset, error) {
	a, err := s.resolve(ref)
	if err != nil {
		return Asset{}, err
	}
	want := make([]string, 0, len(keys))
	seen := map[string]bool{}
	for _, key := range keys {
		if key = strings.TrimSpace(key); key != "" && !seen[key] {
			seen[key] = true
			want = append(want, key)
		}
	}
	if len(want) == 0 {
		return Asset{}, errors.New("未指定要恢复的字段")
	}
	removed, err := s.store.deleteManualAttrs(a.ID, want, s.now(), actor)
	if err != nil {
		return Asset{}, err
	}
	if len(removed) == 0 {
		return Asset{}, errors.New("这些字段没有人工值，无需恢复")
	}
	updated, _, err := s.store.assetByNatural(a.TypeKey, a.NaturalKey)
	return updated, err
}

// LinkDiscovered 建立一条**采集得到**的关联（幂等）。采集侧的唯一入口。
//
// 两条例外：
//   - 这条边被人工抑制过（人删过它）→ 不重建。否则用户删一次、采集建一次，等于没删。
//   - 这条边已被人工认领 → 保持 manual，不降级（人工优先，由 store 的 UPSERT 保证）。
func (s *Service) LinkDiscovered(from, to Ref, kind LinkKind) error {
	fromAsset, toAsset, err := s.resolveLinkPair(from, to, kind)
	if err != nil {
		return err
	}
	suppressed, err := s.store.linkSuppressed(fromAsset.ID, toAsset.ID, kind)
	if err != nil {
		return err
	}
	if suppressed {
		return nil
	}
	return s.store.linkAssets(fromAsset.ID, toAsset.ID, kind, SourceDiscovery, s.now())
}

// LinkManual 由人工建立（或认领）一条关联。
//
// 人工主动加回来会**同时取消抑制**：人再建一次的意思就是要它存在，
// 此时不该继续挡着采集上报同一条边。
func (s *Service) LinkManual(from, to Ref, kind LinkKind) error {
	fromAsset, toAsset, err := s.resolveLinkPair(from, to, kind)
	if err != nil {
		return err
	}
	if err := s.store.clearLinkSuppression(fromAsset.ID, toAsset.ID, kind); err != nil {
		return err
	}
	return s.store.linkAssets(fromAsset.ID, toAsset.ID, kind, SourceManual, s.now())
}

// UnlinkManual 由人工解除一条关联 —— **逻辑删除**：删边 + 记录抑制。
//
// 为什么不能只删边：这条边本来就是采集发现的，下一轮采集立刻把它建回来，
// 用户的操作等于没做。抑制记录保留的正是「人说过这条关系不存在」这个信息。
// 想撤回这个判断用 RestoreDiscovered。
func (s *Service) UnlinkManual(from, to Ref, kind LinkKind, actor string) error {
	fromAsset, toAsset, err := s.resolveLinkPair(from, to, kind)
	if err != nil {
		return err
	}
	if err := s.store.unlinkAssets(fromAsset.ID, toAsset.ID, kind); err != nil {
		return err
	}
	return s.store.suppressLink(fromAsset.ID, toAsset.ID, kind, actor, s.now())
}

// RestoreDiscovered 撤销人工抑制，允许采集侧重新建立这条边（幂等）。
func (s *Service) RestoreDiscovered(from, to Ref, kind LinkKind) error {
	fromAsset, toAsset, err := s.resolveLinkPair(from, to, kind)
	if err != nil {
		return err
	}
	return s.store.clearLinkSuppression(fromAsset.ID, toAsset.ID, kind)
}

// SuppressedLinks 返回与某资产相关、被人工抑制掉的关联（供界面展示并可恢复）。
func (s *Service) SuppressedLinks(ref Ref) ([]SuppressedLink, error) {
	a, err := s.resolve(ref)
	if err != nil {
		return nil, err
	}
	return s.store.suppressedLinksOf(a.ID)
}

// Topology 返回以 ref 为中心、depth 跳以内的关系邻域。
//
// 资源范围在这里裁剪，而不是"取回来再过滤"：范围外的节点一旦进入结果集，
// 任何一处忘记过滤都会变成越权；让它们根本不出现，才是唯一稳妥的做法。
// allowedNodes 为 nil 表示全局（不过滤）；非 nil 时**归属节点为空**的资产也不可见——
// 与 nodeInScope 的取向一致：不知道属于哪台机器就不算在范围内。
//
// 中心资产本身不在范围内时返回 ErrOutOfScope（与标杆条件写同一语义：看不到就该 404）。
func (s *Service) Topology(ref Ref, depth, maxNodes int, allowedNodes []string) (Topology, error) {
	a, err := s.resolve(ref)
	if err != nil {
		return Topology{}, err
	}
	if allowedNodes != nil && !nodeAllowed(allowedNodes, a.Node) {
		return Topology{}, ErrOutOfScope
	}
	if depth <= 0 {
		depth = DefaultTopologyDepth
	}
	if depth > MaxTopologyDepth {
		depth = MaxTopologyDepth
	}
	if maxNodes <= 0 {
		maxNodes = DefaultTopologyNodes
	}
	if maxNodes > MaxTopologyNodes {
		maxNodes = MaxTopologyNodes
	}

	nodes, edges, truncated, err := s.store.topologyAround(a.ID, depth, maxNodes)
	if err != nil {
		return Topology{}, err
	}
	out := Topology{
		Root:      Ref{TypeKey: a.TypeKey, NaturalKey: a.NaturalKey},
		Depth:     depth,
		Truncated: truncated,
		Nodes:     make([]TopologyNode, 0, len(nodes)),
		Edges:     make([]TopologyEdge, 0, len(edges)),
	}
	visible := make(map[int64]bool, len(nodes))
	for _, n := range nodes {
		if allowedNodes != nil && !nodeAllowed(allowedNodes, n.Asset.Node) {
			continue
		}
		visible[n.Asset.ID] = true
		out.Nodes = append(out.Nodes, n)
	}
	for _, e := range edges {
		// 两端都可见这条边才成立：只留一半等于把"另一端是谁"（节点名、自然键）
		// 透给范围外的人，那正是范围裁剪要挡住的东西。
		if !visible[e.FromID] || !visible[e.ToID] {
			continue
		}
		out.Edges = append(out.Edges, e)
	}
	return out, nil
}

// nodeAllowed 判断资产归属节点是否在可见集合内（空归属对受限用户一律不可见）。
func nodeAllowed(allowed []string, node string) bool {
	if node == "" {
		return false
	}
	for _, n := range allowed {
		if n == node {
			return true
		}
	}
	return false
}

// resolveLinkPair 校验关联类型并把两端解析成资产，供上面几个方法共用。
func (s *Service) resolveLinkPair(from, to Ref, kind LinkKind) (Asset, Asset, error) {
	if !kind.Valid() {
		return Asset{}, Asset{}, fmt.Errorf("未知的资产关联类型: %q", kind)
	}
	fromAsset, err := s.resolve(from)
	if err != nil {
		return Asset{}, Asset{}, err
	}
	toAsset, err := s.resolve(to)
	if err != nil {
		return Asset{}, Asset{}, err
	}
	if fromAsset.ID == toAsset.ID {
		return Asset{}, Asset{}, errors.New("不能把资产关联到自身")
	}
	return fromAsset, toAsset, nil
}

// Links 返回与某资产直接相关的关联（出边与入边）。
func (s *Service) Links(ref Ref) ([]Link, error) {
	a, err := s.resolve(ref)
	if err != nil {
		return nil, err
	}
	return s.store.linksOf(a.ID)
}

// History 返回资产的变更记录（时间倒序）。limit<=0 使用默认上限。
func (s *Service) History(ref Ref, limit int) ([]ChangeRecord, error) {
	a, err := s.resolve(ref)
	if err != nil {
		return nil, err
	}
	return s.store.changesOf(a.ID, limit)
}

// Snapshot 抽取指定关注字段的当前生效值，存成一份配置快照。
//
// keys 为空时抽取全部生效字段；字段名去重且忽略空名。
func (s *Service) Snapshot(ref Ref, keys []string) (Snapshot, error) {
	a, err := s.resolve(ref)
	if err != nil {
		return Snapshot{}, err
	}
	want := map[string]bool{}
	for _, k := range keys {
		if k = strings.TrimSpace(k); k != "" {
			want[k] = true
		}
	}
	fields := map[string]string{}
	seen := map[string]bool{}
	for _, attr := range a.Attrs {
		if seen[attr.Key] {
			continue
		}
		if len(want) > 0 && !want[attr.Key] {
			continue
		}
		// 生效值：人工值优先（人工维护的就是期望值）。
		value, ok := a.Value(attr.Key)
		if !ok {
			continue
		}
		seen[attr.Key] = true
		fields[attr.Key] = value
	}
	at := s.now()
	id, err := s.store.recordSnapshot(a.ID, at, fields)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{ID: id, AssetID: a.ID, TakenAt: at, Fields: fields}, nil
}

// Snapshots 列出资产的快照头（时间倒序），不含字段内容。
func (s *Service) Snapshots(ref Ref, limit int) ([]Snapshot, error) {
	a, err := s.resolve(ref)
	if err != nil {
		return nil, err
	}
	return s.store.snapshotsOf(a.ID, limit)
}

// SnapshotFields 读取某快照的字段集合。
func (s *Service) SnapshotFields(snapshotID int64) (map[string]string, error) {
	return s.store.snapshotFieldsOf(snapshotID)
}

// resolve 把引用解析成已存在的资产；不存在时报明确错误，
// 避免调用方拿到一个「静默什么都不做」的成功。
func (s *Service) resolve(ref Ref) (Asset, error) {
	a, ok, err := s.store.assetByNatural(strings.TrimSpace(ref.TypeKey), normalizeKey(ref.NaturalKey))
	if err != nil {
		return Asset{}, err
	}
	if !ok {
		return Asset{}, fmt.Errorf("资产不存在: %s/%s", ref.TypeKey, ref.NaturalKey)
	}
	return a, nil
}

// ---- 差异巡检（inspect）----

// runtimeFields 是巡检默认排除的「运行态字段」。
//
// 巡检问的是「配置有没有变」；up / status / uptime 这类字段每次探活都可能翻转，
// 放进差异清单只会把真正的配置变更淹掉（它们已经有专门的告警与状态通道）。
var runtimeFields = map[string]bool{
	"up": true, "status": true, "uptime": true, "uptimeSeconds": true,
}

// InspectScope 是一次差异巡检的范围。
type InspectScope struct {
	// Filter 复用台账筛选（含资源范围下推）：巡检只覆盖调用方有权看到的资产。
	Filter ListFilter
	// Fields 是关注字段；为空表示「全部生效字段减去运行态字段」（见 runtimeFields）。
	Fields []string
}

// RunInspect 执行一次差异巡检：L2（与上一次快照比对）+ L3（与类型标杆比对）。
//
// 只给结论、不改任何东西（对齐既有"只读决策辅助"口径）：findings 是证据，
// 修复动作属于后续的配置下发能力。
func (s *Service) RunInspect(sc InspectScope, actor string) (InspectRun, error) {
	at := s.now()
	filter := sc.Filter

	// 资产上限：先数后取。超限时用分页版取前 N 个并显式标注 truncated。
	total, err := s.store.countAssets(filter)
	if err != nil {
		return InspectRun{}, err
	}
	truncated := total > maxInspectAssets
	var assets []Asset
	if truncated {
		filter.Limit, filter.Offset = maxInspectAssets, 0
		assets, err = s.store.listAssets(filter)
	} else {
		assets, err = s.store.listAssetsAll(filter)
	}
	if err != nil {
		return InspectRun{}, err
	}

	run := InspectRun{Scope: inspectScopeLabel(sc.Filter), Actor: actor, StartedAt: at, Truncated: truncated}
	findings := make([]InspectFinding, 0, 16)
	members := make([]InspectRunMember, 0, len(assets))
	for _, a := range assets {
		run.Assets++
		beforeAsset := len(findings)
		baselined := false
		cur := focusFields(a, sc.Fields)

		prev, hasPrev, err := s.store.latestSnapshot(a.ID)
		if err != nil {
			return InspectRun{}, err
		}
		// L2：与上一次快照比对。没有快照 = 数据不足（首次巡检）→ 只建立基线，不产出差异，
		// 「未知」不等于「不合规」（沿用对外状态页的既有语义）。
		baselineAdvanced := false
		if hasPrev {
			before := len(findings) // 只看本资产新增的差异，不能拿全局累计数判断
			for k, v := range cur {
				old, ok := prev.Fields[k]
				if !ok {
					findings = append(findings, newFinding(a, at, k, FindingAdded, FindingInfo, "", v))
					continue
				}
				if !sameValue(old, v) {
					findings = append(findings, newFinding(a, at, k, FindingChanged, FindingWarning, old, v))
				}
			}
			for k, v := range prev.Fields {
				if _, ok := cur[k]; !ok {
					findings = append(findings, newFinding(a, at, k, FindingMissing, FindingCritical, v, ""))
				}
			}
			baselineAdvanced = len(findings) > before
		} else {
			run.Baselined++
			baselined = true
		}

		// L3：与该资产类型的标杆比对（标杆资产不与自己比）。
		bl, hasBaseline, err := s.store.baselineOf(a.TypeKey)
		if err != nil {
			return InspectRun{}, err
		}
		baselineAllowed := hasBaseline && bl.AssetID != a.ID
		if baselineAllowed && sc.Filter.Nodes != nil {
			owner, found, err := s.store.assetByID(bl.AssetID)
			if err != nil {
				return InspectRun{}, err
			}
			baselineAllowed = found && slices.Contains(sc.Filter.Nodes, owner.Node)
		}
		if baselineAllowed {
			expected, err := s.store.snapshotFieldsOf(bl.SnapshotID)
			if err != nil {
				return InspectRun{}, err
			}
			for k, want := range expected {
				got, ok := cur[k]
				if ok && !sameValue(want, got) {
					finding := newFinding(a, at, k, FindingDeviation, FindingWarning, want, got)
					finding.BaselineAssetID = bl.AssetID
					findings = append(findings, finding)
				}
			}
		}

		// 快照只在「还没有」或「本资产自身字段真的变了」时推进：
		// 值没变时再存一份同样的快照不会提供任何新信息，只会把快照表刷大
		// （巡检可能比配置变化频繁得多）。
		if !hasPrev || baselineAdvanced {
			if _, err := s.store.recordSnapshot(a.ID, at, cur); err != nil {
				return InspectRun{}, err
			}
		}
		members = append(members, InspectRunMember{
			AssetID: a.ID, Node: a.Node, Baselined: baselined, Findings: len(findings) - beforeAsset,
		})
	}

	run.Findings = len(findings)
	runID, err := s.store.recordInspectRun(run, members, findings)
	if err != nil {
		return InspectRun{}, err
	}
	run.ID = runID
	return run, nil
}

// InspectRuns 列出巡检记录（时间倒序）。
func (s *Service) InspectRuns(limit int) ([]InspectRun, error) { return s.store.inspectRunsOf(limit) }

// InspectFindings 取某次巡检的差异项（严重级别高的排前面）。
func (s *Service) InspectFindings(runID int64, limit int) ([]InspectFinding, error) {
	if runID <= 0 {
		return nil, errors.New("巡检记录 ID 不能为空")
	}
	return s.store.findingsOf(runID, limit)
}

// Baselines 列出各资产类型当前的期望值来源（标杆资产）。
func (s *Service) Baselines() ([]Baseline, error) { return s.store.baselinesOf() }

// InspectRunsInNodes 按当前节点归属读取可见的巡检结果。
func (s *Service) InspectRunsInNodes(limit int, nodes []string) ([]InspectRun, error) {
	if nodes == nil {
		return s.InspectRuns(limit)
	}
	if len(nodes) == 0 {
		return []InspectRun{}, nil
	}
	return s.store.inspectRunsInNodes(limit, nodes)
}

// InspectFindingsInNodes 仅返回当前仍有权读取的差异项。
//
// 返回的 visible 表示「这条记录是否存在且对本调用者可见」：不存在与无权限都返回 false，
// 调用方据此统一回 404——否则全局用户对不存在的记录会拿到 200 + 空差异，
// 而受限用户拿到 404，同一接口两套语义，前端无法区分「空结果」与「记录不存在」。
func (s *Service) InspectFindingsInNodes(runID int64, limit int, nodes []string) ([]InspectFinding, bool, error) {
	if runID <= 0 {
		return nil, false, errors.New("巡检记录 ID 不能为空")
	}
	if nodes == nil {
		exists, err := s.store.inspectRunExists(runID)
		if err != nil || !exists {
			return nil, false, err
		}
		findings, err := s.InspectFindings(runID, limit)
		return findings, true, err
	}
	if len(nodes) == 0 {
		return nil, false, nil
	}
	return s.store.inspectFindingsInNodes(runID, limit, nodes)
}

// BaselinesInNodes 限定标杆资产当前可见的节点。
func (s *Service) BaselinesInNodes(nodes []string) ([]Baseline, error) {
	if nodes == nil {
		return s.Baselines()
	}
	if len(nodes) == 0 {
		return []Baseline{}, nil
	}
	return s.store.baselinesInNodes(nodes)
}

// BaselineForType 查询当前类型标杆，供操作前的资源范围检查。
func (s *Service) BaselineForType(typeKey string) (Baseline, bool, error) {
	return s.store.baselineOf(typeKey)
}

var ErrBaselineChanged = errors.New("标杆已变更")

// ErrOutOfScope 表示目标资产不在当前资源范围内。与 ErrBaselineChanged 区分开：
// 前者是「看不到」（应当 404），后者是「并发被换掉」（应当 409 让用户刷新重试）。
var ErrOutOfScope = errors.New("资产不在当前资源范围内")

// SetBaselineIfCurrent 仅当标杆未被并发替换时更新；0 表示尚未设置。
func (s *Service) SetBaselineIfCurrent(ref Ref, actor string, currentAssetID int64, nodes []string) (Baseline, error) {
	// 受限身份但一个可见节点都没有：条件写必然不命中。提前返回明确的范围错误，
	// 不能让它退化成 ErrBaselineChanged——那会让用户反复刷新重试一个永远不会成功的操作。
	if nodes != nil && len(nodes) == 0 {
		return Baseline{}, ErrOutOfScope
	}
	a, err := s.resolve(ref)
	if err != nil {
		return Baseline{}, err
	}
	id, err := s.store.recordSnapshot(a.ID, s.now(), focusFields(a, nil))
	if err != nil {
		return Baseline{}, err
	}
	b := Baseline{TypeKey: a.TypeKey, AssetID: a.ID, AssetKey: a.NaturalKey,
		SnapshotID: id, SetBy: actor, SetAt: s.now()}
	changed, err := s.store.setBaselineIfCurrent(b, currentAssetID, nodes)
	if err != nil {
		return Baseline{}, err
	}
	if !changed {
		// 条件写没有生效：回收刚建的那份快照。留着它会成为该资产的「最近一次快照」，
		// 既白占存储，又会让下一次巡检拿它当 L2 比对基准（用户什么都没改成，却多了个基准）。
		if derr := s.store.deleteSnapshot(id); derr != nil {
			slog.Warn("回收未被引用的巡检快照失败", "asset", a.ID, "snapshot", id, "err", derr)
		}
		return Baseline{}, ErrBaselineChanged
	}
	return b, nil
}

// ClearBaselineIfCurrent 避免标杆被并发替换后误删另一台资产的标杆。
func (s *Service) ClearBaselineIfCurrent(typeKey string, currentAssetID int64, nodes []string) error {
	if nodes != nil && len(nodes) == 0 {
		return ErrOutOfScope
	}
	changed, err := s.store.clearBaselineIfCurrent(typeKey, currentAssetID, nodes)
	if err != nil {
		return err
	}
	if !changed {
		return ErrBaselineChanged
	}
	return nil
}

// SetBaseline 把某资产的**当前**配置设为该资产类型的期望值（标杆）。
//
// 期望值的语义是「同类资产该长什么样」，所以这里抽的快照必须与巡检用的关注字段口径一致
// （同一 focusFields）：否则标杆里会带上 up 这类运行态字段，导致所有同类资产都"偏差"。
func (s *Service) SetBaseline(ref Ref, actor string) (Baseline, error) {
	a, err := s.resolve(ref)
	if err != nil {
		return Baseline{}, err
	}
	at := s.now()
	id, err := s.store.recordSnapshot(a.ID, at, focusFields(a, nil))
	if err != nil {
		return Baseline{}, err
	}
	b := Baseline{
		TypeKey: a.TypeKey, AssetID: a.ID, AssetKey: a.NaturalKey,
		SnapshotID: id, SetBy: actor, SetAt: at,
	}
	if err := s.store.setBaseline(b); err != nil {
		return Baseline{}, err
	}
	return b, nil
}

// ClearBaseline 清除某资产类型的标杆；本来没有也返回成功（幂等）。
func (s *Service) ClearBaseline(typeKey string) error {
	typeKey = strings.TrimSpace(typeKey)
	if typeKey == "" {
		return errors.New("资产类型不能为空")
	}
	return s.store.clearBaseline(typeKey)
}

// ---- 忽略（隐藏）与彻底删除 ----

// Ignore 把资产从台账隐藏（忽略），返回更新后的资产。
//
// 与删除的区别（重要）：忽略**不停止采集**，属性仍会继续刷新，因此恢复后看到的是最新状态；
// 而采集资产一旦删除会被下一轮上报重建——对它们正确的动作是忽略，不是删除。
// 忽略/恢复属管理动作，只记入操作审计（谁在什么时候隐藏了什么），不写字段级变更历史
// ——后者只记属性值的变化，混进来会让"这个字段从什么变成什么"的时间线失真。
func (s *Service) Ignore(ref Ref, actor, reason string) (Asset, error) {
	a, err := s.resolve(ref)
	if err != nil {
		return Asset{}, err
	}
	if err := s.store.ignoreAsset(a.ID, strings.TrimSpace(reason), actor, s.now()); err != nil {
		return Asset{}, err
	}
	return s.resolve(ref)
}

// Restore 解除忽略（幂等：本来没被忽略也返回成功），返回更新后的资产。
func (s *Service) Restore(ref Ref, actor string) (Asset, error) {
	a, err := s.resolve(ref)
	if err != nil {
		return Asset{}, err
	}
	if err := s.store.restoreAsset(a.ID); err != nil {
		return Asset{}, err
	}
	return s.resolve(ref)
}

// Purge 彻底删除资产（属性 / 关系 / 变更历史 / 快照一并删除，**不可恢复**）。
//
// 只允许「纯人工建档」资产：采集资产删掉后会被下一轮上报重建，表现为"删了又回来"，
// 会让人以为删除没生效——对它们应当用 Ignore。
// 返回值 baselineCleared 表示该资产原本是某资产类型的巡检标杆、已随之清除
// （否则标杆会指向一个不存在的资产，巡检时静默无结论）。
func (s *Service) Purge(ref Ref) (baselineCleared bool, err error) {
	a, err := s.resolve(ref)
	if err != nil {
		return false, err
	}
	if a.HasDiscovery() {
		return false, fmt.Errorf(
			"该资产由采集自动发现（%s），删除后会被下一轮上报重建；如需从台账隐藏请改用「忽略」",
			a.NaturalKey)
	}
	if bl, ok, err := s.store.baselineOf(a.TypeKey); err != nil {
		return false, err
	} else if ok && bl.AssetID == a.ID {
		if err := s.store.clearBaseline(a.TypeKey); err != nil {
			return false, err
		}
		baselineCleared = true
	}
	if err := s.store.deleteAsset(a.ID); err != nil {
		return false, err
	}
	return baselineCleared, nil
}

// labelField 是标签变更在变更历史里的字段名（`label:<key>`）：与属性变更共用一条时间线，
// "谁把这个资产的 env 从 test 改成 prod" 是配置问题里最常被追问的一句。
func labelField(key string) string { return "label:" + key }

// SetLabels 写入/覆盖标签，并删除 remove 中列出的键；返回更新后的资产。
func (s *Service) SetLabels(ref Ref, labels map[string]string, remove []string, actor string) (Asset, error) {
	a, err := s.resolve(ref)
	if err != nil {
		return Asset{}, err
	}
	clean := map[string]string{}
	for k, v := range labels {
		k = strings.TrimSpace(k)
		if k == "" {
			// 空键名会产出无法按值筛选的标签，而症状是"筛不出来"不是报错，必须在入口拦下
			return Asset{}, errors.New("标签键不能为空")
		}
		clean[k] = strings.TrimSpace(v)
	}
	for _, k := range remove {
		if _, dup := clean[strings.TrimSpace(k)]; dup {
			return Asset{}, fmt.Errorf("标签 %s 同时出现在写入与删除中", strings.TrimSpace(k))
		}
	}
	at := s.now()

	// 键名排序后再落库：map 遍历顺序随机，不排序会让同一批变更在时间线里顺序不定（测试也无法断言）。
	removeKeys := make([]string, 0, len(remove))
	for _, k := range remove {
		if k = strings.TrimSpace(k); k != "" {
			removeKeys = append(removeKeys, k)
		}
	}
	sort.Strings(removeKeys)
	for _, k := range removeKeys {
		old, exists := a.Labels[k]
		if !exists {
			continue // 本来就没有：不写变更记录（否则时间线会出现"从空到空"的噪声）
		}
		if err := s.store.removeLabel(a.ID, k); err != nil {
			return Asset{}, err
		}
		if err := s.store.appendChange(ChangeRecord{
			AssetID: a.ID, Field: labelField(k), Old: old, New: "",
			Source: SourceManual, Actor: actor, Kind: ChangeUpdate, At: at,
		}); err != nil {
			return Asset{}, err
		}
	}

	setKeys := make([]string, 0, len(clean))
	for k := range clean {
		setKeys = append(setKeys, k)
	}
	sort.Strings(setKeys)
	for _, k := range setKeys {
		value := clean[k]
		old, exists := a.Labels[k]
		if exists && sameValue(old, value) {
			continue // 值没变：不写库、不写变更记录
		}
		if err := s.store.setLabel(a.ID, k, value, at, actor); err != nil {
			return Asset{}, err
		}
		oldValue := ""
		if exists {
			oldValue = old
		}
		if err := s.store.appendChange(ChangeRecord{
			AssetID: a.ID, Field: labelField(k), Old: oldValue, New: value,
			Source: SourceManual, Actor: actor, Kind: ChangeUpdate, At: at,
		}); err != nil {
			return Asset{}, err
		}
	}
	return s.resolve(ref)
}

// focusFields 抽取资产的「关注字段集合」（生效值，人工优先）。
//
// fields 非空时按它裁剪；为空时取全部生效字段但排除运行态字段（见 runtimeFields）。
func focusFields(a Asset, fields []string) map[string]string {
	want := map[string]bool{}
	for _, k := range fields {
		if k = strings.TrimSpace(k); k != "" {
			want[k] = true
		}
	}
	pick := len(want) > 0
	out := map[string]string{}
	for _, attr := range a.Attrs {
		key := attr.Key
		if _, done := out[key]; done {
			continue
		}
		if pick {
			if !want[key] {
				continue
			}
		} else if runtimeFields[key] {
			continue
		}
		v, ok := a.Value(key)
		if !ok {
			continue
		}
		out[key] = v
	}
	return out
}

// newFinding 构造一条差异项，冗余带上资产身份（证据要在资产被删后仍可解读）。
func newFinding(a Asset, at int64, field string, kind FindingKind, level FindingLevel, expected, actual string) InspectFinding {
	return InspectFinding{
		AssetID: a.ID, AssetType: a.TypeKey, AssetKey: a.NaturalKey, AssetName: a.Name, Node: a.Node,
		Field: field, Kind: kind, Level: level, Expected: expected, Actual: actual, At: at,
	}
}

// sameValue 用与 Apply 相同的规范化口径比较（忽略首尾空白与大小写）：
// 否则巡检会把「仅大小写不同的写入」报成变更，与变更历史的口径自相矛盾。
func sameValue(x, y string) bool { return normalizeValue(x) == normalizeValue(y) }

// inspectScopeLabel 生成人能读懂的巡检范围描述（记录里要能回答"这次检的是哪些"）。
func inspectScopeLabel(f ListFilter) string {
	parts := make([]string, 0, 3)
	if f.TypeKey != "" {
		parts = append(parts, "type:"+f.TypeKey)
	}
	if f.Node != "" {
		parts = append(parts, "node:"+f.Node)
	}
	if f.Keyword != "" {
		parts = append(parts, "q:"+f.Keyword)
	}
	if len(parts) > 0 {
		return strings.Join(parts, " ")
	}
	if f.Nodes != nil {
		// 受限用户：范围由资源范围决定，界面上说明"我的范围内"比写 all 更诚实
		return "scope:mine"
	}
	return "all"
}

func newAttr(key, value string, src Source, at int64, actor string) Attr {
	return Attr{
		Key: strings.TrimSpace(key), Value: value, Source: src,
		UpdatedAt: at, UpdatedBy: actor,
	}
}
