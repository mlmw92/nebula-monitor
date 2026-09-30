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
	Keyword string
	Limit   int
	Offset  int
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
