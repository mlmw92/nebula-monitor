<template>
  <div class="overview">
    <div class="ov-header">
      <div>
        <h2 class="page-title">系统概览</h2>
        <p class="page-desc">主机、中间件与告警的实时健康总览</p>
      </div>
      <div class="ov-actions">
        <el-button v-if="editing" size="small" @click="reset">重置布局</el-button>
        <el-button size="small" :type="editing ? 'primary' : 'default'" @click="setEditing(!editing)">
          {{ editing ? '完成编辑' : '自定义布局' }}
        </el-button>
        <el-button size="small" :icon="RefreshRight" @click="refresh" :loading="loading">刷新</el-button>
      </div>
    </div>

    <div v-if="editing" class="ov-edit-hint">
      编辑模式：使用各区块右上角的 ↑ / ↓ 调整顺序，点击「隐藏」可关闭该区块；配置自动保存到本地。
    </div>

    <div class="ov-grid">
      <section
        v-for="b in visibleBlocks"
        :key="b.key"
        class="ov-block"
        :style="{ gridColumn: 'span ' + b.span }"
      >
        <div class="ov-card">
          <div class="ov-card-head">
            <h3 class="ov-card-title">{{ b.title }}</h3>
            <div v-if="editing" class="ov-edit-tools">
              <button class="ov-tool" title="上移" @click="moveUp(b.key)">↑</button>
              <button class="ov-tool" title="下移" @click="moveDown(b.key)">↓</button>
              <button class="ov-tool" title="隐藏" @click="toggleVisible(b.key)">隐藏</button>
            </div>
          </div>
          <div class="ov-card-body">
            <component :is="compMap[b.key]" v-bind="blockProps(b.key)" />
          </div>
        </div>
      </section>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onBeforeUnmount, markRaw } from 'vue'
import { RefreshRight, Monitor, Bell, Connection, FirstAidKit } from '@element-plus/icons-vue'
import http from '../api/http'
import { middlewareTypes } from './overview/middlewareConfig'
import { formatMetric } from './overview/format'
import { useOverviewLayout } from './overview/useOverviewLayout'

import HealthBlock from './overview/HealthBlock.vue'
import KpiBlock from './overview/KpiBlock.vue'
import CriticalAlerts from './overview/CriticalAlerts.vue'
import HostOverview from './overview/HostOverview.vue'
import MiddlewareOverview from './overview/MiddlewareOverview.vue'
import RecentAlerts from './overview/RecentAlerts.vue'

const { blocks, editing, moveUp, moveDown, toggleVisible, reset, setEditing } = useOverviewLayout()

const compMap = {
  health: HealthBlock,
  kpi: KpiBlock,
  criticalAlerts: CriticalAlerts,
  hostOverview: HostOverview,
  middleware: MiddlewareOverview,
  recentAlerts: RecentAlerts,
}

const loading = ref(false)
const nodes = ref([])
const groups = ref([])
const alertsAll = ref([])
const latest = ref({ metrics: {} })
const mwData = ref({}) // key -> instances[]

const get = (url) => http.get(url).then((r) => r || {}).catch(() => ({}))

async function loadAll() {
  loading.value = true
  try {
    const [nRes, gRes, aRes, lRes, ...mw] = await Promise.all([
      get('/api/v1/nodes'),
      get('/api/v1/groups'),
      get('/api/v1/alerts?state=active'),
      get('/api/v1/nodes/latest'),
      ...middlewareTypes.map((t) => get(t.endpoint)),
    ])
    nodes.value = nRes.nodes || []
    groups.value = gRes.groups || []
    alertsAll.value = aRes.alerts || []
    latest.value = lRes || { metrics: {} }
    const map = {}
    middlewareTypes.forEach((t, i) => {
      const r = mw[i] || {}
      // 不同端点返回结构不统一，做兼容：
      //   - 大多数：{ instances: [...] }
      //   - Docker：  { containers: [...], hosts: [...] }
      //   - K8s：    { clusters: [...], nodes: [...], pods: [...] }
      let arr = []
      if (Array.isArray(r.instances)) arr = r.instances
      else if (Array.isArray(r.containers)) arr = r.containers
      else if (Array.isArray(r.clusters)) arr = r.clusters
      else if (Array.isArray(r)) arr = r
      map[t.key] = arr
    })
    mwData.value = map
  } finally {
    loading.value = false
  }
}

function refresh() {
  loadAll()
}

// ---------- 派生数据 ----------
const latestMap = computed(() => latest.value.metrics || {})

const sortedNodes = computed(() =>
  [...nodes.value].sort((a, b) => {
    const g = (a.group || '').localeCompare(b.group || '')
    if (g !== 0) return g
    const na = a.displayName || a.hostname
    const nb = b.displayName || b.hostname
    return na.localeCompare(nb)
  })
)

const groupedHosts = computed(() => {
  const map = {}
  const order = []
  sortedNodes.value.forEach((n) => {
    const g = n.group && n.group.trim() ? n.group : '未分组'
    if (!map[g]) {
      map[g] = []
      order.push(g)
    }
    map[g].push({ ...n, metrics: latestMap.value[n.hostname] || {} })
  })
  order.sort((a, b) => {
    if (a === '未分组') return 1
    if (b === '未分组') return -1
    return a.localeCompare(b)
  })
  return order.map((g) => ({ group: g, hosts: map[g] }))
})

function avg(arr) {
  if (!arr.length) return 0
  return arr.reduce((a, b) => a + b, 0) / arr.length
}

const health = computed(() => {
  const ns = sortedNodes.value
  const total = ns.length
  const offline = ns.filter((n) => n.status !== 'online').length
  const online = total - offline

  // 资源压力统计：仅对“超过告警阈值”的部分扣分，避免正常负载拉低分数
  const pressureConfig = [
    { key: 'cpu', label: 'CPU', warnAt: 70, weight: 15 },
    { key: 'mem', label: '内存', warnAt: 80, weight: 12 },
    { key: 'disk', label: '磁盘', warnAt: 85, weight: 12 },
  ]
  const pressure = pressureConfig.map((p) => {
    const values = []
    ns.forEach((n) => {
      const v = (latestMap.value[n.hostname] || {})[p.key]
      if (typeof v === 'number' && !isNaN(v)) values.push(v)
    })
    const avgVal = avg(values)
    // 平均超阈值幅度（只统计超过 warnAt 的部分，未超过为 0）
    const avgOver = avg(values.map((v) => Math.max(0, v - p.warnAt)))
    const warnCount = values.filter((v) => v >= p.warnAt).length
    const badCount = values.filter((v) => v >= p.warnAt + 15).length
    return { ...p, rate: avgVal, avgOver, warnCount, badCount, count: values.length }
  })

  const crit = alertsAll.value.filter((x) => (x.severity || '').toLowerCase() === 'critical').length
  const warn = alertsAll.value.filter((x) => (x.severity || '').toLowerCase() === 'warning').length

  let score = 100
  if (total > 0) {
    // 离线扣分（权重最大）
    score -= (offline / total) * 60
    // 资源超阈值扣分（只有实际超过阈值才扣）
    pressure.forEach((p) => {
      score -= (p.avgOver / 100) * p.weight
    })
    // 活跃告警扣分
    score -= crit * 5
    score -= warn * 2
    score = Math.max(0, Math.round(score))
  }

  let statusText = '未知'
  let rank = 'unknown'
  if (total === 0) {
    statusText = '无数据'
  } else if (score >= 90) {
    statusText = '健康'
    rank = 'good'
  } else if (score >= 70) {
    statusText = '轻微风险'
    rank = 'warn'
  } else {
    statusText = '风险较高'
    rank = 'bad'
  }
  return {
    score,
    statusText,
    rank,
    total,
    online,
    offline,
    criticalAlerts: crit,
    warningAlerts: warn,
    pressure,
  }
})

const kpis = computed(() => {
  const ns = sortedNodes.value
  const total = ns.length
  const offline = ns.filter((n) => n.status !== 'online').length
  const online = total - offline
  const a = alertsAll.value
  const crit = a.filter((x) => (x.severity || '').toLowerCase() === 'critical').length
  const warn = a.filter((x) => (x.severity || '').toLowerCase() === 'warning').length
  return [
    {
      label: '主机总数',
      value: total,
      foot: `在线 ${online} · 离线 ${offline}`,
      tone: offline > 0 ? 'warn' : 'good',
      icon: markRaw(Monitor),
    },
    {
      label: '活跃告警',
      value: a.length,
      foot: `紧急 ${crit} · 警告 ${warn}`,
      tone: crit > 0 ? 'bad' : warn > 0 ? 'warn' : 'good',
      icon: markRaw(Bell),
    },
    {
      label: '中间件实例',
      value: Object.values(mwData.value).reduce((s, arr) => s + (arr ? arr.length : 0), 0),
      foot: `${middlewareTypes.length} 种类型`,
      tone: 'good',
      icon: markRaw(Connection),
    },
    {
      label: '系统健康度',
      value: Math.round(health.value.score),
      foot: health.value.statusText,
      tone: health.value.rank === 'bad' ? 'bad' : health.value.rank === 'warn' ? 'warn' : 'good',
      icon: markRaw(FirstAidKit),
    },
  ]
})

const criticalAlerts = computed(() =>
  alertsAll.value
    .filter((a) => (a.severity || '').toLowerCase() === 'critical')
    .slice(0, 8)
)

const recentAlerts = computed(() =>
  [...alertsAll.value]
    .sort((a, b) => new Date(b.startsAt || 0) - new Date(a.startsAt || 0))
    .slice(0, 12)
)

const mwSummaries = computed(() =>
  middlewareTypes.map((t) => {
    const inst = mwData.value[t.key] || []
    const total = inst.length
    const online = inst.filter((i) => i.up).length
    const offline = total - online
    const up = inst.filter((i) => i.up)
    const top = [...up]
      .sort((a, b) => (Number(b[t.topKey]) || 0) - (Number(a[t.topKey]) || 0))
      .slice(0, 3)
      .map((i) => ({
        label: i.name || i.instance || i.node,
        valueText: formatMetric(t.fmt, i[t.topKey]),
        subText: t.subKey ? formatMetric(t.subFmt, i[t.subKey]) : '',
      }))
    return { ...t, total, online, offline, topN: top }
  })
)

const visibleBlocks = computed(() => blocks.value.filter((b) => b.visible))

function blockProps(key) {
  switch (key) {
    case 'health':
      return { health: health.value }
    case 'kpi':
      return { kpis: kpis.value }
    case 'criticalAlerts':
      return { alerts: criticalAlerts.value }
    case 'hostOverview':
      return { groupedHosts: groupedHosts.value }
    case 'middleware':
      return { summaries: mwSummaries.value }
    case 'recentAlerts':
      return { alerts: recentAlerts.value }
    default:
      return {}
  }
}

let timer = null
onMounted(() => {
  loadAll()
  timer = setInterval(loadAll, 30000)
})
onBeforeUnmount(() => {
  if (timer) clearInterval(timer)
})
</script>

<style scoped>
.overview {
  padding: 4px;
}
.ov-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 16px;
}
.page-title {
  font-size: 22px;
  font-weight: 700;
  letter-spacing: -0.01em;
  background: linear-gradient(135deg, var(--text) 0%, var(--text-dim) 100%);
  -webkit-background-clip: text;
  -webkit-text-fill-color: transparent;
  background-clip: text;
}
.page-desc {
  font-size: 13px;
  color: var(--text-dim);
  margin-top: 4px;
}
.ov-actions {
  display: flex;
  gap: 8px;
  flex-shrink: 0;
}
.ov-edit-hint {
  font-size: 12px;
  color: var(--accent);
  background: var(--accent-dim, rgba(64, 158, 255, 0.12));
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 8px 12px;
  margin-bottom: 14px;
}
.ov-grid {
  display: grid;
  grid-template-columns: repeat(12, 1fr);
  gap: 16px;
  align-items: stretch;
}
.ov-block {
  min-width: 0;
}
.ov-card {
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 14px;
  padding: 16px 18px;
  height: 100%;
  display: flex;
  flex-direction: column;
}
.ov-card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 14px;
}
.ov-card-title {
  font-size: 15px;
  font-weight: 700;
  margin: 0;
}
.ov-edit-tools {
  display: flex;
  gap: 6px;
}
.ov-tool {
  font-size: 12px;
  line-height: 1;
  padding: 4px 8px;
  border-radius: 6px;
  border: 1px solid var(--border);
  background: var(--bg-elev);
  color: var(--text-dim);
  cursor: pointer;
  transition: all 0.15s;
}
.ov-tool:hover {
  color: var(--text);
  border-color: var(--accent);
}
.ov-card-body {
  flex: 1;
  min-width: 0;
}

@media (max-width: 1100px) {
  .ov-block {
    grid-column: span 12 !important;
  }
}
</style>
