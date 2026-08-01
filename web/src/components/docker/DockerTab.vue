<template>
  <div class="mw-tab">
    <RefreshBar :loading="loading" @refresh="load" />

    <!-- 未配置：全平台没有任何 Docker 主机上报 -->
    <div v-if="!loading && hosts.length === 0" class="empty-guide glass">
      <div class="empty-icon">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="64" height="64">
          <path d="M22 12c0-5.523-4.477-10-10-10S2 6.477 2 12s4.477 10 10 10 10-4.477 10-10z"/>
          <path d="M2 12h20M12 2c2.5 2.7 4 6.5 4 10s-1.5 7.3-4 10c-2.5-2.7-4-6.5-4-10s1.5-7.3 4-10z"/>
        </svg>
      </div>
      <h2 class="empty-title">尚未配置 Docker 监控</h2>
      <p class="empty-desc">当前没有任何节点上报 Docker 数据。请在运行 Agent 的节点 agent.yaml 中开启 <code>collectors.docker</code> 并配置 <code>dockerInstances</code>。</p>
      <p class="empty-hint">配置完成后约 15-30 秒，本页将出现 Docker 主机卡片。</p>
    </div>

    <template v-if="hosts.length > 0">
      <!-- KPI -->
      <div class="kpi-row">
        <KpiCard :value="hostStats.hosts" label="Docker 主机" tone="host">
          <template #icon><ServerIcon /></template>
        </KpiCard>
        <KpiCard :value="hostStats.total" label="容器总数" tone="total">
          <template #icon><BoxIcon /></template>
        </KpiCard>
        <KpiCard :value="hostStats.running" label="运行中" tone="up">
          <template #icon><PlayIcon /></template>
        </KpiCard>
        <KpiCard :value="hostStats.stopped" label="已停止" tone="down">
          <template #icon><StopIcon /></template>
        </KpiCard>
        <KpiCard :value="hostStats.images" label="镜像数" tone="ops">
          <template #icon><ImageIcon /></template>
        </KpiCard>
        <KpiCard :value="resStats.cpu.toFixed(1) + '%'" label="容器总 CPU" tone="cluster">
          <template #icon><CpuIcon /></template>
        </KpiCard>
        <KpiCard :value="formatBytes(resStats.mem)" label="容器总内存" tone="mem">
          <template #icon><MemoryIcon /></template>
        </KpiCard>
      </div>

      <!-- Docker 主机 -->
      <div class="chart-section glass">
        <div class="section-title">Docker 主机</div>
        <div class="host-grid">
          <div v-for="h in hosts" :key="h.node + '|' + h.daemon" class="host-card">
            <div class="host-head">
              <span class="host-dot" :class="hostClass(h)"></span>
              <span class="host-node">{{ h.node }}</span>
              <span class="host-group">{{ h.group }}</span>
              <span class="host-ip" v-if="h.ip">{{ h.ip }}</span>
            </div>
            <div class="host-daemon mono">{{ h.daemon }}</div>
            <div class="host-stats">
              <span>容器 <b>{{ h.containersTotal }}</b>（运行 {{ h.containersRunning }} / 停止 {{ h.containersStopped }}）</span>
              <span>镜像 <b>{{ h.imagesTotal }}</b></span>
            </div>
          </div>
        </div>
      </div>

      <!-- 容器列表 -->
      <div class="chart-section glass">
        <div class="section-title">容器列表</div>
        <el-table :data="containers" style="width: 100%" @row-click="openDetail" :row-class-name="rowClass" empty-text="当前 Docker 主机上没有任何容器">
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
import { ref, onMounted, onUnmounted, computed, nextTick, h } from 'vue'
import RefreshBar from '../RefreshBar.vue'
import KpiCard from '../KpiCard.vue'
import * as echarts from 'echarts'
import http from '../../api/http'

// ---- 图标组件（内联 SVG，避免依赖 lucide） ----
// ---- 图标组件（内联 SVG，渲染函数规避 runtime-only 无法编译 { template } 问题） ----
function svgIcon(s) {
  const inner = s.replace(/^<svg[^>]*>/, '').replace(/<\/svg>\s*$/, '')
  return () => h('svg', { viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', 'stroke-width': 2, innerHTML: inner })
}
const ServerIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="4" width="18" height="6" rx="1"/><rect x="3" y="14" width="18" height="6" rx="1"/><line x1="7" y1="7" x2="7" y2="7"/><line x1="7" y1="17" x2="7" y2="17"/></svg>')
const BoxIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/><polyline points="3.27 6.96 12 12.01 20.73 6.96"/><line x1="12" y1="22.08" x2="12" y2="12"/></svg>')
const PlayIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><polygon points="10 8 16 12 10 16 10 8"/></svg>')
const StopIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><rect x="9" y="9" width="6" height="6" rx="1"/></svg>')
const ImageIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="8.5" cy="8.5" r="1.5"/><polyline points="21 15 16 10 5 21"/></svg>')
const CpuIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="4" y="4" width="16" height="16" rx="2"/><rect x="9" y="9" width="6" height="6"/><line x1="9" y1="1" x2="9" y2="4"/><line x1="15" y1="1" x2="15" y2="4"/><line x1="9" y1="20" x2="9" y2="23"/><line x1="15" y1="20" x2="15" y2="23"/><line x1="20" y1="9" x2="23" y2="9"/><line x1="20" y1="14" x2="23" y2="14"/><line x1="1" y1="9" x2="4" y2="9"/><line x1="1" y1="14" x2="4" y2="14"/></svg>')
const MemoryIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 19v-3"/><path d="M10 19v-3"/><path d="M14 19v-3"/><path d="M18 19v-3"/><path d="M8 11V9"/><path d="M16 11V9"/><path d="M12 11V9"/><path d="M2 15h20"/><path d="M2 7a2 2 0 0 1 2-2h16a2 2 0 0 1 2 2v1.1a2 2 0 0 0 0 3.837V17a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2v-5.063a2 2 0 0 0 0-3.837Z"/></svg>')

const loading = ref(true)
const containers = ref([])
const hosts = ref([])
const drawerVisible = ref(false)
const selected = ref(null)
const chartRef = ref(null)
let chartInstance = null

const hostStats = computed(() => {
  const s = { hosts: 0, total: 0, running: 0, stopped: 0, images: 0 }
  for (const h of hosts.value) {
    s.hosts++
    s.total += h.containersTotal || 0
    s.running += h.containersRunning || 0
    s.stopped += h.containersStopped || 0
    s.images += h.imagesTotal || 0
  }
  return s
})

const resStats = computed(() => {
  const s = { cpu: 0, mem: 0, netRx: 0 }
  for (const c of containers.value) {
    s.cpu += c.cpuPercent || 0
    s.mem += c.memUsage || 0
    s.netRx += c.netRx || 0
  }
  return s
})

const detailTitle = computed(() => selected.value ? `Docker 详情 - ${selected.name}` : '详情')

async function load() {
  loading.value = true
  try {
    const data = await http.get('/api/v1/middleware/docker/containers')
    containers.value = data.containers || []
    hosts.value = data.hosts || []
  } catch (e) { console.error(e) } finally { loading.value = false }
}

function hostClass(h) {
  if (h.containersRunning > 0) return 'dot-up'
  if (h.containersTotal > 0) return 'dot-warn'
  return 'dot-idle'
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
.empty-desc code { background: rgba(255,255,255,0.08); padding: 2px 6px; border-radius: 4px; font-family: var(--mono); }
.empty-hint { color: var(--text-muted); font-size: 12px; }
.kpi-row { display: grid; grid-template-columns: repeat(auto-fit, minmax(140px, 1fr)); gap: 10px; margin-bottom: 16px; }
.chart-section { padding: 16px; margin-bottom: 16px; }
.section-title { font-size: 14px; font-weight: 600; margin-bottom: 12px; }
.host-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 12px; }
.host-card { padding: 14px; background: rgba(255,255,255,0.03); border-radius: 8px; border: 1px solid rgba(255,255,255,0.05); }
.host-head { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; }
.host-dot { width: 8px; height: 8px; border-radius: 50%; flex: 0 0 auto; }
.dot-up { background: #3fb950; box-shadow: 0 0 6px #3fb950; }
.dot-warn { background: #f0883e; }
.dot-idle { background: #6e7681; }
.host-node { font-size: 14px; font-weight: 600; }
.host-group { font-size: 11px; color: var(--text-muted); background: rgba(255,255,255,0.06); padding: 1px 8px; border-radius: 10px; }
.host-daemon { font-size: 11px; color: var(--text-muted); margin-bottom: 8px; word-break: break-all; }
.host-stats { display: flex; flex-direction: column; gap: 4px; font-size: 12px; color: var(--text-dim); }
.host-stats b { color: var(--text); }
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
