<template>
  <div class="na-panel">
    <!-- 左侧：趋势 / 状态码 / Top 排行 -->
    <div class="na-left">
      <div class="glass na-card">
        <div class="nc-head">
          <span>访问量 / 流量趋势</span>
          <span class="nc-stat">{{ rateShort(totalRate) }} /s</span>
        </div>
        <div ref="trendChart" class="nc-chart"></div>
      </div>
      <div class="glass na-card">
        <div class="nc-head"><span>状态码分布</span><span class="nc-stat">{{ totalRequests }} 次</span></div>
        <div ref="statusChart" class="nc-chart"></div>
      </div>
      <div class="glass na-card na-list">
        <div class="nc-head"><span>Top URI</span></div>
        <div class="rank-list">
          <div class="rank-row" v-for="(u, i) in topUris" :key="u.name">
            <span class="rk-idx" :class="{ top: i < 3 }">{{ i + 1 }}</span>
            <span class="rk-name" :title="u.name">{{ u.name }}</span>
            <span class="rk-bar"><i :style="{ width: uriPct(u) }"></i></span>
            <span class="rk-val">{{ fmtNum(u.count) }}</span>
          </div>
          <div class="nc-empty" v-if="!topUris.length">暂无数据</div>
        </div>
      </div>
      <div class="glass na-card na-list">
        <div class="nc-head"><span>Top IP 来源</span></div>
        <div class="rank-list">
          <div class="rank-row" v-for="(ip, i) in topIps" :key="ip.ip">
            <span class="rk-idx" :class="{ top: i < 3 }">{{ i + 1 }}</span>
            <span class="rk-name" :title="ip.ip">{{ ip.ip }}</span>
            <span class="rk-sub">{{ ip.province || ip.country || '--' }}</span>
            <span class="rk-bar"><i :style="{ width: ipPct(ip) }"></i></span>
            <span class="rk-val">{{ fmtNum(ip.requests) }}</span>
          </div>
          <div class="nc-empty" v-if="!topIps.length">暂无数据</div>
        </div>
      </div>
    </div>

    <!-- 右侧：地理分布 + 来源 Top -->
    <div class="na-right">
      <GeoMap v-model:scope="geoScope" :data="geoData[geoScope]" />
      <div class="glass na-sources">
        <div class="nc-head"><span>来源地 Top · {{ geoScope === 'cn' ? '省份' : '国家' }}</span></div>
        <div class="rank-list src">
          <div class="rank-row" v-for="(s, i) in sourceTop" :key="s.name">
            <span class="rk-idx" :class="{ top: i < 3 }">{{ i + 1 }}</span>
            <span class="rk-name">{{ s.name }}</span>
            <span class="rk-bar"><i :style="{ width: srcPct(s) }"></i></span>
            <span class="rk-val">{{ fmtNum(s.requests) }}</span>
          </div>
          <div class="nc-empty" v-if="!sourceTop.length">暂无数据</div>
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
const sourceTop = computed(() => (geoData.value[geoScope.value]?.points || []).slice(0, 10))

function fmtNum(v) {
  if (v == null) return '-'
  if (v >= 1e6) return (v / 1e6).toFixed(1) + 'M'
  if (v >= 1e3) return (v / 1e3).toFixed(1) + 'k'
  return String(Math.round(v))
}

const maxUri = computed(() => Math.max(...topUris.value.map((u) => u.count), 1))
const maxIp = computed(() => Math.max(...topIps.value.map((i) => i.requests), 1))
const maxSrc = computed(() => Math.max(...sourceTop.value.map((s) => s.requests), 1))

function uriPct(u) {
  return ((u.count / maxUri.value) * 100).toFixed(1) + '%'
}
function ipPct(ip) {
  return ((ip.requests / maxIp.value) * 100).toFixed(1) + '%'
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
  const [req, bytes] = await Promise.all([
    queryClusterTrend(list, 'nginx_access_requests', 'sum'),
    queryClusterTrend(list, 'nginx_access_bytes', 'sum'),
  ])
  trend &&
    trend.setOption(
      monitorOption({
        yMin: 0,
        yFormatter: (v) => fmtNum(v),
        series: [
          { name: '请求数', color: COLORS.cyan, data: req },
          { name: '流量', color: COLORS.purple, data: bytes },
        ],
      }),
      true
    )
}

function renderStatus() {
  if (!status) return
  const data = statusCounts.value
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
          name: '请求数',
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
  await Promise.all([loadSummary(), loadTrends()])
  renderStatus()
  loadGeo('cn')
  loadGeo('world')
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
.na-left {
  display: grid;
  grid-template-columns: 1fr 1fr;
  grid-template-rows: 1fr 1fr;
  gap: 12px;
  min-height: 0;
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
</style>
