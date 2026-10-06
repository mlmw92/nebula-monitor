package api

import (
	"encoding/csv"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/model"
)

func TestWriteMetricsCSVPreservesLabelCells(t *testing.T) {
	series := []model.Series{
		{Labels: map[string]string{"node": "web,01", "job": "app\"server"}, Points: []model.Point{{Timestamp: 1000, Value: 2}}},
		{Labels: map[string]string{"node": "db-01"}, Points: []model.Point{{Timestamp: 2000, Value: 3}}},
	}
	rec := httptest.NewRecorder()
	writeMetricsCSV(rec, "cpu_usage", 1000, 2000, series)
	body := strings.TrimPrefix(rec.Body.String(), "\xEF\xBB\xBF")
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("CSV 数据无法按列解析: %v; body=%q", err, body)
	}
	if len(rows) != 3 || len(rows[1]) != 3 || rows[1][1] != `job=app"server node=web,01` || rows[2][1] != "node=db-01" {
		t.Fatalf("多序列 CSV 列或标签内容错误: %#v", rows)
	}
}
