package alert

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
	"gopkg.in/yaml.v3"
)

// captureNotifier 记录 NotifyGroup / Notify 收到的内容，用于验证收敛在派发层的效果。
type captureNotifier struct {
	channel string
	mu      sync.Mutex
	groups  [][]model.AlertEvent
	singles []model.AlertEvent
}

func (c *captureNotifier) Channel() string { return c.channel }

func (c *captureNotifier) Notify(ev model.AlertEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.singles = append(c.singles, ev)
	return nil
}

func (c *captureNotifier) NotifyGroup(events []model.AlertEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.groups = append(c.groups, events)
	return nil
}

func (c *captureNotifier) batches() [][]model.AlertEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]model.AlertEvent(nil), c.groups...)
}

// stormEvents 生成 n 条「同规则、不同节点」的 critical 告警——典型的风暴形态。
func stormEvents(n int) []model.AlertEvent {
	out := make([]model.AlertEvent, 0, n)
	for i := 0; i < n; i++ {
		node := "node-" + strconv.Itoa(i)
		out = append(out, model.AlertEvent{
			ID:        "e-" + node,
			RuleID:    "r-disk",
			RuleName:  "磁盘使用率过高",
			Node:      node,
			Instance:  "disk_used_percent",
			Metric:    "disk_used_percent",
			Severity:  model.SeverityCritical,
			State:     model.AlertStateFiring,
			Value:     92.4,
			Operator:  ">",
			Threshold: 90,
			StartsAt:  int64(1000 + i),
			Message:   "节点 " + node + " 磁盘使用率 92.40% 超过阈值 90.00%",
			Notify:    []string{"dingtalk"},
		})
	}
	return out
}

// TestConvergeEvents_SingleEventUnchanged 0/1 条时行为与改造前完全一致。
func TestConvergeEvents_SingleEventUnchanged(t *testing.T) {
	if got := convergeEvents(nil, 5, nil); len(got) != 0 {
		t.Fatalf("空切片应原样返回，got %d", len(got))
	}
	one := stormEvents(1)
	got := convergeEvents(one, 5, []string{"不应出现"})
	if len(got) != 1 {
		t.Fatalf("单条不应收敛，got %d", len(got))
	}
	if got[0].Message != one[0].Message {
		t.Fatal("单条不应改写消息")
	}
	if strings.Contains(got[0].Message, "告警收敛") {
		t.Fatal("单条不应追加摘要")
	}
}

// TestConvergeEvents_HeadThenSummary 收敛后的结构：头部为最早触发、正文含计数与折叠说明。
func TestConvergeEvents_HeadThenSummary(t *testing.T) {
	out := convergeEvents(stormEvents(50), 5, nil)
	if len(out) != 1 {
		t.Fatalf("收敛结果应为 1 条，got %d", len(out))
	}
	if out[0].Node != "node-0" {
		t.Fatalf("同级应取最早触发的一条为头部，got %s", out[0].Node)
	}
	msg := out[0].Message
	for _, want := range []string{
		"节点 node-0 磁盘使用率 92.40% 超过阈值 90.00%", // 头部原始描述必须保留
		"【告警收敛】共 50 条告警合并为 1 条",
		"规则：磁盘使用率过高（共 1 类）",
		"范围：50 台主机",
		"级别：critical 50",
		"最高：critical",
		"Top 5：",
		"1. [critical] node-0 disk_used_percent disk_used_percent 92.4 > 90",
		"5. [critical] node-4 disk_used_percent disk_used_percent 92.4 > 90",
		"其余 45 条已折叠",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("收敛摘要缺少 %q\n---\n%s", want, msg)
		}
	}
}

// TestRulesBrief 规则名摘要最多列 3 个，其余折叠。
func TestRulesBrief(t *testing.T) {
	if got := rulesBrief([]string{"a", "b"}); got != "a、b" {
		t.Fatalf("rulesBrief = %q", got)
	}
	if got := rulesBrief([]string{"a", "b", "c", "d"}); got != "a、b、c 等" {
		t.Fatalf("rulesBrief = %q", got)
	}
	if got := rulesBrief(nil); got != "" {
		t.Fatalf("rulesBrief(nil) = %q", got)
	}
}

// TestConvergeEvents_HeadPrefersSeverity 头部优先取更高级别，而非最早触发。
func TestConvergeEvents_HeadPrefersSeverity(t *testing.T) {
	events := []model.AlertEvent{
		{Node: "a", RuleName: "r", Severity: model.SeverityInfo, StartsAt: 100},
		{Node: "b", RuleName: "r", Severity: model.SeverityCritical, StartsAt: 900},
		{Node: "c", RuleName: "r", Severity: model.SeverityWarning, StartsAt: 200},
	}
	out := convergeEvents(events, 5, nil)
	if len(out) != 1 || out[0].Node != "b" {
		t.Fatalf("头部应为 critical 的 b，got %+v", out)
	}
	msg := out[0].Message
	if !strings.Contains(msg, "critical 1") || !strings.Contains(msg, "warning 1") || !strings.Contains(msg, "info 1") {
		t.Fatalf("级别分布统计缺失：%s", msg)
	}
}

// TestConvergeEvents_HeadCountClamped headCount 越界时收敛到 [1, maxHeadCount]。
func TestConvergeEvents_HeadCountClamped(t *testing.T) {
	out := convergeEvents(stormEvents(25), 999, nil)
	if !strings.Contains(out[0].Message, "Top 20：") {
		t.Fatalf("headCount 应被夹到 20：%s", out[0].Message)
	}
	if !strings.Contains(out[0].Message, "其余 5 条已折叠") {
		t.Fatalf("折叠计数不符：%s", out[0].Message)
	}

	out = convergeEvents(stormEvents(25), 0, nil)
	if !strings.Contains(out[0].Message, "Top 5：") {
		t.Fatalf("headCount=0 应回落默认 5：%s", out[0].Message)
	}
}

// TestConvergeEvents_AppendsNotes 关联结论以追加方式写入头部正文。
func TestConvergeEvents_AppendsNotes(t *testing.T) {
	notes := []string{"node-0：磁盘将满，关联 2 个中间件实例不可用", "node-1：内存水位同步升高"}
	out := convergeEvents(stormEvents(3), 3, notes)
	msg := out[0].Message
	if !strings.Contains(msg, "关联结论：") {
		t.Fatalf("缺少关联结论段：%s", msg)
	}
	for _, n := range notes {
		if !strings.Contains(msg, "- "+n) {
			t.Errorf("缺少结论 %q\n%s", n, msg)
		}
	}
	if strings.Index(msg, "【告警收敛】") > strings.Index(msg, "关联结论：") {
		t.Fatal("关联结论应位于摘要之后")
	}
}

// TestConvergeEvents_DoesNotMutateInput 收敛不得修改传入切片与其中的事件（调用方仍持有原数据）。
func TestConvergeEvents_DoesNotMutateInput(t *testing.T) {
	events := stormEvents(10)
	before := make([]string, len(events))
	for i, ev := range events {
		before[i] = ev.Message
	}

	_ = convergeEvents(events, 3, []string{"n"})

	for i, ev := range events {
		if ev.Message != before[i] {
			t.Fatalf("第 %d 条事件被就地修改：%s", i, ev.Message)
		}
	}
	if events[0].Node != "node-0" {
		t.Fatalf("入参顺序被改变：%s", events[0].Node)
	}
}

// TestSeverityBrief 级别摘要：已知级别按严重度降序，未知级别补末尾且不丢计数。
func TestSeverityBrief(t *testing.T) {
	got := severityBrief(map[string]int{"info": 2, "critical": 3, "warning": 1})
	if got != "critical 3 / warning 1 / info 2" {
		t.Fatalf("severityBrief = %q", got)
	}
	got = severityBrief(map[string]int{"custom": 1, "critical": 2})
	if got != "critical 2 / custom 1" {
		t.Fatalf("未知级别应补在末尾：%q", got)
	}
	if got := severityBrief(nil); got != "" {
		t.Fatalf("空统计应返回空串，got %q", got)
	}
}

// TestEventBrief 明细行格式化：节点 + 实例 + 指标 + 阈值比较。
func TestEventBrief(t *testing.T) {
	got := eventBrief(model.AlertEvent{
		Severity: model.SeverityWarning, Node: "web-01", Instance: "disk_used_percent",
		Metric: "disk_used_percent", Value: 92.4, Operator: ">", Threshold: 90,
	})
	want := "[warning] web-01 disk_used_percent disk_used_percent 92.4 > 90"
	if got != want {
		t.Fatalf("eventBrief = %q, want %q", got, want)
	}
	// 无阈值比较时不应残留占位
	got = eventBrief(model.AlertEvent{Severity: model.SeverityInfo, Node: "web-01"})
	if got != "[info] web-01" {
		t.Fatalf("eventBrief = %q", got)
	}
}

// TestGrouper_ClusterKeyConvergesAcrossNodes 收敛维度的核心价值：
// groupBy 含 host（按主机分别通知）时，开启收敛后同规则不同节点必须合并为同一组。
func TestGrouper_ClusterKeyConvergesAcrossNodes(t *testing.T) {
	g := NewGrouper([]string{"name", "host"}, time.Minute, 5*time.Minute, nil)
	critical := model.AlertEvent{RuleID: "r-disk", RuleName: "磁盘使用率过高", Node: "web-01", Severity: model.SeverityCritical}
	otherNode := critical
	otherNode.Node = "db-01"
	warning := critical
	warning.Node = "db-01"
	warning.Severity = model.SeverityWarning

	if g.clusterKey(critical) == g.clusterKey(otherNode) {
		t.Fatal("未开启收敛时不应跨节点合并")
	}

	g.SetConverge(true, []string{"rule", "severity"}, 10*time.Minute)
	if g.clusterKey(critical) != g.clusterKey(otherNode) {
		t.Fatal("开启收敛后应按 rule+severity 跨节点合并")
	}
	if g.clusterKey(critical) == g.clusterKey(warning) {
		t.Fatal("不同级别不应合并（避免 critical 被 info 稀释）")
	}
	// 未开启收敛时 clusterKey 必须与 key 完全一致（零行为变化）
	g.SetConverge(false, nil, 0)
	if g.clusterKey(critical) != g.key(critical) {
		t.Fatal("未开启收敛时 clusterKey 应等于 key")
	}
}

// TestGrouper_WindowRolloverEmitsPreviousGeneration 超过时间窗后开启新一代，
// 旧代立即（异步）发出，避免几小时前的告警与新风暴混在一条通知里。
func TestGrouper_WindowRolloverEmitsPreviousGeneration(t *testing.T) {
	var mu sync.Mutex
	var batches [][]model.AlertEvent
	g := NewGrouper([]string{"name"}, time.Hour, time.Hour, func(events []model.AlertEvent) {
		mu.Lock()
		defer mu.Unlock()
		batches = append(batches, events)
	})
	g.SetConverge(true, []string{"rule"}, 40*time.Millisecond)

	g.Add(stormEvents(2)[0])
	g.Add(stormEvents(2)[1])
	time.Sleep(60 * time.Millisecond)
	g.Add(stormEvents(1)[0]) // 触发换代

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(batches)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(batches) != 1 {
		t.Fatalf("换代应发出 1 批，got %d", len(batches))
	}
	if len(batches[0]) != 2 {
		t.Fatalf("旧代应含 2 条，got %d", len(batches[0]))
	}
}

// TestGroupingConfigNormalize_Defaults 默认值补齐与上限夹取（load 与 Save 共用）。
func TestGroupingConfigNormalize_Defaults(t *testing.T) {
	var cfg GroupingConfig
	cfg.normalize()
	if len(cfg.GroupBy) != 1 || cfg.GroupBy[0] != "name" {
		t.Fatalf("GroupBy 默认值缺失: %v", cfg.GroupBy)
	}
	if cfg.GroupWait != "30s" || cfg.GroupInterval != "5m" {
		t.Fatalf("时间默认值缺失: %q / %q", cfg.GroupWait, cfg.GroupInterval)
	}
	if len(cfg.ConvergeBy) != 2 || cfg.ConvergeBy[0] != "rule" || cfg.ConvergeBy[1] != "severity" {
		t.Fatalf("收敛维度默认值缺失: %v", cfg.ConvergeBy)
	}
	if cfg.ConvergeWindow != "10m" || cfg.HeadCount != 5 {
		t.Fatalf("收敛窗口/明细数默认值缺失: %q / %d", cfg.ConvergeWindow, cfg.HeadCount)
	}
	// 三态语义：未配置 → 默认开启；显式 false → 关闭（见 TestGroupingConfig_ConvergeTriState）
	if !cfg.ConvergeEnabled() {
		t.Fatal("收敛默认必须为开启（开启分组即默认收敛）")
	}
	if cfg.Converge == nil {
		t.Fatal("normalize 后应收敛开关物化为显式值，供接口返回与落盘使用")
	}

	cfg = GroupingConfig{HeadCount: 999}
	cfg.normalize()
	if cfg.HeadCount != maxHeadCount {
		t.Fatalf("HeadCount 应夹到 %d，got %d", maxHeadCount, cfg.HeadCount)
	}
}

// TestSetGrouping_NormalizesAndRebuilds 热更新：补齐默认值、按开关重建分组器、注入收敛参数。
func TestSetGrouping_NormalizesAndRebuilds(t *testing.T) {
	e := &Engine{
		rules:  &RulesStore{rules: map[string]model.AlertRule{}},
		states: map[string]*ruleState{},
		firing: map[string]*firingEntry{},
	}

	// 只提交两个字段，其余应被补齐（否则会出现「保存成功但参数回落默认」的隐性偏差）
	e.SetGrouping(GroupingConfig{Enabled: true, Converge: boolPtr(true)})
	if e.grouper == nil {
		t.Fatal("启用后应重建分组器")
	}
	cfg := e.grouping.Get()
	if cfg.GroupWait != "30s" || cfg.GroupInterval != "5m" || cfg.HeadCount != 5 || cfg.ConvergeWindow != "10m" {
		t.Fatalf("默认值未补齐: %+v", cfg)
	}
	if !e.grouper.converge || len(e.grouper.convergeBy) != 2 {
		t.Fatalf("收敛参数未注入分组器: converge=%v by=%v", e.grouper.converge, e.grouper.convergeBy)
	}
	if e.grouper.window != 10*time.Minute {
		t.Fatalf("收敛窗口未注入: %v", e.grouper.window)
	}

	e.SetGrouping(GroupingConfig{Enabled: false})
	if e.grouper != nil {
		t.Fatal("禁用后分组器应清空")
	}
}

// TestFlushGroup_ConvergeOffIsPassthrough 兼容性关键用例：收敛关闭时通知内容与改造前完全一致。
func TestFlushGroup_ConvergeOffIsPassthrough(t *testing.T) {
	n := &captureNotifier{channel: "dingtalk"}
	e := &Engine{
		rules:     &RulesStore{rules: map[string]model.AlertRule{}},
		notifiers: []Notifier{n},
		grouping:  &GroupingStore{cfg: GroupingConfig{Converge: boolPtr(false)}},
	}
	e.flushGroup(stormEvents(5))

	batches := n.batches()
	if len(batches) != 1 || len(batches[0]) != 5 {
		t.Fatalf("收敛关闭时应原样派发 5 条，got %d 批 / %d 条", len(batches), len(batches[0]))
	}
	if strings.Contains(batches[0][0].Message, "告警收敛") {
		t.Fatal("收敛关闭时不应追加摘要")
	}
}

// TestFlushGroup_ConvergePerChannel 收敛在渠道维度内进行：
// 每个渠道各自收敛成一条，且互不吞并（渠道泛化不能丢）。
func TestFlushGroup_ConvergePerChannel(t *testing.T) {
	ding := &captureNotifier{channel: "dingtalk"}
	mail := &captureNotifier{channel: "email"}
	e := &Engine{
		rules:     &RulesStore{rules: map[string]model.AlertRule{}},
		notifiers: []Notifier{ding, mail},
		grouping: &GroupingStore{cfg: GroupingConfig{
			Converge: boolPtr(true), HeadCount: 2, ConvergeWindow: "10m",
		}},
	}

	events := stormEvents(4)
	events[3].Notify = []string{"email"} // 一条走邮件
	e.flushGroup(events)

	db := ding.batches()
	if len(db) != 1 || len(db[0]) != 1 {
		t.Fatalf("dingtalk 应收到 1 批 1 条（已收敛），got %d 批", len(db))
	}
	if !strings.Contains(db[0][0].Message, "共 3 条告警合并为 1 条") {
		t.Fatalf("dingtalk 应只收敛自己渠道的 3 条：%s", db[0][0].Message)
	}
	mb := mail.batches()
	if len(mb) != 1 || len(mb[0]) != 1 {
		t.Fatalf("email 应收到 1 批 1 条，got %d 批", len(mb))
	}
	if strings.Contains(mb[0][0].Message, "【告警收敛】") {
		t.Fatalf("该渠道只有 1 条时不应追加收敛摘要：%s", mb[0][0].Message)
	}
	if !strings.Contains(mb[0][0].Message, "node-3") {
		t.Fatalf("email 应原样收到自己的事件：%s", mb[0][0].Message)
	}
}

// TestFlushGroup_ConvergeAttachesCorrelationNotes 注入关联结论提供者后，摘要附注其结论。
func TestFlushGroup_ConvergeAttachesCorrelationNotes(t *testing.T) {
	n := &captureNotifier{channel: "dingtalk"}
	e := &Engine{
		rules:     &RulesStore{rules: map[string]model.AlertRule{}},
		notifiers: []Notifier{n},
		grouping:  &GroupingStore{cfg: GroupingConfig{Converge: boolPtr(true), HeadCount: 5}},
	}
	e.SetCorrelationProvider(fakeCorrelator{notes: map[string][]string{
		"node-0": {"磁盘将满，关联 2 个中间件实例不可用"},
	}})

	e.flushGroup(stormEvents(3))

	batches := n.batches()
	if len(batches) != 1 {
		t.Fatalf("应收到 1 批，got %d", len(batches))
	}
	msg := batches[0][0].Message
	if !strings.Contains(msg, "关联结论：") || !strings.Contains(msg, "node-0：磁盘将满") {
		t.Fatalf("关联结论未附注：%s", msg)
	}
}

// TestCorrelationNotes_LimitsAndDedup 结论收集：节点去重、节点数与条数均有上限。
func TestCorrelationNotes_LimitsAndDedup(t *testing.T) {
	e := &Engine{}
	e.SetCorrelationProvider(fakeCorrelator{notes: map[string][]string{
		"node-0": {"n0-a", "n0-b", "n0-c"},
		"node-1": {"n1-a"},
		"node-2": {"n2-a"},
		"node-3": {"n3-a"}, // 第四个节点不应被取用
	}})

	events := stormEvents(4)
	notes := e.correlationNotes(events)
	// node-0 贡献 3 条，node-1 / node-2 各 1 条 = 5 条；node-3 因节点数上限未被取用
	if len(notes) != 5 {
		t.Fatalf("结论收集结果不符，got %d：%v", len(notes), notes)
	}
	if len(notes) > convergeMaxNotes {
		t.Fatalf("结论条数不得超过 %d，got %d", convergeMaxNotes, len(notes))
	}
	for _, n := range notes {
		if strings.HasPrefix(n, "node-3：") {
			t.Fatalf("超过 %d 个节点不应再取结论：%v", convergeMaxNoteNodes, notes)
		}
	}

	// 重复节点只取一次
	dup := []model.AlertEvent{events[0], events[0], events[0]}
	if got := e.correlationNotes(dup); len(got) != 3 {
		t.Fatalf("同一节点应只取一次结论，got %v", got)
	}

	// 未注入提供者时返回空
	bare := &Engine{}
	if got := bare.correlationNotes(events); len(got) != 0 {
		t.Fatalf("未注入提供者应返回空，got %v", got)
	}
}

// TestGroupingConfig_ConvergeTriState 收敛开关的三态语义：
// 字段缺失 = 默认开启；显式 false = 关闭，且不得被默认值翻回。
func TestGroupingConfig_ConvergeTriState(t *testing.T) {
	// YAML：未写 converge → 默认开启
	var missing GroupingConfig
	if err := yaml.Unmarshal([]byte("enabled: true\n"), &missing); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	missing.normalize()
	if !missing.ConvergeEnabled() {
		t.Fatal("YAML 未写 converge 字段时必须默认开启")
	}

	// YAML：显式 false → 保持关闭
	var fromYAML GroupingConfig
	if err := yaml.Unmarshal([]byte("enabled: true\nconverge: false\n"), &fromYAML); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	fromYAML.normalize()
	if fromYAML.ConvergeEnabled() {
		t.Fatal("YAML 中显式 converge: false 必须保持关闭")
	}

	// 接口路径：JSON 未带字段 → 默认开启；显式 false → 关闭
	var fromJSON GroupingConfig
	if err := json.Unmarshal([]byte(`{"enabled":true}`), &fromJSON); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !fromJSON.ConvergeEnabled() {
		t.Fatal("接口未提交 converge 时必须默认开启")
	}
	var jsonOff GroupingConfig
	if err := json.Unmarshal([]byte(`{"enabled":true,"converge":false}`), &jsonOff); err != nil {
		t.Fatalf("json: %v", err)
	}
	if jsonOff.ConvergeEnabled() {
		t.Fatal("接口显式提交 converge:false 必须保持关闭")
	}

	// 默认配置字段齐全（前端据此渲染表单），但分组本身仍需显式开启
	def := DefaultGroupingConfig()
	if def.Converge == nil || !def.ConvergeEnabled() || def.HeadCount != defaultHeadCount || len(def.ConvergeBy) != 2 {
		t.Fatalf("默认配置不完整：%+v", def)
	}
	if def.Enabled {
		t.Fatal("默认配置不应启用分组")
	}
}

// boolPtr 便于在用例中构造三态开关。
func boolPtr(b bool) *bool { return &b }

// fakeCorrelator 是 CorrelationProvider 的测试替身。
type fakeCorrelator struct {
	notes map[string][]string
}

func (f fakeCorrelator) CorrelationNotes(node string) []string { return f.notes[node] }
