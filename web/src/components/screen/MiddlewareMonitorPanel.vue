<template>
  <div class="mw-panel">
    <!-- 8 类型健康度总览 -->
    <div class="glass mw-overview">
      <div class="mw-type-card" v-for="t in overviewTypes" :key="t.type"
        :class="{ active: t.type === activeType }" @click="selectType(t.type)">
        <div class="mt-head">
          <span class="mt-name">{{ t.label }}</span>
          <span class="mt-alert" v-if="t.alertCount" :class="{ warn: t.alertCount > 0 }">{{ t.alertCount }}告警</span>
        </div>
        <div class="mt-body">
          <div class="mt-ring">
            <svg viewBox="0 0 44 44">
              <circle cx="22" cy="22" r="18" fill="none" stroke="rgba(255,255,255,0.07)" stroke-width="4" />
              <circle cx="22" cy="22" r="18" fill="none" stroke="currentColor" stroke-width="4"
                stroke-linecap="round" :stroke-dasharray="ringDash(t)" transform="rotate(-90 22 22)" />
            </svg>
            <span class="mt-online">{{ onlineRate(t) }}%</span>
          </div>
          <div class="mt-meta">
            <span class="mt-up">{{ t.up }}<em>在线</em></span>
            <span class="mt-total">共 {{ t.total }} 实例</span>
          </div>
        </div>
      </div>
    </div>

    <!-- 详情：关键参数趋势 + 实例列表 -->
    <div class="mw-detail">
      <div class="glass mw-params">
        <div class="mp-head">
          <span class="mp-title">{{ activeLabel }} · 关键参数趋势</span>
          <div class="mp-tabs">
            <button v-for="(m, i) in metricsOf(activeType)" :key="m.key"
              :class="{ on: i === paramIdx }" @click="selectParam(i)">{{ m.label }}</button>
          </div>
        </div>
        <div ref="paramChart" class="mp-chart"></div>
      </div>
      <div class="glass mw-instances">
        <div class="mi-title">实例列表 · 点击下钻</div>
        <div class="mi-body">
          <div class="mi-row" v-for="it in instances" :key="it.key || it.name"
            @click="drillMiddle(it)">
            <i class="mi-dot" :class="it.up || it.online ? 'on' : 'off'"></i>
            <span class="mi-name">{{ it.name || it.container || it.ip }}</span>
            <span class="mi-node">{{ it.node }}</span>
            <span class="mi-state" :class="it.up || it.online ? 'ok' : 'bad'">{{ it.up || it.online ? '在线' : '离线' }}</span>
          </div>
          <div class="mi-empty" v-if="!instances.length">暂无实例数据</div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import http from '../../api/http'
import { initChart, monitorOption, COLORS } from '../../charts/echarts'
import { queryClusterTrend } from './useTrend'

const router = useRouter()
const paramChart = ref(null)
let chart = null
let ro = null
let timer = null

const TYPE_LABELS = {
  redis: 'Redis', mysql: 'MySQL', postgres: 'PostgreSQL', nginx: 'Nginx',
  kafka: 'Kafka', docker: 'Docker', rocketmq: 'RocketMQ', k8s: 'Kubernetes',
}
const TYPE_METRICS = {
  redis: [
    { key: 'redis_connected_clients', label: '连接数', mode: 'avg' },
    { key: 'redis_ops_per_sec', label: 'QPS', mode: 'sum' },
    { key: 'redis_cmd_latency_ms', label: '命令延迟 ms', mode: 'avg' },
  ],
  mysql: [
    { key: 'mysql_threads_connected', label: '连接数', mode: 'avg' },
    { key: 'mysql_queries_per_sec', label: 'QPS', mode: 'sum' },
    { key: 'mysql_query_latency_ms', label: '查询延迟 ms', mode: 'avg' },
  ],
  postgres: [
    { key: 'postgres_numbackends', label: '连接数', mode: 'avg' },
    { key: 'postgres_query_latency_ms', label: '查询延迟 ms', mode: 'avg' },
  ],
  nginx: [
    { key: 'nginx_active_connections', label: '活动连接', mode: 'avg' },
    { key: 'nginx_requests', label: '总请求', mode: 'sum' },
  ],
  kafka: [{ key: 'kafka_consumer_lag', label: '消费延迟', mode: 'avg' }],
  docker: [{ key: 'docker_container_cpu_percent', label: '容器 CPU %', mode: 'avg' }],
  rocketmq: [{ key: 'rocketmq_consumer_lag', label: '消费积压', mode: 'avg' }],
  k8s: [{ key: 'k8s_pods_pending', label: '待调度 Pod', mode: 'avg' }],
}
const RING_COLORS = [
  'var(--accent)', 'var(--chart-blue)', 'var(--chart-green)', 'var(--chart-yellow)',
  'var(--chart-purple)', 'var(--chart-orange)', 'var(--chart-red)', 'var(--info)',
]

const overview = ref(null)
const activeType = ref('redis')
const paramIdx = ref(0)
const instances = ref([])
const paramSeries = ref([])

const overviewTypes = computed(() => (overview.value?.types || []).map((t, i) => ({ ...t, color: RING_COLORS[i % 8] })))
const activeLabel = computed(() => TYPE_LABELS[activeType.value] || activeType.value)

function metricsOf(type) {
  return TYPE_METRICS[type] || []
}
function onlineRate(t) {
  if (!t.total) return 0
  return Math.round((t.up / t.total) * 100)
}
function ringDash(t) {
  const C = 2 * Math.PI * 18
  return `${(C * onlineRate(t)) / 100} ${C}`
}

async function loadOverview() {
  try {
    const data = await http.get('/api/v1/middleware/overview')
    overview.value = data
  } catch (e) {
    console.error('overview 加载失败', e)
  }
}

async function loadDetail() {
  const t = activeType.value
  const m = metricsOf(t)[paramIdx.value]
  try {
    const resp = await http.get(
      t === 'docker' ? '/api/v1/middleware/docker/containers' : `/api/v1/middleware/${t}/instances`
    )
    const list = t === 'docker' ? resp?.containers || [] : resp?.instances || []
    instances.value = list
    const nodeList = [...new Set(list.map((i) => i.node).filter(Boolean))]
    if (m) {
      const data = await queryClusterTrend(nodeList, m.key, m.mode)
      paramSeries.value = [{ name: m.label, color: COLORS.cyan, data }]
      renderParam()
    }
  } catch (e) {
    console.error('中间件实例加载失败', t, e)
  }
}

function renderParam() {
  if (!chart) return
  chart.setOption(
    monitorOption({
      yMin: 0,
      series: paramSeries.value.map((s) => ({ name: s.name, color: s.color, data: s.data })),
    }),
    true
  )
}

function selectType(t) {
  activeType.value = t
  paramIdx.value = 0
  loadDetail()
}
function selectParam(i) {
  paramIdx.value = i
  loadDetail()
}
function drillMiddle(it) {
  router.push({ path: '/middleware', query: { type: activeType.value } })
}

onMounted(() => {
  chart = initChart(paramChart.value)
  loadOverview()
  loadDetail()
  timer = setInterval(() => {
    loadOverview()
    loadDetail()
  }, 30000)
  ro = new ResizeObserver(() => chart && chart.resize())
  ro.observe(paramChart.value)
})
onUnmounted(() => {
  clearInterval(timer)
  ro && ro.disconnect()
  chart && chart.dispose()
  chart = null
})
</script>

<style scoped>
.mw-panel {
  display: flex;
  flex-direction: column;
  gap: 12px;
  height: 100%;
  min-height: 0;
}

/* 8 类型总览 */
.mw-overview {
  display: grid;
  grid-template-columns: repeat(8, 1fr);
  gap: 10px;
  padding: 12px;
  flex-shrink: 0;
}
.mw-type-card {
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 10px 10px 8px;
  cursor: pointer;
  transition: border-color 0.2s, background 0.2s, transform 0.2s, box-shadow 0.2s;
  color: var(--text-dim);
}
.mw-type-card:hover {
  transform: translateY(-2px);
  border-color: var(--accent);
  box-shadow: 0 4px 14px var(--accent-dim);
}
.mw-type-card.active {
  color: var(--accent);
  border-color: var(--accent);
  background: var(--accent-dim);
  box-shadow: 0 0 16px var(--accent-glow);
}
.mt-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.mt-name {
  font-size: 13px;
  font-weight: 700;
}
.mt-alert {
  font-size: 10px;
  color: var(--text-dim);
}
.mt-alert.warn {
  color: var(--danger);
  animation: pulse 1.4s infinite;
}
.mt-body {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 6px;
}
.mt-ring {
  position: relative;
  width: 44px;
  height: 44px;
  color: var(--accent);
}
.mt-ring svg {
  width: 100%;
  height: 100%;
}
.mt-online {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 10px;
  font-family: var(--mono);
  color: var(--text);
}
.mt-meta {
  display: flex;
  flex-direction: column;
  gap: 2px;
  font-size: 12px;
}
.mt-up {
  color: var(--text);
  font-family: var(--mono);
}
.mt-up em {
  font-style: normal;
  color: var(--text-dim);
  font-size: 10px;
  margin-left: 4px;
}
.mt-total {
  color: var(--text-muted);
  font-size: 10px;
}

/* 详情区 */
.mw-detail {
  flex: 1;
  display: grid;
  grid-template-columns: 1.3fr 1fr;
  gap: 12px;
  min-height: 0;
}
.mw-params,
.mw-instances {
  display: flex;
  flex-direction: column;
  padding: 10px 12px;
  min-height: 0;
}
.mp-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 6px;
}
.mp-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
}
.mp-tabs {
  display: flex;
  gap: 6px;
}
.mp-tabs button {
  background: transparent;
  border: 1px solid var(--border);
  color: var(--text-dim);
  font-size: 11px;
  padding: 3px 10px;
  border-radius: 12px;
  cursor: pointer;
  transition: all 0.15s;
}
.mp-tabs button:hover {
  border-color: var(--accent);
  color: var(--accent);
}
.mp-tabs button.on {
  background: var(--accent-dim);
  border-color: var(--accent);
  color: var(--accent);
}
.mp-chart {
  flex: 1;
  min-height: 0;
  width: 100%;
}

.mi-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 6px;
}
.mi-body {
  flex: 1;
  overflow-y: auto;
  min-height: 0;
}
.mi-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 7px 8px;
  border-radius: 6px;
  cursor: pointer;
  transition: background 0.15s;
}
.mi-row:hover {
  background: var(--accent-dim);
}
.mi-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  flex-shrink: 0;
}
.mi-dot.on {
  background: var(--chart-green);
  box-shadow: 0 0 6px var(--chart-green);
}
.mi-dot.off {
  background: var(--danger);
}
.mi-name {
  flex: 1;
  font-size: 12px;
  color: var(--text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.mi-node {
  font-size: 11px;
  color: var(--text-muted);
  max-width: 140px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.mi-state {
  font-size: 11px;
  font-family: var(--mono);
}
.mi-state.ok {
  color: var(--chart-green);
}
.mi-state.bad {
  color: var(--danger);
}
.mi-empty {
  padding: 20px 0;
  text-align: center;
  color: var(--text-muted);
  font-size: 12px;
}

@keyframes pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.45; }
}
</style>
