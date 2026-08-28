package analysis

import (
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
)

func points(values []float64) []model.Point {
	out := make([]model.Point, len(values))
	base := time.Now().Add(-time.Duration(len(values)) * time.Hour).UnixMilli()
	for i, value := range values {
		out[i] = model.Point{Timestamp: base + int64(i)*time.Hour.Milliseconds(), Value: value}
	}
	return out
}

func TestCalculateBaselineRejectsSingleSpike(t *testing.T) {
	values := make([]float64, 30)
	for i := range values {
		values[i] = 50
	}
	values[20] = 98
	out := CalculateBaseline("cpu_usage", nil, points(values))
	if out.IsAnomalous {
		t.Fatal("单个离群点不应触发持续异常")
	}
}

func TestCalculateBaselineDetectsSustainedDeviation(t *testing.T) {
	values := make([]float64, 30)
	for i := range values {
		values[i] = 40
	}
	values[27], values[28], values[29] = 75, 76, 78
	out := CalculateBaseline("cpu_usage", nil, points(values))
	if !out.IsAnomalous || out.Consecutive != 3 {
		t.Fatalf("期望识别连续异常，实际=%+v", out)
	}
}

func TestForecastCapacity(t *testing.T) {
	values := make([]float64, 30)
	for i := range values {
		values[i] = 50 + float64(i)*0.8
	}
	out := ForecastCapacity(map[string]string{"mount": "/data"}, points(values))
	if out.Status != "normal" && out.Status != "warning" && out.Status != "urgent" {
		t.Fatalf("预期可预测，实际=%+v", out)
	}
	if out.DaysRemaining <= 0 || out.ExhaustedAt == 0 {
		t.Fatalf("预测结果不完整：%+v", out)
	}
}

func TestForecastExplainsNoGrowth(t *testing.T) {
	values := make([]float64, 30)
	for i := range values {
		values[i] = 60
	}
	out := ForecastCapacity(nil, points(values))
	if out.Status != "unknown" || out.Reason == "" {
		t.Fatalf("非增长趋势应返回不可预测原因：%+v", out)
	}
}

func TestScoreRisk(t *testing.T) {
	score, level := ScoreRisk([]Evidence{{Type: "capacity", Severity: SeverityCritical, Score: 65}, {Type: "anomaly", Severity: SeverityWarning, Score: 35}})
	if score < 90 || level != SeverityCritical {
		t.Fatalf("风险聚合错误：score=%d severity=%s", score, level)
	}
}
