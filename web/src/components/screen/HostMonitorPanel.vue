<template>
  <div class="host-panel">
    <!-- 左侧：实时状态 + 主机健康 -->
    <div class="left-col">
      <ScreenGauges title="集群实时状态" :items="gaugeItems" />
      <div class="glass host-list">
        <div class="hl-title">主机健康 · 点击下钻</div>
        <div class="hl-head">
          <span>主机</span><span>CPU</span><span>内存</span><span>磁盘</span><span>负载</span><span>流量</span>
        </div>
        <div class="hl-body">
          <div class="hl-row" v-for="n in nodes" :key="n.hostname" :class="{ offline: !n.online }" @click="drillNode(n.hostname)">
            <span class="hl-name">
              <i class="dot" :class="n.online ? 'on' : 'off'"></i>
              <span v-if="!n.online" class="offline-badge">🔴离线</span>
              <span class="host-text">
                <span class="host-text-name" :title="n.displayName || n.hostname">{{ n.displayName || n.hostname }}</span>
                <span class="host-text-ip" :title="n.ip">{{ n.ip }}</span>
              </span>
            </span>
            <span class="hl-val" :style="pctStyle(n.cpu)">{{ fmtPct(n.cpu) }}</span>
            <span class="hl-val" :style="pctStyle(n.mem)">{{ fmtPct(n.mem) }}</span>
            <span class="hl-cell-sub">
              <span class="hl-val" :style="pctStyle(n.disk)">{{ fmtPct(n.disk) }}</span>
              <span class="hl-sub" :title="'读/写 IOPS（gopsutil 暂不提供磁盘 await 延迟）'">{{ iopsText(n) }}</span>
            </span>
            <span class="hl-val" :class="loadClass(n)" :title="loadTitle(n)">{{ fmtLoad(n.load1) }}</span>
            <span class="hl-cell-sub">
              <span class="hl-val dim">{{ rateShort((n.netIn || 0) + (n.netOut || 0)) }}</span>
              <span class="hl-sub" :title="'丢包 / TCP重传（每秒）'">{{ netText(n) }}</span>
            </span>
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
          <span class="trend-title">网络流量（按主机）</span>
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
import { queryClusterTrend, queryPerNodeTrend } from './useTrend'

// 多主机折线配色：按索引循环取色
const NODE_COLORS = [COLORS.cyan, COLORS.purple, COLORS.amber, COLORS.green, COLORS.blue, COLORS.red, '#14b8a6', '#f472b6']

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
// 存在离线主机时，集群均值仅统计在线主机，标签同步标注
const hasOffline = computed(() => props.nodes.some((n) => n.online === false))
const avgSuffix = computed(() => (hasOffline.value ? '（仅在线）' : ''))
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
    { key: 'cpu', label: 'CPU 使用率' + avgSuffix.value, value: cpu, text: cpu.toFixed(1) + '%', color: usageColor(cpu) },
    { key: 'mem', label: '内存使用率' + avgSuffix.value, value: mem, text: mem.toFixed(1) + '%', color: usageColor(mem) },
    { key: 'disk', label: '磁盘使用率' + avgSuffix.value, value: disk, text: disk.toFixed(1) + '%', color: usageColor(disk) },
    { key: 'net', label: '实时入流量', type: 'text', text: rateShort(netIn), color: 'var(--info)' },
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

// ---- 列表单元格辅助展示 ----
function fmtLoad(v) {
  return v == null ? '--' : (Math.round(v * 100) / 100).toFixed(2)
}
function loadTitle(n) {
  return `1分钟: ${(n.load1 || 0).toFixed(2)} / 5分钟: ${(n.load5 || 0).toFixed(2)} / 15分钟: ${(n.load15 || 0).toFixed(2)}`
}
function loadClass(n) {
  const v = n.load1 || 0
  if (v >= 10) return { color: 'var(--danger)', fontWeight: 700 }
  if (v >= 5) return { color: 'var(--warn)' }
  return {}
}
function iopsText(n) {
  return `${Math.round(n.diskIopsR || 0)}/${Math.round(n.diskIopsW || 0)} IOPS`
}
function netText(n) {
  const drop = n.netDrop || 0
  const retrans = n.tcpRetrans || 0
  return `丢${drop < 0.05 ? '0' : drop.toFixed(1)} 重${retrans < 0.05 ? '0' : retrans.toFixed(1)}`
}

// ---- 集群趋势 ----
const cpuSeries = ref([])
const memSeries = ref([])
const diskSeries = ref([])
const netSeries = ref([])
const netCur = ref('--')

const onlineNodes = computed(() => props.nodes.filter((n) => n.online !== false))
const onlineHosts = computed(() => onlineNodes.value.map((n) => n.hostname || n.name).filter(Boolean))

async function loadTrends() {
  const list = onlineHosts.value
  if (!list.length) {
    cpuSeries.value = []
    memSeries.value = []
    diskSeries.value = []
    netSeries.value = []
    netCur.value = '暂无在线主机'
    renderNet()
    return
  }
  const labelOf = (host) => {
    const n = onlineNodes.value.find((x) => (x.hostname || x.name) === host)
    return n?.displayName || host
  }
  const [cpu, mem, disk, perNodeIn, perNodeOut] = await Promise.all([
    queryClusterTrend(list, 'cpu_usage', 'avg'),
    queryClusterTrend(list, 'mem_used_percent', 'avg'),
    queryClusterTrend(list, 'disk_used_percent', 'avg', { byNode: true }),
    queryPerNodeTrend(list, 'network_recv_rate'),
    queryPerNodeTrend(list, 'network_sent_rate'),
  ])
  cpuSeries.value = [{ name: 'CPU', color: COLORS.cyan, data: cpu }]
  memSeries.value = [{ name: '内存', color: COLORS.blue, data: mem }]
  diskSeries.value = [{ name: '磁盘', color: COLORS.green, data: disk }]

  // 网络按主机分列：同一角色的多台主机（如两套高可用 Nginx）汇总成一条线时无法区分来源
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

  renderNet()
  const total = perHost.reduce((s, h) => s + h.last, 0)
  netCur.value = `合计 ${rateShort(total)} · ${perHost.length} 台`
}

function renderNet() {
  if (!chart) return
  chart.setOption(
    monitorOption({
      yMin: 0,
      // 多主机曲线叠加时关闭面积填充，避免互相遮挡
      area: netSeries.value.length <= 2,
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

// nodes 每次指标轮询都会重建数组，只在「在线主机名集合」变化时才重新拉取，避免高频重复查询
watch(
  () => onlineHosts.value.slice().sort().join(','),
  () => loadTrends()
)
</script>

<style scoped>
.host-panel {
  display: grid;
  grid-template-columns: minmax(360px, 24%) 1fr;
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
  grid-template-columns: 1.6fr 0.6fr 0.6fr 0.8fr 0.55fr 0.9fr;
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
  white-space: nowrap;
}
.host-text {
  display: flex;
  flex-direction: column;
  min-width: 0;
  line-height: 1.25;
}
.host-text-name {
  overflow: hidden;
  text-overflow: ellipsis;
}
.host-text-ip {
  font-size: 10px;
  color: var(--text-dim);
  font-family: var(--mono);
  overflow: hidden;
  text-overflow: ellipsis;
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
.hl-row.offline {
  background: rgba(239, 68, 68, 0.12);
}
.hl-row.offline:hover {
  background: rgba(239, 68, 68, 0.2);
}
.hl-row.offline .host-text-name {
  color: var(--text-muted);
}
.offline-badge {
  font-size: 10px;
  color: #fff;
  background: var(--danger, #ef4444);
  border-radius: 4px;
  padding: 0 4px;
  white-space: nowrap;
  flex-shrink: 0;
}
.hl-cell-sub {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 1px;
  line-height: 1.15;
}
.hl-sub {
  font-size: 10px;
  color: var(--text-dim);
  font-family: var(--mono);
  white-space: nowrap;
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

/* ============ 宽屏 / 4K 适配 ============ */
@media (min-width: 2400px) {
  .host-panel { gap: 18px; grid-template-columns: minmax(420px, 22%) 1fr; }
  .left-col { gap: 18px; }
  .host-list { padding: 16px 18px; }
  .hl-title { font-size: 17px; margin-bottom: 12px; }
  .hl-head, .hl-row { gap: 10px; font-size: 15px; }
  .hl-head { padding: 6px 12px; }
  .hl-row { padding: 9px 12px; }
  .hl-name { font-size: 15px; gap: 8px; }
  .hl-val { font-size: 15px; }
  .hl-val.dim { font-size: 14px; }
  .host-text-ip { font-size: 12px; }
  .dot { width: 9px; height: 9px; }
  .hl-empty { padding: 26px 0; font-size: 15px; }
  .right-col { gap: 18px; }
  .trend-net { padding: 16px 18px 10px; }
  .trend-title { font-size: 15px; }
  .trend-cur { font-size: 19px; }
}
@media (min-width: 3440px) {
  .host-panel { gap: 24px; grid-template-columns: minmax(520px, 22%) 1fr; }
  .left-col { gap: 24px; }
  .host-list { padding: 20px 22px; }
  .hl-title { font-size: 22px; margin-bottom: 16px; }
  .hl-head, .hl-row { gap: 14px; font-size: 19px; }
  .hl-head { padding: 8px 16px; }
  .hl-row { padding: 12px 16px; }
  .hl-name { font-size: 19px; gap: 10px; }
  .hl-val { font-size: 19px; }
  .hl-val.dim { font-size: 18px; }
  .host-text-ip { font-size: 15px; }
  .dot { width: 11px; height: 11px; }
  .hl-empty { padding: 34px 0; font-size: 19px; }
  .right-col { gap: 24px; }
  .trend-net { padding: 20px 24px 14px; }
  .trend-title { font-size: 19px; }
  .trend-cur { font-size: 25px; }
}
</style>
