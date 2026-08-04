<template>
  <div class="screen-view">
    <!-- 粒子背景 -->
    <ParticleBg />

    <!-- 顶栏 -->
    <header class="hud-top">
      <div class="hud-side hud-clock mono">{{ clock }}</div>
      <div class="hud-center">
        <h1 class="hud-title">
          <span class="ht-en">{{ brand.name }}</span>
          <span class="ht-sep"></span>
          <span class="ht-cn">监控指挥中心</span>
        </h1>
        <!-- 实时倒计时 -->
        <div class="countdown" :class="{ urgent: countdown <= 5 }">
          <span class="cd-label">下次刷新</span>
          <span class="cd-num mono">{{ countdown }}</span>
          <span class="cd-unit">s</span>
        </div>
      </div>
      <div class="hud-side hud-actions">
        <button class="hud-btn" title="全屏" @click="toggleFullscreen"><FullScreen /></button>
        <button class="hud-btn" title="模块设置" @click="settingOpen = true"><Setting /></button>
        <button class="hud-btn" title="返回概览" @click="goBack"><Back /></button>
      </div>
    </header>

    <!-- 五面板主区 -->
    <main class="panel-main">
      <!-- 左侧两个面板 -->
      <div class="side-col left-col">
        <ScrollTablePanel
          class="side-panel"
          title="主机监控实时列表"
          :columns="hostColumns"
          :rows="hostRows"
          :speed="0.4"
        />
        <ScrollTablePanel
          class="side-panel"
          title="实时告警事件"
          :columns="alertColumns"
          :rows="alertRows"
          :speed="0.3"
        />
      </div>

      <!-- 中间主面板 -->
      <div class="center-col">
        <CenterPanel :nodes="nodeCards" :alerts="alerts" />
      </div>

      <!-- 右侧两个面板 -->
      <div class="side-col right-col">
        <ScrollTablePanel
          class="side-panel"
          title="中间件实例状态"
          :columns="mwColumns"
          :rows="mwRows"
          :speed="0.35"
        />
        <ScrollTablePanel
          class="side-panel"
          title="Nginx 访问 Top 排行"
          :columns="nginxColumns"
          :rows="nginxRows"
          :speed="0.3"
        />
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
import { ref, reactive, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { FullScreen, Back, Setting } from '@element-plus/icons-vue'
import http from '../../api/http'
import { connectWS } from '../../api/ws'
import { rateShort } from '../../charts/echarts'
import ParticleBg from './ParticleBg.vue'
import CenterPanel from './CenterPanel.vue'
import ScrollTablePanel from './ScrollTablePanel.vue'
import { useBrand } from '../../composables/useBrand'

const router = useRouter()
const { brand } = useBrand()

const REFRESH_INTERVAL = 30 // 数据刷新周期（秒）
const countdown = ref(REFRESH_INTERVAL)

const nodes = ref([])
const metrics = ref({})
const alerts = ref([])
const clock = ref('')
const mwInstances = ref([])
const nginxSummary = ref(null)
const cfg = reactive({ modules: { alerts: true } })
let dataTimer = null
let clockTimer = null
let countTimer = null
let ws = null
let visible = true

const settingOpen = ref(false)
const savingCfg = ref(false)

const activeAlerts = computed(() => alerts.value.filter((a) => a.state === 'firing'))
const firingCount = computed(() => activeAlerts.value.length)

// ==================== 节点数据 ====================
const nodeCards = computed(() =>
  nodes.value.map((n) => ({
    name: n.hostname,
    ip: n.ip || '-',
    online: n.status === 'online',
    cpu: metrics.value[n.hostname]?.cpu || 0,
    mem: metrics.value[n.hostname]?.mem || 0,
    disk: metrics.value[n.hostname]?.disk || 0,
    load1: metrics.value[n.hostname]?.load1 || 0,
    load: metrics.value[n.hostname]?.load1 || 0,
    netIn: metrics.value[n.hostname]?.netIn || 0,
    netOut: metrics.value[n.hostname]?.netOut || 0,
    memTotal: metrics.value[n.hostname]?.memTotal || 0,
    procCount: metrics.value[n.hostname]?.procCount || 0,
  }))
)

// ==================== 左上：主机列表 ====================
const hostColumns = [
  { key: 'name', label: '主机名', width: '1.4fr' },
  { key: 'cpu', label: 'CPU', width: '1fr', type: 'bar', align: 'right', max: (rows) => Math.max(...rows.map((r) => r.cpu), 100) },
  { key: 'mem', label: '内存', width: '1fr', type: 'bar', align: 'right', max: (rows) => Math.max(...rows.map((r) => r.mem), 100) },
  { key: 'disk', label: '磁盘', width: '1fr', type: 'bar', align: 'right', max: (rows) => Math.max(...rows.map((r) => r.disk), 100) },
  { key: 'netIn', label: '入流量', width: '0.9fr', type: 'num', align: 'right', fmt: (v) => rateShort(v) },
  { key: 'status', label: '状态', width: '0.7fr', type: 'badge', align: 'center' },
]
const hostRows = computed(() =>
  nodeCards.value.map((n) => ({
    name: n.name,
    cpu: n.cpu,
    mem: n.mem,
    disk: n.disk,
    netIn: n.netIn,
    status: n.online ? '在线' : '离线',
  }))
)

// ==================== 左下：告警列表 ====================
const alertColumns = [
  { key: 'severity', label: '级别', width: '0.7fr', type: 'badge', align: 'center' },
  { key: 'node', label: '节点', width: '1.1fr' },
  { key: 'rule', label: '告警规则', width: '1.8fr' },
  { key: 'time', label: '时间', width: '1.1fr', align: 'right' },
]
const alertRows = computed(() =>
  activeAlerts.value.map((a) => ({
    severity: a.severity === 'critical' ? '故障' : a.severity === 'warning' ? '预警' : '提示',
    node: a.node || '-',
    rule: a.ruleName || a.summary || '未知告警',
    time: fmtShort(a.startsAt),
  }))
)

// ==================== 右上：中间件实例 ====================
const mwColumns = [
  { key: 'type', label: '类型', width: '0.8fr', type: 'badge', align: 'center' },
  { key: 'name', label: '实例名称', width: '1.4fr' },
  { key: 'node', label: '节点', width: '1.1fr' },
  { key: 'status', label: '状态', width: '0.7fr', type: 'badge', align: 'center' },
]
const mwRows = computed(() => mwInstances.value)

// ==================== 右下：Nginx Top ====================
const nginxColumns = [
  { key: 'rank', label: '#', width: '0.4fr', align: 'center' },
  { key: 'uri', label: 'URI / IP', width: '2fr' },
  { key: 'count', label: '请求数', width: '1fr', type: 'num', align: 'right' },
  { key: 'pct', label: '占比', width: '0.8fr', type: 'bar', align: 'right', max: (rows) => Math.max(...rows.map((r) => r.pct), 100) },
]
const nginxRows = computed(() => {
  const s = nginxSummary.value
  if (!s) return []
  const uris = s.topUris || []
  const ips = s.topIps || []
  const maxReq = Math.max(...uris.map((u) => u.count || 0), ...ips.map((i) => i.count || 0), 1)
  const rows = []
  uris.slice(0, 15).forEach((u, i) => {
    rows.push({
      rank: i + 1,
      uri: u.name || u.uri || '-',
      count: u.count || 0,
      pct: ((u.count || 0) / maxReq) * 100,
    })
  })
  ips.slice(0, 15).forEach((ip, i) => {
    rows.push({
      rank: i + 1,
      uri: ip.name || ip.ip || '-',
      count: ip.count || 0,
      pct: ((ip.count || 0) / maxReq) * 100,
    })
  })
  return rows
})

// ==================== 工具函数 ====================
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
  exitFullscreen().finally(() => router.push({ name: 'overview' }))
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

// ==================== 数据加载 ====================
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

// 加载中间件实例（聚合各类型）
const MW_TYPES = ['redis', 'mysql', 'postgres', 'nginx', 'kafka', 'docker', 'rocketmq', 'k8s']
const MW_LABELS = {
  redis: 'Redis', mysql: 'MySQL', postgres: 'PG', nginx: 'Nginx',
  kafka: 'Kafka', docker: 'Docker', rocketmq: 'MQ', k8s: 'K8s',
}
async function loadMiddleware() {
  const results = await Promise.all(
    MW_TYPES.map((t) =>
      t === 'docker'
        ? http.get('/api/v1/middleware/docker/containers').catch(() => ({ containers: [] }))
        : http.get(`/api/v1/middleware/${t}/instances`).catch(() => ({ instances: [] }))
    )
  )
  const list = []
  results.forEach((res, i) => {
    const type = MW_TYPES[i]
    const items = type === 'docker' ? res?.containers || [] : res?.instances || []
    items.forEach((it) => {
      list.push({
        type: MW_LABELS[type] || type,
        name: it.name || it.container || it.ip || it.instance || '-',
        node: it.node || '-',
        status: it.up || it.online ? '在线' : '离线',
      })
    })
  })
  mwInstances.value = list
}

async function loadNginx() {
  try {
    nginxSummary.value = await http.get('/api/v1/middleware/nginx/access/summary')
  } catch (e) {
    nginxSummary.value = null
  }
}

async function refreshAll() {
  await Promise.all([loadBase(), loadMiddleware(), loadNginx()])
  countdown.value = REFRESH_INTERVAL
}

function onVis() {
  visible = document.visibilityState === 'visible'
  if (visible) {
    refreshAll()
    if (!dataTimer) dataTimer = setInterval(refreshAll, REFRESH_INTERVAL * 1000)
  } else if (dataTimer) {
    clearInterval(dataTimer)
    dataTimer = null
  }
}

onMounted(async () => {
  tickClock()
  clockTimer = setInterval(tickClock, 1000)
  // 倒计时
  countTimer = setInterval(() => {
    if (countdown.value > 0) countdown.value--
  }, 1000)
  await loadScreenConfig()
  await refreshAll()
  dataTimer = setInterval(refreshAll, REFRESH_INTERVAL * 1000)
  ws = connectWS('alerts', null, { onMessage: () => loadBase() })
  document.addEventListener('visibilitychange', onVis)
})
onUnmounted(() => {
  clockTimer && clearInterval(clockTimer)
  countTimer && clearInterval(countTimer)
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
  grid-template-areas: 'top' 'main' 'bottom';
  grid-template-rows: 64px 1fr 48px;
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
  z-index: 0;
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
  width: 280px;
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
.hud-center {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 2px;
}
.hud-btn {
  width: 34px;
  height: 34px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--bg-card);
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
  font-size: 22px;
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
  height: 20px;
  background: linear-gradient(180deg, transparent, rgba(34, 211, 238, 0.7), transparent);
}
.ht-cn {
  font-size: 17px;
  font-weight: 700;
  color: var(--text);
  letter-spacing: 0.3em;
  text-shadow: 0 0 18px rgba(34, 211, 238, 0.35);
}

/* 倒计时 */
.countdown {
  display: flex;
  align-items: baseline;
  gap: 4px;
  padding: 2px 14px;
  border-radius: 16px;
  background: rgba(34, 211, 238, 0.06);
  border: 1px solid rgba(34, 211, 238, 0.2);
  transition: all 0.3s;
}
.cd-label {
  font-size: 10px;
  color: var(--text-muted);
  letter-spacing: 0.1em;
}
.cd-num {
  font-size: 18px;
  font-weight: 800;
  color: var(--accent);
  text-shadow: 0 0 10px var(--accent-glow);
  min-width: 22px;
  text-align: center;
}
.cd-unit {
  font-size: 11px;
  color: var(--text-muted);
}
.countdown.urgent {
  background: rgba(239, 68, 68, 0.1);
  border-color: rgba(239, 68, 68, 0.4);
}
.countdown.urgent .cd-num {
  color: var(--danger);
  text-shadow: 0 0 10px rgba(239, 68, 68, 0.5);
  animation: cd-pulse 0.8s infinite;
}
@keyframes cd-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.6; }
}

/* 五面板主区 */
.panel-main {
  grid-area: main;
  display: grid;
  grid-template-columns: 1fr 1.5fr 1fr;
  gap: 10px;
  min-height: 0;
  position: relative;
  z-index: 2;
}
.side-col {
  display: grid;
  grid-template-rows: 1fr 1fr;
  gap: 10px;
  min-height: 0;
}
.side-panel {
  min-height: 0;
}
.center-col {
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
  background: var(--bg-card);
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
@keyframes kpi-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.55; }
}

/* 设置抽屉 */
.cfg-list {
  display: flex;
  flex-direction: column;
  gap: 14px;
  padding: 4px 0;
}

/* ============ 宽屏 / 4K 适配 ============ */
@media (min-width: 2400px) {
  .screen-view {
    padding: 14px 24px;
    gap: 14px;
    grid-template-rows: 84px 1fr 68px;
  }
  .hud-side { width: 360px; }
  .hud-clock { font-size: 18px; }
  .hud-btn { width: 42px; height: 42px; }
  .hud-btn svg { width: 21px; height: 21px; }
  .ht-en { font-size: 30px; letter-spacing: 0.42em; }
  .ht-cn { font-size: 23px; letter-spacing: 0.24em; }
  .ht-sep { height: 28px; }
  .cd-label { font-size: 13px; }
  .cd-num { font-size: 24px; }
  .cd-unit { font-size: 14px; }
  .panel-main { gap: 16px; }
  .side-col { gap: 16px; }
  .ab-label { font-size: 16px; gap: 9px; }
  .ab-item { font-size: 16px; gap: 8px; }
  .ab-none { font-size: 16px; }
}

@media (min-width: 3440px) {
  .screen-view {
    padding: 20px 34px;
    gap: 20px;
    grid-template-rows: 104px 1fr 84px;
  }
  .hud-side { width: 440px; }
  .hud-clock { font-size: 22px; }
  .hud-btn { width: 50px; height: 50px; }
  .hud-btn svg { width: 25px; height: 25px; }
  .ht-en { font-size: 40px; letter-spacing: 0.38em; }
  .ht-cn { font-size: 31px; letter-spacing: 0.2em; }
  .ht-sep { height: 36px; }
  .cd-label { font-size: 16px; }
  .cd-num { font-size: 30px; }
  .cd-unit { font-size: 17px; }
  .panel-main { gap: 22px; }
  .side-col { gap: 22px; }
  .ab-label { font-size: 20px; gap: 11px; }
  .ab-item { font-size: 20px; gap: 10px; }
  .ab-none { font-size: 20px; }
}
</style>
