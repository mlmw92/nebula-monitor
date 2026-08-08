<template>
  <div class="network-tab">
    <div class="nt-instance-bar glass">
      <span class="nt-instance-label">Nginx 实例</span>
      <select v-model="selectedInstance" class="nt-instance-select" aria-label="选择 Nginx 实例">
        <option value="">全部实例</option>
        <option v-for="it in nginxInstances" :key="instanceKey(it)" :value="instanceKey(it)">
          {{ instanceLabel(it) }}{{ it.up ? '' : ' · 离线' }}
        </option>
      </select>
      <span class="nt-instance-hint">{{ selectedInstance ? '当前显示单实例数据' : '当前显示集群汇总数据' }}</span>
    </div>
    <!-- 顶部 KPI 条 -->
    <div class="nt-kpi-row">
      <div class="glass nt-kpi" v-for="k in topKpis" :key="k.key">
        <div class="ntk-label">{{ k.label }}</div>
        <div class="ntk-value" :style="{ color: k.color }">{{ k.value }}</div>
        <div class="ntk-sub" v-if="k.sub">{{ k.sub }}</div>
      </div>
    </div>

    <!-- 主体：左侧排行 + 右侧地图 -->
    <div class="nt-body">
      <!-- 左侧列 -->
      <div class="nt-left">
        <!-- 接口访问 Top 10 -->
        <div class="glass nt-card">
          <div class="ntc-head">
            <span>接口访问 Top 10</span>
            <span class="ntc-stat" v-if="totalRequests">{{ totalRequests }} 次</span>
          </div>
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

        <!-- 来源 IP Top 10 -->
        <div class="glass nt-card">
          <div class="ntc-head ntc-head-ip">
            <span>来源 IP Top 10</span>
            <label class="internal-ip-filter" title="默认隐藏回环、私有、链路本地及其他保留地址">
              <input v-model="includeInternalIps" type="checkbox" />
              <span class="filter-toggle" aria-hidden="true"></span>
              <span>包含内部 IP</span>
            </label>
          </div>
          <div class="rank-list">
            <div class="rank-row" v-for="(ip, i) in ipList" :key="ip.ip">
              <span class="rk-idx" :class="{ top: i < 3 }">{{ i + 1 }}</span>
              <span class="rk-name" :title="ipTitle(ip)">{{ ip.ip }}</span>
              <span class="rk-bar"><i :style="{ width: ipPct(ip) }"></i></span>
              <span class="rk-val">{{ fmtNum(ip.requests) }}</span>
            </div>
            <div class="nc-empty" v-if="!ipList.length">暂无数据</div>
          </div>
        </div>

        <!-- 图表区：请求趋势与状态码分布上下排列 -->
        <div class="nt-chart-stack">
          <!-- 请求趋势 -->
          <div class="glass nt-card">
            <div class="ntc-head">
              <span>请求趋势</span>
              <span class="ntc-stat" v-if="totalRate">{{ rateShort(totalRate) }}/s</span>
            </div>
            <div ref="trendChart" class="ntc-chart"></div>
          </div>

          <!-- 状态码分布 -->
          <div class="glass nt-card">
            <div class="ntc-head">
              <span>状态码分布</span>
            </div>
            <div ref="statusChart" class="ntc-chart"></div>
          </div>
        </div>
      </div>

      <!-- 右侧地图 -->
      <div class="nt-right">
        <div class="glass nt-map">
          <div class="ntm-head">
            <span class="ntm-title">请求来源地理分布</span>
            <div class="ntm-scope">
              <button :class="{ on: geoScope === 'cn' }" @click="changeScope('cn')">中国</button>
              <button :class="{ on: geoScope === 'world' }" @click="changeScope('world')">世界</button>
            </div>
          </div>
          <div ref="mapChart" class="ntm-chart"></div>
          <div v-if="!displayPoints.length" class="ntm-empty">
            <div>暂无有效的地理位置数据</div>
            <div class="ntm-empty-sub">当前来源多为内网 / 保留地址</div>
          </div>
        </div>
        <!-- 来源地排名 -->
        <div class="glass nt-sources">
          <div class="ntc-head">
            <span>来源地 Top · {{ geoScope === 'cn' ? '省份 / 国家' : '国家' }}</span>
          </div>
          <div class="rank-list src">
            <div class="rank-row" v-for="(s, i) in sourceList" :key="s.name">
              <span class="rk-idx" :class="{ top: i < 3 }">{{ i + 1 }}</span>
              <span class="rk-name">
                {{ s.name }}
                <em v-if="s.foreign" class="rk-foreign">国外</em>
              </span>
              <span class="rk-bar"><i :style="{ width: srcPct(s) }"></i></span>
              <span class="rk-val">{{ fmtNum(s.requests) }}</span>
            </div>
            <div class="nc-empty" v-if="!sourceList.length">暂无数据</div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import http from '../../../api/http'
import { initChart, monitorOption, COLORS, rateShort, mapGeoOption } from '../../../charts/echarts'
import { registerMaps } from '../../../charts/geoData'
import { queryClusterTrend } from '../useTrend'

const props = defineProps({
  nodes: { type: Array, default: () => [] },
})

// 图表 refs
const trendChart = ref(null)
const statusChart = ref(null)
const mapChart = ref(null)
let trend = null
let status = null
let map = null
let ro = null
let timer = null
let refreshing = false

const summary = ref(null)
const geoData = ref({ cn: null, world: null })
const geoScope = ref('cn')
const validPoints = computed(() => {
  const points = geoData.value[geoScope.value]?.points || []
  return points.filter(isValidPoint)
})
// 国外国家来源（来自 world 视图数据，排除国内），用于在 cn 视图按国家标注国外流量
const foreignCountryPoints = computed(() => {
  const pts = geoData.value.world?.points || []
  return pts
    .filter((p) => isValidPoint(p) && p.name !== '中国')
    .map((p) => ({ name: p.name, requests: p.requests, foreign: true }))
})
// 来源地排行 / 最大来源展示用的合并列表：
// cn 视图 = 国内省份 + 国外国家（按国家标注）；world 视图 = 国家
const displayPoints = computed(() => {
  if (geoScope.value === 'world') {
    return validPoints.value.map((p) => ({ name: p.name, requests: p.requests, foreign: false }))
  }
  const provinces = (geoData.value.cn?.points || [])
    .filter(isValidPoint)
    .map((p) => ({ name: p.name, requests: p.requests, foreign: false }))
  return [...provinces, ...foreignCountryPoints.value]
})
const nginxInstances = ref([])
const selectedInstance = ref('')

function instanceKey(it) { return `${it.node || ''}|${it.instance || ''}` }
function instanceLabel(it) { return [it.name || it.instance, it.node].filter(Boolean).join(' · ') }

const selectedSummary = computed(() => {
  if (!selectedInstance.value) return summary.value || {}
  return summary.value?.instanceSummaries?.[selectedInstance.value] || {}
})
const totalRequests = computed(() => selectedSummary.value.totalRequests || 0)
const totalRate = computed(() => selectedSummary.value.totalRate || 0)

// 是否已启用访问日志
const accessEnabled = computed(() => {
  const s = selectedSummary.value
  return !!((s.topUris && s.topUris.length) || (s.topIps && s.topIps.length) || s.totalRequests)
})

// 过滤无效地理点（ip2region 对内网/保留地址返回 Reserved）
function isValidPoint(p) {
  return p && p.name && p.name !== 'Reserved' && p.name !== '0'
}

// 顶部 KPI 条
const topKpis = computed(() => {
  const s = selectedSummary.value
  const points = displayPoints.value
  const maxSrc = points.length ? points.reduce((a, b) => (a.requests > b.requests ? a : b)) : null
  const isWorld = geoScope.value === 'world'

  return [
    {
      key: 'total',
      label: '总访问量',
      value: s.totalRequests ? fmtNum(s.totalRequests) : '--',
      color: COLORS.cyan,
      sub: '累计请求',
    },
    {
      key: 'regions',
      label: isWorld ? '在线国家' : '在线省份',
      value: points.length || '--',
      color: COLORS.blue,
      sub: isWorld ? '个来源国家' : '个来源地区',
    },
    {
      key: 'maxSrc',
      label: '最大来源',
      value: maxSrc ? (maxSrc.foreign ? '国外·' + maxSrc.name : maxSrc.name) : '--',
      color: COLORS.amber,
      sub: maxSrc ? `${Math.min((maxSrc.requests / (s.totalRequests || 1)) * 100, 100).toFixed(1)}%` : '',
    },
    {
      key: 'peak',
      label: '请求峰值',
      value: s.totalRate ? fmtNum(s.totalRate) + '/s' : '--',
      color: COLORS.purple,
      sub: '实时 QPS',
    },
  ]
})

// 排行列表数据
const topUris = computed(() => selectedSummary.value.topUris || [])
const topIps = computed(() => selectedSummary.value.topIps || [])
const uriList = computed(() => topUris.value.slice(0, 10))
const includeInternalIps = ref(false)
const ipList = computed(() => {
  const list = includeInternalIps.value ? topIps.value : topIps.value.filter((ip) => !isInternalIp(ip.ip))
  return list.slice(0, 10)
})

const maxUri = computed(() => Math.max(...uriList.value.map((u) => u.count || 0), 1))
const maxIp = computed(() => Math.max(...ipList.value.map((i) => i.requests || 0), 1))

function uriPct(u) {
  return ((u.count / maxUri.value) * 100).toFixed(1) + '%'
}
function ipPct(ip) {
  return ((ip.requests / maxIp.value) * 100).toFixed(1) + '%'
}

// 识别回环、私有、链路本地、共享地址及保留地址，默认不纳入来源 IP 排名。
function isInternalIp(value) {
  const ip = String(value || '').trim().replace(/^\[|\]$/g, '')
  if (!ip) return true
  if (ip === '::1' || ip === '::' || ip === '0:0:0:0:0:0:0:1') return true
  if (/^(fc|fd)[0-9a-f]{2}:/i.test(ip) || /^fe[89ab][0-9a-f]:/i.test(ip)) return true

  const parts = ip.split('.').map(Number)
  if (parts.length !== 4 || parts.some((n) => !Number.isInteger(n) || n < 0 || n > 255)) return false
  const [a, b] = parts
  return (
    (a === 0) ||
    ip === '120.0.0.1' ||
    a === 10 ||
    a === 127 ||
    (a === 100 && b >= 64 && b <= 127) ||
    (a === 169 && b === 254) ||
    (a === 172 && b >= 16 && b <= 31) ||
    (a === 192 && b === 168) ||
    (a === 198 && (b === 18 || b === 19)) ||
    a >= 224
  )
}

// 来源地排名（cn 视图下合并国内省份与国外国家，按请求量排序）
const sourceList = computed(() => {
  return displayPoints.value
    .slice()
    .sort((a, b) => b.requests - a.requests)
    .slice(0, 10)
    .map((p) => ({ name: p.name, requests: p.requests, foreign: p.foreign }))
})
const maxSrc = computed(() => Math.max(...sourceList.value.map((s) => s.requests || 0), 1))

function srcPct(s) {
  return ((s.requests / maxSrc.value) * 100).toFixed(1) + '%'
}

function fmtNum(v) {
  if (v == null) return '-'
  if (v >= 1e6) return (v / 1e6).toFixed(1) + 'M'
  if (v >= 1e3) return (v / 1e3).toFixed(1) + 'k'
  return String(Math.round(v))
}

function ipTitle(ip) {
  let t = ip.ip || ''
  if (ip.province) t += ' · ' + ip.province
  if (ip.country) t += ' · ' + ip.country
  return t
}

// 状态码分布
const statusCounts = computed(() => {
  const m = selectedSummary.value.statusCounts || {}
  return Object.keys(m)
    .map((k) => ({ code: k, count: m[k] }))
    .sort((a, b) => a.code.localeCompare(b.code))
})

// 数据加载
async function loadSummary() {
  try {
    const data = await http.get('/api/v1/middleware/nginx/access/summary')
    summary.value = data
  } catch (e) {
    console.error('nginx access summary 加载失败', e)
  }
}

async function loadInstances() {
  try {
    const data = await http.get('/api/v1/middleware/nginx/instances')
    nginxInstances.value = data?.instances || []
  } catch (e) {
    nginxInstances.value = []
  }
}

async function loadGeo(scope) {
  try {
    const instance = selectedInstance.value ? `&instance=${encodeURIComponent(selectedInstance.value)}` : ''
    const data = await http.get(`/api/v1/middleware/nginx/access/geo?scope=${scope}${instance}`)
    geoData.value[scope] = data
  } catch (e) {
    console.error('nginx access geo 加载失败', scope, e)
  }
}

async function loadTrends() {
  const list = props.nodes || []
  if (!list.length || !trend) return
  const instance = selectedInstance.value ? selectedInstance.value.split('|').slice(1).join('|') : ''
  const [req] = await Promise.all([
    queryClusterTrend(list, 'nginx_access_requests', 'sum', { labels: instance ? { instance } : undefined }),
  ])
  trend.setOption(
    monitorOption({
      yMin: 0,
      yFormatter: (v) => fmtNum(v),
      series: [{ name: '请求数', color: COLORS.cyan, data: req }],
    }),
    true
  )
}

function renderStatus() {
  if (!status) return
  const data = statusCounts.value
  const colorMap = { '2': COLORS.green, '3': COLORS.blue, '4': COLORS.amber, '5': COLORS.red }
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
          barMaxWidth: 32,
          data: data.map((d) => ({
            value: d.count,
            itemStyle: {
              borderRadius: [4, 4, 0, 0],
              color: {
                type: 'linear',
                x: 0, y: 0, x2: 0, y2: 1,
                colorStops: [
                  { offset: 0, color: colorMap[d.code.charAt(0)] || COLORS.cyan },
                  { offset: 1, color: (colorMap[d.code.charAt(0)] || COLORS.cyan) + '22' },
                ],
              },
            },
          })),
        },
      ],
    },
    true
  )
}

function renderMap() {
  if (!map) return
  try {
    const d = geoData.value[geoScope.value]
    const points = (d?.points || []).filter(isValidPoint)
    const hasData = !!(points.length || d?.lines?.length || d?.deployPoints?.length)
    if (hasData) {
      const toGeo = {
        points: points.map((p) => ({ name: p.name, countryEn: p.countryEn, requests: p.requests, bytes: p.bytes })),
        deployPoints: (d.deployPoints || []).map((p) => ({ name: p.name, countryEn: p.countryEn, requests: p.requests })),
        lines: (d.lines || []).map((l) => ({
          from: l.from,
          fromEn: l.fromEn,
          to: l.to,
          toEn: l.toEn,
          fromName: l.from,
          toName: l.to,
          value: l.value,
        })),
      }
      map.setOption(mapGeoOption(geoScope.value, toGeo), true)
    } else {
      map.setOption(mapGeoOption(geoScope.value, {}), true)
    }
  } catch (e) {
    console.error('地图渲染失败', e)
  }
}

function changeScope(s) {
  if (s === geoScope.value) return
  geoScope.value = s
  loadGeo(s)
}

watch(selectedInstance, async () => {
	await refresh()
})

async function refresh() {
	if (refreshing) return
	refreshing = true
	try {
	await loadSummary()
	await loadInstances()
	await loadTrends()
	renderStatus()
	await Promise.all([loadGeo('cn'), loadGeo('world')])
	} finally {
		refreshing = false
	}
}

// 监听 geo 数据变化，更新地图
watch(() => geoData.value[geoScope.value], () => {
  renderMap()
}, { deep: false })

onMounted(async () => {
  try {
    await registerMaps()
  } catch (e) {
    console.error('地图 GeoJSON 注册失败', e)
  }
  trend = initChart(trendChart.value)
  status = initChart(statusChart.value)
  map = initChart(mapChart.value)
  await refresh()
  timer = setInterval(refresh, 30000)
  ro = new ResizeObserver(() => {
    trend && trend.resize()
    status && status.resize()
    map && map.resize()
  })
  ro.observe(trendChart.value)
  ro.observe(statusChart.value)
  ro.observe(mapChart.value)
})

onUnmounted(() => {
  clearInterval(timer)
  ro && ro.disconnect()
  trend && trend.dispose()
  status && status.dispose()
  map && map.dispose()
  trend = null
  status = null
  map = null
})
</script>

<style scoped>
.network-tab {
  display: flex;
  flex-direction: column;
  gap: 10px;
  height: 100%;
  min-height: 0;
}

/* 顶部 KPI 条 */
.nt-kpi-row {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 10px;
  flex-shrink: 0;
}

.nt-kpi {
  padding: 10px 16px;
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.ntk-label {
  font-size: 11px;
  color: var(--text-muted);
  letter-spacing: 0.06em;
  text-transform: uppercase;
}

.ntk-value {
  font-size: 24px;
  font-weight: 800;
  font-family: var(--mono);
  text-shadow: 0 0 14px currentColor;
  line-height: 1.2;
}

.ntk-sub {
  font-size: 11px;
  color: var(--text-dim);
  font-family: var(--mono);
}

/* 主体 */
.nt-body {
  display: grid;
  grid-template-columns: 34% 1fr;
  gap: 10px;
  flex: 1;
  min-height: 0;
}

.nt-left {
  display: grid;
  grid-template-columns: 1fr 1fr;
  grid-template-rows: 1fr 1fr;
  gap: 10px;
  min-height: 0;
}

.nt-instance-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 7px 12px;
  flex-shrink: 0;
  border: 1px solid rgba(34, 211, 238, 0.14);
  background: linear-gradient(90deg, rgba(16, 36, 58, 0.88), rgba(13, 24, 42, 0.72));
}

.nt-instance-label {
  color: var(--text-dim);
  font-size: 12px;
  letter-spacing: 0.04em;
}

.nt-instance-select {
  min-width: 220px;
  padding: 5px 28px 5px 9px;
  border: 1px solid rgba(34, 211, 238, 0.36);
  border-radius: 5px;
  background: rgba(5, 15, 29, 0.82);
  color: var(--text);
  font-size: 12px;
  outline: none;
}

.nt-instance-select:focus {
  border-color: var(--accent);
  box-shadow: 0 0 0 2px rgba(34, 211, 238, 0.12);
}

.nt-instance-hint {
  color: var(--text-muted);
  font-size: 11px;
}

.nt-chart-stack {
  grid-column: 1 / -1;
  display: grid;
  grid-template-rows: 1fr 1fr;
  gap: 10px;
  min-height: 0;
}

.nt-card {
  display: flex;
  flex-direction: column;
  padding: 10px 12px 6px;
  min-height: 0;
  overflow: hidden;
}

.ntc-head {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  font-size: 12px;
  color: var(--text-dim);
  margin-bottom: 4px;
  letter-spacing: 0.03em;
  flex-shrink: 0;
}

.ntc-head-ip {
  align-items: center;
}

.internal-ip-filter {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  color: var(--text-muted);
  font-size: 10px;
  line-height: 16px;
  cursor: pointer;
  user-select: none;
  white-space: nowrap;
}

.internal-ip-filter input {
  position: absolute;
  opacity: 0;
  pointer-events: none;
}

.filter-toggle {
  position: relative;
  width: 24px;
  height: 14px;
  border: 1px solid rgba(159, 179, 200, 0.42);
  border-radius: 8px;
  background: rgba(255, 255, 255, 0.07);
  transition: all 0.16s ease;
}

.filter-toggle::after {
  content: '';
  position: absolute;
  top: 2px;
  left: 2px;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--text-muted);
  transition: transform 0.16s ease, background 0.16s ease;
}

.internal-ip-filter input:checked + .filter-toggle {
  border-color: var(--accent);
  background: var(--accent-dim);
  box-shadow: 0 0 8px rgba(34, 211, 238, 0.2);
}

.internal-ip-filter input:checked + .filter-toggle::after {
  background: var(--accent);
  transform: translateX(10px);
}

.internal-ip-filter:focus-within .filter-toggle {
  outline: 1px solid var(--accent);
  outline-offset: 2px;
}

.ntc-stat {
  font-family: var(--mono);
  color: var(--accent);
  font-size: 13px;
  font-weight: 700;
}

.ntc-chart {
  flex: 1;
  min-height: 0;
  width: 100%;
}

/* 排行列表 */
.rank-list {
  flex: 1;
  overflow-y: auto;
  min-height: 0;
}

.rank-row {
  display: grid;
  grid-template-columns: 18px 1fr 1fr 44px;
  align-items: center;
  gap: 8px;
  padding: 3px 2px;
  font-size: 11px;
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

.rk-foreign {
  display: inline-block;
  margin-left: 6px;
  padding: 0 5px;
  font-size: 10px;
  font-style: normal;
  line-height: 15px;
  border-radius: 3px;
  color: #f59e0b;
  background: rgba(245, 158, 11, 0.14);
  border: 1px solid rgba(245, 158, 11, 0.4);
  vertical-align: middle;
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

.nc-empty {
  padding: 16px 0;
  text-align: center;
  color: var(--text-muted);
  font-size: 12px;
}

/* 右侧：地图 + 来源地 Top 水平并排；地图占主区，来源列自适应收窄 */
.nt-right {
  display: grid;
  grid-template-columns: 1fr minmax(200px, 20%);
  gap: 10px;
  min-height: 0;
  min-width: 0;
}

.nt-map {
  display: flex;
  flex-direction: column;
  padding: 10px 12px 6px;
  flex: 1;
  min-height: 0;
  position: relative;
}

.ntm-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  min-height: 28px;
  margin-bottom: 4px;
  padding-bottom: 4px;
  flex-shrink: 0;
  border-bottom: 1px solid rgba(255, 255, 255, 0.06);
}

.ntm-title {
  font-size: 13px;
  font-weight: 700;
  color: var(--text, #fff);
  letter-spacing: 0.04em;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  line-height: 1.4;
}

.ntm-title::before {
  content: '';
  width: 3px;
  height: 12px;
  border-radius: 2px;
  background: var(--accent, #22d3ee);
}

.ntm-scope {
  display: flex;
  gap: 6px;
}

.ntm-scope button {
  background: transparent;
  border: 1px solid var(--border);
  color: var(--text-dim);
  font-size: 11px;
  padding: 3px 12px;
  border-radius: 12px;
  cursor: pointer;
  transition: all 0.15s;
}

.ntm-scope button:hover {
  border-color: var(--accent);
  color: var(--accent);
}

.ntm-scope button.on {
  background: var(--accent-dim);
  border-color: var(--accent);
  color: var(--accent);
}

.ntm-chart {
  flex: 1;
  min-height: 0;
  width: 100%;
}

.ntm-empty {
  position: absolute;
  inset: 44px 0 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  pointer-events: none;
  color: var(--text-dim);
  font-size: 13px;
  text-align: center;
  gap: 6px;
}
.ntm-empty-sub {
  font-size: 11px;
  color: var(--text-muted);
  opacity: 0.8;
}

.nt-sources {
  padding: 10px 12px;
  min-height: 0;
  min-width: 0;
  display: flex;
  flex-direction: column;
}

.nt-sources .rank-list {
  overflow-y: auto;
}

/* 4K 适配 */
@media (min-width: 2400px) {
  .network-tab { gap: 14px; }
  .nt-kpi-row { gap: 14px; }
  .ntk-label { font-size: 14px; }
  .ntk-value { font-size: 32px; }
  .ntk-sub { font-size: 14px; }
  .nt-body { gap: 14px; }
  .nt-left { gap: 14px; }
  .nt-chart-stack { gap: 14px; }
  .nt-right { gap: 14px; grid-template-columns: 1fr minmax(300px, 20%); }
  .nt-card { padding: 14px 16px 8px; }
  .ntc-head { font-size: 15px; }
  .ntc-stat { font-size: 16px; }
  .rank-row { font-size: 14px; padding: 5px 2px; }
  .rk-idx { width: 20px; height: 20px; font-size: 12px; }
  .rk-val { font-size: 14px; }
  .ntm-title { font-size: 16px; }
  .ntm-scope button { font-size: 14px; padding: 4px 16px; }
}

@media (min-width: 3440px) {
  .network-tab { gap: 20px; }
  .nt-kpi-row { gap: 20px; }
  .ntk-label { font-size: 17px; }
  .ntk-value { font-size: 40px; }
  .ntk-sub { font-size: 17px; }
  .nt-body { gap: 20px; }
  .nt-left { gap: 20px; }
  .nt-chart-stack { gap: 20px; }
  .nt-right { gap: 20px; grid-template-columns: 1fr minmax(380px, 20%); }
  .nt-card { padding: 18px 22px 10px; }
  .ntc-head { font-size: 18px; }
  .ntc-stat { font-size: 20px; }
  .rank-row { font-size: 17px; padding: 6px 2px; }
  .rk-idx { width: 24px; height: 24px; font-size: 14px; }
  .rk-val { font-size: 17px; }
  .ntm-title { font-size: 20px; }
  .ntm-scope button { font-size: 17px; padding: 5px 20px; }
}
</style>
