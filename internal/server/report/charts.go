package report

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// 本文件提供自包含的 SVG 图表绘制函数，巡检报告（HTML 下载 / 前端预览）直接内联这些 SVG，
// 无需任何外部 JS 依赖，既能离线查看，也可完整打印。

// linePoint 是折线图的一个数据点。
type linePoint struct {
	T int64   // 毫秒时间戳
	V float64 // 指标值
}

// lineSeries 是折线图的一条序列。
type lineSeries struct {
	Name   string
	Color  string
	Points []linePoint
}

// barItem 是柱状图的一项。
type barItem struct {
	Label string
	Value float64
	Color string
}

// lineChart 生成折线图 SVG。unit 仅用于说明。
func lineChart(title string, series []lineSeries, unit string, width, height int) string {
	if width <= 0 {
		width = 560
	}
	if height <= 0 {
		height = 260
	}
	const (
		padL = 46
		padR = 14
		padT = 30
		padB = 30
	)
	plotW := width - padL - padR
	plotH := height - padT - padB
	if plotW < 40 {
		plotW = 40
	}
	if plotH < 40 {
		plotH = 40
	}

	var tMin, tMax int64
	var vMin, vMax float64
	has := false
	for _, s := range series {
		for _, p := range s.Points {
			if !has {
				tMin, tMax = p.T, p.T
				vMin, vMax = p.V, p.V
				has = true
				continue
			}
			if p.T < tMin {
				tMin = p.T
			}
			if p.T > tMax {
				tMax = p.T
			}
			if p.V < vMin {
				vMin = p.V
			}
			if p.V > vMax {
				vMax = p.V
			}
		}
	}
	if !has {
		return emptyChart(title, width, height, "暂无数据")
	}
	if tMax == tMin {
		tMax = tMin + 1
	}
	if vMax == vMin {
		vMax = vMin + 1
	}
	pad := (vMax - vMin) * 0.12
	vMin -= pad
	vMax += pad
	if vMin < 0 && seriesMinNonNeg(series) {
		vMin = 0
	}

	xOf := func(t int64) float64 {
		return padL + float64(t-tMin)/float64(tMax-tMin)*float64(plotW)
	}
	yOf := func(v float64) float64 {
		return padT + float64(vMax-v)/float64(vMax-vMin)*float64(plotH)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="100%%" preserveAspectRatio="xMidYMid meet" font-family="-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif">`, width, height))
	sb.WriteString(fmt.Sprintf(`<rect x="0" y="0" width="%d" height="%d" fill="#fbfcfe"/>`, width, height))
	sb.WriteString(fmt.Sprintf(`<text x="%d" y="20" font-size="14" font-weight="600" fill="#1f2d3d">%s</text>`, padL, escapeXML(title)))

	div := 4
	for i := 0; i <= div; i++ {
		val := vMin + float64(vMax-vMin)*float64(i)/float64(div)
		y := yOf(val)
		sb.WriteString(fmt.Sprintf(`<line x1="%d" y1="%.1f" x2="%d" y2="%.1f" stroke="#eef1f6" stroke-width="1"/>`, padL, y, width-padR, y))
		sb.WriteString(fmt.Sprintf(`<text x="%d" y="%.1f" font-size="10" fill="#9aa5b1" text-anchor="end" dominant-baseline="middle">%s</text>`, padL-6, y, formatNum(val)))
	}
	for _, frac := range []float64{0, 0.5, 1} {
		t := tMin + int64(float64(tMax-tMin)*frac)
		x := xOf(t)
		sb.WriteString(fmt.Sprintf(`<text x="%.1f" y="%d" font-size="10" fill="#9aa5b1" text-anchor="middle">%s</text>`, x, height-10, escapeXML(shortTime(t))))
	}

	for _, s := range series {
		if len(s.Points) == 0 {
			continue
		}
		color := s.Color
		if color == "" {
			color = "#409eff"
		}
		var path, area strings.Builder
		for j, p := range s.Points {
			x := xOf(p.T)
			y := yOf(p.V)
			if j == 0 {
				path.WriteString(fmt.Sprintf("M%.1f %.1f", x, y))
				area.WriteString(fmt.Sprintf("M%.1f %.1f", x, y))
			} else {
				path.WriteString(fmt.Sprintf("L%.1f %.1f", x, y))
				area.WriteString(fmt.Sprintf("L%.1f %.1f", x, y))
			}
		}
		area.WriteString(fmt.Sprintf("L%.1f %.1f L%.1f %.1f Z", xOf(s.Points[len(s.Points)-1].T), yOf(vMin), xOf(s.Points[0].T), yOf(vMin)))
		sb.WriteString(fmt.Sprintf(`<path d="%s" fill="%s" fill-opacity="0.08" stroke="none"/>`, area.String(), color))
		sb.WriteString(fmt.Sprintf(`<path d="%s" fill="none" stroke="%s" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"/>`, path.String(), color))
		last := s.Points[len(s.Points)-1]
		sb.WriteString(fmt.Sprintf(`<circle cx="%.1f" cy="%.1f" r="2.5" fill="%s"/>`, xOf(last.T), yOf(last.V), color))
	}

	if len(series) > 1 {
		ly := padT - 4
		lx := width - padR
		for i := len(series) - 1; i >= 0; i-- {
			s := series[i]
			color := s.Color
			if color == "" {
				color = "#409eff"
			}
			label := s.Name
			lx -= 10 + len([]rune(label))*7 + 14
			sb.WriteString(fmt.Sprintf(`<rect x="%d" y="%d" width="10" height="10" rx="2" fill="%s"/>`, lx, ly-8, color))
			sb.WriteString(fmt.Sprintf(`<text x="%d" y="%d" font-size="10" fill="#5a6573">%s</text>`, lx+14, ly+1, escapeXML(label)))
		}
	}
	_ = unit
	sb.WriteString(`</svg>`)
	return sb.String()
}

// barChart 生成柱状图 SVG。
func barChart(title string, items []barItem, unit string, width, height int) string {
	if width <= 0 {
		width = 560
	}
	if height <= 0 {
		height = 260
	}
	const (
		padL = 46
		padR = 14
		padT = 30
		padB = 46
	)
	plotW := width - padL - padR
	plotH := height - padT - padB
	if plotW < 40 {
		plotW = 40
	}
	if plotH < 40 {
		plotH = 40
	}
	if len(items) == 0 {
		return emptyChart(title, width, height, "暂无数据")
	}
	vMax := items[0].Value
	for _, it := range items {
		if it.Value > vMax {
			vMax = it.Value
		}
	}
	if vMax <= 0 {
		vMax = 1
	}
	vMax = niceCeil(vMax)
	yOf := func(v float64) float64 {
		return padT + float64(vMax-v)/float64(vMax)*float64(plotH)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="100%%" preserveAspectRatio="xMidYMid meet" font-family="-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif">`, width, height))
	sb.WriteString(fmt.Sprintf(`<rect x="0" y="0" width="%d" height="%d" fill="#fbfcfe"/>`, width, height))
	sb.WriteString(fmt.Sprintf(`<text x="%d" y="20" font-size="14" font-weight="600" fill="#1f2d3d">%s</text>`, padL, escapeXML(title)))

	div := 4
	for i := 0; i <= div; i++ {
		val := vMax * float64(i) / float64(div)
		y := yOf(val)
		sb.WriteString(fmt.Sprintf(`<line x1="%d" y1="%.1f" x2="%d" y2="%.1f" stroke="#eef1f6" stroke-width="1"/>`, padL, y, width-padR, y))
		sb.WriteString(fmt.Sprintf(`<text x="%d" y="%.1f" font-size="10" fill="#9aa5b1" text-anchor="end" dominant-baseline="middle">%s</text>`, padL-6, y, formatNum(val)))
	}

	n := len(items)
	slot := float64(plotW) / float64(n)
	barW := slot * 0.6
	for i, it := range items {
		x := padL + slot*float64(i) + (slot-barW)/2
		y := yOf(it.Value)
		h := float64(padT+plotH) - y
		color := it.Color
		if color == "" {
			color = "#409eff"
		}
		sb.WriteString(fmt.Sprintf(`<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="3" fill="%s"/>`, x, y, barW, h, color))
		sb.WriteString(fmt.Sprintf(`<text x="%.1f" y="%.1f" font-size="10" fill="#3a4654" text-anchor="middle" font-weight="600">%s</text>`, x+barW/2, y-5, formatNum(it.Value)))
		label := it.Label
		if len([]rune(label)) > 8 {
			label = string([]rune(label)[:8]) + "…"
		}
		sb.WriteString(fmt.Sprintf(`<text x="%.1f" y="%d" font-size="10" fill="#5a6573" text-anchor="middle">%s</text>`, x+barW/2, height-30, escapeXML(label)))
	}
	_ = unit
	sb.WriteString(`</svg>`)
	return sb.String()
}

// sparkline 生成无坐标轴的迷你折线图（用于表格内趋势）。
func sparkline(points []linePoint, color string, width, height int) string {
	if width <= 0 {
		width = 120
	}
	if height <= 0 {
		height = 34
	}
	if len(points) == 0 {
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 34" width="120" height="34"></svg>`
	}
	if color == "" {
		color = "#409eff"
	}
	tMin, tMax := points[0].T, points[0].T
	vMin, vMax := points[0].V, points[0].V
	for _, p := range points {
		if p.T < tMin {
			tMin = p.T
		}
		if p.T > tMax {
			tMax = p.T
		}
		if p.V < vMin {
			vMin = p.V
		}
		if p.V > vMax {
			vMax = p.V
		}
	}
	if tMax == tMin {
		tMax = tMin + 1
	}
	if vMax == vMin {
		vMax = vMin + 1
	}
	pad := (vMax - vMin) * 0.15
	vMin -= pad
	vMax += pad
	xOf := func(t int64) float64 {
		return 2 + float64(t-tMin)/float64(tMax-tMin)*float64(width-4)
	}
	yOf := func(v float64) float64 {
		return 2 + float64(vMax-v)/float64(vMax-vMin)*float64(height-4)
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d">`, width, height, width, height))
	var path strings.Builder
	for i, p := range points {
		x := xOf(p.T)
		y := yOf(p.V)
		if i == 0 {
			path.WriteString(fmt.Sprintf("M%.1f %.1f", x, y))
		} else {
			path.WriteString(fmt.Sprintf("L%.1f %.1f", x, y))
		}
	}
	sb.WriteString(fmt.Sprintf(`<path d="%s" fill="none" stroke="%s" stroke-width="1.6" stroke-linejoin="round"/>`, path.String(), color))
	last := points[len(points)-1]
	sb.WriteString(fmt.Sprintf(`<circle cx="%.1f" cy="%.1f" r="2" fill="%s"/>`, xOf(last.T), yOf(last.V), color))
	sb.WriteString(`</svg>`)
	return sb.String()
}

// emptyChart 生成占位 SVG。
func emptyChart(title string, width, height int, msg string) string {
	if width <= 0 {
		width = 560
	}
	if height <= 0 {
		height = 200
	}
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="100%%" font-family="-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif"><rect x="0" y="0" width="%d" height="%d" fill="#fbfcfe"/><text x="%d" y="20" font-size="14" font-weight="600" fill="#1f2d3d">%s</text><text x="%d" y="%d" font-size="12" fill="#9aa5b1" text-anchor="middle">%s</text></svg>`,
		width, height, width, height, 46, escapeXML(title), width/2, height/2, escapeXML(msg))
}

func seriesMinNonNeg(series []lineSeries) bool {
	for _, s := range series {
		for _, p := range s.Points {
			if p.V < 0 {
				return false
			}
		}
	}
	return true
}

// niceCeil 将数值向上取整到友好的刻度上界。
func niceCeil(v float64) float64 {
	if v <= 0 {
		return 1
	}
	pow := math.Pow(10, math.Floor(math.Log10(v)))
	n := v / pow
	var nice float64
	switch {
	case n <= 1:
		nice = 1
	case n <= 2:
		nice = 2
	case n <= 2.5:
		nice = 2.5
	case n <= 5:
		nice = 5
	default:
		nice = 10
	}
	return nice * pow
}

// formatNum 格式化数值：去掉多余小数位。
func formatNum(v float64) string {
	if math.Abs(v) >= 100 {
		return fmt.Sprintf("%.0f", v)
	}
	if math.Abs(v) >= 10 {
		return fmt.Sprintf("%.1f", v)
	}
	return fmt.Sprintf("%.1f", v)
}

// shortTime 将毫秒时间戳格式化为 MM-DD HH:MM。
func shortTime(ms int64) string {
	t := time.UnixMilli(ms)
	return t.Format("01-02 15:04")
}

func escapeXML(s string) string {
	r := strings.NewReplacer(`&`, "&amp;", `<`, "&lt;", `>`, "&gt;", `"`, "&quot;", `'`, "&#39;")
	return r.Replace(s)
}
