<template>
  <div class="mw-tab">
    <RefreshBar :loading="loading" @refresh="load" />
    <div v-if="!loading && instances.length === 0" class="empty-guide glass">
      <div class="empty-icon">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="64" height="64">
          <ellipse cx="12" cy="5" rx="9" ry="3"/>
          <path d="M3 5v6c0 1.66 4 3 9 3s9-1.34 9-3V5"/>
          <path d="M3 11v6c0 1.66 4 3 9 3s9-1.34 9-3v-6"/>
        </svg>
      </div>
      <h2 class="empty-title">尚未配置 PostgreSQL 监控</h2>
      <p class="empty-desc">当前没有已采集的 PostgreSQL 实例。请在运行 Agent 的节点上配置实例连接信息。</p>
      <p class="empty-hint">配置完成后约 15-30 秒数据将出现在此页面。</p>
    </div>

    <template v-if="instances.length > 0">
      <div class="kpi-row">
        <KpiCard label="实例总数" :value="stats.total" tone="total" />
        <KpiCard label="在线实例" :value="stats.up" tone="up" />
        <KpiCard label="离线实例" :value="stats.down" tone="down" />
        <KpiCard label="总连接数" :value="formatNum(stats.totalConnections)" tone="conn" />
        <KpiCard label="总事务提交" :value="formatNum(stats.totalCommits)" tone="ops" />
        <KpiCard label="总死锁数" :value="stats.totalDeadlocks" tone="alert" />
      </div>

      <div class="chart-section glass">
        <div class="section-title">实例拓扑</div>
        <div class="topo-group">
          <div class="topo-group-header">
            <span class="topo-group-title"><svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.6"><rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/></svg><strong>独立实例</strong></span>
            <span class="dim">共 {{ instances.length }} 个</span>
          </div>
          <div class="topo-grid">
            <div v-for="i in instances" :key="i.instance" class="rel-node rel-standalone" :class="{'is-down': !i.up}" @click="openDetail(i)">
              <div class="rel-node-name" :title="i.instance">{{ i.name || i.instance }}</div>
              <div class="rel-node-meta"><span :class="['dot', i.up ? 'up' : 'down']"></span>{{ i.up ? '在线' : '离线' }}<span class="dim">·</span>{{ i.instance }}</div>
              <div class="rel-node-meta" v-if="i.role"><span>角色：{{ i.role }}</span></div>
            </div>
          </div>
        </div>
      </div>

      <div class="mw-list glass">
        <div class="mw-list-title">实例列表</div>
    <el-table :data="pagedInstances" style="width: 100%" @row-click="openDetail" :row-class-name="rowClass" size="small" stripe @sort-change="onSortChange">
      <el-table-column prop="instance" label="实例地址" width="140" show-overflow-tooltip />
      <el-table-column prop="name" label="名称" width="80" show-overflow-tooltip />
      <el-table-column prop="database" label="数据库" width="75" show-overflow-tooltip />
      <el-table-column prop="role" label="角色" width="65">
        <template #default="{ row }"><MwRoleTag :role="row.role" /></template>
      </el-table-column>
      <el-table-column prop="version" label="版本" width="120" show-overflow-tooltip />
      <el-table-column label="状态" width="75">
        <template #default="{ row }"><MwStatusDot :status="row.up ? 'normal' : 'abnormal'" :label="row.up ? '正常' : '离线'" /></template>
      </el-table-column>
      <el-table-column prop="numbackends" label="连接数" width="75" sortable />
      <el-table-column prop="cacheHitRatio" label="缓存命中率" width="90" sortable>
        <template #default="{ row }"><span :class="hitRateClass(row.cacheHitRatio)">{{ row.cacheHitRatio ? row.cacheHitRatio.toFixed(1) + '%' : '-' }}</span></template>
      </el-table-column>
      <el-table-column prop="deadlocks" label="死锁" width="65" sortable />
      <el-table-column prop="replicationLag" label="复制延迟(bytes)" width="115" sortable />
      <el-table-column label="数据库大小" width="85">
        <template #default="{ row }">{{ formatBytes(row.databaseSize) }}</template>
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
          <div class="meta-item"><span class="meta-label">实例</span><span class="mono">{{ selected.instance }}</span></div>
          <div class="meta-item"><span class="meta-label">节点</span>{{ selected.node }}</div>
          <div class="meta-item"><span class="meta-label">角色</span>{{ selected.role }}</div>
          <div class="meta-item"><span class="meta-label">版本</span>{{ selected.version }}</div>
          <div class="meta-item"><span class="meta-label">数据库</span>{{ selected.database }}</div>
        </div>
        <div class="metric-grid">
          <div class="metric-cell"><div class="mc-label">连接数</div><div class="mc-value">{{ selected.numbackends }}</div></div>
          <div class="metric-cell"><div class="mc-label">最大连接</div><div class="mc-value">{{ selected.maxConnections }}</div></div>
          <div class="metric-cell"><div class="mc-label">事务提交</div><div class="mc-value">{{ formatNum(selected.xactCommit) }}</div></div>
          <div class="metric-cell"><div class="mc-label">事务回滚</div><div class="mc-value">{{ formatNum(selected.xactRollback) }}</div></div>
          <div class="metric-cell"><div class="mc-label">缓存命中率</div><div class="mc-value">{{ selected.cacheHitRatio ? selected.cacheHitRatio.toFixed(1) + '%' : '-' }}</div></div>
          <div class="metric-cell"><div class="mc-label">死锁</div><div class="mc-value">{{ selected.deadlocks }}</div></div>
          <div class="metric-cell"><div class="mc-label">复制延迟</div><div class="mc-value">{{ formatBytes(selected.replicationLag) }}</div></div>
          <div class="metric-cell"><div class="mc-label">数据库大小</div><div class="mc-value">{{ formatBytes(selected.databaseSize) }}</div></div>
          <div class="metric-cell"><div class="mc-label">运行时长</div><div class="mc-value">{{ formatUptime(selected.uptime) }}</div></div>
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

const loading = ref(true)
const instances = ref([])
const drawerVisible = ref(false)
const selected = ref(null)
const chartRef = ref(null)
let chartInstance = null

const stats = computed(() => {
  const s = { total: 0, up: 0, down: 0, totalConnections: 0, totalCommits: 0, totalDeadlocks: 0 }
  for (const i of instances.value) {
    s.total++
    if (i.up) s.up++; else s.down++
    s.totalConnections += i.numbackends || 0
    s.totalCommits += i.xactCommit || 0
    s.totalDeadlocks += i.deadlocks || 0
  }
  return s
})

const detailTitle = computed(() => selected.value ? `PostgreSQL 详情 - ${selected.value.name || selected.value.instance}` : '详情')

async function load() {
  loading.value = true
  try {
    const data = await http.get('/api/v1/middleware/postgres/instances')
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
    const connData = await http.get(`/api/v1/query/range?node=${row.node}&metric=postgres_numbackends&start=${start}&end=${end}&step=60`)
    const series = []
    if (connData.series) for (const s of connData.series) {
      if (s.labels?.instance === row.instance) series.push({ name: '连接数', type: 'line', data: s.points.map(p => [p.timestamp, p.value]), smooth: true, areaStyle: { opacity: 0.2 } })
    }
    chartInstance.setOption({
      tooltip: { trigger: 'axis' },
      legend: { data: ['连接数'], textStyle: { color: '#8b949e' } },
      grid: { left: 50, right: 30, top: 40, bottom: 30 },
      xAxis: { type: 'time' },
      yAxis: { type: 'value', name: '连接数' },
      series,
    })
  } catch (e) { console.error(e) }
}

function formatNum(n) { return n != null ? Number(n).toLocaleString() : '-' }
function formatBytes(b) { if (!b) return '-'; const u = ['B','KB','MB','GB','TB']; let i = 0; while (b >= 1024 && i < u.length-1) { b /= 1024; i++ } return b.toFixed(1) + ' ' + u[i] }
function formatUptime(s) { if (!s) return '-'; const d = Math.floor(s / 86400); const h = Math.floor((s % 86400) / 3600); return d > 0 ? `${d}天${h}小时` : `${h}小时` }
function hitRateClass(v) { if (!v) return ''; if (v >= 99) return 'metric-good'; if (v >= 90) return 'metric-warn'; return 'metric-bad' }
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
.section-title { font-size: 14px; font-weight: 600; margin-bottom: 12px; }
.dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 4px; }
.dot.up { background: var(--accent); }
.dot.down { background: var(--danger); }
.metric-good { color: var(--accent); }
.metric-warn { color: var(--warn); }
.metric-bad { color: var(--danger); }
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
.topo-group { margin-bottom: 16px; padding: 14px; border: 1px solid var(--border); border-radius: 10px; background: rgba(255,255,255,0.02); }
.topo-group:last-child { margin-bottom: 0; }
.topo-group-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 12px; flex-wrap: wrap; gap: 8px; }
.topo-group-title { display: inline-flex; align-items: center; gap: 6px; font-size: 14px; color: var(--text); }
.topo-group-title svg { width: 18px; height: 18px; color: var(--accent); flex-shrink: 0; }
.topo-group-header .dim { color: var(--text-muted); font-size: 12px; }
.topo-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 12px; }
.rel-node { padding: 12px 14px; border-radius: 10px; cursor: pointer; border: 1px solid var(--border); background: var(--bg-elev); transition: transform 0.15s, box-shadow 0.15s; }
.rel-node:hover { transform: translateY(-2px); box-shadow: 0 4px 16px rgba(0,0,0,0.3); }
.rel-node.is-down { opacity: 0.6; }
.rel-node-name { font-size: 14px; font-weight: 600; color: var(--text); display: flex; align-items: center; gap: 6px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.rel-node-meta { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--text-dim); margin-top: 6px; }
.rel-standalone { border-left: 4px solid var(--chart-blue); }
.rel-master { border-left: 4px solid var(--chart-orange); }

/* 列表状态点 */
.status-dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 6px; }
.status-dot.up { background: #4ade80; }
.status-dot.down { background: #f87171; }
.status-text { display: inline-flex; align-items: center; font-size: 13px; }
.status-text.status-issue { color: #f87171; }
</style>
