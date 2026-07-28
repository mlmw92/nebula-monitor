// echarts.js — ECharts 科技感图表封装（渐变面积线）。
import * as echarts from 'echarts'

export const COLORS = {
  cyan: '#22d3ee',
  blue: '#3b82f6',
  purple: '#a855f7',
  amber: '#f59e0b',
  red: '#ef4444',
  green: '#22c55e',
}

const AXIS = '#9fb3c8'
const SPLIT = 'rgba(34,211,238,0.08)'

function baseGrid(extra) {
  // containLabel: 自动按坐标轴标签宽度预留边距，避免长标签（如 "102.3 MB/s"）被裁剪
  return Object.assign({ left: 48, right: 18, top: 24, bottom: 30, containLabel: true }, extra || {})
}

function gradientSeries(name, color, data) {
  return {
    name,
    type: 'line',
    smooth: true,
    showSymbol: false,
    lineStyle: { color, width: 2, shadowColor: color, shadowBlur: 10 },
    areaStyle: {
      color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
        { offset: 0, color: color + '55' },
        { offset: 1, color: color + '03' },
      ]),
    },
    data: data || [],
  }
}

// 单指标实时小图
export function areaOption(color, unit) {
  const isPct = unit === '%'
  return {
    grid: { left: 42, right: 14, top: 10, bottom: 6 },
    tooltip: { trigger: 'axis', backgroundColor: 'rgba(11,17,32,0.9)', borderColor: color, textStyle: { color: '#e5edf7' } },
    xAxis: { type: 'time', show: false },
    yAxis: {
      type: 'value',
      min: isPct ? 0 : undefined,
      max: isPct ? 100 : undefined,
      splitNumber: 2,
      axisLabel: { color: AXIS, fontSize: 10 },
      splitLine: { show: false },
    },
    series: [gradientSeries('', color, [])],
  }
}

// 实时多序列折线图（如 磁盘 IO=读取/写入、网络 IO=接收/发送）
// opts.showXAxis: true 时显示横坐标时间轴（用于较大的网络流量组件）
export function areaMultiOption(defs, opts) {
  const showX = !!(opts && opts.showXAxis)
  const names = defs.map((d) => d.name)
  return {
    grid: { left: 48, right: 14, top: 22, bottom: showX ? 26 : 6 },
    tooltip: { trigger: 'axis', backgroundColor: 'rgba(11,17,32,0.9)', textStyle: { color: '#e5edf7' } },
    xAxis: showX
      ? { type: 'time', axisLine: { lineStyle: { color: AXIS } }, axisLabel: { color: AXIS, fontSize: 11, hideOverlap: true }, splitLine: { show: false } }
      : { type: 'time', show: false },
    yAxis: {
      type: 'value', min: 0,
      splitNumber: 2,
      axisLabel: { color: AXIS, fontSize: 10 },
      splitLine: { show: false },
    },
    legend: { top: 0, right: 4, itemWidth: 10, itemHeight: 6, itemGap: 10, textStyle: { color: AXIS, fontSize: 10 }, data: names },
    series: defs.map((d) => gradientSeries(d.name, d.color, [])),
  }
}

// 集群总览趋势（CPU + 内存）
export function trendOption() {
  return {
    grid: baseGrid(),
    tooltip: { trigger: 'axis', backgroundColor: 'rgba(11,17,32,0.9)', textStyle: { color: '#e5edf7' } },
    xAxis: { type: 'time', axisLine: { lineStyle: { color: AXIS } }, axisLabel: { color: AXIS }, splitLine: { show: false } },
    yAxis: { type: 'value', axisLabel: { color: AXIS }, splitLine: { lineStyle: { color: SPLIT } } },
    series: [gradientSeries('CPU', COLORS.cyan, []), gradientSeries('内存', COLORS.purple, [])],
  }
}

// 节点历史趋势（CPU/内存/磁盘/网络）
export function historyOption() {
  return {
    grid: baseGrid({ top: 40 }),
    legend: { textStyle: { color: AXIS }, top: 4 },
    tooltip: { trigger: 'axis', backgroundColor: 'rgba(11,17,32,0.9)', textStyle: { color: '#e5edf7' } },
    xAxis: { type: 'time', axisLine: { lineStyle: { color: AXIS } }, axisLabel: { color: AXIS }, splitLine: { show: false } },
    yAxis: { type: 'value', axisLabel: { color: AXIS }, splitLine: { lineStyle: { color: SPLIT } } },
    series: [
      gradientSeries('CPU', COLORS.cyan, []),
      gradientSeries('内存', COLORS.purple, []),
      gradientSeries('磁盘', COLORS.amber, []),
      gradientSeries('网络', COLORS.blue, []),
    ],
  }
}

// 速率短格式（坐标轴标签）：B/s -> KB/s -> MB/s -> GB/s
export function rateShort(v) {
  const b = Number(v || 0)
  if (b >= 1 << 30) return (b / (1 << 30)).toFixed(1) + ' GB/s'
  if (b >= 1 << 20) return (b / (1 << 20)).toFixed(1) + ' MB/s'
  if (b >= 1 << 10) return (b / (1 << 10)).toFixed(0) + ' KB/s'
  return b.toFixed(0) + ' B/s'
}

// 基础监控面板通用配置。
// opts: { yMin, yMax, yFormatter, tipFormatter, xFormatter, series:[{name,color,data}], colors }
export function monitorOption(opts) {
  const o = opts || {}
  const series = o.series || []
  const colors = o.colors
  return {
    grid: baseGrid({ top: 38, left: 56, right: 18, bottom: 28 }),
    legend: { textStyle: { color: AXIS, fontSize: 11 }, top: 4, icon: 'roundRect', itemWidth: 14, itemHeight: 8 },
    tooltip: {
      trigger: 'axis',
      backgroundColor: 'rgba(11,17,32,0.92)',
      borderColor: 'rgba(34,211,238,0.3)',
      textStyle: { color: '#e5edf7', fontSize: 12 },
      valueFormatter: o.tipFormatter || ((v) => (v == null ? '-' : v)),
    },
    xAxis: {
      type: 'time',
      min: o.xMin != null ? o.xMin : undefined,
      max: o.xMax != null ? o.xMax : undefined,
      axisLine: { lineStyle: { color: AXIS } },
      axisLabel: { color: AXIS, fontSize: 11, hideOverlap: true, formatter: o.xFormatter },
      splitLine: { show: false },
    },
    yAxis: {
      type: 'value',
      min: o.yMin != null ? o.yMin : 0,
      max: o.yMax,
      axisLabel: { color: AXIS, fontSize: 11, formatter: o.yFormatter },
      splitLine: { show: true, lineStyle: { color: SPLIT } },
    },
    series: series.map((s, i) => gradientSeries(s.name, s.color || (colors && colors[i]) || COLORS.cyan, s.data)),
  }
}

// 把查询返回的 series 映射到历史图（按 __name__ 归类）
export function setHistory(chart, series) {
  const map = { cpu_usage: 'CPU', mem_used_percent: '内存', disk_used_percent: '磁盘', network_recv_rate: '网络' }
  const data = {}
  ;(series || []).forEach((s) => {
    const label = map[s.labels.__name__] || s.labels.__name__ || '其他'
    data[label] = (s.points || []).map((p) => [p.timestamp, p.value])
  })
  const order = ['CPU', '内存', '磁盘', '网络']
  chart.setOption({
    series: order.map((name) => ({ name, data: data[name] || [] })),
  })
}

export function initChart(el) {
  return echarts.init(el, null, { renderer: 'canvas' })
}

// 环形仪表盘（用于 CPU/内存/SWAP 使用率展示）
export function gaugeOption(color, name) {
  return {
    series: [
      {
        type: 'gauge',
        startAngle: 90,
        endAngle: -270,
        radius: '92%',
        pointer: { show: false },
        progress: { show: true, width: 12, roundCap: true, itemStyle: { color } },
        axisLine: { lineStyle: { width: 12, color: [[1, 'rgba(255,255,255,0.08)']] } },
        axisTick: { show: false },
        splitLine: { show: false },
        axisLabel: { show: false },
        anchor: { show: false },
        title: { show: false },
        detail: {
          valueAnimation: true,
          fontSize: 20,
          fontWeight: 700,
          color: '#e5edf7',
          offsetCenter: [0, 0],
          formatter: '{value}%',
        },
        data: [{ value: 0, name }],
      },
    ],
  }
}

export { echarts }
