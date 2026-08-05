<template>
  <div class="mw-tab">
    <RefreshBar :loading="loading" @refresh="load" />
    <div v-if="!loading && instances.length === 0" class="empty-guide glass">
      <div class="empty-icon">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="64" height="64">
          <ellipse cx="12" cy="5" rx="9" ry="3" />
          <path d="M3 5v6c0 1.66 4 3 9 3s9-1.34 9-3V5" />
          <path d="M3 11v6c0 1.66 4 3 9 3s9-1.34 9-3v-6" />
        </svg>
      </div>
      <h2 class="empty-title">尚未配置 FastDFS 监控</h2>
      <p class="empty-desc">当前没有已采集的 FastDFS 实例。请运行 Agent 的节点配置 tracker / storage 连接信息，或指向 fastdfs_exporter。</p>
      <p class="empty-hint">配置完成后约 15-30 秒数据将出现在此页面。</p>
    </div>

    <template v-if="instances.length > 0">
      <div class="kpi-row">
        <KpiCard label="实例总数" :value="stats.total" tone="total">
          <template #icon><el-icon :size="20"><Grid /></el-icon></template>
        </KpiCard>
        <KpiCard label="在线实例" :value="stats.up" tone="up">
          <template #icon><el-icon :size="20"><CircleCheck /></el-icon></template>
        </KpiCard>
        <KpiCard label="离线实例" :value="stats.down" tone="down">
          <template #icon><el-icon :size="20"><CircleClose /></el-icon></template>
        </KpiCard>
        <KpiCard label="在线 Storage" :value="formatNum(stats.onlineStorage)" tone="conn">
          <template #icon><el-icon :size="20"><Monitor /></el-icon></template>
        </KpiCard>
        <KpiCard label="总空间" :value="formatMB(stats.totalSpace)" tone="ops">
          <template #icon><el-icon :size="20"><Coin /></el-icon></template>
        </KpiCard>
        <KpiCard label="已用空间" :value="formatMB(stats.usedSpace)" tone="alert">
          <template #icon><el-icon :size="20"><DataLine /></el-icon></template>
        </KpiCard>
      </div>

      <div class="mw-list glass">
        <div class="mw-list-title">实例列表</div>
        <el-table class="fastdfs-table" :data="pagedInstances" style="width: 100%" @row-click="openDetail" :row-class-name="rowClass" size="small" stripe @sort-change="onSortChange">
          <el-table-column prop="instance" label="实例地址" min-width="150" show-overflow-tooltip />
          <el-table-column prop="name" label="名称" min-width="110" show-overflow-tooltip />
          <el-table-column prop="role" label="角色" min-width="80">
            <template #default="{ row }"><MwRoleTag :role="row.role" /></template>
          </el-table-column>
          <el-table-column prop="group" label="分组" min-width="90" show-overflow-tooltip />
          <el-table-column label="状态" min-width="80">
            <template #default="{ row }"><MwStatusDot :status="row.up ? 'normal' : 'abnormal'" :label="row.up ? '正常' : '离线'" /></template>
          </el-table-column>
          <el-table-column label="总空间" min-width="100">
            <template #default="{ row }">{{ row.totalSpaceMB ? formatMB(row.totalSpaceMB) : '-' }}</template>
          </el-table-column>
          <el-table-column label="已用" min-width="100">
            <template #default="{ row }">{{ row.usedSpaceMB ? formatMB(row.usedSpaceMB) : '-' }}</template>
          </el-table-column>
          <el-table-column label="空闲" min-width="100">
            <template #default="{ row }">{{ row.freeSpaceMB ? formatMB(row.freeSpaceMB) : '-' }}</template>
          </el-table-column>
          <el-table-column prop="storageOnline" label="在线Storage" min-width="110" sortable />
        </el-table>
        <div class="pager">
          <el-pagination background layout="total, sizes, prev, pager, next, jumper" :total="sortedInstances.length" :page-size="pageSize" :current-page="currentPage" :page-sizes="[10,20,50,100]" @current-change="v => currentPage = v" @size-change="v => { pageSize = v; currentPage = 1 }" />
        </div>
      </div>
    </template>

    <el-drawer v-model="drawerVisible" :title="detailTitle" size="50%" :destroy-on-close="true">
      <div v-if="selected" class="detail-content">
        <div class="detail-meta">
          <div class="meta-item"><span class="meta-label">实例</span><span class="mono">{{ selected.instance }}</span></div>
          <div class="meta-item"><span class="meta-label">节点</span>{{ selected.node }}</div>
          <div class="meta-item"><span class="meta-label">角色</span>{{ selected.role }}</div>
          <div class="meta-item"><span class="meta-label">分组</span>{{ selected.group || '-' }}</div>
        </div>
        <div class="metric-grid">
          <div class="metric-cell"><div class="mc-label">Group 总数</div><div class="mc-value">{{ formatNum(selected.groupTotal) }}</div></div>
          <div class="metric-cell"><div class="mc-label">Storage 总数</div><div class="mc-value">{{ formatNum(selected.storageTotal) }}</div></div>
          <div class="metric-cell"><div class="mc-label">在线 Storage</div><div class="mc-value">{{ formatNum(selected.storageOnline) }}</div></div>
          <div class="metric-cell"><div class="mc-label">离线 Storage</div><div class="mc-value">{{ formatNum(selected.storageOffline) }}</div></div>
          <div class="metric-cell"><div class="mc-label">总空间</div><div class="mc-value">{{ selected.totalSpaceMB ? formatMB(selected.totalSpaceMB) : '-' }}</div></div>
          <div class="metric-cell"><div class="mc-label">已用空间</div><div class="mc-value">{{ selected.usedSpaceMB ? formatMB(selected.usedSpaceMB) : '-' }}</div></div>
          <div class="metric-cell"><div class="mc-label">空闲空间</div><div class="mc-value">{{ selected.freeSpaceMB ? formatMB(selected.freeSpaceMB) : '-' }}</div></div>
          <div class="metric-cell"><div class="mc-label">Trunk 空闲</div><div class="mc-value">{{ selected.trunkFreeMB ? formatMB(selected.trunkFreeMB) : '-' }}</div></div>
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
import MwRoleTag from '../mw/MwRoleTag.vue'
import { Grid, CircleCheck, CircleClose, Monitor, Coin, DataLine } from '@element-plus/icons-vue'

const loading = ref(true)
const instances = ref([])
const drawerVisible = ref(false)
const selected = ref(null)
const chartRef = ref(null)
let chartInstance = null

const stats = computed(() => {
  const s = { total: 0, up: 0, down: 0, onlineStorage: 0, totalSpace: 0, usedSpace: 0 }
  for (const i of instances.value) {
    s.total++
    if (i.up) s.up++; else s.down++
    s.onlineStorage += i.storageOnline || 0
    s.totalSpace += i.totalSpaceMB || 0
    s.usedSpace += i.usedSpaceMB || 0
  }
  return s
})

const detailTitle = computed(() => selected.value ? `FastDFS 详情 - ${selected.value.name || selected.value.instance}` : '详情')

async function load() {
  loading.value = true
  try {
    const data = await http.get('/api/v1/middleware/fastdfs/instances')
    instances.value = data.instances || []
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
    const totalData = await http.get(`/api/v1/query/range?node=${row.node}&metric=fastdfs_total_space&start=${start}&end=${end}&step=60`)
    const usedData = await http.get(`/api/v1/query/range?node=${row.node}&metric=fastdfs_used_space&start=${start}&end=${end}&step=60`)
    const series = []
    if (totalData.series) for (const s of totalData.series) {
      if (s.labels?.instance === row.instance) series.push({ name: '总空间(GB)', type: 'line', data: s.points.map(p => [p.timestamp, (p.value / 1024 / 1024 / 1024).toFixed(2)]), smooth: true, areaStyle: { opacity: 0.2 } })
    }
    if (usedData.series) for (const s of usedData.series) {
      if (s.labels?.instance === row.instance) series.push({ name: '已用空间(GB)', type: 'line', data: s.points.map(p => [p.timestamp, (p.value / 1024 / 1024 / 1024).toFixed(2)]), smooth: true })
    }
    chartInstance.setOption({
      tooltip: { trigger: 'axis' },
      legend: { data: ['总空间(GB)', '已用空间(GB)'], textStyle: { color: '#8b949e' } },
      grid: { left: 55, right: 30, top: 40, bottom: 30 },
      xAxis: { type: 'time' },
      yAxis: { type: 'value', name: 'GB' },
      series,
    })
  } catch (e) { console.error(e) }
}

function formatNum(n) { return n != null ? Number(n).toLocaleString() : '-' }
function formatMB(n) { if (!n && n !== 0) return '-'; if (n < 1024) return n.toFixed(1) + ' MB'; if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' GB'; return (n / 1024 / 1024).toFixed(2) + ' TB' }
function rowClass({ row }) { return row.up ? '' : 'row-down' }

const currentPage = ref(1)
const pageSize = ref(10)
const sortState = ref({ prop: '', order: '' })
function onSortChange({ prop, order }) { sortState.value = { prop, order }; currentPage.value = 1 }
const sortedInstances = computed(() => {
  const arr = [...instances.value]
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
const pagedInstances = computed(() => sortedInstances.value.slice((currentPage.value - 1) * pageSize.value, currentPage.value * pageSize.value))
watch(instances, () => { currentPage.value = 1 })

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
.pager { display: flex; justify-content: flex-end; margin-top: 12px; }
.fastdfs-table :deep(th) { white-space: nowrap; }
.fastdfs-table :deep(td) { white-space: nowrap; }
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
