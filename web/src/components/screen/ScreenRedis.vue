<template>
  <div class="glass panel-mini screen-redis">
    <div class="sr-head">
      <span class="sr-title">
        <span class="sr-ico"></span>
        中间件 · Redis
      </span>
      <span class="sr-sub mono">{{ stats.total }} 实例 / {{ stats.clusterCount }} 组</span>
    </div>

    <div class="sr-empty" v-if="!instances.length">
      <span>暂无 Redis 实例数据</span>
    </div>

    <template v-else>
      <!-- KPI 摘要 -->
      <div class="sr-kpi">
        <div class="srk">
          <div class="srk-v green">{{ stats.up }}</div>
          <div class="srk-l">在线</div>
        </div>
        <div class="srk">
          <div class="srk-v" :class="stats.down ? 'red' : 'dim'">{{ stats.down }}</div>
          <div class="srk-l">离线</div>
        </div>
        <div class="srk">
          <div class="srk-v cyan">{{ formatBytes(stats.totalMemory) }}</div>
          <div class="srk-l">内存</div>
        </div>
        <div class="srk">
          <div class="srk-v purple">{{ formatNum(stats.totalOps) }}</div>
          <div class="srk-l">OPS</div>
        </div>
        <div class="srk">
          <div class="srk-v" :class="stats.alertCount ? 'amber' : 'dim'">{{ stats.alertCount }}</div>
          <div class="srk-l">风险</div>
        </div>
      </div>

      <!-- 拓扑分布 -->
      <div class="sr-topo">
        <span class="topo-chip"><b>{{ topoCount.cluster }}</b> 集群</span>
        <span class="topo-chip"><b>{{ topoCount.sentinel }}</b> 哨兵</span>
        <span class="topo-chip"><b>{{ topoCount.replication }}</b> 主从</span>
        <span class="topo-chip"><b>{{ topoCount.standalone }}</b> 单机</span>
      </div>

      <!-- OPS 性能排行 Top 5 -->
      <div class="sr-rank">
        <div class="rank-title">OPS 排行</div>
        <div class="rank-row" v-for="i in topOps" :key="i.node + i.instance" @click="$emit('select', i)">
          <span class="rank-name" :title="i.name || i.instance">
            <span class="dot" :class="i.up ? 'up' : 'down'"></span>
            {{ i.name || i.instance }}
          </span>
          <span class="rank-bar">
            <span class="rank-fill" :style="{ width: opsPct(i) + '%' }"></span>
          </span>
          <span class="rank-val mono">{{ formatNum(i.ops) }}</span>
        </div>
      </div>
    </template>
  </div>
</template>

<script setup>
import { ref, reactive, computed, onMounted, onUnmounted } from 'vue'
import http from '../../api/http'

const emit = defineEmits(['select', 'summary'])

const instances = ref([])
const activeAlerts = ref([])
const stats = reactive({ total: 0, up: 0, down: 0, totalMemory: 0, totalClients: 0, totalOps: 0, clusterCount: 0, alertCount: 0 })

let timer = null
let visible = true

// ---- 告警关联（复用 RedisTab 逻辑） ----
const alertsByInstance = computed(() => {
  const m = new Map()
  for (const a of activeAlerts.value) {
    if (!a.instance) continue
    const key = `${a.node}|${a.instance}`
    const list = m.get(key) || []
    list.push(a)
    m.set(key, list)
  }
  return m
})
function isAlert(i) {
  if (!i.up) return true
  return (alertsByInstance.value.get(`${i.node}|${i.instance}`) || []).length > 0
}

// ---- 拓扑分组计数 ----
const topoCount = computed(() => {
  const c = { cluster: 0, sentinel: 0, replication: 0, standalone: 0 }
  const groups = { cluster: new Set(), sentinel: new Set(), replication: new Set() }
  for (const i of instances.value) {
    const g = i.group || i.name || i.instance
    if (i.topology === 'cluster') groups.cluster.add(g)
    else if (i.topology === 'sentinel') groups.sentinel.add(g)
    else if (i.topology === 'replication' || (i.replicaOf && i.topology !== 'cluster')) groups.replication.add(g)
    else c.standalone += 1
  }
  c.cluster = groups.cluster.size
  c.sentinel = groups.sentinel.size
  c.replication = groups.replication.size
  return c
})

// ---- OPS 排行 Top 5 ----
const topOps = computed(() =>
  [...instances.value].sort((a, b) => (b.ops || 0) - (a.ops || 0)).slice(0, 5)
)
const maxOps = computed(() => Math.max(1, ...topOps.value.map((i) => i.ops || 0)))
function opsPct(i) {
  return Math.round(((i.ops || 0) / maxOps.value) * 100)
}

function computeStats() {
  const list = instances.value
  stats.total = list.length
  stats.up = list.filter((i) => i.up).length
  stats.down = list.filter((i) => !i.up).length
  stats.totalMemory = list.reduce((s, i) => s + (i.usedMemory || 0), 0)
  stats.totalClients = list.reduce((s, i) => s + (i.clients || 0), 0)
  stats.totalOps = list.reduce((s, i) => s + (i.ops || 0), 0)
  const clusterNames = new Set()
  for (const i of list) {
    if (i.topology === 'cluster' || i.topology === 'sentinel') {
      const key = i.group || i.name
      if (key) clusterNames.add(key)
    }
  }
  stats.clusterCount = clusterNames.size
  stats.alertCount = list.filter(isAlert).length
  // 向父级（大屏拓扑图）上报汇总，用于 Redis 拓扑节点展示
  emit('summary', {
    total: stats.total,
    up: stats.up,
    down: stats.down,
    clusterCount: stats.clusterCount,
    alertCount: stats.alertCount,
  })
}

async function load() {
  if (!visible) return
  try {
    const [inst, al] = await Promise.all([
      http.get('/api/v1/middleware/redis/instances').catch(() => ({ instances: [] })),
      http.get('/api/v1/alerts?state=firing').catch(() => ({ alerts: [] })),
    ])
    instances.value = inst.instances || []
    activeAlerts.value = (al.alerts || []).filter((a) => a.instance)
    computeStats()
  } catch (e) {
    /* ignore */
  }
}

function onVis() {
  visible = document.visibilityState === 'visible'
  if (visible) load()
}

// ---- 格式化（复用 RedisTab 实现） ----
function formatBytes(b) {
  if (!b || b <= 0) return '0 B'
  if (b >= 1 << 30) return (b / (1 << 30)).toFixed(1) + ' GB'
  if (b >= 1 << 20) return (b / (1 << 20)).toFixed(0) + ' MB'
  if (b >= 1 << 10) return (b / (1 << 10)).toFixed(0) + ' KB'
  return b.toFixed(0) + ' B'
}
function formatNum(n) {
  if (!n) return '0'
  if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M'
  if (n >= 1000) return (n / 1000).toFixed(1) + 'K'
  return n.toFixed(0)
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
</script>

<style scoped>
.screen-redis {
  display: flex;
  flex-direction: column;
  padding: 12px 14px;
  min-height: 0;
}
.sr-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 10px;
}
.sr-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 14px;
  font-weight: 600;
  color: var(--text);
}
.sr-ico {
  width: 10px;
  height: 10px;
  border-radius: 3px;
  background: linear-gradient(135deg, var(--danger), color-mix(in srgb, var(--danger) 65%, #000));
  box-shadow: 0 0 8px rgba(244, 63, 94, 0.5);
}
.sr-sub {
  font-size: 11px;
  color: var(--text-dim);
}
.sr-empty {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--text-dim);
  font-size: 12px;
}

/* KPI 摘要 */
.sr-kpi {
  display: grid;
  grid-template-columns: repeat(5, 1fr);
  gap: 6px;
  margin-bottom: 10px;
}
.srk {
  text-align: center;
  padding: 6px 2px;
  background: rgba(255, 255, 255, 0.02);
  border-radius: 7px;
}
.srk-v {
  font-size: 15px;
  font-weight: 700;
  font-family: var(--mono);
  line-height: 1.1;
}
.srk-v.green { color: var(--accent); }
.srk-v.red { color: var(--danger); }
.srk-v.cyan { color: var(--info); }
.srk-v.purple { color: var(--violet); }
.srk-v.amber { color: var(--warn); }
.srk-v.dim { color: var(--text-dim); }
.srk-l {
  font-size: 10px;
  color: var(--text-dim);
  margin-top: 2px;
}

/* 拓扑分布 */
.sr-topo {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 10px;
}
.topo-chip {
  font-size: 11px;
  color: var(--text-dim);
  padding: 3px 8px;
  background: rgba(255, 255, 255, 0.03);
  border-radius: 6px;
}
.topo-chip b {
  color: var(--text);
  font-family: var(--mono);
  margin-right: 2px;
}

/* OPS 排行 */
.sr-rank {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
  overflow: hidden;
}
.rank-title {
  font-size: 11px;
  color: var(--text-dim);
  text-transform: uppercase;
  letter-spacing: 0.04em;
}
.rank-row {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
}
.rank-name {
  width: 88px;
  flex-shrink: 0;
  font-size: 11px;
  color: var(--text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  display: flex;
  align-items: center;
  gap: 5px;
}
.rank-name .dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  flex-shrink: 0;
}
.rank-name .dot.up { background: var(--accent); }
.rank-name .dot.down { background: var(--danger); }
.rank-bar {
  flex: 1;
  height: 6px;
  background: rgba(255, 255, 255, 0.05);
  border-radius: 3px;
  overflow: hidden;
}
.rank-fill {
  display: block;
  height: 100%;
  background: linear-gradient(90deg, var(--info), var(--accent));
  border-radius: 3px;
  transition: width 0.5s ease;
}
.rank-val {
  width: 44px;
  text-align: right;
  font-size: 11px;
  color: var(--text-dim);
}
</style>
