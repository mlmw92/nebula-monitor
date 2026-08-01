<template>
  <div class="screen blue-theme" :class="healthLevel">
    <!-- 中心拓扑图（SVG/DOM，无 WebGL），铺满中列 -->
    <TopologyMap
      v-if="cfg.modules.topology"
      class="scene-layer"
      :nodes="nodes"
      :metrics="metrics"
      :alerts="activeAlerts"
      :redis-stats="redisSummary"
      :docker-stats="dockerSummary"
      :health-score="healthScore"
      :health-level="healthLevel"
      @select-node="goNode"
      @select-redis="goRedis"
      @select-docker="goDocker"
    />

    <!-- 顶部标题栏 -->
    <header class="hud-top">
      <div class="hud-side hud-left-side">
        <div class="corner tl"></div>
      </div>
      <div class="hud-title">
        <span class="title-deco left"></span>
        <h1>服务器运维数据可视化大屏</h1>
        <span class="title-deco right"></span>
      </div>
      <div class="hud-side hud-right-side">
        <span class="hud-clock mono">{{ clock }}</span>
        <el-button :icon="Setting" circle size="small" @click="settingOpen = true" title="设置" />
        <el-button :icon="FullScreen" circle size="small" @click="toggleFullscreen" title="全屏" />
        <el-button :icon="Back" circle size="small" @click="$router.push('/')" title="返回" />
      </div>
    </header>

    <!-- 顶部 KPI 横排（6 卡片） -->
    <div class="kpi-row" v-if="cfg.modules.kpiTop">
      <div class="glass kpi-card" v-for="k in kpis" :key="k.key">
        <div class="kpi-label">{{ k.label }}</div>
        <div class="kpi-val" :class="k.tone">
          {{ k.value }}<span class="kpi-unit" v-if="k.unit">{{ k.unit }}</span>
        </div>
      </div>
    </div>

    <!-- 左列 -->
    <aside class="col-left">
      <ScreenGauges
        v-if="cfg.modules.gauges"
        :cpu="avgCpu"
        :mem="avgMem"
        :disk="avgDisk"
        :online-rate="onlineRate"
        :load-pct="avgLoadPct"
        :group-count="groupCount"
      />
      <ScreenRisk
        v-if="cfg.modules.risk"
        :nodes="nodes"
        :metrics="metrics"
        :alerts="activeAlerts"
      />
    </aside>

    <!-- 右列 -->
    <aside class="col-right">
      <div class="glass panel-mini alert-table" v-if="cfg.modules.alerts">
        <div class="at-title">
          <span class="alert-pulse" v-if="firingCount"></span>
          故障告警列表
          <span class="at-count">{{ firingCount }}</span>
        </div>
        <div class="at-head">
          <span class="at-c-lv">级别</span>
          <span class="at-c-dev">告警设备</span>
          <span class="at-c-time">发生时间</span>
          <span class="at-c-st">状态</span>
        </div>
        <div class="at-body" v-if="activeAlerts.length">
          <div
            v-for="a in activeAlerts.slice(0, 10)"
            :key="a.id"
            class="at-row"
            :class="a.severity"
            @click="goNode(a.node)"
          >
            <span class="at-c-lv">
              <span class="lv-dot" :class="a.severity"></span>
              {{ a.severity === 'critical' ? '故障' : a.severity === 'warning' ? '预警' : '提示' }}
            </span>
            <span class="at-c-dev" :title="a.ruleName + ' · ' + a.node">{{ a.node }}</span>
            <span class="at-c-time mono">{{ fmtShort(a.startsAt) }}</span>
            <span class="at-c-st" :class="a.severity">未处理</span>
          </div>
        </div>
        <div class="at-empty" v-else>
          <span class="ok">✓</span>
          <span>集群运行正常</span>
        </div>
      </div>
      <ScreenContainers v-if="cfg.modules.containers" @summary="onDockerSummary" />
      <ScreenRedis v-if="cfg.modules.redis" @select="goRedis" @summary="onRedisSummary" />
    </aside>

    <!-- 底部四格 -->
    <footer class="hud-bottom">
      <div class="bottom-cell" v-if="cfg.modules.trends">
        <div class="glass panel-mini trend-pack">
          <div class="tp-title">资源使用趋势</div>
          <div class="tp-body">
            <ScreenTrend title="CPU" :series="cpuSeries" unit="%" :color="COLORS.cyan" compact />
            <ScreenTrend title="内存" :series="memSeries" unit="%" :color="COLORS.blue" compact />
            <ScreenTrend title="入流量" :series="netSeries" unit="rate" :color="COLORS.purple" compact />
          </div>
        </div>
      </div>
      <div class="bottom-cell" v-if="cfg.modules.healthScore">
        <ScreenHealthScore
          :score="healthScore"
          :online-rate="onlineRate"
          :alert-free-rate="alertFreeRate"
          :cpu-headroom="cpuHeadroom"
          :disk-headroom="diskHeadroom"
        />
      </div>
      <div class="bottom-cell" v-if="cfg.modules.alertLevels">
        <ScreenAlertLevels :alerts="activeAlerts" />
      </div>
      <div class="bottom-cell" v-if="cfg.modules.dbConn">
        <ScreenDbConn />
      </div>
    </footer>

    <!-- 设置抽屉：模块显隐配置（持久化到后端） -->
    <el-drawer v-model="settingOpen" title="大屏设置" size="320px" direction="rtl">
      <div class="setting-body">
        <div class="setting-hint">选择要展示的模块（基于已接入的数据），保存后全局生效。</div>
        <el-checkbox v-model="cfg.modules.kpiTop" label="顶部 KPI 卡片" />
        <el-checkbox v-model="cfg.modules.topology" label="中心数据中心拓扑" />
        <el-checkbox v-model="cfg.modules.gauges" label="资源概况环图" />
        <el-checkbox v-model="cfg.modules.risk" label="风险等级分布" />
        <el-checkbox v-model="cfg.modules.alerts" label="故障告警列表" />
        <el-checkbox v-model="cfg.modules.containers" label="容器状态" />
        <el-checkbox v-model="cfg.modules.redis" label="Redis 中间件面板" />
        <el-checkbox v-model="cfg.modules.trends" label="资源使用趋势" />
        <el-checkbox v-model="cfg.modules.healthScore" label="服务健康评分" />
        <el-checkbox v-model="cfg.modules.alertLevels" label="告警级别统计" />
        <el-checkbox v-model="cfg.modules.dbConn" label="数据库连接" />
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
import ScreenContainers from './ScreenContainers.vue'
import ScreenHealthScore from './ScreenHealthScore.vue'
import ScreenAlertLevels from './ScreenAlertLevels.vue'
import ScreenDbConn from './ScreenDbConn.vue'

const router = useRouter()
const nodes = ref([])
const metrics = ref({})
const alerts = ref([])
const clock = ref('')
const redisSummary = ref({ total: 0, up: 0, down: 0, clusterCount: 0, alertCount: 0 })
const dockerSummary = ref({ total: 0, running: 0, stopped: 0, abnormal: 0 })
const cpuSeries = ref([{ name: 'CPU', color: COLORS.cyan, data: [] }])
const memSeries = ref([{ name: '内存', color: COLORS.blue, data: [] }])
const netSeries = ref([{ name: '入流量', color: COLORS.purple, data: [] }])

// 大屏模块显隐配置（默认全开，后端拉取后覆盖）
const settingOpen = ref(false)
const savingCfg = ref(false)
const cfg = reactive({
  modules: {
    kpiTop: true, topology: true, gauges: true, risk: true, alerts: true,
    containers: true, redis: true, trends: true, healthScore: true,
    alertLevels: true, dbConn: true,
  },
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
const groupCount = computed(() => new Set(nodes.value.map((n) => n.group || '默认分组')).size)

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
// 平均负载归一化为百分比（load1 / 8 截断到 100，无核数信息时的近似）
const avgLoadPct = computed(() => Math.min(100, Math.round((avgOf('load1') / 8) * 100)))

// 今日新增告警数（startsAt 为今天）
const todayAlertCount = computed(() => {
  const start = new Date(); start.setHours(0, 0, 0, 0)
  const t0 = start.getTime()
  return alerts.value.filter((a) => a.startsAt && new Date(a.startsAt).getTime() >= t0).length
})

// 实时入流量（在线节点 netIn 求和，格式化）
const netInTotal = computed(() => onlineMetrics.value.reduce((s, m) => s + (m.netIn || 0), 0))
function fmtRate(b) {
  if (!b || b <= 0) return '0'
  if (b >= 1 << 30) return (b / (1 << 30)).toFixed(1)
  if (b >= 1 << 20) return (b / (1 << 20)).toFixed(1)
  if (b >= 1 << 10) return (b / (1 << 10)).toFixed(1)
  return b.toFixed(0)
}
function fmtRateUnit(b) {
  if (!b || b <= 0) return 'B/s'
  if (b >= 1 << 30) return 'GB/s'
  if (b >= 1 << 20) return 'MB/s'
  if (b >= 1 << 10) return 'KB/s'
  return 'B/s'
}

// 顶部 6 KPI（全部真实数据；无对应数据源的项已替换为最接近真实指标）
const kpis = computed(() => [
  { key: 'total', label: '服务器总数', value: nodes.value.length, unit: '台', tone: 'cyan' },
  { key: 'online', label: '在线主机', value: onlineCount.value, unit: '台', tone: 'green' },
  { key: 'containers', label: '容器实例', value: dockerSummary.value.total || 0, unit: '个', tone: 'blue' },
  { key: 'disk', label: '平均磁盘使用率', value: avgDisk.value, unit: '%', tone: 'purple' },
  { key: 'net', label: '实时入流量', value: fmtRate(netInTotal.value), unit: fmtRateUnit(netInTotal.value), tone: 'cyan' },
  { key: 'alertToday', label: '今日告警', value: todayAlertCount.value, unit: '条', tone: firingCount.value ? 'red' : 'green' },
])

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
// 健康评分派生维度（真实）
const alertFreeRate = computed(() => {
  const t = nodes.value.length || 1
  const alertingNodes = new Set(activeAlerts.value.map((a) => a.node)).size
  return Math.max(0, Math.round(((t - alertingNodes) / t) * 100))
})
const cpuHeadroom = computed(() => Math.max(0, 100 - avgCpu.value))
const diskHeadroom = computed(() => Math.max(0, 100 - avgDisk.value))

function fmtShort(ts) {
  if (!ts) return '-'
  const d = new Date(ts)
  const p = (n) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}
function goNode(name) {
  if (name) router.push('/node/' + name)
}
function goRedis() {
  router.push('/middleware')
}
function goDocker() {
  router.push('/middleware')
}
function onRedisSummary(s) {
  redisSummary.value = s || redisSummary.value
}
function onDockerSummary(s) {
  dockerSummary.value = s || dockerSummary.value
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
    memSeries.value = [{ name: '内存', color: COLORS.blue, data: [] }]
    netSeries.value = [{ name: '入流量', color: COLORS.purple, data: [] }]
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
    memSeries.value = [{ name: '内存', color: COLORS.blue, data: aggregate(memRes, 'avg') }]
    netSeries.value = [{ name: '入流量', color: COLORS.purple, data: aggregate(netRes, 'sum') }]
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
/* 科技风大屏：accent 跟随全局主题（默认极光蓝） */
.screen {
  position: fixed;
  inset: 0;
  overflow: hidden;
  display: grid;
  grid-template-columns: 264px 1fr 300px;
  grid-template-rows: 52px auto 1fr 210px;
  grid-template-areas:
    'top    top     top'
    'kpi    kpi     kpi'
    'left   center  right'
    'bottom bottom  bottom';
  gap: 10px;
  padding: 10px 14px 12px;
  background:
    radial-gradient(1200px 700px at 50% 30%, rgba(56, 189, 248, 0.08), transparent 70%),
    radial-gradient(900px 600px at 15% 90%, rgba(30, 144, 255, 0.06), transparent 70%),
    radial-gradient(900px 600px at 85% 90%, rgba(56, 189, 248, 0.05), transparent 70%),
    #060b16;
}

/* 中心拓扑铺在 center 网格区 */
.scene-layer {
  grid-area: center;
  position: relative;
  min-height: 0;
}

/* 顶部标题栏 */
.hud-top {
  grid-area: top;
  display: grid;
  grid-template-columns: 1fr auto 1fr;
  align-items: center;
}
.hud-side {
  display: flex;
  align-items: center;
  gap: 8px;
}
.hud-right-side {
  justify-content: flex-end;
}
.hud-clock {
  font-size: 13px;
  color: var(--info);
  margin-right: 6px;
  letter-spacing: 0.04em;
}
.hud-title {
  display: flex;
  align-items: center;
  gap: 14px;
}
.hud-title h1 {
  margin: 0;
  font-size: 24px;
  font-weight: 800;
  letter-spacing: 0.12em;
  background: var(--brand-grad);
  -webkit-background-clip: text;
  background-clip: text;
  color: transparent;
  text-shadow: 0 0 22px var(--accent-glow);
}
.title-deco {
  width: 60px;
  height: 2px;
  background: linear-gradient(90deg, transparent, var(--info));
}
.title-deco.right {
  background: linear-gradient(90deg, var(--info), transparent);
}

/* KPI 行 */
.kpi-row {
  grid-area: kpi;
  display: grid;
  grid-template-columns: repeat(6, 1fr);
  gap: 10px;
}
.kpi-card {
  display: flex;
  flex-direction: column;
  justify-content: center;
  align-items: center;
  padding: 8px 10px;
  border-radius: 10px;
  position: relative;
  overflow: hidden;
}
.kpi-card::before {
  content: '';
  position: absolute;
  left: 0; top: 0; bottom: 0;
  width: 3px;
  background: var(--info);
  box-shadow: 0 0 10px var(--info);
}
.kpi-label {
  font-size: 12px;
  color: var(--text-dim);
  margin-bottom: 3px;
}
.kpi-val {
  font-size: 26px;
  font-weight: 800;
  font-family: var(--mono);
  line-height: 1.05;
  color: var(--text);
}
.kpi-val.cyan { color: var(--info); }
.kpi-val.blue { color: var(--info); }
.kpi-val.green { color: var(--accent); }
.kpi-val.purple { color: var(--violet); }
.kpi-val.red { color: var(--danger); }
.kpi-unit {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-dim);
  margin-left: 3px;
}

/* 左右列 */
.col-left {
  grid-area: left;
  display: flex;
  flex-direction: column;
  gap: 10px;
  min-height: 0;
}
.col-right {
  grid-area: right;
  display: flex;
  flex-direction: column;
  gap: 10px;
  min-height: 0;
}
.col-left > *,
.col-right > * {
  flex: 1;
  min-height: 0;
}

/* 故障告警表格 */
.alert-table {
  display: flex;
  flex-direction: column;
  padding: 12px 14px;
  min-height: 0;
}
.at-title {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 14px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 8px;
}
.at-count {
  margin-left: auto;
  font-size: 12px;
  color: var(--danger);
  font-family: var(--mono);
}
.alert-pulse {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--danger);
  box-shadow: 0 0 0 0 rgba(244, 63, 94, 0.5);
  animation: pulse 1.5s infinite;
}
@keyframes pulse {
  0% { box-shadow: 0 0 0 0 rgba(244, 63, 94, 0.5); }
  70% { box-shadow: 0 0 0 6px rgba(244, 63, 94, 0); }
  100% { box-shadow: 0 0 0 0 rgba(244, 63, 94, 0); }
}
.at-head,
.at-row {
  display: grid;
  grid-template-columns: 52px 1fr 78px 46px;
  align-items: center;
  gap: 4px;
}
.at-head {
  font-size: 11px;
  color: var(--text-dim);
  padding: 4px 0;
  border-bottom: 1px solid rgba(255, 255, 255, 0.08);
}
.at-body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
}
.at-row {
  font-size: 12px;
  padding: 6px 0;
  border-bottom: 1px solid rgba(255, 255, 255, 0.04);
  cursor: pointer;
  transition: background 0.15s;
}
.at-row:hover { background: rgba(255, 255, 255, 0.03); }
.at-c-lv {
  display: flex;
  align-items: center;
  gap: 4px;
  color: var(--text);
}
.lv-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  flex-shrink: 0;
}
.lv-dot.critical { background: var(--danger); }
.lv-dot.warning { background: var(--warn); }
.lv-dot.info { background: var(--info); }
.at-c-dev {
  color: var(--text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.at-c-time { color: var(--text-dim); font-size: 11px; }
.at-c-st {
  font-size: 11px;
  text-align: right;
}
.at-c-st.critical { color: var(--danger); }
.at-c-st.warning { color: var(--warn); }
.at-c-st.info { color: var(--info); }
.at-empty {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  color: var(--text-dim);
  font-size: 12px;
}
.at-empty .ok {
  font-size: 24px;
  color: var(--accent);
}

/* 底部四格 */
.hud-bottom {
  grid-area: bottom;
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 10px;
  min-height: 0;
}
.bottom-cell {
  min-height: 0;
  display: flex;
}
.bottom-cell > * {
  flex: 1;
  min-height: 0;
}
.trend-pack {
  display: flex;
  flex-direction: column;
  padding: 12px 14px;
  min-height: 0;
}
.tp-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 6px;
}
.tp-body {
  flex: 1;
  display: grid;
  grid-template-rows: repeat(3, 1fr);
  gap: 4px;
  min-height: 0;
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

/* 响应式 */
@media (max-width: 1400px) {
  .screen { grid-template-columns: 234px 1fr 270px; }
  .kpi-val { font-size: 22px; }
}
@media (max-width: 1100px) {
  .hud-title h1 { font-size: 18px; letter-spacing: 0.06em; }
  .title-deco { width: 30px; }
  .screen {
    grid-template-columns: 200px 1fr 230px;
    grid-template-rows: 48px auto 1fr 190px;
  }
}
</style>
