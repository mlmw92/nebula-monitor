package alert

import (
	"strconv"

	"github.com/nebula/monitor/internal/model"
)

// Compare 根据运算符比较 left 与 right。
func Compare(op string, left, right float64) bool {
	switch op {
	case ">":
		return left > right
	case ">=":
		return left >= right
	case "<":
		return left < right
	case "<=":
		return left <= right
	case "==":
		return left == right
	case "!=":
		return left != right
	default:
		return false
	}
}

// matchesGroup 判断节点是否属于规则作用的分组（空分组表示全部）。
func matchesGroup(ruleGroup, nodeGroup string) bool {
	return ruleGroup == "" || ruleGroup == nodeGroup
}

// triggerMessage 构造告警描述。
func triggerMessage(r model.AlertRule, node string, value float64) string {
	return "节点 " + node + " 指标 " + r.Metric + " = " + formatFloat(value) +
		" " + r.Operator + " 阈值 " + formatFloat(r.Threshold) + "（规则：" + r.Name + "）"
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64)
}
