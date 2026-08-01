<template>
  <div class="mw-tab">
    <RefreshBar :loading="loading" @refresh="load" />

    <!-- 未配置：全平台没有任何 K8s 集群上报 -->
    <div v-if="!loading && clusters.length === 0" class="empty-guide glass">
      <div class="empty-icon">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="64" height="64">
          <path d="M12 2 3 7v10l9 5 9-5V7z"/>
          <path d="M12 22V12M3 7l9 5 9-5"/>
        </svg>
      </div>
      <h2 class="empty-title">尚未配置 Kubernetes 监控</h2>
      <p class="empty-desc">当前没有任何节点上报 Kubernetes 数据。请在能访问 apiserver 的节点 agent.yaml 中开启 <code>collectors.k8s</code> 并配置 <code>k8sInstances</code>（kubeconfig 或 apiServer+token）。</p>
      <p class="empty-hint">配置完成后约 15-30 秒，本页将出现集群卡片。</p>
    </div>

    <template v-if="clusters.length > 0">
      <!-- KPI -->
      <div class="kpi-row">
        <KpiCard :value="stats.clusters" label="集群数" tone="cluster" />
        <KpiCard :value="stats.nodesReady + '/' + stats.nodesTotal" label="节点就绪/总数" tone="host" />
        <KpiCard :value="stats.podsRunning" label="运行 Pod" tone="up" />
        <KpiCard :value="stats.podsTotal" label="Pod 总数" tone="total" />
        <KpiCard :value="stats.podsAbnormal" label="异常 Pod" tone="down" />
        <KpiCard :value="stats.workloadsUnhealthy" label="不健康工作负载" tone="alert" />
      </div>

      <!-- 集群卡片 -->
      <div class="chart-section glass">
        <div class="section-title">集群</div>
        <div class="host-grid">
          <div v-for="c in clusters" :key="c.node + '|' + c.instance" class="host-card">
            <div class="host-head">
              <span class="host-dot" :class="c.up ? 'up' : 'down'"></span>
              <span class="host-node">{{ c.name || c.instance }}</span>
              <span class="host-group">{{ c.version || '-' }}</span>
            </div>
            <div class="host-daemon mono">{{ c.instance }}</div>
            <div class="host-stats">
              <span>节点 <b>{{ c.nodesReady }}/{{ c.nodesTotal }}</b></span>
              <span>Pod <b>{{ c.podsRunning }}</b>/{{ c.podsTotal }}（异常 {{ c.podsPending + c.podsFailed }}）</span>
              <span>工作负载不健康 <b :class="{ 'metric-bad': workloadUnhealthy(c) > 0 }">{{ workloadUnhealthy(c) }}</b></span>
              <span>采集节点 {{ c.node }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- 节点列表 -->
      <div class="chart-section glass">
        <div class="section-title">节点列表</div>
        <el-table :data="nodes" style="width: 100%" empty-text="暂无节点数据">
          <el-table-column prop="nodeName" label="节点名" min-width="180" show-overflow-tooltip />
          <el-table-column prop="ip" label="IP" min-width="140" show-overflow-tooltip />
          <el-table-column prop="cluster" label="集群" width="140" />
          <el-table-column label="角色" width="100">
            <template #default="{ row }"><el-tag size="small">{{ row.role }}</el-tag></template>
          </el-table-column>
          <el-table-column label="状态" width="90">
            <template #default="{ row }"><span :class="['dot', row.ready ? 'up' : 'down']"></span>{{ row.ready ? 'Ready' : 'NotReady' }}</template>
          </el-table-column>
          <el-table-column label="CPU 用量" width="110" sortable :sort-by="'cpuCores'">
            <template #default="{ row }">{{ row.cpuCores ? row.cpuCores.toFixed(2) + ' 核' : '-' }}</template>
          </el-table-column>
          <el-table-column label="内存用量" width="110" sortable :sort-by="'memBytes'">
            <template #default="{ row }">{{ formatBytes(row.memBytes) }}</template>
          </el-table-column>
        </el-table>
      </div>

      <!-- 异常 Pod -->
      <div v-if="pods.length > 0" class="chart-section glass">
        <div class="section-title">异常 Pod（非 Running/Succeeded）</div>
        <el-table :data="pods" style="width: 100%">
          <el-table-column prop="pod" label="Pod" min-width="220" show-overflow-tooltip />
          <el-table-column prop="namespace" label="命名空间" width="160" />
          <el-table-column prop="cluster" label="集群" width="140" />
          <el-table-column label="状态" width="120">
            <template #default="{ row }"><el-tag type="danger" size="small">{{ row.phase }}</el-tag></template>
          </el-table-column>
        </el-table>
      </div>
    </template>
  </div>
</template>

<script setup>
import { ref, onMounted, computed } from 'vue'
import http from '../../api/http'
import RefreshBar from '../RefreshBar.vue'
import KpiCard from '../KpiCard.vue'

const loading = ref(true)
const clusters = ref([])
const nodes = ref([])
const pods = ref([])

const stats = computed(() => {
  const s = { clusters: 0, nodesTotal: 0, nodesReady: 0, podsTotal: 0, podsRunning: 0, podsAbnormal: 0, workloadsUnhealthy: 0 }
  for (const c of clusters.value) {
    s.clusters++
    s.nodesTotal += c.nodesTotal || 0
    s.nodesReady += c.nodesReady || 0
    s.podsTotal += c.podsTotal || 0
    s.podsRunning += c.podsRunning || 0
    s.podsAbnormal += (c.podsPending || 0) + (c.podsFailed || 0)
    s.workloadsUnhealthy += workloadUnhealthy(c)
  }
  return s
})

function workloadUnhealthy(c) {
  return (c.deploymentsUnhealthy || 0) + (c.statefulSetsUnhealthy || 0) + (c.daemonSetsUnhealthy || 0)
}

async function load() {
  loading.value = true
  try {
    const data = await http.get('/api/v1/middleware/k8s/instances')
    clusters.value = data.clusters || []
    nodes.value = data.nodes || []
    pods.value = data.pods || []
  } catch (e) { console.error(e) } finally { loading.value = false }
}

function formatBytes(n) {
  if (!n) return '-'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n, i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return v.toFixed(1) + ' ' + units[i]
}

onMounted(load)
</script>

<style scoped>
.mw-tab { padding: 4px 0; }
.empty-guide { text-align: center; padding: 48px 24px; }
.empty-icon { color: var(--text-muted); margin-bottom: 16px; }
.empty-title { font-size: 18px; font-weight: 600; margin: 0 0 8px; }
.empty-desc { color: var(--text-dim); margin: 0 0 8px; font-size: 13px; }
.empty-hint { color: var(--text-muted); font-size: 12px; }
.kpi-row { display: grid; grid-template-columns: repeat(auto-fit, minmax(140px, 1fr)); gap: 10px; margin-bottom: 16px; }
.chart-section { padding: 16px; margin-bottom: 16px; }
.section-title { font-size: 14px; font-weight: 600; margin-bottom: 12px; }
.host-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 12px; }
.host-card { padding: 14px; background: rgba(255,255,255,0.03); border-radius: 8px; }
.host-head { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; }
.host-dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; }
.host-dot.up { background: #3fb950; }
.host-dot.down { background: #dc382d; }
.host-node { font-weight: 600; font-size: 14px; }
.host-group { font-size: 12px; color: var(--text-muted); margin-left: auto; }
.host-daemon { font-size: 12px; color: var(--text-dim); margin-bottom: 8px; word-break: break-all; }
.host-stats { display: flex; flex-direction: column; gap: 4px; font-size: 12px; color: var(--text-dim); }
.dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 4px; }
.dot.up { background: #3fb950; }
.dot.down { background: #dc382d; }
.metric-bad { color: #dc382d; }
.mono { font-family: var(--mono); }
</style>
