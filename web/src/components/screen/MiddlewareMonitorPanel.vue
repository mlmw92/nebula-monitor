<template>
  <div class="mw-panel">
    <!-- P1#3 中间件健康总览 -->
    <div class="glass mw-health">
      <div class="mh-score">
        <div class="mh-ring">
          <svg viewBox="0 0 64 64">
            <circle cx="32" cy="32" r="26" fill="none" stroke="rgba(255,255,255,0.08)" stroke-width="6" />
            <circle cx="32" cy="32" r="26" fill="none" :stroke="scoreColor" stroke-width="6"
              stroke-linecap="round" :stroke-dasharray="scoreDash" transform="rotate(-90 32 32)" />
          </svg>
          <div class="mh-score-val">{{ healthScore }}<span>分</span></div>
        </div>
        <div class="mh-score-label">综合健康评分</div>
      </div>
      <div class="mh-stats">
        <div class="mh-stat"><b>{{ overview?.total || 0 }}</b><span>实例总数</span></div>
        <div class="mh-stat ok"><b>{{ overview?.up || 0 }}</b><span>在线</span></div>
        <div class="mh-stat bad"><b>{{ overview?.down || 0 }}</b><span>离线</span></div>
        <div class="mh-stat warn"><b>{{ overview?.alertCount || 0 }}</b><span>活跃告警</span></div>
      </div>
      <div class="mh-bar">
        <div class="mh-bar-fill" :style="{ width: onlineRateAll + '%', background: scoreColor }"></div>
      </div>
    </div>

    <!-- P0#1 + P1#4 + P2#10 类型健康度卡片 -->
    <div class="mw-overview">
      <div v-for="t in visibleTypes" :key="t.type" class="glass mw-type-card"
        :class="{ active: t.type === activeType, dim: !t.total }" @click="selectType(t.type)">
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
        <div class="mt-summary" v-if="t.summary && t.summary.length">
          <div class="ms-item" v-for="s in t.summary" :key="s.key" :class="{ warn: s.warn }">
            <span class="ms-label">{{ s.label }}</span>
            <span class="ms-val">{{ fmt(s.value) }}<em v-if="s.unit">{{ s.unit }}</em></span>
          </div>
        </div>
      </div>
      <div class="mw-showall" v-if="hiddenTypesCount">
        <button @click="showAll = !showAll">{{ showAll ? '收起无实例类型' : '展开 ' + hiddenTypesCount + ' 类无实例' }}</button>
      </div>
    </div>

    <!-- 详情：关键参数趋势 + 拓扑/慢查询 + 实例列表 -->
    <div class="mw-detail">
      <div class="mw-left">
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

        <!-- P2#8 Redis 拓扑（参考 RedisTab 树形结构） -->
        <div class="glass mw-topo" v-if="activeType === 'redis' && redisInstances.length">
          <div class="mt-title">Redis 拓扑（主从/集群）</div>
          <div class="screen-redis-topo">
            <template v-if="redisTopoGroups.clusters.length">
              <div v-for="grp in redisTopoGroups.clusters" :key="'c-'+grp.name" class="srt-group">
                <div class="srt-group-head">
                  <span class="srt-group-name">集群 {{ grp.name }}</span>
                  <span class="srt-group-meta dim">M:{{ grp.masters.length }} S:{{ grp.slaves.length }}</span>
                </div>
                <div class="srt-tree">
                  <div v-for="(m, idx) in grp.masters" :key="'cm-'+m.instance" class="srt-unit">
                    <div class="srt-node srt-master" :class="{ 'is-down': !m.up }" @click="openDetail(m)">
                      <span class="srt-badge srt-badge-m">M</span>
                      <span class="srt-name" :title="m.instance">{{ m.instance }}</span>
                      <span :class="['srt-dot', m.up ? 'up' : 'down']"></span>
                      <span class="srt-status">{{ m.up ? '在线' : '离线' }}</span>
                    </div>
                    <div v-if="(grp.slavesByMaster[m.instance] || []).length" class="srt-branch">
                      <div class="srt-slaves">
                        <div v-for="s in grp.slavesByMaster[m.instance]" :key="'cs-'+s.instance"
                             class="srt-node srt-slave" :class="{ 'is-down': !s.up }" @click.stop="openDetail(s)">
                          <span class="srt-badge srt-badge-s">S</span>
                          <span class="srt-name" :title="s.instance">{{ s.instance }}</span>
                          <span :class="['srt-dot', s.up ? 'up' : 'down']"></span>
                        </div>
                      </div>
                    </div>
                  </div>
                </div>
              </div>
            </template>
            <template v-if="redisTopoGroups.replications.length">
              <div v-for="grp in redisTopoGroups.replications" :key="'r-'+grp.name" class="srt-group">
                <div class="srt-group-head">
                  <span class="srt-group-name">主从 {{ grp.name }}</span>
                  <span class="srt-group-meta dim">M:{{ grp.masters.length }} S:{{ grp.slaves.length }}</span>
                </div>
                <div class="srt-tree">
                  <div v-for="(m, idx) in grp.masters" :key="'rm-'+idx" class="srt-unit">
                    <div class="srt-node srt-master" :class="{ 'is-down': !m.up }" @click="openDetail(m)">
                      <span class="srt-badge srt-badge-m">M</span>
                      <span class="srt-name" :title="m.instance">{{ m.instance }}</span>
                      <span :class="['srt-dot', m.up ? 'up' : 'down']"></span>
                      <span class="srt-status">{{ m.up ? '在线' : '离线' }}</span>
                    </div>
                    <div v-if="grp.slaves.filter(s => s.replicaOf === m.instance).length" class="srt-branch">
                      <div class="srt-slaves">
                        <div v-for="s in grp.slaves.filter(ss => ss.replicaOf === m.instance)" :key="s.instance"
                             class="srt-node srt-slave" :class="{ 'is-down': !s.up }" @click.stop="openDetail(s)">
                          <span class="srt-badge srt-badge-s">S</span>
                          <span class="srt-name" :title="s.instance">{{ s.instance }}</span>
                          <span :class="['srt-dot', s.up ? 'up' : 'down']"></span>
                        </div>
                      </div>
                    </div>
                  </div>
                </div>
              </div>
            </template>
            <template v-if="redisTopoGroups.standalones.length">
              <div class="srt-group">
                <div class="srt-group-head">
                  <span class="srt-group-name">独立实例</span>
                  <span class="srt-group-meta dim">{{ redisTopoGroups.standalones.length }} 个</span>
                </div>
                <div class="srt-grid">
                  <div v-for="(i, idx) in redisTopoGroups.standalones" :key="'sa-'+idx"
                       class="srt-node srt-standalone" :class="{ 'is-down': !i.up }" @click="openDetail(i)">
                    <span class="srt-name" :title="i.instance">{{ i.instance }}</span>
                    <span :class="['srt-dot', i.up ? 'up' : 'down']"></span>
                  </div>
                </div>
              </div>
            </template>
          </div>
        </div>

        <!-- P2#9 慢查询 Top3 -->
        <div class="glass mw-slow" v-if="activeType === 'redis' || activeType === 'mysql'">
          <div class="ms-title">慢查询 Top 3</div>
          <template v-if="slowList.length">
            <div class="ms-row" v-for="(s, i) in slowList" :key="i">
              <span class="ms-rank">{{ i + 1 }}</span>
              <span class="ms-name">{{ s.name }}</span>
              <span class="ms-val">{{ fmt(s.value) }}<em>次/s</em></span>
            </div>
          </template>
          <div class="ms-hint" v-else-if="activeType === 'redis'">当前版本未采集 Redis 慢查询明细</div>
          <div class="ms-hint" v-else>当前版本未采集 MySQL 慢查询明细</div>
        </div>
      </div>

      <div class="glass mw-instances">
        <div class="mi-title">实例列表 · 按内存使用率降序 · 点击查看详情</div>
        <div class="mi-body">
          <div class="mi-row" v-for="it in sortedInstances" :key="rowKey(it)"
            @click="openDetail(it)">
            <i class="mi-dot" :class="it.up || it.online ? 'on' : 'off'"></i>
            <span class="mi-role" :class="roleClass(it.role)">{{ roleText(it.role) }}</span>
            <span class="mi-name" @click.stop="openDetail(it)">{{ it.name || it.container || it.instance || it.ip }}</span>
            <span class="mi-metric" :class="{ warn: memWarn(it) }">{{ metricLabel(it) }}<b>{{ metricVal(it) }}</b><em v-if="metricUnit(it)">{{ metricUnit(it) }}</em></span>
            <span class="mi-tag" v-if="it.group || it.businessTag">{{ it.businessTag || it.group }}</span>
            <span class="mi-node">{{ it.node }}</span>
          </div>
          <div class="mi-empty" v-if="!sortedInstances.length">暂无实例数据</div>
        </div>
      </div>
    </div>
  </div>

    <!-- 实例详情弹窗（大屏相对独立，点击实例内弹窗展示，不跳转主机监控） -->
    <InstanceDetailDialog v-model:visible="detailVisible" :instance="detailInstance" />
  </template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import http from '../../api/http'
import InstanceDetailDialog from './InstanceDetailDialog.vue'
import { initChart, monitorOption, COLORS } from '../../charts/echarts'
import { queryClusterTrend } from './useTrend'

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
    { key: 'redis_used_memory_percent', label: '内存使用率 %', mode: 'avg' },
    { key: 'redis_ops_per_sec', label: 'QPS', mode: 'sum' },
    { key: 'redis_cmd_latency_ms', label: '命令延迟 ms', mode: 'avg' },
    { key: 'redis_hit_rate', label: '命中率 %', mode: 'avg' },
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
  mongodb: [{ key: 'mongodb_connections_current', label: '当前连接', mode: 'avg' }],
  fastdfs: [{ key: 'fastdfs_storage_online_count', label: '在线 Storage', mode: 'avg' }],
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
const showAll = ref(false)

const overviewTypes = computed(() =>
  (overview.value?.types || []).map((t, i) => ({ ...t, color: RING_COLORS[i % 8] }))
)
const activeLabel = computed(() => TYPE_LABELS[activeType.value] || activeType.value)
const visibleTypes = computed(() =>
  showAll.value ? overviewTypes.value : overviewTypes.value.filter((t) => t.total > 0)
)
const hiddenTypesCount = computed(
  () => overviewTypes.value.filter((t) => t.total === 0).length
)

// P1#3 综合健康评分
const healthScore = computed(() => {
  const o = overview.value
  if (!o || !o.total) return 100
  const base = (o.up / o.total) * 100
  const penalty = Math.min(o.alertCount * 3, 25)
  return Math.max(0, Math.min(100, Math.round(base - penalty)))
})
const scoreColor = computed(() => {
  const s = healthScore.value
  if (s >= 90) return 'var(--chart-green)'
  if (s >= 70) return 'var(--chart-yellow)'
  if (s >= 50) return 'var(--chart-orange)'
  return 'var(--chart-red)'
})
const onlineRateAll = computed(() => {
  const o = overview.value
  if (!o || !o.total) return 100
  return Math.round((o.up / o.total) * 100)
})
const scoreDash = computed(() => {
  const C = 2 * Math.PI * 26
  return `${(C * healthScore.value) / 100} ${C}`
})

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
// 入参可能是数字、数字字符串或空串（无可用指标时 primaryMetric 返回 ''），
// 统一转成 Number 后再格式化，避免对字符串调用 toFixed 抛错。
function fmt(v) {
  if (v == null || v === '') return '—'
  const n = typeof v === 'number' ? v : Number(v)
  if (!Number.isFinite(n)) return '—'
  const a = Math.abs(n)
  if (a >= 1e9) return (n / 1e9).toFixed(1) + 'B'
  if (a >= 1e6) return (n / 1e6).toFixed(1) + 'M'
  if (a >= 1e3) return (n / 1e3).toFixed(1) + 'k'
  return Number.isInteger(n) ? String(n) : n.toFixed(1)
}

// P1#6/P1#7 实例列表辅助
function rowKey(it) {
  return it.node + '|' + (it.instance || it.container || it.name || it.ip || '')
}
function roleText(role) {
  switch ((role || '').toLowerCase()) {
    case 'master':
    case 'primary':
      return '主'
    case 'slave':
    case 'replica':
    case 'secondary':
      return '从'
    case 'sentinel':
      return '哨兵'
    case 'cluster':
      return '集群'
    case 'broker':
      return 'Broker'
    case 'node':
      return '节点'
    default:
      return role || '—'
  }
}
function roleClass(role) {
  const r = (role || '').toLowerCase()
  if (r === 'master' || r === 'primary') return 'role-master'
  if (r === 'slave' || r === 'replica' || r === 'secondary') return 'role-slave'
  if (r === 'sentinel') return 'role-sentinel'
  if (r === 'cluster') return 'role-cluster'
  return ''
}
function primaryMetric(it) {
  if (it.memPercent != null && it.memPercent !== 0)
    return { label: '内存', value: it.memPercent, unit: '%', warn: it.memPercent >= 85 }
  if (it.ops != null && it.ops !== 0) return { label: 'QPS', value: it.ops, unit: '', warn: false }
  if (it.qps != null && it.qps !== 0) return { label: 'QPS', value: it.qps, unit: '', warn: false }
  if (it.cpuPercent != null && it.cpuPercent !== 0)
    return { label: 'CPU', value: it.cpuPercent, unit: '%', warn: it.cpuPercent >= 85 }
  if (it.consumerLag != null) return { label: '积压', value: it.consumerLag, unit: '', warn: false }
  if (it.connections != null) return { label: '连接', value: it.connections, unit: '', warn: false }
  if (it.threadsRunning != null) return { label: '活跃线程', value: it.threadsRunning, unit: '', warn: false }
  return { label: '', value: '', unit: '', warn: false }
}
function metricLabel(it) {
  return primaryMetric(it).label
}
function metricVal(it) {
  return fmt(primaryMetric(it).value)
}
function metricUnit(it) {
  return primaryMetric(it).unit
}
function memWarn(it) {
  return it.memPercent != null && it.memPercent >= 85
}
const sortedInstances = computed(() =>
  [...instances.value].sort((a, b) => (b.memPercent ?? -1) - (a.memPercent ?? -1))
)

// P1#7 跳转主机详情
// 大屏相对独立：点击实例用弹窗展示详情，不跳转主机监控
const detailVisible = ref(false)
const detailInstance = ref(null)
function openDetail(it) {
  if (it) {
    detailInstance.value = it
    detailVisible.value = true
  }
}

// P2#9 慢查询 Top3（依赖实例 slow 字段，暂无采集时为空）
const slowList = computed(() => {
  const list = instances.value
    .filter((i) => i.slow != null)
    .map((i) => ({ name: i.name || i.instance || i.container, value: i.slow }))
  list.sort((a, b) => b.value - a.value)
  return list.slice(0, 3)
})

// P2#8 Redis 拓扑 —— 参考 RedisTab 的 topologyGroups 分组逻辑
const redisInstances = computed(() => {
  if (activeType.value !== 'redis') return []
  return instances.value
})
const redisTopoGroups = computed(() => {
  const clusters = {}, replications = {}, standalones = []
  for (const i of redisInstances.value) {
    const g = i.group || i.name || i.instance
    if (i.topology === 'cluster') {
      clusters[g] = clusters[g] || { name: g, masters: [], slaves: [], slavesByMaster: {}, unlinkedSlaves: [] }
      if (i.role === 'slave' || i.role === 'replica') {
        clusters[g].slaves.push(i)
        if (i.replicaOf) {
          clusters[g].slavesByMaster[i.replicaOf] = clusters[g].slavesByMaster[i.replicaOf] || []
          clusters[g].slavesByMaster[i.replicaOf].push(i)
        } else {
          clusters[g].unlinkedSlaves.push(i)
        }
      } else clusters[g].masters.push(i)
    } else if (i.topology === 'replication' || (i.replicaOf && i.topology !== 'cluster')) {
      replications[g] = replications[g] || { name: g, masters: [], slaves: [] }
      if (i.role === 'slave' || i.role === 'replica') replications[g].slaves.push(i)
      else replications[g].masters.push(i)
    } else {
      standalones.push(i)
    }
  }
  return { clusters: Object.values(clusters), replications: Object.values(replications), standalones }
})

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

/* P1#3 健康总览 */
.mw-health {
  display: flex;
  align-items: center;
  gap: 24px;
  padding: 12px 18px;
  flex-shrink: 0;
}
.mh-score {
  display: flex;
  flex-direction: column;
  align-items: center;
}
.mh-ring {
  position: relative;
  width: 64px;
  height: 64px;
}
.mh-ring svg {
  width: 100%;
  height: 100%;
}
.mh-score-val {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 20px;
  font-weight: 700;
  font-family: var(--mono);
  color: var(--text);
}
.mh-score-val span {
  font-size: 10px;
  margin-left: 2px;
  color: var(--text-dim);
}
.mh-score-label {
  font-size: 11px;
  color: var(--text-dim);
  margin-top: 2px;
}
.mh-stats {
  display: flex;
  gap: 26px;
}
.mh-stat {
  display: flex;
  flex-direction: column;
  align-items: center;
}
.mh-stat b {
  font-size: 22px;
  font-family: var(--mono);
  color: var(--text);
  line-height: 1.1;
}
.mh-stat span {
  font-size: 11px;
  color: var(--text-dim);
  margin-top: 2px;
}
.mh-stat.ok b {
  color: var(--chart-green);
}
.mh-stat.bad b {
  color: var(--danger);
}
.mh-stat.warn b {
  color: var(--chart-yellow);
}
.mh-bar {
  flex: 1;
  height: 6px;
  border-radius: 3px;
  background: rgba(255, 255, 255, 0.08);
  overflow: hidden;
  min-width: 80px;
}
.mh-bar-fill {
  height: 100%;
  border-radius: 3px;
  transition: width 0.4s;
}

/* P0#1 + P1#4 类型卡片 */
.mw-overview {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(168px, 1fr));
  gap: 10px;
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
.mw-type-card.dim {
  opacity: 0.45;
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
  flex-shrink: 0;
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
.mt-summary {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
  margin-top: 8px;
  padding-top: 7px;
  border-top: 1px dashed var(--border);
}
.ms-item {
  display: flex;
  align-items: baseline;
  gap: 4px;
  font-size: 11px;
}
.ms-label {
  color: var(--text-muted);
}
.ms-val {
  font-family: var(--mono);
  color: var(--text);
  font-weight: 600;
}
.ms-val em {
  font-style: normal;
  font-size: 9px;
  color: var(--text-dim);
  margin-left: 1px;
}
.ms-item.warn .ms-val {
  color: var(--danger);
}
.mw-showall {
  grid-column: 1 / -1;
  display: flex;
  justify-content: center;
}
.mw-showall button {
  background: transparent;
  border: 1px dashed var(--border);
  color: var(--text-dim);
  font-size: 11px;
  padding: 4px 14px;
  border-radius: 14px;
  cursor: pointer;
}
.mw-showall button:hover {
  border-color: var(--accent);
  color: var(--accent);
}

/* 详情区 */
.mw-detail {
  flex: 1;
  display: grid;
  grid-template-columns: 1.3fr 1fr;
  gap: 12px;
  min-height: 0;
}
.mw-left {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-height: 0;
  overflow-y: auto;
}
.mw-params,
.mw-instances,
.mw-topo,
.mw-slow {
  display: flex;
  flex-direction: column;
  padding: 10px 12px;
  min-height: 0;
}
.mw-params {
  flex: 1;
  min-height: 220px;
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
  flex-wrap: wrap;
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

/* P2#8 Redis 拓扑（HTML/CSS 树形结构，参考 RedisTab） */
.mw-topo {
  flex-shrink: 0;
  max-height: 280px;
  overflow-y: auto;
}
.mt-title,
.ms-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 8px;
}

/* 大屏 Redis 拓扑容器 */
.screen-redis-topo {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.srt-group {
  background: rgba(255, 255, 255, 0.03);
  border: 1px solid rgba(255, 255, 255, 0.06);
  border-radius: 8px;
  padding: 10px 12px;
}
.srt-group-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
  gap: 8px;
}
.srt-group-name {
  font-size: 12px;
  font-weight: 600;
  color: var(--accent);
}
.srt-group-meta {
  font-size: 11px;
}
.srt-tree {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.srt-unit {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

/* 节点卡片 */
.srt-node {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  background: rgba(255, 255, 255, 0.05);
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 6px;
  padding: 6px 10px;
  cursor: pointer;
  transition: all 0.15s ease;
  max-width: 100%;
}
.srt-node:hover {
  background: rgba(255, 255, 255, 0.08);
  border-color: rgba(99, 179, 237, 0.35);
}
.srt-node.is-down {
  opacity: 0.5;
}

/* Master 节点 */
.srt-master {
  border-left: 3px solid var(--chart-green);
}
.srt-master:hover {
  border-color: rgba(74, 222, 128, 0.5);
}

/* Slave 节点 */
.srt-slave {
  margin-left: 14px;
  border-left: 3px solid var(--chart-blue);
}
.srt-slave:hover {
  border-color: rgba(96, 165, 250, 0.5);
}

/* 独立实例 */
.srt-standalone {
  display: inline-flex;
}

/* 角色徽章 */
.srt-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  font-size: 9px;
  font-weight: 700;
  border-radius: 3px;
  flex-shrink: 0;
}
.srt-badge-m {
  background: rgba(220, 56, 45, 0.2);
  color: #ff8a80;
  border: 1px solid rgba(220, 56, 45, 0.35);
}
.srt-badge-s {
  background: rgba(34, 197, 94, 0.15);
  color: #4ade80;
  border: 1px solid rgba(34, 197, 94, 0.3);
}

/* 节点名（截断长名） */
.srt-name {
  font-size: 11px;
  font-weight: 600;
  color: var(--text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  min-width: 0;
  flex: 1;
}

/* 状态指示 */
.srt-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  flex-shrink: 0;
}
.srt-dot.up {
  background: var(--chart-green);
  box-shadow: 0 0 4px var(--chart-green);
}
.srt-dot.down {
  background: var(--danger);
}
.srt-status {
  font-size: 10px;
  color: var(--text-dim);
  flex-shrink: 0;
}

/* 从节点分支 */
.srt-branch {
  display: flex;
  gap: 0;
  margin-left: 6px;
}
.srt-slaves {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  padding: 2px 0;
}

/* 独立实例网格 */
.srt-grid {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

/* P2#9 慢查询 */
.mw-slow {
  flex-shrink: 0;
}
.ms-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 5px 0;
  border-bottom: 1px solid var(--border);
  font-size: 12px;
}
.ms-rank {
  width: 18px;
  height: 18px;
  border-radius: 50%;
  background: var(--accent-dim);
  color: var(--accent);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 10px;
  font-family: var(--mono);
  flex-shrink: 0;
}
.ms-name {
  flex: 1;
  color: var(--text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ms-val {
  font-family: var(--mono);
  color: var(--chart-yellow);
}
.ms-val em {
  font-style: normal;
  font-size: 9px;
  color: var(--text-dim);
  margin-left: 1px;
}
.ms-hint {
  font-size: 11px;
  color: var(--text-muted);
  padding: 6px 0;
}

/* P1#6 实例列表 */
.mi-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 6px;
  flex-shrink: 0;
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
.mi-role {
  font-size: 10px;
  padding: 1px 6px;
  border-radius: 9px;
  flex-shrink: 0;
  background: rgba(255, 255, 255, 0.08);
  color: var(--text-dim);
}
.mi-role.role-master {
  background: rgba(76, 175, 80, 0.2);
  color: var(--chart-green);
}
.mi-role.role-slave {
  background: rgba(33, 150, 243, 0.2);
  color: var(--chart-blue);
}
.mi-role.role-sentinel {
  background: rgba(255, 193, 7, 0.2);
  color: var(--chart-yellow);
}
.mi-role.role-cluster {
  background: rgba(156, 39, 176, 0.2);
  color: var(--chart-purple);
}
.mi-name {
  font-size: 12px;
  color: var(--accent);
  max-width: 130px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  text-decoration: underline dotted;
}
.mi-name:hover {
  color: var(--chart-cyan);
}
.mi-metric {
  font-size: 11px;
  color: var(--text-dim);
  white-space: nowrap;
}
.mi-metric b {
  font-family: var(--mono);
  color: var(--text);
  margin: 0 2px;
  font-size: 12px;
}
.mi-metric.warn b {
  color: var(--danger);
}
.mi-tag {
  font-size: 10px;
  padding: 1px 7px;
  border-radius: 9px;
  background: var(--accent-dim);
  color: var(--accent);
  flex-shrink: 0;
  max-width: 90px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.mi-node {
  font-size: 11px;
  color: var(--text-muted);
  max-width: 120px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  margin-left: auto;
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
