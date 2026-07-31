<template>
  <div class="screen" :class="healthLevel">
    <!-- 中心拓扑图（SVG/DOM，无 WebGL） -->
    <TopologyMap
      v-if="cfg.modules.topology"
      class="scene-layer"
      :nodes="nodes"
      :metrics="metrics"
      :alerts="activeAlerts"
      :redis-stats="redisSummary"
      :health-score="healthScore"
      :health-level="healthLevel"
      @select-node="goNode"
      @select-redis="goRedis"
    />

    <!-- 顶部 HUD 栏 -->
    <header class="hud-top">
      <div class="hud-brand">
        <div class="brand-mark"></div>
        <div class="brand-txt">
          <h1>NebulaEye 监控大屏</h1>
          <p>Cluster Situation Awareness</p>
        </div>
      </div>
      <div class="hud-clock mono">{{ clock }}</div>
      <div class="hud-actions">
        <el-tag :type="healthTagType" effect="dark" size="large" class="health-badge">
          {{ healthLabel }} · {{ healthScore }}分
        </el-tag>
        <el-button :icon="Setting" circle size="small" @click="settingOpen = true" title="设置" />
        <el-button :icon="FullScreen" circle size="small" @click="toggleFullscreen" title="全屏" />
        <el-button :icon="Back" circle size="small" @click="$router.push('/')" title="返回" />
      </div>
    </header>

    <!-- 左侧 HUD：KPI + 资源概况 + 风险分布 + 健康度环 -->
    <aside class="hud-left">
      <div class="kpi-col" v-if="cfg.modules.kpiTop">
        <div class="glass panel-mini kpi-mini">
          <div class="km-label">主机总数</div>
          <div class="km-val cyan">{{ nodes.length }}</div>
        </div>
        <div class="glass panel-mini kpi-mini">
          <div class="km-label">在线</div>
          <div class="km-val green">{{ onlineCount }}</div>
        </div>
        <div class="glass panel-mini kpi-mini">
          <div class="km-label">告警中</div>
          <div class="km-val red">{{ firingCount }}</div>
        </div>
        <div class="glass panel-mini kpi-mini">
          <div class="km-label">离线</div>
          <div class="km-val amber">{{ offlineCount }}</div>
        </div>
      </div>

      <ScreenGauges
        v-if="cfg.modules.gauges"
        :cpu="avgCpu"
        :mem="avgMem"
        :disk="avgDisk"
        :online-rate="onlineRate"
      />

      <ScreenRisk
        v-if="cfg.modules.risk"
        :nodes="nodes"
        :metrics="metrics"
        :alerts="activeAlerts"
      />

      <div class="glass panel-mini health-mini">
        <div class="hm-title">系统健康度</div>
        <div class="hm-ring" :class="healthLevel">
          <svg viewBox="0 0 120 120">
            <circle cx="60" cy="60" r="52" fill="none" stroke="rgba(255,255,255,0.06)" stroke-width="9" />
            <circle
              cx="60" cy="60" r="52" fill="none"
              :stroke="ringColor" stroke-width="9" stroke-linecap="round"
              :stroke-dasharray="ringDash" transform="rotate(-90 60 60)" class="ring-prog"
            />
          </svg>
          <div class="hm-center">
            <span class="hm-num">{{ healthScore }}</span>
            <span class="hm-unit">分</span>
          </div>
        </div>
        <div class="hm-rows">
          <div class="hm-row"><span class="d green"></span>在线率<b>{{ onlineRate }}%</b></div>
          <div class="hm-row"><span class="d red"></span>活跃告警<b>{{ firingCount }}</b></div>
          <div class="hm-row"><span class="d amber"></span>离线主机<b>{{ offlineCount }}</b></div>
        </div>
      </div>
    </aside>

    <!-- 右侧 HUD：告警列表 + 中间件监控 -->
    <aside class="hud-right" v-if="cfg.modules.alerts || cfg.modules.redis">
      <div class="glass panel-mini alert-mini" v-if="cfg.modules.alerts">
        <div class="am-title">
          <span class="alert-pulse" v-if="firingCount"></span>
          实时告警
          <span class="am-count">{{ firingCount }}</span>
        </div>
        <div class="am-list" v-if="activeAlerts.length">
          <div
            v-for="a in activeAlerts.slice(0, 12)"
            :key="a.id"
            class="am-row"
            :class="a.severity"
            @click="goNode(a.node)"
          >
            <span class="dot"></span>
            <div class="am-body">
              <div class="am-name">{{ a.ruleName }}</div>
              <div class="am-meta">{{ a.node }} · {{ fmt(a.startsAt) }}</div>
            </div>
            <el-tag :type="a.severity === 'critical' ? 'danger' : 'warning'" size="small" effect="dark">
              {{ a.severity === 'critical' ? '严重' : '警告' }}
            </el-tag>
          </div>
        </div>
        <div class="am-empty" v-else>
          <span class="ok">✓</span>
          <span>集群运行正常</span>
        </div>
      </div>
      <ScreenRedis v-if="cfg.modules.redis" @select="goRedis" @summary="onRedisSummary" />
    </aside>

    <!-- 底部 HUD：趋势图 -->
    <footer class="hud-bottom" v-if="cfg.modules.trends">
      <ScreenTrend title="集群 CPU 平均使用率" :series="cpuSeries" unit="%" :color="COLORS.cyan" />
      <ScreenTrend title="集群内存平均使用率" :series="memSeries" unit="%" :color="COLORS.purple" />
      <ScreenTrend title="集群网络入流量" :series="netSeries" unit="rate" :color="COLORS.blue" />
    </footer>

    <!-- 设置抽屉：模块显隐配置（持久化到后端） -->
    <el-drawer v-model="settingOpen" title="大屏设置" size="320px" direction="rtl">
      <div class="setting-body">
        <div class="setting-hint">选择要展示的模块（基于已接入的数据），保存后全局生效。</div>
        <el-checkbox v-model="cfg.modules.topology" label="中心拓扑图" />
        <el-checkbox v-model="cfg.modules.kpiTop" label="KPI 概览卡片" />
        <el-checkbox v-model="cfg.modules.gauges" label="资源概况环图" />
        <el-checkbox v-model="cfg.modules.risk" label="风险等级分布" />
        <el-checkbox v-model="cfg.modules.alerts" label="实时告警列表" />
        <el-checkbox v-model="cfg.modules.redis" label="Redis 中间件面板" />
        <el-checkbox v-model="cfg.modules.trends" label="资源使用趋势" />
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
import { COLORS } from '../../charts/echarts'
import TopologyMap from './TopologyMap.vue'
import ScreenTrend from './ScreenTrend.vue'
import ScreenRedis from './ScreenRedis.vue'
import ScreenGauges from './ScreenGauges.vue'
import ScreenRisk from './ScreenRisk.vue'

const router = useRouter()
const nodes = ref([])
const metrics = ref({})
const alerts = ref([])
const clock = ref('')
const redisSummary = ref({ total: 0, up: 0, down: 0, clusterCount: 0, alertCount: 0 })
const cpuSeries = ref([{ name: 'CPU', color: COLORS.cyan, data: [] }])
const memSeries = ref([{ name: '内存', color: COLORS.purple, data: [] }])
const netSeries = ref([{ name: '入流量', color: COLORS.blue, data: [] }])

// 大屏模块显隐配置（默认全开，后端拉取后覆盖）
const settingOpen = ref(false)
const savingCfg = ref(false)
const cfg = reactive({
  modules: { topology: true, kpiTop: true, gauges: true, risk: true, alerts: true, redis: true, trends: true },
})

let dataTimer = null
let clockTimer = null
let ws = null
let visible = true

const activeAlerts = computed(() => alerts.value.filter((a) => a.state === 'firing'))
const onlineCount = computed(() => nodes.value.filter((n) => n.status === 'online').length)
const offlineCount = computed(() => nodes.value.filter((n) => n.status !== 'online').length)
const firingCount = computed(() => activeAlerts.value.length)
const onlineRate = computed(() => (nodes.value.length ? Math.round((onlineCount.value / nodes.value.length) * 100) : 0))

// 在线节点资源均值（供资源概况环图）
const onlineMetrics = computed(() =>
  nodes.value.filter((n) => n.status === 'online').map((n) => metrics.value[n.hostname] || {})
)
function avgOf(key) {
  const list = onlineMetrics.value
  if (!list.length) return 0
  const sum = list.reduce((s, m) => s + (typeof m[key] === 'number' ? m[key] : 0), 0)
  return +(sum / list.length).toFixed(1)
}
const avgCpu = computed(() => avgOf('cpu'))
const avgMem = computed(() => avgOf('mem'))
const avgDisk = computed(() => avgOf('disk'))

// 健康度：沿用 OverviewView 算法
const healthScore = computed(() => {
  const firing = activeAlerts.value
  const crit = firing.filter((a) => a.severity === 'critical').length
  const warn = firing.filter((a) => a.severity === 'warning').length
  const score = 100 - offlineCount.value * 15 - crit * 5 - warn * 2
  return Math.max(0, Math.min(100, score))
})
const healthLevel = computed(() => {
  const s = healthScore.value
  if (s >= 80) return 'green'
  if (s >= 60) return 'amber'
  return 'red'
})
const healthLabel = computed(() => ({ green: '健康', amber: '警告', red: '严重' }[healthLevel.value]))
const healthTagType = computed(() => ({ green: 'success', amber: 'warning', red: 'danger' }[healthLevel.value]))
const ringColor = computed(() => ({ green: 'var(--accent)', amber: 'var(--warn)', red: 'var(--danger)' }[healthLevel.value]))
const ringDash = computed(() => {
  const c = 2 * Math.PI * 52
  return `${c * (healthScore.value / 100)} ${c}`
})

function fmt(ts) {
  if (!ts) return '-'
  return new Date(ts).toLocaleString('zh-CN', { hour12: false })
}
function goNode(name) {
  if (name) router.push('/node/' + name)
}
function goRedis() {
  router.push('/middleware')
}
function onRedisSummary(s) {
  redisSummary.value = s || redisSummary.value
}

// 拉取大屏模块显隐配置（失败回退默认全开）
async function loadScreenConfig() {
  try {
    const res = await http.get('/api/v1/screen/config')
    if (res && res.modules) {
      cfg.modules = { ...cfg.modules, ...res.modules }
    }
  } catch (e) {
    /* 保持默认全开 */
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
function tickClock() {
  clock.value = new Date().toLocaleString('zh-CN', { hour12: false })
}
function toggleFullscreen() {
  const el = document.documentElement
  if (!document.fullscreenElement) el.requestFullscreen && el.requestFullscreen()
  else document.exitFullscreen && document.exitFullscreen()
}

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

// 集群维度趋势：取在线节点近 30 分钟，CPU/内存做均值、网络入流量做求和
async function loadTrends() {
  if (!visible) return
  const online = nodes.value.filter((n) => n.status === 'online').map((n) => n.hostname)
  if (!online.length) {
    cpuSeries.value = [{ name: 'CPU', color: COLORS.cyan, data: [] }]
    memSeries.value = [{ name: '内存', color: COLORS.purple, data: [] }]
    netSeries.value = [{ name: '入流量', color: COLORS.blue, data: [] }]
    return
  }
  const end = Date.now()
  const start = end - 30 * 60 * 1000
  const step = 60000
  const sample = online.slice(0, 20) // 限制并发，最多取 20 台聚合
  const qs = (node, metric) =>
    http
      .get(`/api/v1/query/range?node=${encodeURIComponent(node)}&metric=${metric}&start=${start}&end=${end}&step=${step}`)
      .catch(() => ({ series: [] }))

  try {
    const [cpuRes, memRes, netRes] = await Promise.all([
      Promise.all(sample.map((n) => qs(n, 'cpu_usage'))),
      Promise.all(sample.map((n) => qs(n, 'mem_used_percent'))),
      Promise.all(sample.map((n) => qs(n, 'network_recv_rate'))),
    ])
    cpuSeries.value = [{ name: 'CPU', color: COLORS.cyan, data: aggregate(cpuRes, 'avg') }]
    memSeries.value = [{ name: '内存', color: COLORS.purple, data: aggregate(memRes, 'avg') }]
    netSeries.value = [{ name: '入流量', color: COLORS.blue, data: aggregate(netRes, 'sum') }]
  } catch (e) {
    /* ignore */
  }
}

// 把多节点 range 结果按时间戳对齐聚合（avg / sum）
function aggregate(resList, mode) {
  const bucket = new Map() // ts -> { sum, count }
  resList.forEach((res) => {
    ;(res.series || []).forEach((s) => {
      ;(s.points || []).forEach((p) => {
        const b = bucket.get(p.timestamp) || { sum: 0, count: 0 }
        b.sum += p.value
        b.count += 1
        bucket.set(p.timestamp, b)
      })
    })
  })
  const out = [...bucket.entries()]
    .sort((a, b) => a[0] - b[0])
    .map(([ts, b]) => [ts, mode === 'avg' ? +(b.sum / b.count).toFixed(2) : +b.sum.toFixed(2)])
  return out
}

function onVis() {
  visible = document.visibilityState === 'visible'
  if (visible) {
    loadBase().then(loadTrends)
    if (!dataTimer) dataTimer = setInterval(refreshAll, 30000)
  } else if (dataTimer) {
    clearInterval(dataTimer)
    dataTimer = null
  }
}

async function refreshAll() {
  await loadBase()
  await loadTrends()
}

onMounted(async () => {
  tickClock()
  clockTimer = setInterval(tickClock, 1000)
  await loadScreenConfig()
  await loadBase()
  await loadTrends()
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
.screen {
  position: fixed;
  inset: 0;
  overflow: hidden;
  background:
    radial-gradient(1200px 800px at 50% 40%, rgba(0, 217, 163, 0.06), transparent 70%),
    radial-gradient(900px 600px at 80% 90%, rgba(56, 189, 248, 0.05), transparent 70%),
    var(--bg);
}
.scene-layer {
  position: absolute;
  inset: 0;
  z-index: 0;
}

/* HUD 分层：面板可交互，中间留给 3D 拖拽 */
.hud-top,
.hud-left,
.hud-right,
.hud-bottom {
  position: absolute;
  z-index: 10;
  pointer-events: none;
}
.hud-top > *,
.hud-left > *,
.hud-right > *,
.hud-bottom > * {
  pointer-events: auto;
}

/* 顶部栏 */
.hud-top {
  top: 0;
  left: 0;
  right: 0;
  height: 62px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 20px;
  background: linear-gradient(180deg, rgba(10, 14, 20, 0.85), transparent);
}
.hud-brand {
  display: flex;
  align-items: center;
  gap: 12px;
}
.brand-mark {
  width: 34px;
  height: 34px;
  border-radius: 9px;
  background: linear-gradient(135deg, var(--accent), #00a37a);
  box-shadow: 0 0 18px var(--accent-glow);
  position: relative;
}
.brand-mark::after {
  content: '';
  position: absolute;
  inset: 8px;
  border: 2px solid #002b22;
  border-radius: 3px;
  border-top-color: transparent;
  border-right-color: transparent;
  transform: rotate(-45deg);
}
.brand-txt h1 {
  font-size: 18px;
  font-weight: 700;
  letter-spacing: 0.04em;
  background: linear-gradient(90deg, #e6edf3, var(--accent));
  -webkit-background-clip: text;
  background-clip: text;
  -webkit-text-fill-color: transparent;
}
.brand-txt p {
  font-size: 10px;
  color: var(--text-dim);
  letter-spacing: 0.15em;
  text-transform: uppercase;
}
.hud-clock {
  position: absolute;
  left: 50%;
  transform: translateX(-50%);
  font-size: 18px;
  font-weight: 600;
  color: var(--text);
  letter-spacing: 0.05em;
  text-shadow: 0 0 12px rgba(0, 217, 163, 0.3);
}
.hud-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}
.health-badge {
  font-weight: 600;
}

/* 左侧 */
.hud-left {
  top: 74px;
  left: 20px;
  width: 244px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.panel-mini {
  padding: 12px 14px;
}
.kpi-col {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 10px;
}
.kpi-mini {
  text-align: center;
  padding: 10px 8px;
}
.km-label {
  font-size: 11px;
  color: var(--text-dim);
  text-transform: uppercase;
  letter-spacing: 0.04em;
  margin-bottom: 4px;
}
.km-val {
  font-size: 26px;
  font-weight: 800;
  font-family: var(--mono);
}
.km-val.cyan { color: var(--info); }
.km-val.green { color: var(--accent); }
.km-val.red { color: var(--danger); }
.km-val.amber { color: var(--warn); }

.health-mini {
  display: flex;
  flex-direction: column;
  align-items: center;
}
.hm-title {
  font-size: 13px;
  color: var(--text);
  font-weight: 600;
  margin-bottom: 8px;
  align-self: flex-start;
}
.hm-ring {
  position: relative;
  width: 130px;
  height: 130px;
}
.hm-ring svg {
  width: 100%;
  height: 100%;
}
.ring-prog {
  transition: stroke-dasharray 0.6s ease;
}
.hm-center {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-direction: column;
}
.hm-num {
  font-size: 34px;
  font-weight: 800;
  font-family: var(--mono);
  line-height: 1;
}
.hm-ring.green .hm-num { color: var(--accent); }
.hm-ring.amber .hm-num { color: var(--warn); }
.hm-ring.red .hm-num { color: var(--danger); }
.hm-unit {
  font-size: 11px;
  color: var(--text-dim);
  margin-top: 2px;
}
.hm-rows {
  width: 100%;
  margin-top: 12px;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.hm-row {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--text-dim);
}
.hm-row b {
  margin-left: auto;
  color: var(--text);
  font-family: var(--mono);
}
.hm-row .d {
  width: 6px;
  height: 6px;
  border-radius: 50%;
}
.d.green { background: var(--accent); }
.d.red { background: var(--danger); }
.d.amber { background: var(--warn); }

/* 右侧告警 + 中间件 */
.hud-right {
  top: 74px;
  right: 20px;
  width: 300px;
  bottom: 208px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.alert-mini {
  flex: 1 1 55%;
  display: flex;
  flex-direction: column;
  min-height: 0;
}
.screen-redis {
  flex: 1 1 45%;
  min-height: 0;
}
.am-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 14px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 10px;
}
.am-count {
  margin-left: auto;
  font-family: var(--mono);
  color: var(--danger);
  font-weight: 700;
}
.alert-pulse {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--danger);
  animation: pulse-red 1s infinite;
}
@keyframes pulse-red {
  0%, 100% { opacity: 1; box-shadow: 0 0 0 0 var(--danger); }
  50% { opacity: 0.5; box-shadow: 0 0 0 6px transparent; }
}
.am-list {
  flex: 1;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.am-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  background: rgba(255, 255, 255, 0.02);
  border-radius: 8px;
  border-left: 3px solid var(--text-muted);
  cursor: pointer;
  transition: background 0.15s;
}
.am-row:hover {
  background: rgba(255, 255, 255, 0.05);
}
.am-row.critical { border-left-color: var(--danger); }
.am-row.warning { border-left-color: var(--warn); }
.am-row .dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--danger);
  flex-shrink: 0;
}
.am-body {
  flex: 1;
  min-width: 0;
}
.am-name {
  font-size: 13px;
  color: var(--text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.am-meta {
  font-size: 11px;
  color: var(--text-dim);
  margin-top: 2px;
}
.am-empty {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
  color: var(--text-dim);
  font-size: 13px;
}
.am-empty .ok {
  width: 40px;
  height: 40px;
  border-radius: 50%;
  background: var(--accent-dim);
  color: var(--accent);
  font-size: 22px;
  display: flex;
  align-items: center;
  justify-content: center;
}

/* 底部趋势 */
.hud-bottom {
  bottom: 16px;
  left: 20px;
  right: 20px;
  height: 176px;
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 14px;
}

/* 响应式 */
@media (max-width: 1400px) {
  .hud-left { width: 210px; }
  .hud-right { width: 260px; }
}
@media (max-width: 1100px) {
  .hud-clock { display: none; }
  .hud-left { width: 180px; }
  .hud-right {
    width: 240px;
    bottom: 16px;
  }
  .hud-bottom {
    left: 200px;
    right: 260px;
    grid-template-columns: 1fr;
    height: auto;
    max-height: 176px;
    overflow: hidden;
  }
  .hud-bottom .screen-trend:nth-child(n + 2) { display: none; }
}
@media (max-width: 820px) {
  .hud-left,
  .hud-right {
    width: 46vw;
    max-width: 260px;
  }
  .hud-right { bottom: auto; height: 60vh; }
  .hud-bottom { left: 20px; right: 20px; bottom: 8px; }
}

/* 设置抽屉 */
.setting-body {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.setting-hint {
  font-size: 12px;
  color: var(--text-dim);
  line-height: 1.5;
  margin-bottom: 4px;
}
</style>
