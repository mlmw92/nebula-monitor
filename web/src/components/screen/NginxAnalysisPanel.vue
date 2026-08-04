<template>
  <div class="na-panel">
    <div v-if="!accessEnabled" class="na-hint glass">
      未启用 Nginx 访问日志分析，当前展示 Nginx 实例指标。如需 Top URI / Top IP / 状态码分布 / 来源地理分布，请在 Agent 开启
      <b>NginxLog</b> 并为实例配置 <b>accessLog</b> 路径。
    </div>

    <!-- 左侧：趋势 / 状态码 / Top 排行 -->
    <div class="na-left" :class="{ full: !accessEnabled }">
      <div class="glass na-card">
        <div class="nc-head">
          <span>请求趋势</span>
          <span class="nc-stat">{{ accessEnabled ? rateShort(totalRate) + ' /s' : nginxInstances.length + ' 实例' }}</span>
        </div>
        <div ref="trendChart" class="nc-chart"></div>
      </div>
      <div class="glass na-card">
        <div class="nc-head">
          <span>{{ accessEnabled ? '状态码分布' : '实例活跃连接' }}</span>
          <span class="nc-stat" v-if="accessEnabled">{{ totalRequests }} 次</span>
        </div>
        <div ref="statusChart" class="nc-chart"></div>
      </div>
      <div class="glass na-card na-list">
        <div class="nc-head"><span>{{ accessEnabled ? 'Top URI' : '实例 Top · 活跃连接' }}</span></div>
        <div class="rank-list">
          <div class="rank-row" v-for="(u, i) in uriList" :key="u.name">
            <span class="rk-idx" :class="{ top: i < 3 }">{{ i + 1 }}</span>
            <span class="rk-name" :title="u.name">{{ u.name }}</span>
            <span class="rk-bar"><i :style="{ width: uriPct(u) }"></i></span>
            <span class="rk-val">{{ fmtNum(u.count) }}</span>
          </div>
          <div class="nc-empty" v-if="!uriList.length">暂无数据</div>
        </div>
      </div>
      <div class="glass na-card na-list">
        <div class="nc-head"><span>{{ accessEnabled ? 'Top IP 来源' : '实例 Top · 请求数' }}</span></div>
        <div class="rank-list">
          <div class="rank-row" v-for="(ip, i) in ipList" :key="ip.name">
            <span class="rk-idx" :class="{ top: i < 3 }">{{ i + 1 }}</span>
            <span class="rk-name" :title="ip.name">{{ ip.name }}</span>
            <span class="rk-bar"><i :style="{ width: ipPct(ip) }"></i></span>
            <span class="rk-val">{{ fmtNum(ip.count) }}</span>
          </div>
          <div class="nc-empty" v-if="!ipList.length">暂无数据</div>
        </div>
      </div>
    </div>

    <!-- 右侧：地理分布 + 来源 Top / 实例概览 -->
    <div class="na-right">
      <GeoMap v-if="accessEnabled" v-model:scope="geoScope" :data="geoData[geoScope]" />
      <div v-else class="glass na-card na-inst">
        <div class="nc-head"><span>Nginx 实例概览</span></div>
        <div class="rank-list">
          <div class="rank-row inst" v-for="it in nginxInstances" :key="it.instance">
            <span class="rk-name">{{ it.name || it.instance }}</span>
            <span :class="['rk-badge', it.up ? 'on' : 'off']">{{ it.up ? '在线' : '离线' }}</span>
            <span class="rk-sub">活跃 {{ fmtNum(it.activeConnections) }}</span>
            <span class="rk-sub">请求 {{ fmtNum(it.requests) }}</span>
          </div>
          <div class="nc-empty" v-if="!nginxInstances.length">暂无数据</div>
        </div>
      </div>
      <div class="glass na-sources">
        <div class="nc-head">
          <span>{{ accessEnabled ? '来源地 Top · ' + (geoScope === 'cn' ? '省份' : '国家') : '实例请求 Top' }}</span>
        </div>
        <div class="rank-list src">
          <div class="rank-row" v-for="(s, i) in sourceList" :key="s.name">
            <span class="rk-idx" :class="{ top: i < 3 }">{{ i + 1 }}</span>
            <span class="rk-name">{{ s.name }}</span>
            <span class="rk-bar"><i :style="{ width: srcPct(s) }"></i></span>
            <span class="rk-val">{{ fmtNum(s.requests) }}</span>
          </div>
          <div class="nc-empty" v-if="!sourceList.length">暂无数据</div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import http from '../../api/http'
import { initChart, monitorOption, COLORS, rateShort } from '../../charts/echarts'
import { queryClusterTrend } from './useTrend'
import GeoMap from './GeoMap.vue'

const props = defineProps({
  nodes: { type: Array, default: () => [] }, // 在线节点名数组，用于趋势查询
})

const trendChart = ref(null)
const statusChart = ref(null)
let trend = null
let status = null
let ro = null
let timer = null

const summary = ref(null)
const geoData = ref({ cn: null, world: null })
const geoScope = ref('cn')
const nginxInstances = ref([])

// 是否已启用 Nginx 访问日志分析（access log 数据是否有）
const accessEnabled = computed(() => {
  const s = summary.value || {}
  return !!(
    (s.topUris && s.topUris.length) ||
    (s.topIps && s.topIps.length) ||
    s.totalRequests ||
    (s.statusCounts && Object.keys(s.statusCounts).length)
  )
})

const totalRequests = computed(() => summary.value?.totalRequests || 0)
const totalRate = computed(() => summary.value?.totalRate || 0)
const topUris = computed(() => summary.value?.topUris || [])
const topIps = computed(() => summary.value?.topIps || [])
const statusCounts = computed(() => {
  const m = summary.value?.statusCounts || {}
  return Object.keys(m)
    .map((k) => ({ code: k, count: m[k] }))
    .sort((a, b) => a.code.localeCompare(b.code))
})

// 未启用访问日志分析时，用 Nginx 实例指标兜底展示
const instByConns = computed(() =>
  [...nginxInstances.value].sort((a, b) => (b.activeConnections || 0) - (a.activeConnections || 0)).slice(0, 10)
)
const instByReqs = computed(() =>
  [...nginxInstances.value].sort((a, b) => (b.requests || 0) - (a.requests || 0)).slice(0, 10)
)
const uriList = computed(() =>
  accessEnabled.value
    ? topUris.value
    : instByConns.value.map((i) => ({ name: i.name || i.instance, count: i.activeConnections || 0 }))
)
const ipList = computed(() =>
  accessEnabled.value
    ? topIps.value
    : instByReqs.value.map((i) => ({ name: i.instance, count: i.requests || 0 }))
)
const statusData = computed(() =>
  accessEnabled.value && statusCounts.value.length
    ? statusCounts.value
    : instByConns.value.map((i) => ({ code: i.name || i.instance, count: i.activeConnections || 0 }))
)
const sourceList = computed(() =>
  accessEnabled.value
    ? (geoData.value[geoScope.value]?.points || []).filter((p) => p.name && p.name !== 'Reserved' && p.name !== '0').slice(0, 10)
    : instByReqs.value.map((i) => ({ name: i.name || i.instance, requests: i.requests || 0 }))
)

function fmtNum(v) {
  if (v == null) return '-'
  if (v >= 1e6) return (v / 1e6).toFixed(1) + 'M'
  if (v >= 1e3) return (v / 1e3).toFixed(1) + 'k'
  return String(Math.round(v))
}

const maxUri = computed(() => Math.max(...uriList.value.map((u) => u.count || 0), 1))
const maxIp = computed(() => Math.max(...ipList.value.map((i) => i.count || 0), 1))
const maxSrc = computed(() => Math.max(...sourceList.value.map((s) => s.requests || 0), 1))

function uriPct(u) {
  return ((u.count / maxUri.value) * 100).toFixed(1) + '%'
}
function ipPct(ip) {
  return ((ip.count / maxIp.value) * 100).toFixed(1) + '%'
}
function srcPct(s) {
  return ((s.requests / maxSrc.value) * 100).toFixed(1) + '%'
}

async function loadSummary() {
  try {
    summary.value = await http.get('/api/v1/middleware/nginx/access/summary')
  } catch (e) {
    console.error('nginx access summary 加载失败', e)
  }
}

async function loadInstances() {
  try {
    nginxInstances.value = await http.get('/api/v1/middleware/nginx/instances')
  } catch (e) {
    nginxInstances.value = []
  }
}

async function loadGeo(scope) {
  try {
    const data = await http.get(`/api/v1/middleware/nginx/access/geo?scope=${scope}`)
    geoData.value[scope] = data
  } catch (e) {
    console.error('nginx access geo 加载失败', scope, e)
  }
}

async function loadTrends() {
  const list = props.nodes || []
  if (!list.length) return
  let reqMetric = 'nginx_access_requests'
  let byteMetric = 'nginx_access_bytes'
  let name1 = '请求数'
  let name2 = '流量'
  if (!accessEnabled.value) {
    reqMetric = 'nginx_requests'
    byteMetric = 'nginx_connections_active'
    name1 = '请求数'
    name2 = '活跃连接'
  }
  const [req, bytes] = await Promise.all([
    queryClusterTrend(list, reqMetric, 'sum'),
    queryClusterTrend(list, byteMetric, 'sum'),
  ])
  trend &&
    trend.setOption(
      monitorOption({
        yMin: 0,
        yFormatter: (v) => fmtNum(v),
        series: [
          { name: name1, color: COLORS.cyan, data: req },
          { name: name2, color: COLORS.purple, data: bytes },
        ],
      }),
      true
    )
}

function renderStatus() {
  if (!status) return
  const data = statusData.value
  status.setOption(
    {
      tooltip: {
        trigger: 'axis',
        backgroundColor: 'rgba(11,17,32,0.92)',
        borderColor: 'rgba(34,211,238,0.3)',
        textStyle: { color: '#e5edf7', fontSize: 12 },
      },
      grid: { top: 24, left: 44, right: 12, bottom: 26 },
      xAxis: {
        type: 'category',
        data: data.map((d) => d.code),
        axisLine: { lineStyle: { color: 'rgba(159,179,200,0.35)' } },
        axisLabel: { color: 'rgba(159,179,200,0.8)', fontSize: 11 },
      },
      yAxis: {
        type: 'value',
        min: 0,
        axisLabel: { color: 'rgba(159,179,200,0.8)', fontSize: 11, formatter: (v) => fmtNum(v) },
        splitLine: { lineStyle: { color: 'rgba(255,255,255,0.06)' } },
      },
      series: [
        {
          name: '数量',
          type: 'bar',
          barMaxWidth: 26,
          data: data.map((d) => d.count),
          itemStyle: {
            borderRadius: [4, 4, 0, 0],
            color: {
              type: 'linear',
              x: 0, y: 0, x2: 0, y2: 1,
              colorStops: [
                { offset: 0, color: '#22d3ee' },
                { offset: 1, color: 'rgba(34,211,238,0.15)' },
              ],
            },
          },
        },
      ],
    },
    true
  )
}

async function refresh() {
  await loadSummary()
  await loadInstances()
  await loadTrends()
  renderStatus()
  await Promise.all([loadGeo('cn'), loadGeo('world')])
}

onMounted(() => {
  trend = initChart(trendChart.value)
  status = initChart(statusChart.value)
  refresh()
  timer = setInterval(refresh, 30000)
  ro = new ResizeObserver(() => {
    trend && trend.resize()
    status && status.resize()
  })
  ro.observe(trendChart.value)
  ro.observe(statusChart.value)
})
onUnmounted(() => {
  clearInterval(timer)
  ro && ro.disconnect()
  trend && trend.dispose()
  status && status.dispose()
  trend = null
  status = null
})
</script>

<style scoped>
.na-panel {
  display: grid;
  grid-template-columns: 42% 1fr;
  gap: 12px;
  height: 100%;
  min-height: 0;
}
.na-hint {
  grid-column: 1 / -1;
  padding: 8px 12px;
  font-size: 12px;
  color: var(--text-dim);
  line-height: 1.5;
}
.na-hint b {
  color: var(--accent);
  font-weight: 600;
}
.na-left {
  display: grid;
  grid-template-columns: 1fr 1fr;
  grid-template-rows: 1fr 1fr;
  gap: 12px;
  min-height: 0;
}
.na-left.full {
  grid-template-rows: 0.9fr 1.1fr 1.1fr 1.1fr;
}
.na-card {
  display: flex;
  flex-direction: column;
  padding: 10px 12px 6px;
  min-height: 0;
}
.na-card.na-list {
  padding-bottom: 10px;
}
.nc-head {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  font-size: 12px;
  color: var(--text-dim);
  margin-bottom: 4px;
  letter-spacing: 0.03em;
}
.nc-stat {
  font-family: var(--mono);
  color: var(--accent);
  font-size: 13px;
  font-weight: 700;
}
.nc-chart {
  flex: 1;
  min-height: 0;
  width: 100%;
}
.nc-empty {
  padding: 16px 0;
  text-align: center;
  color: var(--text-muted);
  font-size: 12px;
}

/* 排行列表 */
.rank-list {
  flex: 1;
  overflow-y: auto;
  min-height: 0;
}
.rank-row {
  display: grid;
  grid-template-columns: 18px 1fr 64px;
  align-items: center;
  gap: 8px;
  padding: 4px 2px;
  font-size: 12px;
}
.rank-row + .rank-row {
  border-top: 1px solid rgba(255, 255, 255, 0.04);
}
.rank-row.inst {
  grid-template-columns: 1fr 56px 72px 72px;
}
.rk-idx {
  width: 16px;
  height: 16px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 4px;
  font-size: 10px;
  color: var(--text-dim);
  background: rgba(255, 255, 255, 0.05);
}
.rk-idx.top {
  background: var(--accent-dim);
  color: var(--accent);
}
.rk-name {
  color: var(--text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.rk-sub {
  font-size: 10px;
  color: var(--text-muted);
  text-align: right;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.rk-badge {
  font-size: 10px;
  text-align: center;
  border-radius: 4px;
  padding: 1px 0;
}
.rk-badge.on {
  color: #22d3ee;
  background: rgba(34, 211, 238, 0.12);
}
.rk-badge.off {
  color: #f87171;
  background: rgba(248, 113, 113, 0.12);
}
.rk-bar {
  height: 4px;
  border-radius: 2px;
  background: rgba(255, 255, 255, 0.05);
  overflow: hidden;
}
.rk-bar i {
  display: block;
  height: 100%;
  border-radius: 2px;
  background: linear-gradient(90deg, rgba(34, 211, 238, 0.4), #22d3ee);
}
.rk-val {
  font-family: var(--mono);
  font-size: 11px;
  color: var(--text);
  text-align: right;
  min-width: 44px;
}

/* 右侧 */
.na-right {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-height: 0;
}
.na-sources {
  flex-shrink: 0;
  padding: 10px 12px;
  max-height: 200px;
  display: flex;
  flex-direction: column;
}
.na-sources .rank-list {
  overflow-y: auto;
}
.na-inst {
  flex: 1;
  padding: 10px 12px;
  min-height: 0;
}
</style>
