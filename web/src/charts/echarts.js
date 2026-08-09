// echarts.js — ECharts 科技感图表封装（渐变面积线）。
import * as echarts from 'echarts'
import { geoCoord } from './geoCoords'

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

// 渐变对象缓存：同色系渐变复用同一个 LinearGradient 实例，
// 避免实时图每秒多次 setOption 反复构造对象造成 GC 压力
const gradientCache = new Map()
function cachedGradient(color) {
  let g = gradientCache.get(color)
  if (!g) {
    g = new echarts.graphic.LinearGradient(0, 0, 0, 1, [
      { offset: 0, color: color + '55' },
      { offset: 1, color: color + '03' },
    ])
    gradientCache.set(color, g)
  }
  return g
}

function baseGrid(extra) {
  // containLabel: 自动按坐标轴标签宽度预留边距，避免长标签（如 "102.3 MB/s"）被裁剪
  return Object.assign({ left: 48, right: 18, top: 24, bottom: 30, containLabel: true }, extra || {})
}

// area=false 时只画折线不填充：多序列（如按主机拆分的十余条曲线）叠加填充会互相遮挡
function gradientSeries(name, color, data, area = true) {
  const s = {
    name,
    type: 'line',
    smooth: true,
    showSymbol: false,
    lineStyle: { color, width: 2, shadowColor: color, shadowBlur: 10 },
    data: data || [],
  }
  if (area) s.areaStyle = { color: cachedGradient(color) }
  return s
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
  if (b >= _1GB) return (b / _1GB).toFixed(1) + ' GB/s'
  if (b >= _1MB) return (b / _1MB).toFixed(1) + ' MB/s'
  if (b >= _1KB) return (b / _1KB).toFixed(0) + ' KB/s'
  return b.toFixed(0) + ' B/s'
}

// 字节格式化：B -> KB -> MB -> GB -> TB
// 注意：JS 的 << 是 32 位整数运算，1<<40 会溢出为 256，必须用字面量或 Math.pow(2,n)
const _1TB = 1099511627776   // 2^40
const _1GB = 1073741824      // 2^30
const _1MB = 1048576         // 2^20
const _1KB = 1024            // 2^10

function formatBytes(v) {
  const b = Number(v || 0)
  if (b >= _1TB) return (b / _1TB).toFixed(2) + ' TB'
  if (b >= _1GB) return (b / _1GB).toFixed(2) + ' GB'
  if (b >= _1MB) return (b / _1MB).toFixed(1) + ' MB'
  if (b >= _1KB) return (b / _1KB).toFixed(0) + ' KB'
  return b.toFixed(0) + ' B'
}

// 根据单位和值格式化仪表盘显示文本
function gaugeFormatter(value, unit) {
  if (unit === 'B') return formatBytes(value)
  if (unit === '%') return value.toFixed(1) + '%'
  if (unit === '个') return value.toLocaleString() + ' 个'
  if (unit === '核') return value.toLocaleString() + ' 核'
  if (unit === '次/s') return value.toLocaleString() + ' 次/s'
  if (unit === '个/s') return value.toLocaleString() + ' 个/s'
  if (unit === 's') return formatSeconds(value)
  if (value >= 1000000000) return (value / 1e9).toFixed(2) + 'G'
  if (value >= 1000000) return (value / 1e6).toFixed(2) + 'M'
  if (value >= 10000) return (value / 1000).toFixed(1) + 'k'
  return value.toLocaleString()
}

// 秒数格式化：s -> m -> h -> d
function formatSeconds(v) {
  const s = Number(v || 0)
  if (s >= 86400) return (s / 86400).toFixed(1) + ' 天'
  if (s >= 3600) return (s / 3600).toFixed(1) + ' 时'
  if (s >= 60) return (s / 60).toFixed(0) + ' 分'
  return s.toFixed(0) + ' 秒'
}

// 趋势图 Y 轴标签格式化：根据单位把裸数字转为可读形式
function axisValueFormatter(value, unit) {
  if (unit === 'B') return formatBytes(value)
  if (unit === 'B/s') return rateShort(value)
  if (unit === '%') return Number(value).toFixed(0) + '%'
  if (unit === '个') return Number(value).toLocaleString() + ' 个'
  if (unit === '核') return Number(value).toLocaleString() + ' 核'
  if (unit === '次/s') return Number(value).toLocaleString() + ' 次/s'
  if (unit === '个/s') return Number(value).toLocaleString() + ' 个/s'
  if (unit === 's') return formatSeconds(value)
  if (value >= 1000000000) return (value / 1e9).toFixed(1) + 'G'
  if (value >= 1000000) return (value / 1e6).toFixed(1) + 'M'
  if (value >= 10000) return (value / 1000).toFixed(1) + 'k'
  return Number(value).toLocaleString()
}

// 基础监控面板通用配置。
// opts: { chartType, yMin, yMax, yFormatter, tipFormatter, xFormatter, series:[{name,color,data}], colors }
export function monitorOption(opts) {
  const o = opts || {}
  const series = o.series || []
  const colors = o.colors
  const chartType = o.chartType === 'area' ? 'area' : (o.chartType || 'line')

  // 非趋势图推荐展示当前值：每条时序只取最后一个有效点。
  const latest = series.map((s) => {
    const points = (s.data || []).filter((p) => Array.isArray(p) && Number.isFinite(Number(p[1])))
    const point = points.length ? points[points.length - 1] : null
    return { name: s.name, value: point ? Number(point[1]) : 0, color: s.color || COLORS.cyan }
  })

  if (chartType === 'bar') {
    return {
      grid: baseGrid({ top: 38, left: 56, right: 18, bottom: 42 }),
      tooltip: {
        trigger: 'axis',
        axisPointer: { type: 'shadow' },
        backgroundColor: 'rgba(11,17,32,0.92)',
        borderColor: 'rgba(34,211,238,0.3)',
        textStyle: { color: '#e5edf7', fontSize: 12 },
      },
      xAxis: { type: 'category', data: latest.map((s) => s.name), axisLine: { lineStyle: { color: AXIS } }, axisLabel: { color: AXIS, fontSize: 11, hideOverlap: true } },
      yAxis: { type: 'value', min: o.yMin != null ? o.yMin : 0, max: o.yMax, axisLabel: { color: AXIS, fontSize: 11, formatter: o.yFormatter || ((v) => axisValueFormatter(v, o.unit)) }, splitLine: { lineStyle: { color: SPLIT } } },
      series: [{
        type: 'bar',
        data: latest.map((s) => ({ value: s.value, itemStyle: { color: s.color, borderRadius: [4, 4, 0, 0] } })),
        barMaxWidth: 46,
      }],
    }
  }

  if (chartType === 'gauge') {
    const current = latest[0] || { value: 0, color: COLORS.cyan, name: '' }
    const unit = o.unit || ''
    // 百分比指标固定 0-100；字节/数值类根据当前值自动计算上限
    const max = o.yMax != null ? o.yMax : (unit === '%' ? 100 : Math.max(1, current.value * 1.2))
    const formatted = gaugeFormatter(current.value, unit)
    return {
      tooltip: { formatter: `${current.name}<br/>当前值: ${formatted}` },
      series: [{
        type: 'gauge', min: o.yMin || 0, max, startAngle: 90, endAngle: -270, radius: '78%',
        pointer: { show: false },
        progress: { show: true, width: 20, roundCap: true, itemStyle: { color: current.color } },
        axisLine: { lineStyle: { width: 20, color: [[1, 'rgba(255,255,255,0.08)']] } },
        axisTick: { show: false }, splitLine: { show: false }, axisLabel: { show: false }, anchor: { show: false },
        title: { show: true, color: AXIS, offsetCenter: [0, '52%'], fontSize: 13 },
        detail: {
          valueAnimation: true,
          color: current.color,
          fontSize: current.value >= 10000000 ? 22 : 28,
          fontWeight: 700,
          offsetCenter: [0, '4%'],
          formatter: () => formatted,
        },
        data: [{ value: current.value, name: current.name }],
      }],
    }
  }

  if (chartType === 'pie') {
    return {
      tooltip: { trigger: 'item', backgroundColor: 'rgba(11,17,32,0.92)', textStyle: { color: '#e5edf7', fontSize: 12 }, formatter: '{b}<br/>当前值: {c} ({d}%)' },
      legend: { bottom: 4, type: 'scroll', textStyle: { color: AXIS, fontSize: 11 } },
      series: [{
        type: 'pie', radius: ['42%', '70%'], center: ['50%', '46%'], avoidLabelOverlap: true,
        itemStyle: { borderColor: '#0b1120', borderWidth: 2 },
        label: { color: '#e5edf7', formatter: '{b}: {c}' },
        data: latest.map((s) => ({ name: s.name, value: s.value, itemStyle: { color: s.color } })),
      }],
    }
  }

  return {
    grid: baseGrid({ top: 38, left: 56, right: 18, bottom: 28 }),
    legend: { textStyle: { color: AXIS, fontSize: 11 }, top: 4, icon: 'roundRect', itemWidth: 14, itemHeight: 8 },
    tooltip: {
      trigger: 'axis',
      backgroundColor: 'rgba(11,17,32,0.92)',
      borderColor: 'rgba(34,211,238,0.3)',
      textStyle: { color: '#e5edf7', fontSize: 12 },
      valueFormatter: o.tipFormatter || ((v) => (v == null ? '-' : axisValueFormatter(v, o.unit))),
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
      axisLabel: { color: AXIS, fontSize: 11, formatter: o.yFormatter || ((v) => axisValueFormatter(v, o.unit)) },
      splitLine: { show: true, lineStyle: { color: SPLIT } },
    },
    series: series.map((s, i) => gradientSeries(s.name, s.color || (colors && colors[i]) || COLORS.cyan, s.data, chartType === 'area' || (chartType === 'line' && o.area !== false))),
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

// 地图热力散点 + 流量动线 option 工厂。
// data: { points:[{name,requests,bytes}], deployPoints:[{name,requests}], lines:[{fromName,toName,value}] }
// scope: 'cn' 中国省份地图 | 'world' 世界国家地图。调用前需先执行 registerMaps()。
export function mapGeoOption(scope, data) {
  const o = data || {}
  const isWorld = scope === 'world'
  const map = isWorld ? 'world' : 'china'
  const series = []

  // 访问动线（中心 → 来源地），geo 坐标系下 lines 需要经纬度 coords
  if (o.lines && o.lines.length) {
    const lineData = o.lines
      .map((l) => {
        const f = geoCoord(scope, { name: l.from, countryEn: l.fromEn })
        const t = geoCoord(scope, { name: l.to, countryEn: l.toEn })
        if (!f || !t) return null
        return { fromName: l.fromName, toName: l.toName, coords: [f, t], value: l.value }
      })
      .filter(Boolean)
    if (lineData.length) {
      series.push({
        name: '访问动线',
        type: 'lines',
        coordinateSystem: 'geo',
        zlevel: 2,
        effect: { show: true, period: 4, trailLength: 0.25, symbol: 'arrow', symbolSize: 4, color: '#22d3ee' },
        lineStyle: { color: 'rgba(34,211,238,0.45)', width: 1, curveness: 0.3 },
        data: lineData,
      })
    }
  }

  // 访问来源地理散点：value 前两位必须是经纬度，否则无法在 geo 上定位
  if (o.points && o.points.length) {
    const pts = o.points
      .map((p) => {
        const c = geoCoord(scope, p)
        if (!c) return null
        return { name: p.name, value: [...c, p.requests, p.bytes] }
      })
      .filter(Boolean)
    if (pts.length) {
      series.push({
        name: '访问来源',
        type: 'effectScatter',
        coordinateSystem: 'geo',
        zlevel: 2,
        rippleEffect: { brushType: 'stroke', scale: 3 },
        symbolSize: (val) => Math.max(6, Math.min(28, Math.sqrt(val[2] || 1) * 3)),
        itemStyle: { color: '#f59e0b', shadowBlur: 14, shadowColor: '#f59e0b' },
        label: { show: false },
        data: pts,
      })
    }
  }

  // 数据中心/部署点
  if (o.deployPoints && o.deployPoints.length) {
    const dps = o.deployPoints
      .map((p) => {
        const c = geoCoord(scope, p)
        if (!c) return null
        return { name: p.name, value: [...c, p.requests || 0] }
      })
      .filter(Boolean)
    if (dps.length) {
      series.push({
        name: '数据中心',
        type: 'effectScatter',
        coordinateSystem: 'geo',
        zlevel: 3,
        symbol: 'pin',
        symbolSize: 26,
        symbolOffset: [0, '-50%'],
        rippleEffect: { brushType: 'stroke', scale: 3.5, period: 3 },
        itemStyle: { color: '#22c55e', shadowBlur: 18, shadowColor: '#22c55e' },
        label: {
          show: true,
          formatter: '{b}',
          position: 'top',
          distance: 8,
          color: '#d1fae5',
          fontSize: 12,
          fontWeight: 600,
          backgroundColor: 'rgba(6,32,20,0.75)',
          borderColor: 'rgba(34,197,94,0.6)',
          borderWidth: 1,
          borderRadius: 3,
          padding: [3, 6],
        },
        data: dps,
      })
    }
  }

  return {
    tooltip: {
      trigger: 'item',
      backgroundColor: 'rgba(11,17,32,0.92)',
      borderColor: 'rgba(34,211,238,0.3)',
      textStyle: { color: '#e5edf7', fontSize: 12 },
      confine: true,
      formatter: (p) => {
        if (p.seriesType === 'lines') {
          return `${p.data.fromName} → ${p.data.toName}<br/>请求量: ${p.data.value}`
        }
        if (p.seriesName === '数据中心') {
          const v = p.data.value || []
          return `${p.name}<br/>服务器所在地<br/>本地请求量: ${v[2] || 0}`
        }
        if (p.seriesType === 'effectScatter') {
          const v = p.data.value || []
          return `${p.name}<br/>请求量: ${v[2] || 0}<br/>流量: ${rateShort(v[3] || 0)}`
        }
        return p.name || ''
      },
    },
    geo: {
      map,
      roam: false,
      zoom: isWorld ? 1.18 : 1.0,
      layoutCenter: ['50%', '50%'],
      layoutSize: isWorld ? '96%' : '92%',
      itemStyle: {
        areaColor: 'rgba(59,130,246,0.12)',
        borderColor: 'rgba(34,211,238,0.35)',
        borderWidth: 0.8,
      },
      emphasis: {
        label: { show: false },
        itemStyle: { areaColor: 'rgba(34,211,238,0.25)' },
      },
      // 部署点所在区域底色高亮；世界地图的 GeoJSON 区域名为英文，需用 countryEn
      regions: o.deployPoints && o.deployPoints[0]
        ? [{
            name: isWorld
              ? o.deployPoints[0].countryEn || o.deployPoints[0].name
              : o.deployPoints[0].name,
            itemStyle: { areaColor: 'rgba(34,197,94,0.18)' },
          }]
        : [],
    },
    series,
  }
}

export { echarts }
