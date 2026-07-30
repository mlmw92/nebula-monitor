<template>
  <div class="mw-tab">
    <div v-if="!loading && containers.length === 0" class="empty-guide glass">
      <div class="empty-icon">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="64" height="64">
          <path d="M22 12c0-5.523-4.477-10-10-10S2 6.477 2 12s4.477 10 10 10 10-4.477 10-10z"/>
          <path d="M2 12h20M12 2c2.5 2.7 4 6.5 4 10s-1.5 7.3-4 10c-2.5-2.7-4-6.5-4-10s1.5-7.3 4-10z"/>
        </svg>
      </div>
      <h2 class="empty-title">尚未配置 Docker 监控</h2>
      <p class="empty-desc">当前没有已采集的 Docker 容器。请在运行 Agent 的节点上配置 Docker daemon 地址。</p>
      <p class="empty-hint">配置完成后约 15-30 秒数据将出现在此页面。</p>
    </div>

    <template v-if="containers.length > 0">
      <div class="kpi-row">
        <div class="kpi-card gradient-total"><div class="kpi-body"><div class="kpi-num">{{ stats.total }}</div><div class="kpi-text">容器总数</div></div></div>
        <div class="kpi-card gradient-up"><div class="kpi-body"><div class="kpi-num">{{ stats.running }}</div><div class="kpi-text">运行中</div></div></div>
        <div class="kpi-card gradient-down"><div class="kpi-body"><div class="kpi-num">{{ stats.stopped }}</div><div class="kpi-text">已停止</div></div></div>
        <div class="kpi-card gradient-conn"><div class="kpi-body"><div class="kpi-num">{{ stats.totalCPU.toFixed(1) }}%</div><div class="kpi-text">总 CPU</div></div></div>
        <div class="kpi-card gradient-ops"><div class="kpi-body"><div class="kpi-num">{{ formatBytes(stats.totalMem) }}</div><div class="kpi-text">总内存</div></div></div>
        <div class="kpi-card gradient-mem"><div class="kpi-body"><div class="kpi-num">{{ formatBytes(stats.totalNetRx) }}</div><div class="kpi-text">总网络接收</div></div></div>
      </div>

      <div class="chart-section glass">
        <div class="section-title">容器列表</div>
        <el-table :data="containers" style="width: 100%" @row-click="openDetail" :row-class-name="rowClass">
          <el-table-column prop="name" label="容器名" min-width="150" />
          <el-table-column prop="node" label="节点" width="120" />
          <el-table-column prop="image" label="镜像" min-width="200" show-overflow-tooltip />
          <el-table-column label="状态" width="90">
            <template #default="{ row }">
              <el-tag :type="statusType(row.status)" size="small">{{ row.status }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="CPU" width="80" sortable :sort-by="'cpuPercent'">
            <template #default="{ row }">{{ row.cpuPercent ? row.cpuPercent.toFixed(1) + '%' : '-' }}</template>
          </el-table-column>
          <el-table-column label="内存" width="90" sortable :sort-by="'memUsage'">
            <template #default="{ row }">{{ formatBytes(row.memUsage) }}</template>
          </el-table-column>
          <el-table-column label="内存%" width="80" sortable :sort-by="'memPercent'">
            <template #default="{ row }"><span :class="memClass(row.memPercent)">{{ row.memPercent ? row.memPercent.toFixed(0) + '%' : '-' }}</span></template>
          </el-table-column>
          <el-table-column label="网络接收" width="100">
            <template #default="{ row }">{{ formatBytes(row.netRx) }}</template>
          </el-table-column>
          <el-table-column label="网络发送" width="100">
            <template #default="{ row }">{{ formatBytes(row.netTx) }}</template>
          </el-table-column>
          <el-table-column prop="pidsCurrent" label="进程数" width="80" />
        </el-table>
      </div>
    </template>

    <el-drawer v-model="drawerVisible" :title="detailTitle" size="50%" :destroy-on-close="true">
      <div v-if="selected" class="detail-content">
        <div class="detail-meta">
          <div class="meta-item"><span class="meta-label">容器名</span><span class="mono">{{ selected.name }}</span></div>
          <div class="meta-item"><span class="meta-label">容器 ID</span><span class="mono">{{ selected.instance }}</span></div>
          <div class="meta-item"><span class="meta-label">节点</span>{{ selected.node }}</div>
          <div class="meta-item"><span class="meta-label">镜像</span>{{ selected.image }}</div>
          <div class="meta-item"><span class="meta-label">状态</span>{{ selected.status }}</div>
        </div>
        <div class="metric-grid">
          <div class="metric-cell"><div class="mc-label">CPU 使用率</div><div class="mc-value">{{ selected.cpuPercent ? selected.cpuPercent.toFixed(1) + '%' : '-' }}</div></div>
          <div class="metric-cell"><div class="mc-label">内存使用</div><div class="mc-value">{{ formatBytes(selected.memUsage) }}</div></div>
          <div class="metric-cell"><div class="mc-label">内存限制</div><div class="mc-value">{{ formatBytes(selected.memLimit) }}</div></div>
          <div class="metric-cell"><div class="mc-label">内存使用率</div><div class="mc-value">{{ selected.memPercent ? selected.memPercent.toFixed(1) + '%' : '-' }}</div></div>
          <div class="metric-cell"><div class="mc-label">网络接收</div><div class="mc-value">{{ formatBytes(selected.netRx) }}</div></div>
          <div class="metric-cell"><div class="mc-label">网络发送</div><div class="mc-value">{{ formatBytes(selected.netTx) }}</div></div>
          <div class="metric-cell"><div class="mc-label">磁盘读</div><div class="mc-value">{{ formatBytes(selected.diskRead) }}</div></div>
          <div class="metric-cell"><div class="mc-label">磁盘写</div><div class="mc-value">{{ formatBytes(selected.diskWrite) }}</div></div>
          <div class="metric-cell"><div class="mc-label">进程数</div><div class="mc-value">{{ selected.pidsCurrent }}</div></div>
        </div>
        <div class="chart-box" ref="chartRef"></div>
      </div>
    </el-drawer>
  </div>
</template>

<script setup>
import { ref, onMounted, computed, nextTick } from 'vue'
import * as echarts from 'echarts'
import http from '../../api/http'

const loading = ref(true)
const containers = ref([])
const drawerVisible = ref(false)
const selected = ref(null)
const chartRef = ref(null)
let chartInstance = null

const stats = computed(() => {
  const s = { total: 0, running: 0, stopped: 0, totalCPU: 0, totalMem: 0, totalNetRx: 0 }
  for (const c of containers.value) {
    s.total++
    if (c.status === 'running') s.running++; else s.stopped++
    s.totalCPU += c.cpuPercent || 0
    s.totalMem += c.memUsage || 0
    s.totalNetRx += c.netRx || 0
  }
  return s
})

const detailTitle = computed(() => selected.value ? `Docker 详情 - ${selected.name}` : '详情')

async function load() {
  loading.value = true
  try {
    const data = await http.get('/api/v1/middleware/docker/containers')
    containers.value = data.containers || []
  } catch (e) { console.error(e) } finally { loading.value = false }
}

function openDetail(row) {
  selected.value = row
  drawerVisible.value = true
  nextTick(() => loadTrendChart(row))
}

async function loadTrendChart(row) {
  if (!chartRef.value) return
  if (chartInstance) { chartInstance.dispose(); chartInstance = null }
  chartInstance = echarts.init(chartRef.value)
  const end = Date.now()
  const start = end - 3600 * 1000
  try {
    const [cpuData, memData] = await Promise.all([
      http.get(`/api/v1/query/range?node=${row.node}&metric=docker_container_cpu_percent&start=${start}&end=${end}&step=60`),
      http.get(`/api/v1/query/range?node=${row.node}&metric=docker_container_mem_usage_bytes&start=${start}&end=${end}&step=60`),
    ])
    const series = []
    if (cpuData.series) for (const s of cpuData.series) {
      if (s.labels?.instance === row.instance) series.push({ name: 'CPU%', type: 'line', data: s.points.map(p => [p.timestamp, p.value]), smooth: true })
    }
    if (memData.series) for (const s of memData.series) {
      if (s.labels?.instance === row.instance) series.push({ name: '内存(bytes)', type: 'line', yAxisIndex: 1, data: s.points.map(p => [p.timestamp, p.value]), smooth: true })
    }
    chartInstance.setOption({
      tooltip: { trigger: 'axis' },
      legend: { data: series.map(s => s.name), textStyle: { color: '#8b949e' } },
      grid: { left: 50, right: 60, top: 40, bottom: 30 },
      xAxis: { type: 'time' },
      yAxis: [{ type: 'value', name: 'CPU%' }, { type: 'value', name: '内存' }],
      series,
    })
  } catch (e) { console.error(e) }
}

function formatBytes(b) { if (!b) return '-'; const u = ['B','KB','MB','GB','TB']; let i = 0; while (b >= 1024 && i < u.length-1) { b /= 1024; i++ } return b.toFixed(1) + ' ' + u[i] }
function statusType(s) { if (s === 'running') return 'success'; if (s === 'paused') return 'warning'; return 'danger' }
function memClass(p) { if (!p) return ''; if (p >= 90) return 'metric-bad'; if (p >= 70) return 'metric-warn'; return 'metric-good' }
function rowClass({ row }) { return row.up ? '' : 'row-down' }

onMounted(load)
</script>

<style scoped>
.mw-tab { padding: 4px 0; }
.empty-guide { text-align: center; padding: 48px 24px; }
.empty-icon { color: var(--text-muted); margin-bottom: 16px; }
.empty-title { font-size: 18px; font-weight: 600; margin: 0 0 8px; }
.empty-desc { color: var(--text-dim); margin: 0 0 8px; font-size: 13px; }
.empty-hint { color: var(--text-muted); font-size: 12px; }
.kpi-row { display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); gap: 12px; margin-bottom: 16px; }
.kpi-card { border-radius: var(--radius); padding: 16px; }
.kpi-num { font-size: 24px; font-weight: 700; }
.kpi-text { font-size: 12px; color: var(--text-dim); margin-top: 2px; }
.gradient-total { background: linear-gradient(135deg, #1c2129, #2d3548); }
.gradient-up { background: linear-gradient(135deg, #1a3a2a, #2d5a3d); }
.gradient-down { background: linear-gradient(135deg, #3a1a1a, #5a2d2d); }
.gradient-conn { background: linear-gradient(135deg, #1a2a3a, #2d4a5d); }
.gradient-ops { background: linear-gradient(135deg, #2a1a3a, #4a2d5d); }
.gradient-mem { background: linear-gradient(135deg, #3a2a1a, #5d4a2d); }
.chart-section { padding: 16px; margin-bottom: 16px; }
.section-title { font-size: 14px; font-weight: 600; margin-bottom: 12px; }
.dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 4px; }
.metric-good { color: #3fb950; }
.metric-warn { color: #f0883e; }
.metric-bad { color: #dc382d; }
:deep(.row-down) { opacity: 0.6; }
.detail-content { padding: 0 20px; }
.detail-meta { display: flex; flex-wrap: wrap; gap: 16px; margin-bottom: 24px; padding: 16px; background: rgba(255,255,255,0.03); border-radius: 8px; }
.meta-item { font-size: 13px; }
.meta-label { color: var(--text-muted); margin-right: 6px; }
.mono { font-family: var(--mono); }
.metric-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(140px, 1fr)); gap: 12px; margin-bottom: 24px; }
.metric-cell { padding: 12px; background: rgba(255,255,255,0.03); border-radius: 8px; text-align: center; }
.mc-label { font-size: 11px; color: var(--text-muted); margin-bottom: 4px; }
.mc-value { font-size: 18px; font-weight: 600; }
.chart-box { width: 100%; height: 300px; }
</style>
