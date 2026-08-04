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
          <span>集群资源趋势</span>
          <span class="cb-cur">{{ trendCur }}</span>
        </div>
        <div ref="trendChart" class="cb-chart"></div>
      </div>

      <div class="glass chart-block">
        <div class="cb-head">
          <i class="cb-bar"></i>
          <span>网络总流量</span>
          <span class="cb-cur" :style="{ color: COLORS.purple }">{{ netCur }}</span>
        </div>
        <div ref="netChart" class="cb-chart"></div>
      </div>
    </div>

    <!-- 底部KPI小卡 -->
    <div class="mini-row">
      <div class="glass mini-card" v-for="m in minis" :key="m.key">
        <div class="mini-label">{{ m.label }}</div>
        <div class="mini-value" :style="{ color: m.color }">{{ m.value }}</div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { initChart, monitorOption, COLORS, rateShort } from '../../charts/echarts'
import { queryClusterTrend } from './useTrend'

const props = defineProps({
  nodes: { type: Array, default: () => [] },
  alerts: { type: Array, default: () => [] },
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
function sum(key) {
  return props.nodes.reduce((s, n) => s + (n[key] || 0), 0)
}
function usageColor(v) {
  if (v >= 90) return COLORS.red
  if (v >= 70) return COLORS.amber
  return COLORS.cyan
}

const kpis = computed(() => {
  const cpu = avg('cpu')
  const mem = avg('mem')
  const disk = avg('disk')
  return [
    { key: 'hosts', label: '监控主机', value: props.nodes.length, unit: '台', color: COLORS.cyan, sub: `在线 ${onlineCount.value} / 离线 ${props.nodes.length - onlineCount.value}` },
    { key: 'cpu', label: 'CPU 均值', value: cpu.toFixed(1), unit: '%', color: usageColor(cpu), sub: `峰值 ${Math.max(...onlineNodes.value.map((n) => n.cpu || 0), 0).toFixed(0)}%` },
    { key: 'mem', label: '内存均值', value: mem.toFixed(1), unit: '%', color: usageColor(mem), sub: `峰值 ${Math.max(...onlineNodes.value.map((n) => n.mem || 0), 0).toFixed(0)}%` },
    { key: 'disk', label: '磁盘均值', value: disk.toFixed(1), unit: '%', color: usageColor(disk), sub: `峰值 ${Math.max(...onlineNodes.value.map((n) => n.disk || 0), 0).toFixed(0)}%` },
  ]
})

const minis = computed(() => {
  const netIn = sum('netIn')
  const netOut = sum('netOut')
  const totalMem = sum('memTotal')
  const load = avg('load')
  const procCount = sum('procCount')
  const activeAlerts = (props.alerts || []).filter((a) => a.state !== 'resolved').length
  return [
    { key: 'netIn', label: '实时入流量', value: rateShort(netIn), color: COLORS.cyan },
    { key: 'netOut', label: '实时出流量', value: rateShort(netOut), color: COLORS.purple },
    { key: 'load', label: '平均负载', value: load.toFixed(2), color: COLORS.blue },
    { key: 'memTotal', label: '内存总量', value: formatBytes(totalMem), color: COLORS.amber },
    { key: 'proc', label: '进程总数', value: procCount, color: COLORS.green },
    { key: 'alert', label: '活跃告警', value: activeAlerts, color: activeAlerts > 0 ? COLORS.red : COLORS.green },
  ]
})

function formatBytes(b) {
  const v = Number(b || 0)
  if (v >= 1 << 30) return (v / (1 << 30)).toFixed(1) + ' GB'
  if (v >= 1 << 20) return (v / (1 << 20)).toFixed(0) + ' MB'
  if (v >= 1 << 10) return (v / (1 << 10)).toFixed(0) + ' KB'
  return v + ' B'
}

async function loadTrends() {
  const list = onlineNodes.value.map((n) => n.name)
  const [cpu, mem, disk, netIn, netOut] = await Promise.all([
    queryClusterTrend(list, 'cpu_usage', 'avg'),
    queryClusterTrend(list, 'mem_used_percent', 'avg'),
    queryClusterTrend(list, 'disk_used_percent', 'avg'),
    queryClusterTrend(list, 'network_recv_rate', 'sum'),
    queryClusterTrend(list, 'network_sent_rate', 'sum'),
  ])
  trendSeries.value = [
    { name: 'CPU', color: COLORS.cyan, data: cpu },
    { name: '内存', color: COLORS.purple, data: mem },
    { name: '磁盘', color: COLORS.amber, data: disk },
  ]
  netSeries.value = [
    { name: '入', color: COLORS.cyan, data: netIn },
    { name: '出', color: COLORS.purple, data: netOut },
  ]
  renderTrend()
  renderNet()
  const lastCpu = cpu.length ? cpu[cpu.length - 1][1] : 0
  const lastMem = mem.length ? mem[mem.length - 1][1] : 0
  trendCur.value = `CPU ${lastCpu.toFixed(1)}% / 内存 ${lastMem.toFixed(1)}%`
  const li = netIn.length ? netIn[netIn.length - 1][1] : 0
  const lo = netOut.length ? netOut[netOut.length - 1][1] : 0
  netCur.value = `↓ ${rateShort(li)} / ↑ ${rateShort(lo)}`
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

watch(() => props.nodes, loadTrends, { deep: false })
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
  grid-template-columns: repeat(4, 1fr);
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
  text-shadow: 0 0 12px currentColor;
}
</style>
