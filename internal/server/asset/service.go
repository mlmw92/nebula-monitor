package asset

import (
	"errors"
	"fmt"
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
)

// ListFilter 是资产列表的查询条件。零值表示「全部资产」，但始终受默认分页约束。
type ListFilter struct {
	TypeKey string
	Node    string
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
	Limit       int
	Offset      int
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

// GetByID 按主键取资产。
func (s *Service) GetByID(id int64) (Asset, bool, error) { return s.store.assetByID(id) }

// List 按条件分页列出资产。
func (s *Service) List(f ListFilter) ([]Asset, error) { return s.store.listAssets(f) }

// Count 返回符合条件的资产总数（忽略 Limit/Offset），供列表接口做分页。
//
// 与 List 用同一套条件（含 Nodes 资源范围下推），保证「总数」与「能翻到的条数」一致。
func (s *Service) Count(f ListFilter) (int, error) { return s.store.countAssets(f) }

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

// Link 建立资产关联（幂等：重复建立不报错、不产生重复边）。
func (s *Service) Link(from, to Ref, kind LinkKind) error {
	if !kind.Valid() {
		return fmt.Errorf("未知的资产关联类型: %q", kind)
	}
	fromAsset, err := s.resolve(from)
	if err != nil {
		return err
	}
	toAsset, err := s.resolve(to)
	if err != nil {
		return err
	}
	if fromAsset.ID == toAsset.ID {
		return errors.New("不能把资产关联到自身")
	}
	return s.store.linkAssets(fromAsset.ID, toAsset.ID, kind, s.now())
}

// Unlink 解除资产关联（幂等）。
func (s *Service) Unlink(from, to Ref, kind LinkKind) error {
	fromAsset, err := s.resolve(from)
	if err != nil {
		return err
	}
	toAsset, err := s.resolve(to)
	if err != nil {
		return err
	}
	return s.store.unlinkAssets(fromAsset.ID, toAsset.ID, kind)
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

func newAttr(key, value string, src Source, at int64, actor string) Attr {
	return Attr{
		Key: strings.TrimSpace(key), Value: value, Source: src,
		UpdatedAt: at, UpdatedBy: actor,
	}
}
