<template>
  <div class="overview">
    <!-- 顶部：健康度 + KPI + 紧急告警摘要 -->
    <div class="top-grid">
      <!-- 全局健康度 -->
      <div class="glass panel health-panel">
        <div class="panel-title">系统健康度</div>
        <div class="health-body">
          <div class="health-ring" :class="healthLevel">
            <svg viewBox="0 0 120 120" class="ring-svg">
              <circle cx="60" cy="60" r="52" fill="none" stroke="rgba(255,255,255,0.06)" stroke-width="10" />
              <circle
                cx="60" cy="60" r="52" fill="none"
                :stroke="ringColor"
                stroke-width="10"
                stroke-linecap="round"
                :stroke-dasharray="ringDash"
                transform="rotate(-90 60 60)"
                class="ring-progress"
              />
            </svg>
            <div class="health-center">
              <span class="health-num">{{ healthScore }}</span>
              <span class="health-unit">分</span>
            </div>
          </div>
          <div class="health-status">
            <el-tag :type="healthTagType" effect="dark" size="large">{{ healthLabel }}</el-tag>
            <div class="health-breakdown">
              <div class="bd-row">
                <span class="bd-dot green"></span>
                <span class="bd-label">在线率</span>
                <span class="bd-val">{{ onlineRate }}%</span>
              </div>
              <div class="bd-row">
                <span class="bd-dot red"></span>
                <span class="bd-label">活跃告警</span>
                <span class="bd-val">{{ alertCount }}</span>
              </div>
              <div class="bd-row">
                <span class="bd-dot amber"></span>
                <span class="bd-label">离线主机</span>
                <span class="bd-val">{{ offlineCount }}</span>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- KPI 卡片 -->
      <div class="kpi-stack">
        <div class="glass panel kpi">
          <div class="kpi-label">主机总数</div>
          <div class="kpi-value cyan">{{ nodes.length }}</div>
        </div>
        <div class="glass panel kpi">
          <div class="kpi-label">在线</div>
          <div class="kpi-value green">{{ onlineCount }}</div>
        </div>
        <div class="glass panel kpi">
          <div class="kpi-label">告警中</div>
          <div class="kpi-value red">{{ alertCount }}</div>
        </div>
        <div class="glass panel kpi">
          <div class="kpi-label">离线</div>
          <div class="kpi-value amber">{{ offlineCount }}</div>
        </div>
      </div>

      <!-- 紧急告警摘要 -->
      <div class="glass panel alert-summary">
        <div class="panel-title-row">
          <span class="panel-title" style="margin: 0">
            <span class="alert-pulse" v-if="criticalAlerts.length"></span>
            紧急告警
          </span>
          <el-button link size="small" @click="$router.push('/alerts')">全部</el-button>
        </div>
        <div class="critical-list" v-if="criticalAlerts.length">
          <div
            v-for="a in criticalAlerts.slice(0, 5)"
            :key="a.id"
            class="crit-row"
            @click="goNode(a.node)"
          >
            <span class="crit-icon">!</span>
            <div class="crit-body">
              <div class="crit-title">{{ a.ruleName }}</div>
              <div class="crit-meta">{{ a.node }} · {{ fmt(a.startsAt) }}</div>
            </div>
          </div>
        </div>
        <div class="crit-empty" v-else>
          <span class="ok-icon">✓</span>
          <span>无紧急告警</span>
        </div>
      </div>
    </div>

    <!-- 节点卡片网格 + 最近告警 -->
    <div class="panel-grid">
      <!-- 左：节点卡片网格 -->
      <div class="glass panel">
        <div class="panel-title-row">
          <span class="panel-title" style="margin: 0">节点状态</span>
          <span class="panel-hint" v-if="nodes.length">共 {{ nodes.length }} 台 · 点击查看详情</span>
        </div>
        <div class="node-card-grid" v-if="nodes.length">
          <div
            v-for="n in sortedNodes"
            :key="n.hostname"
            class="node-card"
            :class="{ offline: n.status !== 'online', warning: hasWarning(n) }"
            @click="goNode(n.hostname)"
          >
            <div class="nc-head">
              <span class="nc-led" :class="n.status === 'online' ? 'on' : 'off'"></span>
              <OsIcon :os="n.os" />
              <span class="nc-name">{{ n.hostname }}</span>
            </div>
            <div class="nc-ip">{{ n.ip || '-' }}</div>
            <div class="nc-metrics" v-if="n.status === 'online' && m(n.hostname)">
              <div class="nc-bar-row">
                <span class="nc-bar-label">CPU</span>
                <div class="nc-bar">
                  <div class="nc-bar-fill" :class="rateClass(m(n.hostname).cpu)" :style="{ width: pct(m(n.hostname).cpu) + '%' }"></div>
                </div>
                <span class="nc-bar-val">{{ fmtNum(m(n.hostname).cpu) }}%</span>
              </div>
              <div class="nc-bar-row">
                <span class="nc-bar-label">MEM</span>
                <div class="nc-bar">
                  <div class="nc-bar-fill" :class="rateClass(m(n.hostname).mem)" :style="{ width: pct(m(n.hostname).mem) + '%' }"></div>
                </div>
                <span class="nc-bar-val">{{ fmtNum(m(n.hostname).mem) }}%</span>
              </div>
              <div class="nc-bar-row">
                <span class="nc-bar-label">DISK</span>
                <div class="nc-bar">
                  <div class="nc-bar-fill" :class="rateClass(m(n.hostname).disk)" :style="{ width: pct(m(n.hostname).disk) + '%' }"></div>
                </div>
                <span class="nc-bar-val">{{ fmtNum(m(n.hostname).disk) }}%</span>
              </div>
            </div>
            <div class="nc-offline-tip" v-else-if="n.status !== 'online'">已离线</div>
            <div class="nc-loading" v-else>采集数据加载中…</div>
          </div>
        </div>
        <el-empty v-else description="暂无节点" :image-size="80" />
      </div>

      <!-- 右：最近告警 -->
      <div class="glass panel">
        <div class="panel-title">最近告警</div>
        <div class="alert-list" v-if="alerts.length">
          <div v-for="a in alerts.slice(0, 8)" :key="a.id" class="alert-row" :class="a.severity" @click="goNode(a.node)">
            <span class="dot"></span>
            <div class="alert-content">
              <div class="alert-title">{{ a.ruleName }}</div>
              <div class="alert-meta">{{ a.node }} · {{ fmt(a.startsAt) }}</div>
            </div>
            <el-tag :type="a.state === 'firing' ? 'danger' : 'success'" size="small" effect="dark">
              {{ a.state === 'firing' ? '告警' : '恢复' }}
            </el-tag>
          </div>
        </div>
        <el-empty v-else description="暂无告警" :image-size="60" />
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import http from '../api/http'
import OsIcon from './OsIcon.vue'

const router = useRouter()
const nodes = ref([])
const alerts = ref([])
const metrics = ref({})
let timer = null
let visible = true

const onlineCount = computed(() => nodes.value.filter((n) => n.status === 'online').length)
const offlineCount = computed(() => nodes.value.filter((n) => n.status !== 'online').length)
const alertCount = computed(() => alerts.value.filter((a) => a.state === 'firing').length)
const criticalAlerts = computed(() => alerts.value.filter((a) => a.state === 'firing' && a.severity === 'critical'))

const onlineRate = computed(() => {
  if (!nodes.value.length) return 0
  return Math.round((onlineCount.value / nodes.value.length) * 100)
})

// 健康度计算：100 - 离线*15 - 告警*5 - 警告*2，最低 0
const healthScore = computed(() => {
  const firing = alerts.value.filter((a) => a.state === 'firing')
  const crit = firing.filter((a) => a.severity === 'critical').length
  const warn = firing.filter((a) => a.severity === 'warning').length
  let score = 100 - offlineCount.value * 15 - crit * 5 - warn * 2
  return Math.max(0, Math.min(100, score))
})

const healthLevel = computed(() => {
  const s = healthScore.value
  if (s >= 80) return 'green'
  if (s >= 60) return 'amber'
  return 'red'
})
const healthLabel = computed(() => {
  const m = { green: '健康', amber: '警告', red: '严重' }
  return m[healthLevel.value]
})
const healthTagType = computed(() => {
  const m = { green: 'success', amber: 'warning', red: 'danger' }
  return m[healthLevel.value]
})
const ringColor = computed(() => {
  const m = { green: 'var(--accent)', amber: 'var(--warn)', red: 'var(--danger)' }
  return m[healthLevel.value]
})
const ringDash = computed(() => {
  const c = 2 * Math.PI * 52
  const filled = c * (healthScore.value / 100)
  return `${filled} ${c}`
})

// 异常节点置顶排序：离线 > 严重指标 > 警告指标 > 正常
const sortedNodes = computed(() => {
  return [...nodes.value].sort((a, b) => {
    const sa = nodeSeverity(a)
    const sb = nodeSeverity(b)
    return sb - sa
  })
})

function nodeSeverity(n) {
  if (n.status !== 'online') return 100
  const mm = metrics.value[n.hostname]
  if (!mm) return 0
  let s = 0
  if (typeof mm.cpu === 'number' && mm.cpu >= 90) s = Math.max(s, 80)
  if (typeof mm.mem === 'number' && mm.mem >= 90) s = Math.max(s, 80)
  if (typeof mm.disk === 'number' && mm.disk >= 90) s = Math.max(s, 80)
  if (typeof mm.cpu === 'number' && mm.cpu >= 70) s = Math.max(s, 50)
  if (typeof mm.mem === 'number' && mm.mem >= 70) s = Math.max(s, 50)
  if (typeof mm.disk === 'number' && mm.disk >= 70) s = Math.max(s, 50)
  return s
}
function hasWarning(n) {
  return nodeSeverity(n) >= 50 && n.status === 'online'
}

function m(hostname) {
  return metrics.value[hostname] || null
}
function pct(v) {
  if (typeof v !== 'number') return 0
  return Math.min(100, Math.max(0, v))
}
function fmtNum(v) {
  if (typeof v !== 'number') return '--'
  return v.toFixed(1)
}
function rateClass(v) {
  if (typeof v !== 'number') return ''
  if (v >= 90) return 'red'
  if (v >= 70) return 'amber'
  return 'green'
}
function fmt(ts) {
  if (!ts) return '-'
  return new Date(ts).toLocaleString('zh-CN', { hour12: false })
}
function goNode(name) {
  if (name) router.push('/node/' + name)
}

async function load() {
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

function reload() {
  load()
}

function onVis() {
  visible = document.visibilityState === 'visible'
  if (visible) {
    load()
    if (!timer) timer = setInterval(load, 30000)
  } else if (timer) {
    clearInterval(timer)
    timer = null
  }
}

onMounted(() => {
  load()
  timer = setInterval(load, 30000)
  document.addEventListener('visibilitychange', onVis)
})
onUnmounted(() => {
  timer && clearInterval(timer)
  document.removeEventListener('visibilitychange', onVis)
})

defineExpose({ reload })
</script>

<style scoped>
/* 顶部网格：健康度 | KPI | 紧急告警 */
.top-grid {
  display: grid;
  grid-template-columns: 1.2fr 0.8fr 1fr;
  gap: 16px;
  margin-bottom: 16px;
}
.kpi-stack {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px;
}
.kpi-stack .kpi {
  padding: 12px 14px;
}
.kpi-stack .kpi-value {
  font-size: 22px;
}

/* 健康度面板 */
.health-panel {
  display: flex;
  flex-direction: column;
}
.health-body {
  display: flex;
  align-items: center;
  gap: 20px;
  flex: 1;
}
.health-ring {
  position: relative;
  width: 120px;
  height: 120px;
  flex-shrink: 0;
}
.ring-svg {
  width: 100%;
  height: 100%;
}
.ring-progress {
  transition: stroke-dasharray 0.6s ease;
}
.health-center {
  position: absolute;
  top: 0;
  left: 0;
  width: 100%;
  height: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-direction: column;
}
.health-num {
  font-size: 32px;
  font-weight: 800;
  font-family: var(--mono);
  line-height: 1;
}
.health-unit {
  font-size: 11px;
  color: var(--text-dim);
  margin-top: 2px;
}
.health-ring.green .health-num { color: var(--accent); }
.health-ring.amber .health-num { color: var(--warn); }
.health-ring.red .health-num { color: var(--danger); }

.health-status {
  display: flex;
  flex-direction: column;
  gap: 10px;
  flex: 1;
}
.health-breakdown {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.bd-row {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
}
.bd-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
}
.bd-dot.green { background: var(--accent); }
.bd-dot.red { background: var(--danger); }
.bd-dot.amber { background: var(--warn); }
.bd-label {
  color: var(--text-dim);
}
.bd-val {
  margin-left: auto;
  font-family: var(--mono);
  font-weight: 600;
  color: var(--text);
}

/* 紧急告警摘要 */
.alert-summary {
  display: flex;
  flex-direction: column;
}
.alert-pulse {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--danger);
  animation: pulse-red 1s infinite;
  margin-right: 6px;
}
@keyframes pulse-red {
  0%, 100% { opacity: 1; box-shadow: 0 0 0 0 var(--danger); }
  50% { opacity: 0.5; box-shadow: 0 0 0 6px transparent; }
}
.critical-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  overflow-y: auto;
  max-height: 180px;
}
.crit-row {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  padding: 8px 10px;
  background: var(--danger-dim);
  border-radius: 8px;
  border-left: 3px solid var(--danger);
  cursor: pointer;
  transition: background 0.15s;
}
.crit-row:hover {
  background: rgba(244, 63, 94, 0.22);
}
.crit-icon {
  width: 18px;
  height: 18px;
  border-radius: 50%;
  background: var(--danger);
  color: #fff;
  font-size: 12px;
  font-weight: 700;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  margin-top: 1px;
}
.crit-body {
  flex: 1;
  min-width: 0;
}
.crit-title {
  font-size: 13px;
  color: var(--text);
  font-weight: 600;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.crit-meta {
  font-size: 11px;
  color: var(--text-dim);
  margin-top: 2px;
}
.crit-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 8px;
  flex: 1;
  color: var(--text-dim);
  font-size: 13px;
}
.ok-icon {
  width: 36px;
  height: 36px;
  border-radius: 50%;
  background: var(--accent-dim);
  color: var(--accent);
  font-size: 20px;
  display: flex;
  align-items: center;
  justify-content: center;
}

/* 面板标题行 */
.panel-title-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 14px;
}
.panel-hint {
  font-size: 11px;
  color: var(--text-dim);
}

/* 节点卡片网格 */
.panel-grid {
  display: grid;
  grid-template-columns: 1.4fr 1fr;
  gap: 16px;
  margin-bottom: 16px;
}
.node-card-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(190px, 1fr));
  gap: 10px;
  max-height: 360px;
  overflow-y: auto;
  padding: 2px;
}
.node-card {
  background: rgba(255, 255, 255, 0.03);
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 10px 12px;
  cursor: pointer;
  transition: all 0.15s;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.node-card:hover {
  background: color-mix(in srgb, var(--accent) 7%, transparent);
  border-color: var(--accent);
  transform: translateY(-1px);
}
.node-card.offline {
  border-color: var(--danger);
  background: var(--danger-dim);
  opacity: 0.85;
}
.node-card.warning {
  border-color: var(--warn);
  background: var(--warn-dim);
}
.nc-head {
  display: flex;
  align-items: center;
  gap: 6px;
}
.nc-led {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}
.nc-led.on {
  background: var(--accent);
  box-shadow: 0 0 8px var(--accent-glow);
  animation: pulse 2s infinite;
}
.nc-led.off {
  background: var(--danger);
}
@keyframes pulse {
  0%, 100% { box-shadow: 0 0 6px var(--accent-glow); }
  50% { box-shadow: 0 0 12px var(--accent-glow); }
}
.nc-name {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  flex: 1;
}
.nc-ip {
  font-size: 11px;
  color: var(--text-dim);
  font-family: var(--mono);
  margin-left: 14px;
}
.nc-metrics {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-top: 2px;
}
.nc-bar-row {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 11px;
}
.nc-bar-label {
  width: 32px;
  color: var(--text-dim);
  flex-shrink: 0;
}
.nc-bar {
  flex: 1;
  height: 4px;
  background: rgba(255, 255, 255, 0.06);
  border-radius: 2px;
  overflow: hidden;
}
.nc-bar-fill {
  height: 100%;
  border-radius: 2px;
  transition: width 0.3s;
}
.nc-bar-fill.green { background: var(--accent); }
.nc-bar-fill.amber { background: var(--warn); }
.nc-bar-fill.red { background: var(--danger); }
.nc-bar-val {
  width: 36px;
  text-align: right;
  color: var(--text-dim);
  font-family: var(--mono);
  font-size: 10px;
}
.nc-offline-tip {
  font-size: 11px;
  color: var(--danger);
  margin-top: 4px;
}
.nc-loading {
  font-size: 11px;
  color: var(--text-muted);
  margin-top: 4px;
}

/* 告警列表 */
.alert-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
  max-height: 360px;
  overflow-y: auto;
}
.alert-row {
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
.alert-row:hover {
  background: rgba(255, 255, 255, 0.05);
}
.alert-row.critical {
  border-left-color: var(--danger);
}
.alert-row.warning {
  border-left-color: var(--warn);
}
.alert-row .dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--danger);
  flex-shrink: 0;
}
.alert-content {
  flex: 1;
  min-width: 0;
}
.alert-title {
  font-size: 13px;
  color: var(--text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.alert-meta {
  font-size: 11px;
  color: var(--text-dim);
  margin-top: 2px;
}

@media (max-width: 1200px) {
  .top-grid {
    grid-template-columns: 1fr;
  }
}
@media (max-width: 1100px) {
  .panel-grid {
    grid-template-columns: 1fr;
  }
}
</style>
