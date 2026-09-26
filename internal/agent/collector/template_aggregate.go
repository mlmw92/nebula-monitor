package collector

import (
	"log/slog"
	"regexp"
	"sort"
	"strings"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/template"
)

// 本文件处理模板产出里最容易静默出错的一件事：**多条序列塌缩成同一序列**。
//
// 背景：像 RabbitMQ 这类中间件按维度暴露指标（rabbitmq_queue_messages{queue="a"}、
// {queue="b"}、……）。用户想「汇总所有队列」时最自然的写法是 unlabel: ["queue"]，
// 但那会得到 N 条「同名 + 同标签 + 不同值」的序列，写进时序库后是 last-write-wins
// （谁最后写谁生效），数值无意义且完全不报错——静默的数据损坏。
//
// 因此这里给两道处理：
//  1. 显式聚合（rules.aggregate）：声明「这些指标按 sum/max/min/avg 合并」后语义明确，
//     也才真的能表达「所有队列的消息总数」这类需求；
//  2. 冲突护栏：没声明聚合却出现重复序列时，**只保留一条并告警**（不写入互相覆盖的多条），
//     并把修法直接写进日志。宁可少一条数据，也不能写入无意义的覆盖值。

// applyAggregate 按 rules.aggregate 合并同名序列；未匹配到规则的指标原样保留。
//
// 匹配对象是**最终指标名**（含模板前缀，即「指标浏览」里看到的名字）。
// 这一点与 keep/drop/rename 不同（那三条作用于响应中的原名），原因见 Rules.Aggregate 的注释：
// 聚合发生在「产出我们自己的指标」这一步，用最终名才不会出现「匹配不到」的意外。
// 多条规则时首条匹配生效（与 rename 的语义一致，避免规则互相覆盖）。
func (r *TemplateRunner) applyAggregate(tpl template.Config, metrics []model.Metric) ([]model.Metric, error) {
	if len(tpl.Rules.Aggregate) == 0 {
		return metrics, nil
	}
	type aggRule struct {
		re *regexp.Regexp
		op string
	}
	rules := make([]aggRule, 0, len(tpl.Rules.Aggregate))
	for _, a := range tpl.Rules.Aggregate {
		re, err := r.regexp(a.Match)
		if err != nil {
			return nil, err
		}
		if re == nil {
			continue
		}
		rules = append(rules, aggRule{re: re, op: a.Op})
	}
	if len(rules) == 0 {
		return metrics, nil
	}

	type acc struct {
		metric model.Metric
		op     string
		sum    float64
		min    float64
		max    float64
		count  int
	}
	states := make(map[string]*acc, len(metrics))
	order := make([]string, 0, len(metrics))
	out := make([]model.Metric, 0, len(metrics))

	for _, m := range metrics {
		op := ""
		for _, rule := range rules {
			if rule.re.MatchString(m.Name) {
				op = rule.op
				break
			}
		}
		if op == "" {
			out = append(out, m)
			continue
		}
		key := seriesKey(m)
		st, ok := states[key]
		if !ok {
			states[key] = &acc{metric: m, op: op, sum: m.Value, min: m.Value, max: m.Value, count: 1}
			order = append(order, key)
			continue
		}
		st.sum += m.Value
		st.count++
		if m.Value < st.min {
			st.min = m.Value
		}
		if m.Value > st.max {
			st.max = m.Value
		}
	}

	// 被聚合的指标统一追加在末尾：顺序稳定即可（时序库与报告都不关心顺序）
	for _, key := range order {
		st := states[key]
		m := st.metric
		switch st.op {
		case template.AggSum:
			m.Value = st.sum
		case template.AggMax:
			m.Value = st.max
		case template.AggMin:
			m.Value = st.min
		case template.AggAvg:
			if st.count > 0 {
				m.Value = st.sum / float64(st.count)
			}
		}
		out = append(out, m)
	}
	return out, nil
}

// dropCollisions 处理「未声明聚合（或聚合后仍然）出现重复序列」的情况：
// 每个序列键只保留第一条（顺序确定，不随 map 遍历变化），并告警一次。
//
// 为什么不去重后默默求和：聚合方式取决于指标语义（消息数该 sum、队列深度该 max），
// 猜错会给出「看起来正常但含义错误」的数字。少一条数据 + 明确告警，比一个错误的数字安全。
func (r *TemplateRunner) dropCollisions(tpl template.Config, metrics []model.Metric) []model.Metric {
	if len(metrics) == 0 {
		return metrics
	}
	seen := make(map[string]struct{}, len(metrics))
	out := make([]model.Metric, 0, len(metrics))
	dropped := map[string]int{}
	for _, m := range metrics {
		key := seriesKey(m)
		if _, ok := seen[key]; ok {
			dropped[m.Name]++
			continue
		}
		seen[key] = struct{}{}
		out = append(out, m)
	}
	for name, n := range dropped {
		r.warnCollisionOnce(tpl.ID, name, n)
	}
	return out
}

// warnCollisionOnce 同一（模板, 指标）只告警一次：否则每轮采集都刷屏，反而把其它问题淹掉。
func (r *TemplateRunner) warnCollisionOnce(tplID, metric string, dropped int) {
	key := tplID + "\x00" + metric
	if _, loaded := r.warned.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	slog.Warn("模板产出存在同名同标签的重复序列，已只保留第一条",
		"template", tplID, "metric", metric, "dropped", dropped,
		"hint", "通常是 rules.unlabel 丢掉了区分序列的标签；如需汇总请用 rules.aggregate 声明 sum/max/min/avg")
}

// seriesKey 返回「指标名 + 标签集」的稳定标识（标签按键排序，用不可见分隔符避免歧义）。
func seriesKey(m model.Metric) string {
	keys := make([]string, 0, len(m.Labels))
	for k := range m.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(m.Name)
	for _, k := range keys {
		b.WriteByte(0)
		b.WriteString(k)
		b.WriteByte(1)
		b.WriteString(m.Labels[k])
	}
	return b.String()
}
