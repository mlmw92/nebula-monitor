<template>
  <div class="mw-tab">
    <RefreshBar :loading="loading" @refresh="load" />
    <div v-if="!loading && instances.length === 0" class="empty-guide glass">
      <div class="empty-icon">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="64" height="64">
          <path d="M12 2v20M2 12h20"/>
          <circle cx="12" cy="12" r="9"/>
        </svg>
      </div>
      <h2 class="empty-title">尚未配置 Nginx 监控</h2>
      <p class="empty-desc">当前没有已采集的 Nginx 实例。请在运行 Agent 的节点上启用 Nginx stub_status 模块。</p>
      <p class="empty-hint">配置完成后约 15-30 秒数据将出现在此页面。</p>
    </div>

    <template v-if="instances.length > 0">
      <div class="kpi-row">
        <KpiCard label="实例总数" :value="stats.total" tone="total">
          <template #icon><ServerIcon /></template>
        </KpiCard>
        <KpiCard label="在线实例" :value="stats.up" tone="up">
          <template #icon><CheckCircleIcon /></template>
        </KpiCard>
        <KpiCard label="离线实例" :value="stats.down" tone="down">
          <template #icon><XCircleIcon /></template>
        </KpiCard>
        <KpiCard label="总活动连接" :value="stats.totalActive" tone="conn">
          <template #icon><ConnectionIcon /></template>
        </KpiCard>
        <KpiCard label="总请求" :value="formatNum(stats.totalRequests)" tone="ops">
          <template #icon><ActivityIcon /></template>
        </KpiCard>
        <KpiCard label="总等待连接" :value="stats.totalWaiting" tone="mem">
          <template #icon><ClockIcon /></template>
        </KpiCard>
      </div>

      <div class="chart-section glass">
        <div class="section-title">实例拓扑</div>
        <div class="topo-group">
          <div class="topo-group-header">
            <span class="topo-group-title"><ServerIcon /><strong>独立实例</strong></span>
            <span class="dim">共 {{ instances.length }} 个</span>
          </div>
          <div class="topo-grid">
            <div v-for="i in instances" :key="i.instance" class="rel-node rel-standalone" :class="{'is-down': !i.up}" @click="openDetail(i)">
              <div class="rel-node-name" :title="i.name || i.instance">{{ i.name || i.instance }}</div>
              <div class="rel-node-meta">
                <span :class="['dot', i.up ? 'up' : 'down']"></span>
                <span>{{ i.up ? '在线' : '离线' }}</span>
                <span class="dim">·</span>
                <span class="mono">{{ i.nodeIp || nginxDisplayAddr(i) }}</span>
              </div>
            </div>
          </div>
        </div>
      </div>

      <div class="mw-list glass">
        <div class="mw-list-title">实例列表</div>
        <el-table :data="pagedInstances" style="width: 100%" @row-click="openDetail" :row-class-name="rowClass" size="small" stripe @sort-change="onSortChange">
          <el-table-column label="实例地址" min-width="140" show-overflow-tooltip>
            <template #default="{ row }"><span class="mono">{{ row.nodeIp || nginxDisplayAddr(row) }}</span></template>
          </el-table-column>
          <el-table-column prop="name" label="名称" min-width="90" show-overflow-tooltip />
          <el-table-column label="状态" width="80">
            <template #default="{ row }"><MwStatusDot :status="row.up ? 'normal' : 'abnormal'" :label="row.up ? '正常' : '离线'" /></template>
          </el-table-column>
          <el-table-column prop="activeConnections" label="活动连接" min-width="85" sortable />
          <el-table-column label="已接收" min-width="85" sortable :sort-by="'accepts'">
            <template #default="{ row }"><span class="mono">{{ formatNum(row.accepts) }}</span></template>
          </el-table-column>
          <el-table-column label="已处理" min-width="85" sortable :sort-by="'handled'">
            <template #default="{ row }"><span class="mono">{{ formatNum(row.handled) }}</span></template>
          </el-table-column>
          <el-table-column label="总请求" min-width="85" sortable :sort-by="'requests'">
            <template #default="{ row }"><span class="mono">{{ formatNum(row.requests) }}</span></template>
          </el-table-column>
          <el-table-column label="读取" width="65" sortable :sort-by="'reading'">
            <template #default="{ row }"><span class="mono">{{ row.reading }}</span></template>
          </el-table-column>
          <el-table-column label="写入" width="65" sortable :sort-by="'writing'">
            <template #default="{ row }"><span class="mono">{{ row.writing }}</span></template>
          </el-table-column>
          <el-table-column label="等待" width="65" sortable :sort-by="'waiting'">
            <template #default="{ row }"><span class="mono">{{ row.waiting }}</span></template>
          </el-table-column>
        </el-table>
        <div class="pager">
          <el-pagination background layout="total, sizes, prev, pager, next, jumper" :total="sortedInstances.length" :page-size="pageSize" :current-page="currentPage" :page-sizes="[10,20,50,100]" @current-change="v => currentPage = v" @size-change="v => { pageSize = v; currentPage = 1 }" />
        </div>
      </div>
    </template>

    <el-drawer v-model="drawerVisible" :title="detailTitle" size="50%" :destroy-on-close="true">
      <div v-if="selected" class="detail-content">
        <div class="detail-meta">
          <div class="meta-item"><span class="meta-label">实例地址</span><span class="mono">{{ selected.nodeIp || nginxDisplayAddr(selected) }}</span></div>
          <div class="meta-item" v-if="selected.version"><span class="meta-label">版本</span>{{ selected.version }}</div>
        </div>
        <div class="metric-grid">
          <div class="metric-cell"><div class="mc-label">活动连接</div><div class="mc-value">{{ selected.activeConnections }}</div></div>
          <div class="metric-cell"><div class="mc-label">已接收</div><div class="mc-value">{{ selected.accepts }}</div></div>
          <div class="metric-cell"><div class="mc-label">等待连接</div><div class="mc-value">{{ selected.waiting }}</div></div>
          <div class="metric-cell"><div class="mc-label">读取中</div><div class="mc-value">{{ selected.reading }}</div></div>
          <div class="metric-cell"><div class="mc-label">写入中</div><div class="mc-value">{{ selected.writing }}</div></div>
          <div class="metric-cell"><div class="mc-label">已处理</div><div class="mc-value">{{ selected.handled }}</div></div>
          <div class="metric-cell"><div class="mc-label">总请求</div><div class="mc-value">{{ selected.requests }}</div></div>
          <div class="metric-cell"><div class="mc-label">运行时长</div><div class="mc-value">{{ formatUptime(selected.uptime) }}</div></div>
        </div>
        <div class="chart-box" ref="chartRef"></div>
      </div>
    </el-drawer>
  </div>
</template>

<script setup>
import { ref, onMounted, computed, nextTick, watch, h } from 'vue'
import * as echarts from 'echarts'
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
const ServerIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="2" y="3" width="20" height="8" rx="2"/><rect x="2" y="13" width="20" height="8" rx="2"/><line x1="6" y1="7" x2="6.01" y2="7"/><line x1="6" y1="17" x2="6.01" y2="17"/></svg>')
const CheckCircleIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><polyline points="9 12 12 15 16 10"/></svg>')
const XCircleIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><line x1="15" y1="9" x2="9" y2="15"/><line x1="9" y1="9" x2="15" y2="15"/></svg>')
const ConnectionIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M13.144 10.144a4 4 0 1 0-5.742 0"/><path d="M11 14.48V17"/><circle cx="11" cy="19" r="2"/><path d="M16 9a5 5 0 0 1 4.516 2.861"/><path d="M19.922 12.633a5 5 0 0 1-.39 4.155"/><circle cx="21" cy="18" r="2"/></svg>')
const ActivityIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="22 12 18 12 15 21 9 3 6 12 2 12"/></svg>')
const ClockIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/></svg>')

const loading = ref(true)
const instances = ref([])
const drawerVisible = ref(false)
const selected = ref(null)
const chartRef = ref(null)
let chartInstance = null

const stats = computed(() => {
  const s = { total: 0, up: 0, down: 0, totalActive: 0, totalRequests: 0, totalWaiting: 0 }
  for (const i of instances.value) {
    s.total++
    if (i.up) s.up++; else s.down++
    s.totalActive += i.activeConnections || 0
    s.totalRequests += i.requests || 0
    s.totalWaiting += i.waiting || 0
  }
  return s
})

const detailTitle = computed(() => selected.value ? `Nginx 详情 - ${selected.value.name || selected.value.instance}` : '详情')

async function load() {
  loading.value = true
  try {
    const data = await http.get('/api/v1/middleware/nginx/instances')
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
    const connData = await http.get(`/api/v1/query/range?node=${row.node}&metric=nginx_active_connections&start=${start}&end=${end}&step=60`)
    const series = []
    if (connData.series) for (const s of connData.series) {
      if (s.labels?.instance === row.instance) series.push({ name: '活动连接', type: 'line', data: s.points.map(p => [p.timestamp, p.value]), smooth: true, areaStyle: { opacity: 0.2 } })
    }
    chartInstance.setOption({
      tooltip: { trigger: 'axis' },
      legend: { data: ['活动连接'], textStyle: { color: '#8b949e' } },
      grid: { left: 60, right: 30, top: 40, bottom: 30 },
      xAxis: { type: 'time' },
      yAxis: { type: 'value', name: '连接数' },
      series,
    })
  } catch (e) { console.error(e) }
}

function formatUptime(s) { if (!s) return '-'; const d = Math.floor(s / 86400); const h = Math.floor((s % 86400) / 3600); return d > 0 ? `${d}天${h}小时` : `${h}小时` }
function formatNum(n) { if (!n) return '0'; if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M'; if (n >= 1000) return (n / 1000).toFixed(1) + 'K'; return n.toFixed(0) }
function nginxDisplayAddr(i) { if (!i) return '-'; return i.nodeIp || i.instance || '-' }
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
.dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 4px; }
.dot.up { background: var(--accent); }
.dot.down { background: var(--danger); }
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

/* 实例拓扑 */
.topo-group { margin-bottom: 0; }
.topo-group-header { display: flex; align-items: center; gap: 8px; margin-bottom: 12px; }
.topo-group-title { display: inline-flex; align-items: center; gap: 8px; font-size: 14px; }
.topo-group-title svg { width: 18px; height: 18px; color: #93c5fd; }
.topo-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 12px; }
.rel-node { padding: 12px 14px; border-radius: 10px; cursor: pointer; border: 1px solid var(--border); background: var(--bg-elev); transition: transform 0.15s, box-shadow 0.15s; }
.rel-node:hover { transform: translateY(-2px); box-shadow: 0 4px 16px rgba(0,0,0,0.3); }
.rel-node.is-down { opacity: 0.6; }
.rel-node-name { font-size: 14px; font-weight: 600; color: var(--text); display: flex; align-items: center; gap: 6px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.rel-node-meta { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--text-dim); margin-top: 6px; }
.rel-standalone { border-left: 4px solid var(--chart-blue); }

/* 列表状态点 */
.status-dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 6px; }
.status-dot.up { background: #4ade80; }
.status-dot.down { background: #f87171; }
.status-text { display: inline-flex; align-items: center; font-size: 13px; }
.status-text.status-issue { color: #f87171; }
</style>
