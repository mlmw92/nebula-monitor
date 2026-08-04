<template>
  <div class="screen-view">
    <!-- 粒子背景 -->
    <ParticleBg />

    <!-- 顶栏 -->
    <header class="hud-top">
      <div class="hud-side hud-left">
        <div class="hud-clock mono">
          <span class="clock-text">{{ clock }}</span>
          <!-- 实时倒计时 -->
          <div class="countdown" :class="{ urgent: countdown <= 5 }">
            <span class="cd-label">刷新</span>
            <span class="cd-num mono">{{ countdown }}</span>
            <span class="cd-unit">s</span>
          </div>
        </div>
        <span class="weekday-text">{{ weekday }}</span>
      </div>

      <div class="hud-center">
        <h1 class="hud-title">
          <span class="ht-sub">{{ brand.name }}</span>
          <span class="ht-main">监控指挥中心</span>
        </h1>
        <div class="title-deco">
          <span class="td-line"></span>
          <span class="td-dot"></span>
          <span class="td-line"></span>
        </div>
        <div class="hud-tabs">
          <ScreenTabBar :active="activeTab" :firingCount="firingCount" @update:active="activeTab = $event" />
        </div>
      </div>

      <div class="hud-side hud-right">
        <div class="hud-actions">
          <button class="hud-btn" title="全屏" @click="toggleFullscreen"><FullScreen /></button>
          <button class="hud-btn" title="模块设置" @click="settingOpen = true"><Setting /></button>
          <button class="hud-btn" title="返回概览" @click="goBack"><Back /></button>
        </div>
      </div>
    </header>

    <!-- 持久 KPI 条 -->
    <div class="hud-kpi-bar">
      <div class="kpi-bar-item" v-for="k in kpiBar" :key="k.key">
        <span class="kpi-bar-label">{{ k.label }}</span>
        <span class="kpi-bar-value" :style="{ color: k.color }">{{ k.value }}</span>
        <span class="kpi-bar-unit" v-if="k.unit">{{ k.unit }}</span>
      </div>
    </div>

    <!-- Tab 内容区 -->
    <main class="tab-content">
      <Transition name="tab-fade" mode="out-in">
        <OverviewTab
          v-if="activeTab === 'overview'"
          key="overview"
          :nodes="nodes"
          :metrics="metrics"
          :alerts="alerts"
        />
        <HostsTab
          v-else-if="activeTab === 'hosts'"
          key="hosts"
          :nodes="nodes"
          :metrics="metrics"
        />
        <MiddlewareTab
          v-else-if="activeTab === 'middleware'"
          key="middleware"
        />
        <NetworkTab
          v-else-if="activeTab === 'network'"
          key="network"
          :nodes="nodeNames"
        />
        <AlertsTab
          v-else-if="activeTab === 'alerts'"
          key="alerts"
          :nodes="nodes"
          :metrics="metrics"
          :alerts="alerts"
        />
      </Transition>
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
        <div class="cfg-item">
          <span class="cfg-label">刷新间隔</span>
          <el-select v-model="cfg.refreshInterval" size="small" style="width: 110px">
            <el-option v-for="s in intervalOptions" :key="s" :value="s" :label="`${s} 秒`" />
          </el-select>
        </div>
        <div class="cfg-tip">数据自动刷新周期，同时也是大屏右上角倒计时总时长。</div>
        <div class="cfg-item">
          <span class="cfg-label">服务器所在地</span>
          <el-select v-model="cfg.deployLocation" size="small" filterable style="width: 110px">
            <el-option v-for="p in provinceOptions" :key="p" :value="p" :label="p" />
          </el-select>
        </div>
        <div class="cfg-tip">网络地图上标注的数据中心位置，访问动线由各来源地指向该点。</div>
      </div>
      <template #footer>
        <el-button @click="settingOpen = false">取消</el-button>
        <el-button type="primary" :loading="savingCfg" @click="onSaveCfg">保存</el-button>
      </template>
    </el-drawer>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { FullScreen, Back, Setting } from '@element-plus/icons-vue'
import { connectWS } from '../../api/ws'
import { rateShort } from '../../charts/echarts'
import ParticleBg from './ParticleBg.vue'
import ScreenTabBar from './ScreenTabBar.vue'
import OverviewTab from './tabs/OverviewTab.vue'
import HostsTab from './tabs/HostsTab.vue'
import MiddlewareTab from './tabs/MiddlewareTab.vue'
import NetworkTab from './tabs/NetworkTab.vue'
import AlertsTab from './tabs/AlertsTab.vue'
import { useBrand } from '../../composables/useBrand'
import { useScreenData } from './composables/useScreenData'
import { useCountdown } from './composables/useCountdown'
import { useScreenConfig, REFRESH_INTERVALS } from './composables/useScreenConfig'
import { CN_PROVINCES } from '../../charts/geoCoords'

const router = useRouter()
const { brand } = useBrand()
const { nodes, metrics, alerts, activeAlerts, firingCount, nodeCards, refreshAll } = useScreenData()
const refreshInterval = ref(30)
const intervalOptions = REFRESH_INTERVALS
const provinceOptions = CN_PROVINCES
const { countdown, start: startCountdown, reset: resetCountdown } = useCountdown(refreshInterval)
const { cfg, settingOpen, savingCfg, loadScreenConfig, saveScreenConfig } = useScreenConfig()

// 应用刷新间隔：同步到倒计时并重启数据定时器
function applyInterval() {
  if (cfg.refreshInterval > 0) refreshInterval.value = cfg.refreshInterval
  resetCountdown()
  if (dataTimer) {
    clearInterval(dataTimer)
    dataTimer = setInterval(() => {
      refreshAll()
      resetCountdown()
    }, refreshInterval.value * 1000)
  }
}

// 保存大屏配置并应用刷新间隔；立即拉一次数据使新的服务器所在地生效
async function onSaveCfg() {
  await saveScreenConfig()
  applyInterval()
  refreshAll()
}
const activeTab = ref('overview')
const clock = ref('')
const weekday = ref('')
let dataTimer = null
let clockTimer = null
let ws = null
let visible = true

// 节点名列表（用于趋势查询）
const nodeNames = computed(() => nodeCards.value.filter((n) => n.online).map((n) => n.name))

// 持久 KPI 条数据
const kpiBar = computed(() => {
  const online = nodeCards.value.filter((n) => n.online !== false).length
  const total = nodeCards.value.length
  const cpu = total > 0 ? nodeCards.value.reduce((s, n) => s + (n.cpu || 0), 0) / total : 0
  const mem = total > 0 ? nodeCards.value.reduce((s, n) => s + (n.mem || 0), 0) / total : 0

  function usageColor(v) {
    if (v >= 90) return 'var(--danger)'
    if (v >= 70) return 'var(--warn)'
    return 'var(--accent)'
  }

  return [
    { key: 'hosts', label: '主机', value: `${online}/${total}`, color: 'var(--accent)', unit: '在线/总数' },
    { key: 'cpu', label: 'CPU', value: cpu.toFixed(1), color: usageColor(cpu), unit: '%' },
    { key: 'mem', label: '内存', value: mem.toFixed(1), color: usageColor(mem), unit: '%' },
    { key: 'alerts', label: '告警', value: firingCount.value, color: firingCount.value > 0 ? 'var(--danger)' : 'var(--chart-green)', unit: firingCount.value > 0 ? '条活跃' : '正常' },
    { key: 'health', label: '健康', value: healthScore.value, color: healthScoreColor.value, unit: '分' },
  ]
})

const healthScore = computed(() => {
  const online = nodeCards.value.filter((n) => n.online !== false).length
  const total = nodeCards.value.length
  const cpu = total > 0 ? nodeCards.value.reduce((s, n) => s + (n.cpu || 0), 0) / total : 0
  const mem = total > 0 ? nodeCards.value.reduce((s, n) => s + (n.mem || 0), 0) / total : 0
  const disk = total > 0 ? nodeCards.value.reduce((s, n) => s + (n.disk || 0), 0) / total : 0
  const onlineScore = total > 0 ? (online / total) * 100 : 100
  const cpuScore = Math.max(0, 100 - cpu)
  const memScore = Math.max(0, 100 - mem)
  const diskScore = Math.max(0, 100 - disk)
  const alertScore = Math.max(0, 100 - firingCount.value * 10)
  return Math.round((onlineScore + cpuScore + memScore + diskScore + alertScore) / 5)
})

const healthScoreColor = computed(() => {
  if (healthScore.value >= 90) return 'var(--chart-green)'
  if (healthScore.value >= 70) return 'var(--accent)'
  if (healthScore.value >= 50) return 'var(--warn)'
  return 'var(--danger)'
})

// 工具函数
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
  const days = ['星期日', '星期一', '星期二', '星期三', '星期四', '星期五', '星期六']
  weekday.value = days[d.getDay()]
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

// 可见性
function onVis() {
  visible = document.visibilityState === 'visible'
  if (visible) {
    refreshAll()
    resetCountdown()
    if (!dataTimer) dataTimer = setInterval(() => {
      refreshAll()
      resetCountdown()
    }, refreshInterval.value * 1000)
  } else if (dataTimer) {
    clearInterval(dataTimer)
    dataTimer = null
  }
}

onMounted(async () => {
  tickClock()
  clockTimer = setInterval(tickClock, 1000)
  startCountdown()
  await loadScreenConfig()
  if (cfg.refreshInterval > 0) refreshInterval.value = cfg.refreshInterval
  await refreshAll()
  dataTimer = setInterval(() => {
    refreshAll()
    resetCountdown()
  }, refreshInterval.value * 1000)
  ws = connectWS('alerts', null, { onMessage: () => refreshAll() })
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
  /* 大屏专用：深蓝 + 青蓝科技光 + 金黄数据高亮 */
  --accent: #38bdf8;
  --accent-dim: rgba(56, 189, 248, 0.14);
  --accent-glow: rgba(56, 189, 248, 0.22);
  --warn: #f5c542;
  --warn-dim: rgba(245, 197, 66, 0.16);
  --warn-glow: rgba(245, 197, 66, 0.25);
  --danger: #ff5d6c;
  --danger-dim: rgba(255, 93, 108, 0.16);
  --text: #e8f1ff;
  --text-dim: #6e8bb8;
  --text-muted: #455670;
  --border: rgba(80, 140, 220, 0.12);
  --border-strong: rgba(80, 140, 220, 0.22);
  --bg-card: rgba(10, 22, 42, 0.92);
  --chart-green: #22c55e;
  --chart-amber: #f5c542;
  --chart-red: #ef4444;

  position: relative;
  width: 100vw;
  height: 100vh;
  overflow: hidden;
  display: grid;
  grid-template-areas:
    'top'
    'kpi'
    'main'
    'bottom';
  grid-template-rows: auto 48px 1fr 48px;
  gap: 8px;
  padding: 8px 12px;
  color: var(--text);
  background:
    radial-gradient(1200px 520px at 50% -8%, rgba(56, 189, 248, 0.10), transparent 60%),
    radial-gradient(900px 420px at 10% 0%, rgba(245, 197, 66, 0.06), transparent 55%),
    radial-gradient(900px 420px at 90% 0%, rgba(56, 189, 248, 0.06), transparent 55%),
    radial-gradient(1000px 360px at 50% 110%, rgba(56, 189, 248, 0.05), transparent 55%),
    linear-gradient(160deg, #040811 0%, #081426 55%, #040811 100%);
}

.screen-view::before {
  content: '';
  position: absolute;
  inset: 0;
  pointer-events: none;
  background-image:
    linear-gradient(rgba(56, 189, 248, 0.03) 1px, transparent 1px),
    linear-gradient(90deg, rgba(56, 189, 248, 0.03) 1px, transparent 1px);
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
  padding: 6px 0 4px;
}

.hud-side {
  width: 300px;
  display: flex;
  align-items: center;
  gap: 10px;
}

.hud-left {
  justify-content: flex-start;
}

.hud-right {
  justify-content: flex-end;
}

.hud-clock {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 13px;
  color: var(--text-dim);
  letter-spacing: 0.06em;
}

.clock-text {
  white-space: nowrap;
  color: var(--text);
}

.weekday-text {
  font-size: 12px;
  color: var(--text-dim);
  margin-left: 2px;
}

.hud-actions {
  justify-content: flex-end;
}

.hud-center {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: flex-start;
  position: relative;
}

.hud-btn {
  width: 32px;
  height: 32px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(10, 22, 42, 0.6);
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
  width: 16px;
  height: 16px;
}

.hud-title {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 2px;
  margin: 0;
  user-select: none;
  position: relative;
}

.ht-sub {
  font-size: 13px;
  font-weight: 600;
  letter-spacing: 0.35em;
  color: var(--warn);
  text-shadow: 0 0 10px var(--warn-glow);
}

.ht-main {
  font-size: 32px;
  font-weight: 800;
  letter-spacing: 0.28em;
  background: linear-gradient(180deg, #e8f1ff 0%, #38bdf8 60%, #0ea5e9 100%);
  -webkit-background-clip: text;
  background-clip: text;
  color: transparent;
  filter: drop-shadow(0 0 18px rgba(56, 189, 248, 0.55));
}

.title-deco {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 6px;
}

.td-line {
  width: 80px;
  height: 1px;
  background: linear-gradient(90deg, transparent, rgba(56, 189, 248, 0.7), transparent);
}

.td-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--accent);
  box-shadow: 0 0 10px var(--accent-glow), 0 0 20px var(--accent-glow);
}

.hud-tabs {
  margin-top: 6px;
  width: fit-content;
  max-width: 90%;
}

/* 倒计时 */
.countdown {
  display: flex;
  align-items: baseline;
  gap: 3px;
  padding: 2px 10px;
  border-radius: 16px;
  background: rgba(56, 189, 248, 0.06);
  border: 1px solid rgba(56, 189, 268, 0.2);
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
}

/* 模块设置抽屉内的配置项 */
.cfg-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: 16px;
  padding: 10px 12px;
  border-radius: 8px;
  background: rgba(56, 189, 248, 0.05);
  border: 1px solid rgba(56, 189, 248, 0.15);
}

.cfg-label {
  font-size: 13px;
  color: var(--text);
}

.cfg-tip {
  margin-top: 10px;
  font-size: 12px;
  line-height: 1.6;
  color: var(--text-muted);
}

/* 持久 KPI 条 */
.hud-kpi-bar {
  grid-area: kpi;
  display: flex;
  align-items: center;
  gap: 0;
  padding: 0 16px;
  border-radius: var(--radius);
  background: var(--bg-card);
  border: 1px solid var(--border);
  position: relative;
  z-index: 2;
}

.kpi-bar-item {
  flex: 1;
  display: flex;
  align-items: baseline;
  gap: 6px;
  padding: 8px 12px;
  position: relative;
}

.kpi-bar-item + .kpi-bar-item::before {
  content: '';
  position: absolute;
  left: 0;
  top: 25%;
  height: 50%;
  width: 1px;
  background: var(--border);
}

.kpi-bar-label {
  font-size: 11px;
  color: var(--text-muted);
  letter-spacing: 0.05em;
  flex-shrink: 0;
}

.kpi-bar-value {
  font-size: 20px;
  font-weight: 800;
  font-family: var(--mono);
  text-shadow: 0 0 10px currentColor;
  line-height: 1;
}

.kpi-bar-unit {
  font-size: 10px;
  color: var(--text-dim);
  font-family: var(--mono);
}

/* Tab 内容区 */
.tab-content {
  grid-area: main;
  min-height: 0;
  position: relative;
  z-index: 2;
  overflow: hidden;
}

/* Tab 切换动画 */
.tab-fade-enter-active,
.tab-fade-leave-active {
  transition: opacity 0.2s ease, transform 0.2s ease;
}

.tab-fade-enter-from {
  opacity: 0;
  transform: translateY(8px);
}

.tab-fade-leave-to {
  opacity: 0;
  transform: translateY(-8px);
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
    grid-template-rows: auto 64px 1fr 68px;
  }
  .hud-side { width: 380px; }
  .hud-clock { font-size: 16px; }
  .hud-btn { width: 40px; height: 40px; }
  .hud-btn svg { width: 20px; height: 20px; }
  .ht-sub { font-size: 15px; letter-spacing: 0.3em; }
  .ht-main { font-size: 40px; letter-spacing: 0.24em; }
  .title-deco { margin-top: 8px; }
  .td-line { width: 110px; }
  .hud-tabs { margin-top: 8px; }
  .weekday-text { font-size: 14px; }
  .cd-label { font-size: 13px; }
  .cd-num { font-size: 24px; }
  .cd-unit { font-size: 14px; }
  .kpi-bar-label { font-size: 14px; }
  .kpi-bar-value { font-size: 26px; }
  .kpi-bar-unit { font-size: 13px; }
  .ab-label { font-size: 16px; gap: 9px; }
  .ab-item { font-size: 16px; gap: 8px; }
  .ab-none { font-size: 16px; }
}

@media (min-width: 3440px) {
  .screen-view {
    padding: 20px 34px;
    gap: 20px;
    grid-template-rows: auto 80px 1fr 84px;
  }
  .hud-side { width: 460px; }
  .hud-clock { font-size: 20px; }
  .hud-btn { width: 48px; height: 48px; }
  .hud-btn svg { width: 24px; height: 24px; }
  .ht-sub { font-size: 18px; letter-spacing: 0.28em; }
  .ht-main { font-size: 52px; letter-spacing: 0.2em; }
  .title-deco { margin-top: 10px; }
  .td-line { width: 140px; }
  .hud-tabs { margin-top: 10px; }
  .weekday-text { font-size: 17px; }
  .cd-label { font-size: 16px; }
  .cd-num { font-size: 30px; }
  .cd-unit { font-size: 17px; }
  .kpi-bar-label { font-size: 17px; }
  .kpi-bar-value { font-size: 32px; }
  .kpi-bar-unit { font-size: 16px; }
  .ab-label { font-size: 20px; gap: 11px; }
  .ab-item { font-size: 20px; gap: 10px; }
  .ab-none { font-size: 20px; }
}
</style>