package api

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// handleMetricsExport 导出历史指标数据为 CSV。
// GET /api/v1/metrics/export?metric=&node=&instance=&start=&end=&step=&labels=
//   - metric: 指标名（必填）
//   - node:   限定主机（可选）
//   - start/end: 毫秒时间戳（必填）
//   - step:   步长毫秒（可选，默认根据跨度自动选择）
//   - labels: 附加筛选标签，逗号分隔 key=value 对（可选）
//
// 多序列时输出长表：timestamp,labels,value（Excel 友好）；
// 单序列简化为：timestamp,value。
func (a *API) handleMetricsExport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	metric := q.Get("metric")
	if metric == "" {
		http.Error(w, "metric 必填", http.StatusBadRequest)
		return
	}
	start, err1 := strconv.ParseInt(q.Get("start"), 10, 64)
	end, err2 := strconv.ParseInt(q.Get("end"), 10, 64)
	if err1 != nil || err2 != nil || start <= 0 || end <= 0 || end <= start {
		http.Error(w, "start/end 必填且 end>start（毫秒时间戳）", http.StatusBadRequest)
		return
	}
	// 跨度上限：7 天，防止大查询拖垮时序库。
	const maxSpan = int64(7 * 24 * 3600 * 1000)
	if end-start > maxSpan {
		http.Error(w, "导出时间跨度上限为 7 天", http.StatusBadRequest)
		return
	}
	step := parseInt64(q.Get("step"), 0)
	if step <= 0 {
		// 默认约 300 个点
		step = (end - start) / 300
		if step < 1000 {
			step = 1000
		}
	}

	// labels 先只承接 instance 与 `labels=k=v,...` 中的附加筛选；node 交给 metricTarget
	// 统一判定，避免此处的 labels.node 直接覆盖 node 造成越权。
	labels := map[string]string{}
	if inst := q.Get("instance"); inst != "" {
		labels["instance"] = inst
	}
	for _, kv := range strings.Split(q.Get("labels"), ",") {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			labels[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	node, ok := a.metricTarget(w, r, "metrics:export", labels)
	if !ok {
		return
	}

	p := Principal(r)
	if node == "" && !a.visibleMetricNodes(p) {
		// 受限用户没有任何可见节点：不查询存储，只返回标题行。
		writeMetricsCSV(w, metric, start, end, nil)
		return
	}

	series, err := a.store.QueryRange(node, metric, labels, start, end, step)
	if err != nil {
		http.Error(w, "查询失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	series = a.visibleMetricSeries(p, node, series)
	writeMetricsCSV(w, metric, start, end, series)
}

// writeMetricsCSV 按序列数量选择单序列/多序列表头并写出 CSV 响应。
//
// 写出统一走 csvDownload（响应头 / BOM / 公式注入消毒都在那里）。
func writeMetricsCSV(w http.ResponseWriter, metric string, start, end int64, series []model.Series) {
	fname := fmt.Sprintf("metric_%s_%d_%d.csv", metric, start, end)
	multi := len(series) > 1
	header := []string{"timestamp", "value"}
	if multi {
		header = []string{"timestamp", "labels", "value"}
	}
	csvDownload(w, fname, header, func(write func([]string)) {
		for _, s := range series {
			for _, p := range s.Points {
				row := []string{time.UnixMilli(p.Timestamp).Format("2006-01-02 15:04:05")}
				if multi {
					row = append(row, labelStr(s.Labels))
				}
				row = append(row, strconv.FormatFloat(p.Value, 'g', 6, 64))
				write(row)
			}
		}
	})
}

// labelStr 将标签集序列化为可读字符串（排除内部 __name__）。
func labelStr(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		if k != "__name__" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, " ")
}

// parseInt64 解析整数，失败返回 def。
func parseInt64(s string, def int64) int64 {
	if s == "" {
		return def
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return v
}
