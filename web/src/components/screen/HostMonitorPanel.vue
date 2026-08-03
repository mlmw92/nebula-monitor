<template>
  <div class="host-panel">
    <!-- 左侧：实时状态 + 主机健康 -->
    <div class="left-col">
      <ScreenGauges title="集群实时状态" :items="gaugeItems" />
      <div class="glass host-list">
        <div class="hl-title">主机健康 · 点击下钻</div>
        <div class="hl-head">
          <span>主机</span><span>CPU</span><span>内存</span><span>磁盘</span><span>流量</span>
        </div>
        <div class="hl-body">
          <div class="hl-row" v-for="n in nodes" :key="n.name" @click="drillNode(n.name)">
            <span class="hl-name">
              <i class="dot" :class="n.online ? 'on' : 'off'"></i>{{ n.name }}
            </span>
            <span class="hl-val" :style="pctStyle(n.cpu)">{{ fmtPct(n.cpu) }}</span>
            <span class="hl-val" :style="pctStyle(n.mem)">{{ fmtPct(n.mem) }}</span>
            <span class="hl-val" :style="pctStyle(n.disk)">{{ fmtPct(n.disk) }}</span>
            <span class="hl-val dim">{{ rateShort((n.netIn || 0) + (n.netOut || 0)) }}</span>
          </div>
          <div class="hl-empty" v-if="!nodes.length">暂无主机数据</div>
        </div>
      </div>
    </div>

    <!-- 右侧：集群趋势 2x2 -->
    <div class="right-col">
      <ScreenTrend title="CPU 集群均值" :series="cpuSeries" unit="%" :color="COLORS.cyan" />
      <ScreenTrend title="内存集群均值" :series="memSeries" unit="%" :color="COLORS.blue" />
      <ScreenTrend title="磁盘集群均值" :series="diskSeries" unit="%" :color="COLORS.green" />
      <div class="glass trend-net">
        <div class="trend-head">
          <span class="trend-title">网络流量（入 / 出）</span>
          <span class="trend-cur" :style="{ color: COLORS.purple }">{{ netCur }}</span>
        </div>
        <div ref="netChart" class="trend-chart"></div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { initChart, monitorOption, COLORS, rateShort } from '../../charts/echarts'
import ScreenGauges from './ScreenGauges.vue'
import ScreenTrend from './ScreenTrend.vue'
import { queryClusterTrend } from './useTrend'

const props = defineProps({
  nodes: { type: Array, default: () => [] },
})

const router = useRouter()
const netChart = ref(null)
let chart = null
let ro = null
let timer = null

// ---- 实时状态（集群均值） ----
function avg(key) {
  const list = props.nodes.filter((n) => n.online !== false)
  if (!list.length) return 0
  return list.reduce((s, n) => s + (n[key] || 0), 0) / list.length
}
function usageColor(v) {
  if (v >= 90) return 'var(--danger)'
  if (v >= 70) return 'var(--warn)'
  return 'var(--accent)'
}
const gaugeItems = computed(() => {
  const cpu = avg('cpu')
  const mem = avg('mem')
  const disk = avg('disk')
  const netIn = props.nodes.reduce((s, n) => s + (n.netIn || 0), 0)
  return [
    { key: 'cpu', label: 'CPU 使用率', value: cpu, text: cpu.toFixed(1) + '%', color: usageColor(cpu) },
    { key: 'mem', label: '内存使用率', value: mem, text: mem.toFixed(1) + '%', color: usageColor(mem) },
    { key: 'disk', label: '磁盘使用率', value: disk, text: disk.toFixed(1) + '%', color: usageColor(disk) },
    { key: 'net', label: '实时入流量', value: 100, text: rateShort(netIn), color: 'var(--info)' },
  ]
})

// ---- 主机健康列表 ----
function fmtPct(v) {
  return (v == null ? '--' : (Math.round(v * 10) / 10) + '%')
}
function pctStyle(v) {
  const c = usageColor(v || 0)
  return v >= 70 ? { color: c, fontWeight: 700 } : {}
}
function drillNode(name) {
  router.push('/node/' + encodeURIComponent(name))
}

// ---- 集群趋势 ----
const cpuSeries = ref([])
const memSeries = ref([])
const diskSeries = ref([])
const netSeries = ref([])
const netCur = ref('--')

const onlineNodes = computed(() => props.nodes.filter((n) => n.online !== false).map((n) => n.name))

async function loadTrends() {
  const list = onlineNodes.value
  const [cpu, mem, disk, netIn, netOut] = await Promise.all([
    queryClusterTrend(list, 'cpu_usage', 'avg'),
    queryClusterTrend(list, 'mem_used_percent', 'avg'),
    queryClusterTrend(list, 'disk_used_percent', 'avg'),
    queryClusterTrend(list, 'network_recv_rate', 'sum'),
    queryClusterTrend(list, 'network_sent_rate', 'sum'),
  ])
  cpuSeries.value = [{ name: 'CPU', color: COLORS.cyan, data: cpu }]
  memSeries.value = [{ name: '内存', color: COLORS.blue, data: mem }]
  diskSeries.value = [{ name: '磁盘', color: COLORS.green, data: disk }]
  netSeries.value = [
    { name: '入', color: COLORS.cyan, data: netIn },
    { name: '出', color: COLORS.purple, data: netOut },
  ]
  renderNet()
  const lastIn = netIn.length ? netIn[netIn.length - 1][1] : 0
  const lastOut = netOut.length ? netOut[netOut.length - 1][1] : 0
  netCur.value = `↓ ${rateShort(lastIn)} / ↑ ${rateShort(lastOut)}`
}

function renderNet() {
  if (!chart) return
  chart.setOption(
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
  chart = initChart(netChart.value)
  loadTrends()
  timer = setInterval(loadTrends, 30000)
  ro = new ResizeObserver(() => chart && chart.resize())
  ro.observe(netChart.value)
})
onUnmounted(() => {
  clearInterval(timer)
  ro && ro.disconnect()
  chart && chart.dispose()
  chart = null
})

watch(() => props.nodes, loadTrends, { deep: false })
</script>

<style scoped>
.host-panel {
  display: grid;
  grid-template-columns: 400px 1fr;
  gap: 12px;
  min-height: 0;
  height: 100%;
}
.left-col {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-height: 0;
}

/* 主机健康列表 */
.host-list {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  padding: 10px 12px;
}
.hl-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 8px;
  letter-spacing: 0.04em;
}
.hl-head,
.hl-row {
  display: grid;
  grid-template-columns: 1.6fr 0.7fr 0.7fr 0.7fr 0.9fr;
  gap: 6px;
  align-items: center;
  font-size: 12px;
}
.hl-head {
  color: var(--text-muted);
  padding: 4px 8px;
  border-bottom: 1px solid var(--border);
  margin-bottom: 2px;
}
.hl-body {
  flex: 1;
  overflow-y: auto;
  min-height: 0;
}
.hl-row {
  padding: 6px 8px;
  border-radius: 6px;
  cursor: pointer;
  transition: background 0.15s, transform 0.15s;
}
.hl-row:hover {
  background: var(--accent-dim);
  transform: translateX(2px);
}
.hl-name {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  flex-shrink: 0;
}
.dot.on {
  background: var(--chart-green);
  box-shadow: 0 0 6px var(--chart-green);
}
.dot.off {
  background: var(--text-muted);
}
.hl-val {
  font-family: var(--mono);
  color: var(--text);
  text-align: right;
}
.hl-val.dim {
  color: var(--text-dim);
  font-size: 11px;
}
.hl-empty {
  padding: 18px 0;
  text-align: center;
  color: var(--text-muted);
  font-size: 12px;
}

/* 右侧趋势 */
.right-col {
  display: grid;
  grid-template-columns: 1fr 1fr;
  grid-template-rows: 1fr 1fr;
  gap: 12px;
  min-height: 0;
}
.trend-net {
  display: flex;
  flex-direction: column;
  padding: 10px 12px 6px;
  min-height: 0;
}
.trend-head {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  margin-bottom: 4px;
}
.trend-title {
  font-size: 12px;
  color: var(--text-dim);
  letter-spacing: 0.03em;
}
.trend-cur {
  font-size: 14px;
  font-weight: 700;
  font-family: var(--mono);
}
.trend-chart {
  flex: 1;
  min-height: 0;
  width: 100%;
}
</style>
