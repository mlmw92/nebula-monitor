package analysis

// ScoreRisk 按证据严重度生成可解释分数；同类证据只累加最严重的两条，避免单指标放大风险。
func ScoreRisk(evidence []Evidence) (int, Severity) {
	if len(evidence) == 0 {
		return 0, SeverityInfo
	}
	score, critical, warning := 0, false, false
	byType := make(map[string][]Evidence)
	for _, item := range evidence {
		byType[item.Type] = append(byType[item.Type], item)
	}
	for _, items := range byType {
		best, second := 0, 0
		for _, item := range items {
			if item.Score > best {
				second, best = best, item.Score
			} else if item.Score > second {
				second = item.Score
			}
			if item.Severity == SeverityCritical {
				critical = true
			}
			if item.Severity == SeverityWarning {
				warning = true
			}
		}
		score += best + second/3
	}
	if score > 100 {
		score = 100
	}
	if critical || score >= 60 {
		return score, SeverityCritical
	}
	if warning || score >= 25 {
		return score, SeverityWarning
	}
	return score, SeverityInfo
}
