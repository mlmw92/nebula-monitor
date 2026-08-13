package security

import (
	"encoding/json"
	"log/slog"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// 安全事件与基线的服务端存储：内存索引 + JSON 文件持久化。
// 安全数据为低基数结构化数据，不进入高基数时序库；按节点保存最新基线，
// 事件列表设容量上限，防止内存膨胀。

const (
	// maxEvents 内存中保留的最大安全事件条数。
	maxEvents = 2000
	// defaultStoreFile 默认持久化文件名。
	defaultStoreFile = "security_store.json"
)

// Store 安全事件/基线存储。
type Store struct {
	mu       sync.RWMutex
	events   []model.SecurityEvent            // 全量事件（按时间倒序追加，裁剪到 maxEvents）
	baseline map[string]model.SecurityBaseline // node -> 最新基线
	path     string                            // 持久化文件路径
}

// New 创建安全存储；若 file 为空使用默认文件名。加载已持久化数据（若存在）。
func New(file string) *Store {
	if file == "" {
		file = defaultStoreFile
	}
	s := &Store{
		baseline: map[string]model.SecurityBaseline{},
		path:     file,
	}
	s.load()
	return s
}

// load 从磁盘载入既有事件与基线。
func (s *Store) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var snap struct {
		Events   []model.SecurityEvent            `json:"events"`
		Baseline map[string]model.SecurityBaseline `json:"baseline"`
	}
	if err := json.Unmarshal(data, &snap); err != nil {
		slog.Warn("安全存储加载失败，忽略旧数据", "path", s.path, "err", err)
		return
	}
	if snap.Events != nil {
		s.events = snap.Events
	}
	if snap.Baseline != nil {
		s.baseline = snap.Baseline
	}
	slog.Info("已加载安全存储", "events", len(s.events), "baselines", len(s.baseline))
}

// save 持久化事件与基线到磁盘。失败时仅记日志，不阻塞上报链路。
func (s *Store) save() {
	snap := struct {
		Events   []model.SecurityEvent            `json:"events"`
		Baseline map[string]model.SecurityBaseline `json:"baseline"`
	}{
		Events:   s.events,
		Baseline: s.baseline,
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		slog.Warn("安全存储序列化失败", "err", err)
		return
	}
	// 原子写入：先写临时文件再 rename
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		slog.Warn("安全存储写入失败", "err", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		slog.Warn("安全存储落盘失败", "err", err)
	}
}

// Ingest 接收来自某节点的安全事件与基线，更新内存索引并持久化。
// 事件先按 ID 去重（避免同一条事件重复入库），基线直接覆盖为最新。
func (s *Store) Ingest(node string, events []model.SecurityEvent, baseline *model.SecurityBaseline) {
	if len(events) == 0 && baseline == nil {
		return
	}
	s.mu.Lock()
	// 事件去重：已存在的 ID 跳过
	seen := make(map[string]struct{}, len(s.events))
	for _, e := range s.events {
		seen[e.ID] = struct{}{}
	}
	for _, e := range events {
		if e.Node == "" {
			e.Node = node
		}
		if _, dup := seen[e.ID]; dup {
			continue
		}
		seen[e.ID] = struct{}{}
		s.events = append(s.events, e)
	}
	// 裁剪到最大容量（保留最新）
	if len(s.events) > maxEvents {
		s.events = s.events[len(s.events)-maxEvents:]
	}
	if baseline != nil {
		if baseline.Node == "" {
			baseline.Node = node
		}
		s.baseline[baseline.Node] = *baseline
	}
	s.mu.Unlock()
	s.save()
}

// Events 返回安全事件列表（按时间倒序），支持按分类/节点筛选与数量上限。
func (s *Store) Events(limit int, category, node string) []model.SecurityEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.SecurityEvent, 0, len(s.events))
	for i := len(s.events) - 1; i >= 0; i-- {
		e := s.events[i]
		if category != "" && e.Category != category {
			continue
		}
		if node != "" && e.Node != node {
			continue
		}
		out = append(out, e)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// Baselines 返回各节点最新基线（按评分升序，风险最高在前）。
func (s *Store) Baselines() []model.SecurityBaseline {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.SecurityBaseline, 0, len(s.baseline))
	for _, b := range s.baseline {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Node < out[j].Node
		}
		return out[i].Score < out[j].Score
	})
	return out
}

// Summary 汇总安全态势：合规评分均值、事件总数、风险主机数、FIM 变化数。
func (s *Store) Summary() SecuritySummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var nodes = map[string]struct{}{}
	var totalScore float64
	for _, b := range s.baseline {
		nodes[b.Node] = struct{}{}
		totalScore += b.Score
	}
	var fimChanges int
	var riskNodes = map[string]struct{}{}
	for _, e := range s.events {
		switch e.Category {
		case model.SecurityCatFIM:
			fimChanges++
		}
		// 关键事件（critical/warning）视为风险主机
		if e.Severity == model.SeverityCritical || e.Severity == model.SeverityWarning {
			riskNodes[e.Node] = struct{}{}
		}
	}
	scoreAvg := 0.0
	if len(s.baseline) > 0 {
		scoreAvg = totalScore / float64(len(s.baseline))
	}
	return SecuritySummary{
		Score:        scoreAvg,
		EventCount:   len(s.events),
		RiskNodes:    len(riskNodes),
		FIMChanges:   fimChanges,
		BaselineHosts: len(nodes),
		GeneratedAt:  time.Now().UnixMilli(),
	}
}

// SecuritySummary 安全态势概览。
type SecuritySummary struct {
	Score         float64 `json:"score"`         // 合规评分均值
	EventCount    int     `json:"eventCount"`    // 安全事件总数
	RiskNodes     int     `json:"riskNodes"`     // 风险主机数（有关键事件）
	FIMChanges    int     `json:"fimChanges"`    // FIM 变化数
	BaselineHosts int     `json:"baselineHosts"` // 已上报基线的主机数
	GeneratedAt   int64   `json:"generatedAt"`   // 统计时间（毫秒）
}
