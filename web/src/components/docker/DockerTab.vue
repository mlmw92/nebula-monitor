<template>
  <div class="mw-tab">
    <RefreshBar :loading="loading" @refresh="load" />
    <div v-if="!loading && stats.hosts.length === 0" class="empty-guide glass">
      <div class="empty-icon">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="64" height="64">
          <rect x="3" y="4" width="18" height="16" rx="2"/>
          <path d="M3 9h18M9 9v11M15 9v11M3 14h6M15 14h6"/>
        </svg>
      </div>
      <h2 class="empty-title">尚未配置 Docker 监控</h2>
      <p class="empty-desc">当前没有已采集的 Docker 主机。请在运行 Agent 的节点上启用 Docker 守护进程。</p>
      <p class="empty-hint">配置完成后约 15-30 秒数据将出现在此页面。</p>
    </div>

    <template v-if="stats.hosts.length > 0">
      <div class="kpi-row">
        <KpiCard label="主机总数" :value="stats.hosts.length" tone="total" />
        <KpiCard label="在线主机" :value="stats.upHosts" tone="up" />
        <KpiCard label="容器总数" :value="stats.total" tone="cluster" />
        <KpiCard label="运行中" :value="stats.running" tone="ok" />
        <KpiCard label="已停止" :value="stats.stopped" tone="down" />
        <KpiCard label="总镜像数" :value="stats.totalImages" tone="mem" />
      </div>

      <div class="chart-section glass">
        <div class="section-title">实例拓扑</div>
        <div class="host-grid">
          <div v-for="h in stats.hosts" :key="h.node" class="host-block">
            <div class="host-header" :class="{'is-down': !h.up}">
              <span class="host-dot" :class="h.up ? 'up' : 'down'"></span>
              <span class="host-name">{{ h.node }}</span>
              <span class="host-count">{{ h.containers.length }} 容器</span>
            </div>
            <div class="host-daemon mono">{{ h.daemon }}<span v-if="h.ip" class="host-ip"> · {{ h.ip }}</span></div>
            <div class="host-body">
              <div v-for="c in h.containers" :key="c.id" class="container-chip" :class="['st-' + c.state, {'is-down': c.state !== 'running'}]" @click="openDetail(c)">
                <span class="container-name">{{ c.name }}</span>
                <span class="container-state">{{ dockerStatusText(c.state) }}</span>
              </div>
              <div v-if="h.containers.length === 0" class="host-empty">无容器</div>
            </div>
          </div>
        </div>
      </div>

      <div class="mw-list glass">
        <div class="mw-list-title">容器列表</div>
        <el-table :data="pagedContainers" style="width: 100%" @row-click="openDetail" :row-class-name="rowClass" size="small" stripe @sort-change="onSortChange">
          <el-table-column prop="name" label="名称" min-width="150" show-overflow-tooltip />
          <el-table-column prop="image" label="镜像" min-width="200" show-overflow-tooltip />
          <el-table-column label="所在主机" min-width="160">
            <template #default="{ row }">
              <div class="host-cell">
                <span class="host-cell-name">{{ row.host }}</span>
                <span v-if="row.hostIp" class="host-cell-ip">{{ row.hostIp }}</span>
              </div>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="100">
            <template #default="{ row }">
              <MwStatusDot :status="row.up ? 'normal' : 'abnormal'" :label="dockerStatusText(row.status || row.state)" />
            </template>
          </el-table-column>
          <el-table-column prop="cpuPercent" label="CPU%" width="90" sortable />
          <el-table-column prop="memUsage" label="内存" width="100" sortable>
            <template #default="{ row }">{{ formatBytes(row.memUsage) }}</template>
          </el-table-column>
          <el-table-column prop="memPercent" label="内存%" width="90" sortable />
        </el-table>
        <div class="pager">
          <el-pagination background layout="total, sizes, prev, pager, next, jumper" :total="sortedContainers.length" :page-size="pageSize" :current-page="currentPage" :page-sizes="[10,20,50,100]" @current-change="v => currentPage = v" @size-change="v => { pageSize = v; currentPage = 1 }" />
        </div>
      </div>
    </template>

    <el-drawer v-model="drawerVisible" :title="detailTitle" size="50%" :destroy-on-close="true">
      <div v-if="selected" class="detail-content">
        <div class="detail-meta">
          <div class="meta-item"><span class="meta-label">名称</span>{{ selected.name }}</div>
          <div class="meta-item"><span class="meta-label">ID</span><span class="mono">{{ selected.id }}</span></div>
          <div class="meta-item"><span class="meta-label">主机</span>{{ selected.host }}</div>
          <div class="meta-item"><span class="meta-label">状态</span>{{ selected.state }}</div>
          <div class="meta-item"><span class="meta-label">镜像</span>{{ selected.image }}</div>
        </div>
        <div class="metric-grid">
          <div class="metric-cell"><div class="mc-label">CPU</div><div class="mc-value">{{ selected.cpuPercent?.toFixed(1) }}%</div></div>
          <div class="metric-cell"><div class="mc-label">内存</div><div class="mc-value">{{ formatBytes(selected.memoryUsage) }}</div></div>
          <div class="metric-cell"><div class="mc-label">重启次数</div><div class="mc-value">{{ selected.restartCount }}</div></div>
          <div class="metric-cell"><div class="mc-label">PID</div><div class="mc-value">{{ selected.pid }}</div></div>
          <div class="metric-cell"><div class="mc-label">运行时长</div><div class="mc-value">{{ formatUptime(selected.uptime) }}</div></div>
          <div class="metric-cell"><div class="mc-label">创建时间</div><div class="mc-value">{{ selected.createdAt || '-' }}</div></div>
        </div>
        <div class="chart-box" ref="chartRef"></div>
      </div>
    </el-drawer>
  </div>
</template>

<script setup>
import { ref, onMounted, computed, nextTick, watch } from 'vue'
import * as echarts from 'echarts'
import http from '../../api/http'
import RefreshBar from '../RefreshBar.vue'
import KpiCard from '../KpiCard.vue'
import MwStatusDot from '../mw/MwStatusDot.vue'

const loading = ref(true)
const stats = ref({ hosts: [] })
const drawerVisible = ref(false)
const selected = ref(null)
const chartRef = ref(null)
let chartInstance = null

const statusTextMap = {
  running: '运行中',
  paused: '已暂停',
  exited: '已停止',
  restarting: '重启中',
  dead: '异常',
  created: '已创建',
}
function dockerStatusText(state) { return statusTextMap[state] || state || '-' }

const containers = computed(() => {
  const arr = []
  for (const h of (stats.value.hosts || [])) for (const c of (h.containers || [])) arr.push({ ...c, host: h.node, hostIp: h.ip })
  return arr
})

const detailTitle = computed(() => selected.value ? `容器详情 - ${selected.value.name}` : '详情')

async function load() {
  loading.value = true
  try {
    const data = await http.get('/api/v1/middleware/docker/containers')
    const rawHosts = data.hosts || []
    const rawContainers = data.containers || []
    // 后端 containers 为按 node 的扁平列表，归并到对应主机以渲染容器列表
    const byNode = {}
    for (const h of rawHosts) { h.containers = []; byNode[h.node] = h }
    for (const c of rawContainers) {
      const h = byNode[c.node]
      if (h) h.containers.push(c)
    }
    stats.value = {
      hosts: rawHosts,
      total: 0, running: 0, stopped: 0, totalImages: 0, upHosts: 0,
    }
    let total = 0, running = 0, stopped = 0, images = 0, up = 0
    for (const h of stats.value.hosts) {
      up++
      images += h.imagesTotal || 0
      total += h.containersTotal || 0
      running += h.containersRunning || 0
      stopped += h.containersStopped || 0
    }
    stats.value.total = total
    stats.value.running = running
    stats.value.stopped = stopped
    stats.value.totalImages = images
    stats.value.upHosts = up
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
    const cpuData = await http.get(`/api/v1/query/range?node=${row.host}&metric=docker_container_cpu_percent&start=${start}&end=${end}&step=60`)
    const series = []
    if (cpuData.series) for (const s of cpuData.series) {
      if (s.labels?.id === row.id || s.labels?.name === row.name) series.push({ name: 'CPU%', type: 'line', data: s.points.map(p => [p.timestamp, p.value]), smooth: true, areaStyle: { opacity: 0.2 } })
    }
    chartInstance.setOption({
      tooltip: { trigger: 'axis' },
      legend: { data: ['CPU%'], textStyle: { color: '#8b949e' } },
      grid: { left: 50, right: 30, top: 40, bottom: 30 },
      xAxis: { type: 'time' },
      yAxis: { type: 'value', name: '%' },
      series,
    })
  } catch (e) { console.error(e) }
}

function formatBytes(b) { if (!b && b !== 0) return '-'; const u = ['B','KB','MB','GB','TB']; let i = 0; let v = b; while (v >= 1024 && i < u.length-1) { v /= 1024; i++ } return v.toFixed(1) + ' ' + u[i] }
function statusType(state) { if (state === 'running') return 'success'; if (state === 'paused') return 'warning'; if (state === 'exited') return 'info'; return 'danger' }
function rowClass({ row }) { return row.up ? '' : 'row-down' }

const currentPage = ref(1)
const pageSize = ref(10)
const sortState = ref({ prop: '', order: '' })
function onSortChange({ prop, order }) { sortState.value = { prop, order }; currentPage.value = 1 }
const sortedContainers = computed(() => {
  const arr = [...containers.value]
  const { prop, order } = sortState.value
  if (prop && order) {
    arr.sort((a, b) => {
      let av = a[prop], bv = b[prop]
      if (typeof av === 'string') return order === 'ascending' ? av.localeCompare(bv) : bv.localeCompare(av)
      av = av ?? 0; bv = bv ?? 0
      return order === 'ascending' ? av - bv : bv - av
    })
  }
  return arr
})
const pagedContainers = computed(() => sortedContainers.value.slice((currentPage.value - 1) * pageSize.value, currentPage.value * pageSize.value))
watch(containers, () => { currentPage.value = 1 })

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
.chart-section { padding: 16px; margin-bottom: 16px; }
.section-title { font-size: 14px; font-weight: 600; margin-bottom: 12px; }
.host-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 12px; }
.host-block { border: 1px solid var(--border); border-radius: 10px; overflow: hidden; background: var(--bg-elev); }
.host-header { display: flex; align-items: center; gap: 8px; padding: 10px 12px; border-bottom: 1px solid var(--border); font-weight: 600; }
.host-header.is-down { opacity: 0.6; }
.host-dot { width: 8px; height: 8px; border-radius: 50%; }
.host-dot.up { background: #4ade80; }
.host-dot.down { background: #f87171; }
.host-name { font-size: 13px; }
.host-count { margin-left: auto; font-size: 12px; color: var(--text-muted); font-weight: 400; }
.host-daemon { padding: 6px 12px; font-size: 11px; color: var(--text-muted); border-bottom: 1px solid var(--border); }
.host-ip { color: var(--text-muted); }
.host-body { padding: 10px 12px; display: flex; flex-wrap: wrap; gap: 8px; }
.container-chip { display: flex; flex-direction: column; padding: 6px 10px; border-radius: 8px; border: 1px solid var(--border); cursor: pointer; background: rgba(255,255,255,0.03); font-size: 12px; min-width: 110px; }
.container-chip.is-down { opacity: 0.6; }
.container-chip.st-running { border-left: 3px solid var(--chart-green); }
.container-chip.st-paused { border-left: 3px solid var(--chart-orange); }
.container-chip.st-exited { border-left: 3px solid var(--chart-red); }
.container-name { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.container-state { font-size: 11px; color: var(--text-muted); }
.host-empty { font-size: 12px; color: var(--text-muted); padding: 4px 0; }
.host-cell { display: flex; flex-direction: column; }
.host-cell-name { font-size: 13px; }
.host-cell-ip { font-size: 11px; color: var(--text-muted); font-family: var(--mono); }
.pager { display: flex; justify-content: flex-end; margin-top: 12px; }
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
