<template>
  <div class="screen-view">
    <!-- 顶栏 -->
    <header class="hud-top">
      <div class="hud-side hud-clock mono">{{ clock }}</div>
      <h1 class="hud-title">
        <span class="ht-en">{{ brand.name }}</span>
        <span class="ht-sep"></span>
        <span class="ht-cn">{{ brand.name }}监控指挥中心</span>
      </h1>
      <div class="hud-side hud-actions">
        <button class="hud-btn" title="全屏" @click="toggleFullscreen"><FullScreen /></button>
        <button class="hud-btn" title="模块设置" @click="settingOpen = true"><Setting /></button>
        <button class="hud-btn" title="返回概览" @click="goBack"><Back /></button>
      </div>
    </header>

    <!-- KPI 行 -->
    <section class="kpi-row" v-if="cfg.modules.kpiTop">
      <div class="kpi-card" v-for="k in kpis" :key="k.key" :class="k.tone">
        <span class="kpi-label">{{ k.label }}</span>
        <span class="kpi-value mono">{{ k.value }}<em>{{ k.unit }}</em></span>
        <i class="kpi-glow"></i>
      </div>
    </section>

    <!-- Tab 主区 -->
    <main class="tab-main">
      <div class="tab-bar" ref="tabBarRef">
        <div class="tab-item" v-for="(t, i) in tabs" :key="t.key" ref="tabRefs"
          :class="{ on: tab === t.key }" @click="tab = t.key">{{ t.label }}</div>
        <div class="tab-slider" :style="sliderStyle"></div>
      </div>
      <div class="tab-body">
        <HostMonitorPanel v-show="tab === 'host'" v-if="cfg.modules.hostMonitor" :nodes="nodeCards" />
        <MiddlewareMonitorPanel v-show="tab === 'middleware'" v-if="cfg.modules.middlewareMonitor" />
        <NginxAnalysisPanel v-show="tab === 'nginx'" v-if="cfg.modules.nginxAnalysis" :nodes="onlineHosts" />
      </div>
    </main>

    <!-- 底部告警滚动区 -->
    <footer class="hud-bottom" v-if="cfg.modules.alerts">
      <span class="ab-label" :class="{ warn: firingCount > 0 }">
        <i class="ab-dot"></i>实时告警 {{ firingCount }}
      </span>
      <div class="ab-marquee" :class="{ 'has-alert': firingCount > 0 }">
        <div class="ab-track" v-if="activeAlerts.length">
          <span v-for="a in activeAlerts" :key="a.id" class="ab-item" :class="a.severity"
            @click="goNode(a.node)">
            <i class="ab-lv"></i>
            {{ a.severity === 'critical' ? '故障' : a.severity === 'warning' ? '预警' : '提示' }}
            · {{ a.node }} · {{ a.ruleName || a.summary || '未知告警' }} · {{ fmtShort(a.startsAt) }}
          </span>
        </div>
        <span class="ab-none" v-else>当前无活动告警 ✓</span>
      </div>
    </footer>

    <!-- 模块设置抽屉 -->
    <el-drawer v-model="settingOpen" title="大屏模块设置" size="300px" :append-to-body="true">
      <div class="cfg-list">
        <el-checkbox v-model="cfg.modules.kpiTop" label="顶部 KPI 指标卡" />
        <el-checkbox v-model="cfg.modules.hostMonitor" label="主机监控板块" />
        <el-checkbox v-model="cfg.modules.middlewareMonitor" label="中间件监控板块" />
        <el-checkbox v-model="cfg.modules.nginxAnalysis" label="Nginx 分析板块" />
        <el-checkbox v-model="cfg.modules.alerts" label="底部告警滚动区" />
      </div>
      <template #footer>
        <el-button @click="settingOpen = false">取消</el-button>
        <el-button type="primary" :loading="savingCfg" @click="saveScreenConfig">保存</el-button>
      </template>
    </el-drawer>
  </div>
</template>

<script setup>
import { ref, reactive, computed, onMounted, onUnmounted, watch, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { FullScreen, Back, Setting } from '@element-plus/icons-vue'
import http from '../../api/http'
import { connectWS } from '../../api/ws'
import { rateShort } from '../../charts/echarts'
import HostMonitorPanel from './HostMonitorPanel.vue'
import MiddlewareMonitorPanel from './MiddlewareMonitorPanel.vue'
import NginxAnalysisPanel from './NginxAnalysisPanel.vue'
import { useBrand } from '../../composables/useBrand'

const router = useRouter()
const { brand } = useBrand()

const nodes = ref([])
const metrics = ref({})
const alerts = ref([])
const clock = ref('')
const middlewareTotal = ref(0)
const ngxRequests = ref(0)
const cfg = reactive({
  modules: { kpiTop: true, hostMonitor: true, middlewareMonitor: true, nginxAnalysis: true, alerts: true },
})
const tab = ref('host')
const tabs = [
  { key: 'host', label: '主机监控' },
  { key: 'middleware', label: '中间件监控' },
  { key: 'nginx', label: 'Nginx 分析' },
]
const tabIndex = computed(() => tabs.findIndex((t) => t.key === tab.value))

const tabBarRef = ref(null)
const tabRefs = ref([])
const sliderStyle = ref({})
function updateSlider() {
  nextTick(() => {
    const idx = tabIndex.value
    const el = tabRefs.value ? tabRefs.value[idx] : null
    if (!el || !tabBarRef.value) return
    sliderStyle.value = {
      transform: `translateX(${el.offsetLeft}px)`,
      width: `${el.offsetWidth}px`,
    }
  })
}
watch(tabIndex, updateSlider)
onMounted(() => {
  window.addEventListener('resize', updateSlider)
  updateSlider()
})
onUnmounted(() => {
  window.removeEventListener('resize', updateSlider)
})

const settingOpen = ref(false)
const savingCfg = ref(false)
let dataTimer = null
let clockTimer = null
let ws = null
let visible = true

const activeAlerts = computed(() => alerts.value.filter((a) => a.state === 'firing'))
const onlineCount = computed(() => nodes.value.filter((n) => n.status === 'online').length)
const firingCount = computed(() => activeAlerts.value.length)

// 主机卡片（供主机面板）
const nodeCards = computed(() =>
  nodes.value.map((n) => ({
    name: n.hostname,
    ip: n.ip || '-',
    online: n.status === 'online',
    cpu: metrics.value[n.hostname]?.cpu || 0,
    mem: metrics.value[n.hostname]?.mem || 0,
    disk: metrics.value[n.hostname]?.disk || 0,
    load1: metrics.value[n.hostname]?.load1 || 0,
    netIn: metrics.value[n.hostname]?.netIn || 0,
    netOut: metrics.value[n.hostname]?.netOut || 0,
  }))
)
const onlineHosts = computed(() => nodeCards.value.filter((n) => n.online).map((n) => n.name))

function avgOf(key) {
  const list = nodeCards.value.filter((n) => n.online)
  if (!list.length) return 0
  return list.reduce((s, n) => s + (n[key] || 0), 0) / list.length
}
const avgCpu = computed(() => avgOf('cpu'))
const avgMem = computed(() => avgOf('mem'))
const netInTotal = computed(() => nodeCards.value.reduce((s, n) => s + n.netIn, 0))

function toneOf(v) {
  if (v >= 90) return 'red'
  if (v >= 70) return 'warn'
  return ''
}
const kpis = computed(() => [
  { key: 'total', label: '服务器总数', value: nodes.value.length, unit: '台', tone: 'cyan' },
  { key: 'online', label: '在线主机', value: onlineCount.value, unit: '台', tone: 'green' },
  { key: 'cpu', label: '平均 CPU', value: Math.round(avgCpu.value), unit: '%', tone: toneOf(avgCpu.value) || 'blue' },
  { key: 'mem', label: '平均内存', value: Math.round(avgMem.value), unit: '%', tone: toneOf(avgMem.value) || 'purple' },
  { key: 'mw', label: '中间件实例', value: middlewareTotal.value, unit: '个', tone: 'cyan' },
  { key: 'alert', label: '异常告警', value: firingCount.value, unit: '条', tone: firingCount.value ? 'red' : 'green' },
  { key: 'net', label: '实时流量', value: rateShort(netInTotal.value), unit: '/s', tone: 'blue' },
  { key: 'req', label: '访问请求', value: shortNum(ngxRequests.value), unit: '次', tone: 'purple' },
])

function shortNum(v) {
  if (v >= 1e6) return (v / 1e6).toFixed(1) + 'M'
  if (v >= 1e3) return (v / 1e3).toFixed(1) + 'k'
  return String(Math.round(v || 0))
}
function fmtShort(ts) {
  if (!ts) return '--'
  const d = new Date(ts)
  const p = (x) => String(x).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}
function goNode(name) {
  if (name) router.push('/node/' + encodeURIComponent(name))
}

// 时钟
function tickClock() {
  const d = new Date()
  const p = (x) => String(x).padStart(2, '0')
  clock.value = `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

// 全屏
function toggleFullscreen() {
  const el = document.documentElement
  if (!document.fullscreenElement) el.requestFullscreen && el.requestFullscreen()
  else document.exitFullscreen && document.exitFullscreen()
}
function exitFullscreen() {
  if (document.fullscreenElement && document.exitFullscreen) {
    return document.exitFullscreen().catch(() => {})
  }
  return Promise.resolve()
}
function goBack() {
  exitFullscreen().finally(() => router.push('/overview'))
}

// 模块配置
async function loadScreenConfig() {
  try {
    const res = await http.get('/api/v1/screen/config')
    if (res && res.modules) cfg.modules = { ...cfg.modules, ...res.modules }
  } catch (e) {
    /* 默认全开 */
  }
}
async function saveScreenConfig() {
  savingCfg.value = true
  try {
    await http.put('/api/v1/screen/config', { modules: { ...cfg.modules } })
    ElMessage.success('大屏配置已保存')
    settingOpen.value = false
  } catch (e) {
    ElMessage.error('保存失败')
  } finally {
    savingCfg.value = false
  }
}

// 基础数据
async function loadBase() {
  if (!visible) return
  try {
    const [nd, ad, md] = await Promise.all([
      http.get('/api/v1/nodes').catch(() => ({ nodes: [] })),
      http.get('/api/v1/alerts?state=active').catch(() => ({ alerts: [] })),
      http.get('/api/v1/nodes/latest').catch(() => ({ metrics: {} })),
    ])
    nodes.value = nd.nodes || []
    alerts.value = ad.alerts || []
    metrics.value = md.metrics || {}
  } catch (e) {
    /* ignore */
  }
}
async function loadExtras() {
  const [ov, sm] = await Promise.all([
    http.get('/api/v1/middleware/overview').catch(() => null),
    http.get('/api/v1/middleware/nginx/access/summary').catch(() => null),
  ])
  if (ov) middlewareTotal.value = ov.total || 0
  if (sm) ngxRequests.value = sm.totalRequests || 0
}

function onVis() {
  visible = document.visibilityState === 'visible'
  if (visible) {
    refreshAll()
    if (!dataTimer) dataTimer = setInterval(refreshAll, 30000)
  } else if (dataTimer) {
    clearInterval(dataTimer)
    dataTimer = null
  }
}
async function refreshAll() {
  await Promise.all([loadBase(), loadExtras()])
}

onMounted(async () => {
  tickClock()
  clockTimer = setInterval(tickClock, 1000)
  await loadScreenConfig()
  await refreshAll()
  dataTimer = setInterval(refreshAll, 30000)
  ws = connectWS('alerts', null, { onMessage: () => loadBase() })
  document.addEventListener('visibilitychange', onVis)
})
onUnmounted(() => {
  clockTimer && clearInterval(clockTimer)
  dataTimer && clearInterval(dataTimer)
  ws && ws.close()
  document.removeEventListener('visibilitychange', onVis)
})
</script>

<style scoped>
.screen-view {
  position: relative;
  width: 100vw;
  height: 100vh;
  overflow: hidden;
  display: grid;
  grid-template-areas: 'top' 'kpi' 'main' 'bottom';
  grid-template-rows: 56px 104px 1fr 52px;
  gap: 8px;
  padding: 8px 12px;
  color: var(--text);
  background:
    radial-gradient(1200px 500px at 50% -10%, rgba(34, 211, 238, 0.08), transparent 60%),
    radial-gradient(900px 420px at 90% 110%, rgba(168, 85, 247, 0.07), transparent 60%),
    linear-gradient(160deg, #060b16 0%, #0d1526 55%, #060b16 100%);
}
.screen-view::before {
  content: '';
  position: absolute;
  inset: 0;
  pointer-events: none;
  background-image:
    linear-gradient(rgba(34, 211, 238, 0.03) 1px, transparent 1px),
    linear-gradient(90deg, rgba(34, 211, 238, 0.03) 1px, transparent 1px);
  background-size: 44px 44px;
}

/* 顶栏 */
.hud-top {
  grid-area: top;
  display: flex;
  align-items: center;
  justify-content: space-between;
  position: relative;
  z-index: 2;
}
.hud-side {
  width: 300px;
  display: flex;
  align-items: center;
  gap: 8px;
}
.hud-clock {
  font-size: 14px;
  color: var(--text-dim);
  letter-spacing: 0.08em;
}
.hud-actions {
  justify-content: flex-end;
}
.hud-btn {
  width: 34px;
  height: 34px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--glass);
  border: 1px solid var(--border);
  border-radius: 8px;
  color: var(--text-dim);
  cursor: pointer;
  transition: all 0.2s;
}
.hud-btn:hover {
  color: var(--accent);
  border-color: var(--accent);
  box-shadow: 0 0 12px var(--accent-glow);
}
.hud-btn svg {
  width: 17px;
  height: 17px;
}
.hud-title {
  display: flex;
  align-items: center;
  gap: 14px;
  margin: 0;
  user-select: none;
}
.ht-en {
  font-size: 24px;
  font-weight: 800;
  letter-spacing: 0.5em;
  background: linear-gradient(90deg, #22d3ee, #3b82f6, #a855f7);
  -webkit-background-clip: text;
  background-clip: text;
  color: transparent;
  filter: drop-shadow(0 0 14px rgba(34, 211, 238, 0.45));
}
.ht-sep {
  width: 1px;
  height: 22px;
  background: linear-gradient(180deg, transparent, rgba(34, 211, 238, 0.7), transparent);
}
.ht-cn {
  font-size: 19px;
  font-weight: 700;
  color: var(--text);
  letter-spacing: 0.3em;
  text-shadow: 0 0 18px rgba(34, 211, 238, 0.35);
}

/* KPI 行 */
.kpi-row {
  grid-area: kpi;
  display: grid;
  grid-template-columns: repeat(8, 1fr);
  gap: 10px;
  position: relative;
  z-index: 2;
}
.kpi-card {
  position: relative;
  overflow: hidden;
  display: flex;
  flex-direction: column;
  justify-content: center;
  gap: 4px;
  padding: 12px 16px;
  border-radius: 12px;
  background: var(--glass);
  border: 1px solid var(--border);
  transition: transform 0.2s, border-color 0.2s;
}
.kpi-card:hover {
  transform: translateY(-3px);
}
.kpi-label {
  font-size: 11px;
  color: var(--text-dim);
  letter-spacing: 0.12em;
}
.kpi-value {
  font-size: 26px;
  font-weight: 700;
  color: var(--text);
  line-height: 1;
}
.kpi-value em {
  font-style: normal;
  font-size: 12px;
  color: var(--text-muted);
  margin-left: 4px;
  font-weight: 400;
}
.kpi-glow {
  position: absolute;
  right: -20px;
  top: -20px;
  width: 70px;
  height: 70px;
  border-radius: 50%;
  background: radial-gradient(circle, rgba(34, 211, 238, 0.14), transparent 70%);
  pointer-events: none;
}
.kpi-card.cyan .kpi-value { color: var(--info); }
.kpi-card.green .kpi-value { color: var(--chart-green); }
.kpi-card.blue .kpi-value { color: var(--chart-blue); }
.kpi-card.purple .kpi-value { color: var(--chart-purple); }
.kpi-card.red .kpi-value {
  color: var(--danger);
  animation: kpi-pulse 1.3s infinite;
}
.kpi-card.red { border-color: rgba(239, 68, 68, 0.4); }
.kpi-card.warn .kpi-value { color: var(--warn); }
.kpi-card.warn { border-color: rgba(245, 158, 11, 0.35); }
@keyframes kpi-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.55; }
}

/* Tab 主区 */
.tab-main {
  grid-area: main;
  display: flex;
  flex-direction: column;
  min-height: 0;
  position: relative;
  z-index: 2;
}
.tab-bar {
  position: relative;
  display: flex;
  gap: 4px;
  margin-bottom: 8px;
}
.tab-item {
  position: relative;
  z-index: 2;
  padding: 8px 26px;
  font-size: 14px;
  font-weight: 600;
  color: var(--text-dim);
  cursor: pointer;
  border-radius: 8px;
  transition: color 0.2s;
  user-select: none;
}
.tab-item:hover {
  color: var(--text);
}
.tab-item.on {
  color: var(--accent);
}
.tab-slider {
  position: absolute;
  left: 0;
  top: 0;
  height: 100%;
  border-radius: 8px;
  background: linear-gradient(90deg, rgba(34, 211, 238, 0.16), rgba(34, 211, 238, 0.04));
  border: 1px solid rgba(34, 211, 238, 0.35);
  box-shadow: 0 0 14px var(--accent-glow);
  transition: transform 0.3s cubic-bezier(0.4, 0, 0.2, 1), width 0.3s cubic-bezier(0.4, 0, 0.2, 1);
  pointer-events: none;
}
.tab-body {
  flex: 1;
  min-height: 0;
}

/* 底部告警 */
.hud-bottom {
  grid-area: bottom;
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 0 14px;
  border-radius: 10px;
  background: var(--glass);
  border: 1px solid var(--border);
  overflow: hidden;
  position: relative;
  z-index: 2;
}
.ab-label {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--text-dim);
  letter-spacing: 0.06em;
}
.ab-label.warn {
  color: var(--danger);
}
.ab-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--chart-green);
  box-shadow: 0 0 8px var(--chart-green);
}
.ab-label.warn .ab-dot {
  background: var(--danger);
  box-shadow: 0 0 8px var(--danger);
  animation: kpi-pulse 1s infinite;
}
.ab-marquee {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  white-space: nowrap;
}
.ab-track {
  display: inline-flex;
  gap: 28px;
  padding-left: 100%;
  animation: marquee 32s linear infinite;
}
.ab-track:hover {
  animation-play-state: paused;
}
.ab-item {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--text);
  cursor: pointer;
  white-space: nowrap;
}
.ab-item:hover {
  color: var(--accent);
}
.ab-lv {
  width: 7px;
  height: 7px;
  border-radius: 50%;
}
.ab-item.critical .ab-lv {
  background: var(--danger);
  box-shadow: 0 0 6px var(--danger);
  animation: kpi-pulse 1s infinite;
}
.ab-item.warning .ab-lv {
  background: var(--warn);
  box-shadow: 0 0 6px var(--warn);
}
.ab-item.info .ab-lv {
  background: var(--info);
}
.ab-none {
  color: var(--chart-green);
  font-size: 12px;
}
@keyframes marquee {
  from { transform: translateX(0); }
  to { transform: translateX(-100%); }
}

/* 设置抽屉 */
.cfg-list {
  display: flex;
  flex-direction: column;
  gap: 14px;
  padding: 4px 0;
}

/* ============ 宽屏 / 4K 适配 ============ */
/* 2K+ 宽屏：整体放大字号与间距，避免元素稀疏 */
@media (min-width: 2400px) {
  .screen-view {
    padding: 14px 24px;
    gap: 14px;
    grid-template-rows: 78px 132px 1fr 68px;
  }
  .hud-side { width: 380px; }
  .hud-clock { font-size: 18px; }
  .hud-btn { width: 42px; height: 42px; }
  .hud-btn svg { width: 21px; height: 21px; }
  .ht-en { font-size: 32px; letter-spacing: 0.42em; }
  .ht-cn { font-size: 25px; letter-spacing: 0.24em; }
  .ht-sep { height: 30px; }
  .kpi-row { gap: 16px; }
  .kpi-card { padding: 18px 26px; gap: 6px; }
  .kpi-label { font-size: 15px; letter-spacing: 0.1em; }
  .kpi-value { font-size: 40px; }
  .kpi-value em { font-size: 17px; margin-left: 6px; }
  .kpi-glow { right: -26px; top: -26px; width: 96px; height: 96px; }
  .tab-item { padding: 11px 34px; font-size: 18px; }
  .ab-label { font-size: 16px; gap: 9px; }
  .ab-item { font-size: 16px; gap: 8px; }
  .ab-none { font-size: 16px; }
  :deep(.hp-title) { font-size: 20px; }
  :deep(.hp-sub) { font-size: 13px; }
  :deep(.hp-kpi-label) { font-size: 13px; }
  :deep(.hp-kpi-value) { font-size: 24px; }
  :deep(.sg-label) { font-size: 15px; }
  :deep(.sg-text-value) { font-size: 18px; }
  :deep(.st-title) { font-size: 15px; }
  :deep(.stat-label) { font-size: 13px; }
  :deep(.stat-value) { font-size: 26px; }
  :deep(.stat-unit) { font-size: 13px; }
}

/* 4K 及以上：进一步放大 */
@media (min-width: 3440px) {
  .screen-view {
    padding: 20px 34px;
    gap: 20px;
    grid-template-rows: 96px 162px 1fr 84px;
  }
  .hud-side { width: 460px; }
  .hud-clock { font-size: 22px; }
  .hud-btn { width: 50px; height: 50px; }
  .hud-btn svg { width: 25px; height: 25px; }
  .ht-en { font-size: 42px; letter-spacing: 0.38em; }
  .ht-cn { font-size: 33px; letter-spacing: 0.2em; }
  .ht-sep { height: 38px; }
  .kpi-row { gap: 22px; }
  .kpi-card { padding: 24px 34px; gap: 8px; }
  .kpi-label { font-size: 19px; letter-spacing: 0.08em; }
  .kpi-value { font-size: 54px; }
  .kpi-value em { font-size: 22px; margin-left: 8px; }
  .kpi-glow { right: -32px; top: -32px; width: 120px; height: 120px; }
  .tab-item { padding: 14px 42px; font-size: 22px; }
  .ab-label { font-size: 20px; gap: 11px; }
  .ab-item { font-size: 20px; gap: 10px; }
  .ab-none { font-size: 20px; }
  :deep(.hp-title) { font-size: 26px; }
  :deep(.hp-sub) { font-size: 16px; }
  :deep(.hp-kpi-label) { font-size: 16px; }
  :deep(.hp-kpi-value) { font-size: 32px; }
  :deep(.sg-label) { font-size: 19px; }
  :deep(.sg-text-value) { font-size: 24px; }
  :deep(.st-title) { font-size: 19px; }
  :deep(.stat-label) { font-size: 16px; }
  :deep(.stat-value) { font-size: 34px; }
  :deep(.stat-unit) { font-size: 16px; }
}
</style>
