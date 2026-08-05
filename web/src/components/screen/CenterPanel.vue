<template>
  <div class="center-panel">
    <!-- 顶部大KPI -->
    <div class="kpi-row">
      <div class="kpi-card glass" v-for="k in kpis" :key="k.key">
        <div class="kpi-label">{{ k.label }}</div>
        <div class="kpi-value" :style="{ color: k.color }">
          <span class="kpi-num">{{ k.value }}</span>
          <span class="kpi-unit" v-if="k.unit">{{ k.unit }}</span>
        </div>
        <div class="kpi-sub" v-if="k.sub">{{ k.sub }}</div>
      </div>
    </div>

    <!-- 中部图表区 -->
    <div class="chart-area">
      <div class="glass chart-block">
        <div class="cb-head">
          <i class="cb-bar"></i>
          <span>集群资源平均趋势</span>
          <span class="cb-cur">{{ trendCur }}</span>
        </div>
        <div ref="trendChart" class="cb-chart"></div>
      </div>

      <div class="glass chart-block">
        <div class="cb-head">
          <i class="cb-bar"></i>
          <span>网络流量（按主机）</span>
          <span class="cb-cur" :style="{ color: COLORS.purple }">{{ netCur }}</span>
        </div>
        <div ref="netChart" class="cb-chart"></div>
      </div>
    </div>

    <!-- 底部KPI小卡 -->
    <div class="mini-row">
      <div class="glass mini-card" v-for="m in minis" :key="m.key">
        <div class="mini-label">{{ m.label }}</div>
        <div class="mini-value" :class="{ wide: String(m.value).length > 8 }" :style="{ color: m.color }">{{ m.value }}</div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { initChart, monitorOption, COLORS, rateShort } from '../../charts/echarts'
import { queryClusterTrend, queryPerNodeTrend } from './useTrend'

// 多主机折线配色：主机数量不定，按索引循环取色，保证同一主机在同一次渲染内颜色稳定
const NODE_COLORS = [COLORS.cyan, COLORS.purple, COLORS.amber, COLORS.green, COLORS.blue, COLORS.red, '#14b8a6', '#f472b6']

const props = defineProps({
  nodes: { type: Array, default: () => [] },
  alerts: { type: Array, default: () => [] },
  healthScore: { type: Number, default: 0 },
  healthLevel: { type: String, default: 'green' }, // green | amber | red
})

const trendChart = ref(null)
const netChart = ref(null)
let tChart = null
let nChart = null
let timer = null
let ro = null

const trendSeries = ref([])
const netSeries = ref([])
const trendCur = ref('')
const netCur = ref('--')

const onlineNodes = computed(() => props.nodes.filter((n) => n.online !== false))
const onlineCount = computed(() => onlineNodes.value.length)

function avg(key) {
  const list = onlineNodes.value
  if (!list.length) return 0
  return list.reduce((s, n) => s + (n[key] || 0), 0) / list.length
}
// 仅累加在线主机：离线主机的最后一次上报值不代表当前状态，计入会虚增总量
function sumOnline(key) {
  return onlineNodes.value.reduce((s, n) => s + (n[key] || 0), 0)
}
function usageColor(v) {
  if (v >= 90) return COLORS.red
  if (v >= 70) return COLORS.amber
  return COLORS.cyan
}
function healthColor(v) {
  if (v >= 80) return COLORS.green
  if (v >= 60) return COLORS.amber
  return COLORS.red
}

const healthLevelText = computed(() => {
  if (props.healthLevel === 'green') return '优秀'
  if (props.healthLevel === 'amber') return '一般'
  return '告警'
})

const kpis = computed(() => {
  const cpu = avg('cpu')
  const mem = avg('mem')
  const disk = avg('disk')
  return [
    { key: 'health', label: '健康评分', value: props.healthScore, unit: '', color: healthColor(props.healthScore), sub: healthLevelText.value },
    { key: 'hosts', label: '监控主机', value: props.nodes.length, unit: '台', color: COLORS.cyan, sub: `在线 ${onlineCount.value} / 离线 ${props.nodes.length - onlineCount.value}` },
    { key: 'cpu', label: 'CPU 均值', value: cpu.toFixed(1), unit: '%', color: usageColor(cpu), sub: `峰值 ${Math.max(...onlineNodes.value.map((n) => n.cpu || 0), 0).toFixed(0)}%` },
    { key: 'mem', label: '内存均值', value: mem.toFixed(1), unit: '%', color: usageColor(mem), sub: `峰值 ${Math.max(...onlineNodes.value.map((n) => n.mem || 0), 0).toFixed(0)}%` },
    { key: 'disk', label: '磁盘均值', value: disk.toFixed(1), unit: '%', color: usageColor(disk), sub: `峰值 ${Math.max(...onlineNodes.value.map((n) => n.disk || 0), 0).toFixed(0)}%` },
  ]
})

const minis = computed(() => {
  const netIn = sumOnline('netIn')
  const netOut = sumOnline('netOut')
  const totalMem = sumOnline('memTotal')
  const usedMem = sumOnline('memUsed')
  const load = avg('load')
  const activeAlerts = (props.alerts || []).filter((a) => a.state === 'firing').length
  return [
    { key: 'netIn', label: '实时入流量', value: rateShort(netIn), color: COLORS.cyan },
    { key: 'netOut', label: '实时出流量', value: rateShort(netOut), color: COLORS.purple },
    { key: 'load', label: '平均负载', value: load.toFixed(2), color: COLORS.blue },
    {
      key: 'memTotal',
      label: '内存已用 / 总量',
      value: `${formatBytes(usedMem, true)} / ${formatBytes(totalMem, true)}`,
      color: COLORS.amber,
    },
    { key: 'alert', label: '活跃告警', value: activeAlerts, color: activeAlerts > 0 ? COLORS.red : COLORS.green },
  ]
})

// compact=true 时省略单位后缀（用于「已用 / 总量」并排展示，仅在末位保留单位）
function formatBytes(b, compact = false) {
  const v = Number(b || 0)
  const unit = (u) => (compact ? u.trim() : u)
  if (v >= 1 << 30) return (v / (1 << 30)).toFixed(1) + unit(' G')
  if (v >= 1 << 20) return (v / (1 << 20)).toFixed(0) + unit(' M')
  if (v >= 1 << 10) return (v / (1 << 10)).toFixed(0) + unit(' K')
  return v + unit(' B')
}

async function loadTrends() {
  // 节点对象由 OverviewTab 组装，主机名字段为 name（兼容可能存在的 hostname 写法）
  const list = onlineNodes.value.map((n) => n.name || n.hostname).filter(Boolean)
  if (!list.length) {
    trendSeries.value = []
    netSeries.value = []
    trendCur.value = '暂无在线主机'
    netCur.value = '--'
    renderTrend()
    renderNet()
    return
  }
  const labelOf = (host) => {
    const n = onlineNodes.value.find((x) => (x.name || x.hostname) === host)
    return n?.displayName || host
  }
  const [cpu, mem, disk, perNodeIn, perNodeOut] = await Promise.all([
    queryClusterTrend(list, 'cpu_usage', 'avg'),
    queryClusterTrend(list, 'mem_used_percent', 'avg'),
    queryClusterTrend(list, 'disk_used_percent', 'avg', { byNode: true }),
    queryPerNodeTrend(list, 'network_recv_rate'),
    queryPerNodeTrend(list, 'network_sent_rate'),
  ])
  trendSeries.value = [
    { name: 'CPU', color: COLORS.cyan, data: cpu },
    { name: '内存', color: COLORS.purple, data: mem },
    { name: '磁盘', color: COLORS.amber, data: disk },
  ]

  // 网络流量按主机分别成线：多套高可用集群（如两组 Nginx）汇总成一条线时无法区分来源，
  // 这里每台主机一条「入+出」合计曲线，并按当前流量从高到低取前 8 台，避免图例过密。
  const outMap = new Map(perNodeOut.map((r) => [r.node, r.points]))
  const perHost = perNodeIn.map((r) => {
    const acc = new Map()
    for (const [ts, v] of r.points) acc.set(ts, (acc.get(ts) || 0) + v)
    for (const [ts, v] of outMap.get(r.node) || []) acc.set(ts, (acc.get(ts) || 0) + v)
    const data = [...acc.entries()].sort((a, b) => a[0] - b[0])
    return { node: r.node, data, last: data.length ? data[data.length - 1][1] : 0 }
  })
  perHost.sort((a, b) => b.last - a.last)
  netSeries.value = perHost.slice(0, 8).map((h, i) => ({
    name: labelOf(h.node),
    color: NODE_COLORS[i % NODE_COLORS.length],
    data: h.data,
  }))

  renderTrend()
  renderNet()
  const lastCpu = cpu.length ? cpu[cpu.length - 1][1] : 0
  const lastMem = mem.length ? mem[mem.length - 1][1] : 0
  const lastDisk = disk.length ? disk[disk.length - 1][1] : 0
  trendCur.value = `CPU ${lastCpu.toFixed(1)}% / 内存 ${lastMem.toFixed(1)}% / 磁盘 ${lastDisk.toFixed(1)}%`
  const totalNow = perHost.reduce((s, h) => s + h.last, 0)
  netCur.value = `合计 ${rateShort(totalNow)} · ${perHost.length} 台`
}

function renderTrend() {
  if (!tChart) return
  tChart.setOption(
    monitorOption({
      yMin: 0,
      yMax: 100,
      yFormatter: (v) => v + '%',
      tipFormatter: (v) => (v == null ? '-' : v.toFixed(1) + '%'),
      series: trendSeries.value.map((s) => ({ name: s.name, color: s.color, data: s.data })),
    }),
    true
  )
}

function renderNet() {
  if (!nChart) return
  nChart.setOption(
    monitorOption({
      yMin: 0,
      // 按主机拆分后曲线较多，关闭面积填充避免互相遮挡
      area: netSeries.value.length <= 2,
      yFormatter: (v) => rateShort(v),
      tipFormatter: (v) => (v == null ? '-' : rateShort(v)),
      series: netSeries.value.map((s) => ({ name: s.name, color: s.color, data: s.data })),
    }),
    true
  )
}

onMounted(() => {
  tChart = initChart(trendChart.value)
  nChart = initChart(netChart.value)
  loadTrends()
  timer = setInterval(loadTrends, 30000)
  ro = new ResizeObserver(() => {
    tChart && tChart.resize()
    nChart && nChart.resize()
  })
  ro.observe(trendChart.value)
  ro.observe(netChart.value)
})
onUnmounted(() => {
  clearInterval(timer)
  ro && ro.disconnect()
  tChart && tChart.dispose()
  nChart && nChart.dispose()
  tChart = null
  nChart = null
})

// nodes 每次指标轮询都会重建数组，只在「在线主机名集合」变化时才重新拉取趋势，避免高频重复查询
watch(
  () => onlineNodes.value.map((n) => n.name || n.hostname).sort().join(','),
  () => loadTrends()
)
</script>

<style scoped>
.center-panel {
  display: flex;
  flex-direction: column;
  height: 100%;
  gap: 10px;
  min-height: 0;
}

/* 顶部大KPI */
.kpi-row {
  display: grid;
  grid-template-columns: repeat(5, 1fr);
  gap: 10px;
  flex-shrink: 0;
}
.kpi-card {
  padding: 14px 18px;
  position: relative;
  overflow: hidden;
}
.kpi-card::after {
  content: '';
  position: absolute;
  right: -20px;
  top: -20px;
  width: 80px;
  height: 80px;
  border-radius: 50%;
  background: var(--accent-glow);
  opacity: 0.08;
  filter: blur(20px);
}
.kpi-label {
  font-size: 12px;
  color: var(--text-muted);
  letter-spacing: 0.06em;
  text-transform: uppercase;
  margin-bottom: 4px;
}
.kpi-value {
  display: flex;
  align-items: baseline;
  gap: 4px;
  line-height: 1;
}
.kpi-num {
  font-size: 38px;
  font-weight: 800;
  font-family: var(--mono);
  text-shadow: 0 0 18px currentColor;
}
.kpi-unit {
  font-size: 14px;
  color: var(--text-muted);
  font-weight: 600;
}
.kpi-sub {
  font-size: 11px;
  color: var(--text-dim);
  margin-top: 4px;
  font-family: var(--mono);
}

/* 中部图表 */
.chart-area {
  display: grid;
  grid-template-rows: 1fr 1fr;
  grid-template-columns: 1fr;
  gap: 10px;
  flex: 1;
  min-height: 0;
}
.chart-block {
  display: flex;
  flex-direction: column;
  padding: 10px 14px;
  min-height: 0;
  overflow: hidden;
}
.cb-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 4px;
  flex-shrink: 0;
}
.cb-bar {
  width: 3px;
  height: 14px;
  background: var(--accent);
  border-radius: 2px;
  box-shadow: 0 0 8px var(--accent-glow);
}
.cb-head span:nth-child(2) {
  font-size: 14px;
  font-weight: 600;
  color: var(--text);
  letter-spacing: 0.04em;
}
.cb-cur {
  margin-left: auto;
  font-size: 12px;
  color: var(--text-muted);
  font-family: var(--mono);
}
.cb-chart {
  flex: 1;
  min-height: 0;
}

/* 底部mini KPI */
.mini-row {
  display: grid;
  grid-template-columns: repeat(6, 1fr);
  gap: 8px;
  flex-shrink: 0;
}
.mini-card {
  padding: 10px 14px;
  text-align: center;
}
.mini-label {
  font-size: 11px;
  color: var(--text-muted);
  margin-bottom: 4px;
  letter-spacing: 0.04em;
}
.mini-value {
  font-size: 20px;
  font-weight: 700;
  font-family: var(--mono);
  white-space: nowrap;
  text-shadow: 0 0 12px currentColor;
}
/* 「已用 / 总量」这类较长文本缩小字号，避免在 1/6 宽度的小卡里溢出 */
.mini-value.wide {
  font-size: 15px;
}
</style>
