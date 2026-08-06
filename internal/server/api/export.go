package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// handleMetricsExport 导出历史指标数据为 CSV。
// GET /api/v1/metrics/export?metric=&node=&instance=&start=&end=&step=&labels=
//   - metric: 指标名（必填）
//   - node:   限定主机（可选）
//   - start/end: 毫秒时间戳（必填）
//   - step:   步长毫秒（可选，默认根据跨度自动选择）
//   - labels: 附加筛选标签，逗号分隔 key=value 对（可选）
//
// 多序列时输出长表：timestamp,node,labels,value（Excel 友好）；
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

	node := q.Get("node")
	labels := map[string]string{}
	if node != "" {
		labels["node"] = node
	}
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

	series, err := a.store.QueryRange(node, metric, labels, start, end, step)
	if err != nil {
		http.Error(w, "查询失败: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var sb strings.Builder
	if len(series) <= 1 {
		sb.WriteString("timestamp,value\n")
	} else {
		sb.WriteString("timestamp,labels,value\n")
	}
	for _, s := range series {
		for _, p := range s.Points {
			t := time.UnixMilli(p.Timestamp).Format("2006-01-02 15:04:05")
			if len(series) <= 1 {
				sb.WriteString(fmt.Sprintf("%s,%.6g\n", t, p.Value))
			} else {
				sb.WriteString(fmt.Sprintf("%s,%s,%.6g\n", t, labelStr(s.Labels), p.Value))
			}
		}
	}

	fname := fmt.Sprintf("metric_%s_%d_%d.csv", metric, start, end)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+fname)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("\xEF\xBB\xBF")) // BOM 便于 Excel 识别 UTF-8
	_, _ = w.Write([]byte(sb.String()))
}

// labelStr 将标签集序列化为可读字符串（排除内部 __name__）。
func labelStr(m map[string]string) string {
	var parts []string
	for k, v := range m {
		if k == "__name__" {
			continue
		}
		parts = append(parts, k+"="+v)
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
