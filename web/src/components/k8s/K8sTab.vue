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
        <KpiCard :value="stats.clusters" label="集群数" tone="cluster">
          <template #icon><ClusterIcon /></template>
        </KpiCard>
        <KpiCard :value="stats.nodesReady + '/' + stats.nodesTotal" label="节点就绪/总数" tone="host">
          <template #icon><ServerIcon /></template>
        </KpiCard>
        <KpiCard :value="stats.podsRunning" label="运行 Pod" tone="up">
          <template #icon><PodIcon /></template>
        </KpiCard>
        <KpiCard :value="stats.podsTotal" label="Pod 总数" tone="total">
          <template #icon><BoxIcon /></template>
        </KpiCard>
        <KpiCard :value="stats.podsAbnormal" label="异常 Pod" :tone="stats.podsAbnormal > 0 ? 'down' : 'total'">
          <template #icon><AlertIcon /></template>
        </KpiCard>
        <KpiCard :value="stats.workloadsUnhealthy" label="不健康工作负载" :tone="stats.workloadsUnhealthy > 0 ? 'alert' : 'total'">
          <template #icon><ActivityIcon /></template>
        </KpiCard>
      </div>

      <!-- 集群卡片 -->
      <div class="chart-section glass">
        <div class="section-title">集群</div>
        <div class="host-grid">
          <div v-for="c in clusters" :key="c.node + '|' + c.instance" class="host-card" :class="{'is-down': !c.up}">
            <div class="host-head">
              <span class="host-dot" :class="c.up ? 'up' : 'down'"></span>
              <span class="host-node">{{ c.name || c.instance }}</span>
              <span class="host-version" v-if="c.version">{{ c.version }}</span>
            </div>
            <div class="host-stats">
              <div class="stat-item">
                <span class="stat-label">节点</span>
                <span class="stat-val" :class="c.nodesReady < c.nodesTotal ? 'warn' : 'ok'">{{ c.nodesReady }}/{{ c.nodesTotal }}</span>
              </div>
              <div class="stat-item">
                <span class="stat-label">Pod</span>
                <span class="stat-val">{{ c.podsRunning }}/{{ c.podsTotal }}</span>
              </div>
              <!-- 用 podsAbnormal（按容器级有效状态算）而不是 pending+failed：
                   phase=Running 但容器在 CrashLoopBackOff 的 Pod 也必须算进异常。
                   服务端已对旧 Agent 做过口径回退，这里恒有值。 -->
              <div class="stat-item" v-if="c.podsAbnormal > 0">
                <span class="stat-label">异常</span>
                <span class="stat-val warn">{{ c.podsAbnormal }}</span>
              </div>
              <div class="stat-item" v-if="workloadUnhealthy(c) > 0">
                <span class="stat-label">负载异常</span>
                <span class="stat-val warn">{{ workloadUnhealthy(c) }}</span>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- 节点列表 -->
      <div class="mw-list glass">
        <div class="mw-list-title">节点列表</div>
        <el-table :data="nodes" style="width: 100%" empty-text="暂无节点数据">
          <el-table-column prop="nodeName" label="节点名" min-width="180" show-overflow-tooltip />
          <el-table-column prop="ip" label="IP" min-width="140" show-overflow-tooltip />
          <el-table-column prop="cluster" label="集群" width="140" />
          <!-- 150 而不是 120：'Control-Plane' 标签连内边距约需 116px，再加单元格左右内边距，
               120px 的列里会被裁掉一截——这就是"角色显示不全"。 -->
          <el-table-column label="角色" width="150">
            <template #default="{ row }"><MwRoleTag :role="row.role" /></template>
          </el-table-column>
          <el-table-column label="状态" width="100">
            <template #default="{ row }"><MwStatusDot :status="row.ready ? 'normal' : 'abnormal'" :label="row.ready ? '就绪' : '未就绪'" /></template>
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
      <div v-if="pods.length > 0" class="mw-list glass">
        <div class="mw-list-title">异常 Pod（非 Running/Succeeded）</div>
        <el-table :data="pods" style="width: 100%">
          <el-table-column prop="pod" label="Pod" min-width="220" show-overflow-tooltip />
          <el-table-column prop="namespace" label="命名空间" width="160" />
          <el-table-column prop="cluster" label="集群" width="140" />
          <el-table-column label="状态" width="160">
            <template #default="{ row }">
              <!-- 显示有效状态（status），phase 只在旧 Agent 缺 status 时兜底。
                   两者含义不同：ImagePullBackOff 的 Pod 的 phase 是 Pending，
                   CrashLoopBackOff 的 Pod 的 phase 是 Running。 -->
              <MwStatusDot
                :status="['Running', 'Succeeded', 'Completed'].includes(row.status || row.phase) ? 'normal' : 'abnormal'"
                :label="row.status || row.phase"
              />
            </template>
          </el-table-column>
        </el-table>
      </div>
    </template>
  </div>
</template>

<script setup>
import { ref, onMounted, computed, h } from 'vue'
import http from '../../api/http'
import RefreshBar from '../RefreshBar.vue'
import KpiCard from '../KpiCard.vue'
import MwStatusDot from '../mw/MwStatusDot.vue'
import MwRoleTag from '../mw/MwRoleTag.vue'

// ---- 图标组件（内联 SVG 渲染函数） ----
function svgIcon(s) {
  const inner = s.replace(/^<svg[^>]*>/, '').replace(/<\/svg>\s*$/, '')
  return () => h('svg', { viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', 'stroke-width': 2, innerHTML: inner })
}
const ClusterIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="3"/><circle cx="5" cy="5" r="2"/><circle cx="19" cy="5" r="2"/><circle cx="5" cy="19" r="2"/><circle cx="19" cy="19" r="2"/><line x1="6.5" y1="6.5" x2="9.5" y2="9.5"/><line x1="14.5" y1="9.5" x2="17.5" y2="6.5"/><line x1="6.5" y1="17.5" x2="9.5" y2="14.5"/><line x1="14.5" y1="14.5" x2="17.5" y2="17.5"/></svg>')
const ServerIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="2" y="3" width="20" height="8" rx="2"/><rect x="2" y="13" width="20" height="8" rx="2"/><line x1="6" y1="7" x2="6.01" y2="7"/><line x1="6" y1="17" x2="6.01" y2="17"/></svg>')
const PodIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 2 3 7v10l9 5 9-5V7z"/><path d="M12 22V12M3 7l9 5 9-5"/></svg>')
const BoxIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/><polyline points="3.27 6.96 12 12.01 20.73 6.96"/><line x1="12" y1="22.08" x2="12" y2="12"/></svg>')
const AlertIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>')
const ActivityIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="22 12 18 12 15 21 9 3 6 12 2 12"/></svg>')

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
    // 口径：用服务端算好的 podsAbnormal（按容器级有效状态），不再是 pending+failed。
    // 后者漏掉"phase 是 Running、但容器在 CrashLoopBackOff"的 Pod——那正是最该被看到的。
    // 用 ?? 而不是 ||：0 是合法值；只有字段根本不存在（新旧版本错配）才回退旧口径。
    s.podsAbnormal += c.podsAbnormal ?? ((c.podsPending || 0) + (c.podsFailed || 0))
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
.empty-hint { color: var(--text-muted); font-size: 13px; }
.kpi-row { display: grid; grid-template-columns: repeat(auto-fit, minmax(140px, 1fr)); gap: 10px; margin-bottom: 16px; }
.chart-section { padding: 16px; margin-bottom: 16px; }
.section-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 12px;
  display: flex;
  align-items: center;
  gap: 8px;
}
.section-title::before {
  content: '';
  width: 3px;
  height: 14px;
  background: var(--accent);
  border-radius: 2px;
}
.host-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 12px; }
.host-card { padding: 14px; background: var(--fill-1); border-radius: 10px; border: 1px solid var(--border); }
.host-card.is-down { opacity: 0.6; }
.host-head { display: flex; align-items: center; gap: 8px; margin-bottom: 12px; }
.host-dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; }
.host-dot.up { background: var(--ok); box-shadow: 0 0 6px rgba(74,222,128,0.5); }
.host-dot.down { background: var(--danger); }
.host-node { font-weight: 600; font-size: 14px; }
.host-version { font-size: 13px; color: var(--text-muted); margin-left: auto; padding: 2px 8px; background: var(--fill-2); border-radius: 4px; }
.host-stats { display: flex; flex-wrap: wrap; gap: 12px; }
.stat-item { display: flex; flex-direction: column; gap: 2px; }
.stat-label { font-size: 13px; color: var(--text-muted); }
.stat-val { font-size: 14px; font-weight: 600; font-family: var(--mono); }
.stat-val.ok { color: var(--ok); }
.stat-val.warn { color: var(--warn); }
.dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 4px; }
.dot.up { background: var(--accent); }
.dot.down { background: var(--danger); }
.metric-bad { color: var(--danger); }
.mono { font-family: var(--mono); }
</style>
