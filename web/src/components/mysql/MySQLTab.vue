<template>
  <div class="mw-tab">
    <RefreshBar :loading="loading" @refresh="load" />
    <!-- 空状态 -->
    <div v-if="!loading && instances.length === 0" class="empty-guide glass">
      <div class="empty-icon">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="64" height="64">
          <ellipse cx="12" cy="5" rx="9" ry="3"/>
          <path d="M3 5v6c0 1.66 4 3 9 3s9-1.34 9-3V5"/>
          <path d="M3 11v6c0 1.66 4 3 9 3s9-1.34 9-3v-6"/>
        </svg>
      </div>
      <h2 class="empty-title">尚未配置 MySQL 监控</h2>
      <p class="empty-desc">当前没有已采集的 MySQL 实例。请在运行 Agent 的节点上配置 MySQL 实例连接信息。</p>
      <p class="empty-hint">配置完成后约 15-30 秒数据将出现在此页面。</p>
    </div>

    <template v-if="instances.length > 0">
      <!-- KPI 概览卡片：与 Redis 统一使用 KpiCard 组件 -->
      <div class="kpi-row">
        <KpiCard :value="stats.total" label="实例总数" tone="total">
          <template #icon><el-icon :size="20"><Grid /></el-icon></template>
        </KpiCard>
        <KpiCard :value="stats.up" label="在线实例" tone="up">
          <template #icon><el-icon :size="20"><CircleCheck /></el-icon></template>
        </KpiCard>
        <KpiCard :value="stats.down" label="离线实例" tone="down">
          <template #icon><el-icon :size="20"><CircleClose /></el-icon></template>
        </KpiCard>
        <KpiCard :value="formatNum(stats.totalConnections)" label="总连接数" tone="conn">
          <template #icon><el-icon :size="20"><Connection /></el-icon></template>
        </KpiCard>
        <KpiCard :value="formatNum(stats.totalQPS)" label="总 QPS" tone="ops">
          <template #icon><el-icon :size="20"><DataLine /></el-icon></template>
        </KpiCard>
        <KpiCard :value="stats.totalSlowQueries" label="慢查询累计" tone="alert">
          <template #icon><el-icon :size="20"><Bell /></el-icon></template>
        </KpiCard>
      </div>

      <!-- 实例列表 -->
      <div class="chart-section glass">
        <div class="section-title">实例列表</div>
        <el-table :data="instances" class="mysql-table" style="width: 100%" @row-click="openDetail" :row-class-name="rowClass">
          <el-table-column prop="instance" label="实例地址" min-width="180" show-overflow-tooltip />
          <el-table-column prop="name" label="名称" min-width="130" show-overflow-tooltip>
            <template #header>
              <el-tooltip content="Agent 配置中指定的实例别名/名称；未配置时为空" placement="top">
                <span>名称 <el-icon :size="12" style="vertical-align: middle; margin-left: 2px;"><QuestionFilled /></el-icon></span>
              </el-tooltip>
            </template>
            <template #default="{ row }">
              <span class="text-muted">{{ row.name || '-' }}</span>
            </template>
          </el-table-column>
          <el-table-column prop="role" label="角色" min-width="90">
            <template #default="{ row }">
              <el-tag :type="row.role === 'master' ? 'warning' : 'info'" size="small">{{ row.role }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="version" label="版本" min-width="140" show-overflow-tooltip />
          <el-table-column label="状态" min-width="100">
            <template #default="{ row }">
              <span :class="['dot', row.up ? 'up' : 'down']"></span>
              {{ row.up ? '在线' : '离线' }}
            </template>
          </el-table-column>
          <el-table-column prop="threadsConnected" label="连接数" min-width="110" sortable />
          <el-table-column prop="queriesPerSec" label="QPS" min-width="100" sortable />
          <el-table-column label="缓冲命中率" min-width="150" sortable :sort-by="'bufferPoolHitRate'">
            <template #default="{ row }">
              <span :class="hitRateClass(row.bufferPoolHitRate)">{{ row.bufferPoolHitRate ? row.bufferPoolHitRate.toFixed(1) + '%' : '-' }}</span>
            </template>
          </el-table-column>
          <el-table-column prop="secondsBehindMaster" label="复制延迟(s)" min-width="150" sortable />
          <el-table-column prop="uptime" label="运行时长" min-width="120">
            <template #default="{ row }">{{ formatUptime(row.uptime) }}</template>
          </el-table-column>
        </el-table>
      </div>
    </template>

    <!-- 详情抽屉 -->
    <el-drawer v-model="drawerVisible" :title="detailTitle" size="50%" :destroy-on-close="true">
      <div v-if="selected" class="detail-content">
        <div class="detail-meta">
          <div class="meta-item"><span class="meta-label">实例</span><span class="mono">{{ selected.instance }}</span></div>
          <div class="meta-item"><span class="meta-label">节点</span>{{ selected.node }}</div>
          <div class="meta-item"><span class="meta-label">角色</span>{{ selected.role }}</div>
          <div class="meta-item"><span class="meta-label">版本</span>{{ selected.version }}</div>
          <div class="meta-item"><span class="meta-label">拓扑</span>{{ selected.topology }}</div>
          <div class="meta-item" v-if="selected.replicaOf"><span class="meta-label">主库</span>{{ selected.replicaOf }}</div>
        </div>
        <div class="detail-metrics">
          <div class="metric-grid">
            <div class="metric-cell"><div class="mc-label">连接数</div><div class="mc-value">{{ selected.threadsConnected }}</div></div>
            <div class="metric-cell"><div class="mc-label">活跃连接</div><div class="mc-value">{{ selected.threadsRunning }}</div></div>
            <div class="metric-cell"><div class="mc-label">最大连接</div><div class="mc-value">{{ selected.maxConnections }}</div></div>
            <div class="metric-cell"><div class="mc-label">QPS</div><div class="mc-value">{{ formatNum(selected.queriesPerSec) }}</div></div>
            <div class="metric-cell"><div class="mc-label">慢查询</div><div class="mc-value">{{ selected.slowQueries }}</div></div>
            <div class="metric-cell"><div class="mc-label">缓冲命中率</div><div class="mc-value">{{ selected.bufferPoolHitRate ? selected.bufferPoolHitRate.toFixed(1) + '%' : '-' }}</div></div>
            <div class="metric-cell"><div class="mc-label">行锁等待</div><div class="mc-value">{{ selected.rowLockWaits }}</div></div>
            <div class="metric-cell"><div class="mc-label">死锁</div><div class="mc-value">{{ selected.deadlocks }}</div></div>
            <div class="metric-cell"><div class="mc-label">复制延迟</div><div class="mc-value">{{ selected.secondsBehindMaster }}s</div></div>
            <div class="metric-cell"><div class="mc-label">Commit</div><div class="mc-value">{{ formatNum(selected.comCommit) }}</div></div>
            <div class="metric-cell"><div class="mc-label">Rollback</div><div class="mc-value">{{ formatNum(selected.comRollback) }}</div></div>
            <div class="metric-cell"><div class="mc-label">运行时长</div><div class="mc-value">{{ formatUptime(selected.uptime) }}</div></div>
          </div>
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
import RefreshBar from '../RefreshBar.vue'
import KpiCard from '../KpiCard.vue'
import {
  Grid,
  CircleCheck,
  CircleClose,
  Connection,
  DataLine,
  Bell,
  QuestionFilled,
} from '@element-plus/icons-vue'

const loading = ref(true)
const instances = ref([])
const drawerVisible = ref(false)
const selected = ref(null)
const chartRef = ref(null)
let chartInstance = null

const stats = computed(() => {
  const s = { total: 0, up: 0, down: 0, totalConnections: 0, totalQPS: 0, totalSlowQueries: 0 }
  for (const i of instances.value) {
    s.total++
    if (i.up) s.up++; else s.down++
    s.totalConnections += i.threadsConnected || 0
    s.totalQPS += i.queriesPerSec || 0
    s.totalSlowQueries += i.slowQueries || 0
  }
  return s
})

const detailTitle = computed(() => selected.value ? `MySQL 详情 - ${selected.value.name || selected.value.instance}` : '详情')

async function load() {
  loading.value = true
  try {
    const data = await http.get('/api/v1/middleware/mysql/instances')
    instances.value = data.instances || []
  } catch (e) {
    console.error('加载 MySQL 实例失败', e)
  } finally {
    loading.value = false
  }
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
    const [qpsData, connData] = await Promise.all([
      http.get(`/api/v1/query/range?node=${row.node}&metric=mysql_queries_per_sec&start=${start}&end=${end}&step=60`),
      http.get(`/api/v1/query/range?node=${row.node}&metric=mysql_threads_connected&start=${start}&end=${end}&step=60`),
    ])
    const series = []
    if (qpsData.series) for (const s of qpsData.series) {
      if (s.labels?.instance === row.instance) series.push({ name: 'QPS', type: 'line', data: s.points.map(p => [p.timestamp, p.value]), smooth: true })
    }
    if (connData.series) for (const s of connData.series) {
      if (s.labels?.instance === row.instance) series.push({ name: '连接数', type: 'line', yAxisIndex: 1, data: s.points.map(p => [p.timestamp, p.value]), smooth: true })
    }
    chartInstance.setOption({
      tooltip: { trigger: 'axis' },
      legend: { data: series.map(s => s.name), textStyle: { color: '#8b949e' } },
      grid: { left: 50, right: 50, top: 40, bottom: 30 },
      xAxis: { type: 'time' },
      yAxis: [
        { type: 'value', name: 'QPS' },
        { type: 'value', name: '连接数' },
      ],
      series,
    })
  } catch (e) { console.error(e) }
}

function formatNum(n) { return n != null ? Number(n).toLocaleString() : '-' }
function formatUptime(s) { if (!s) return '-'; const d = Math.floor(s / 86400); const h = Math.floor((s % 86400) / 3600); return d > 0 ? `${d}天${h}小时` : `${h}小时` }
function hitRateClass(v) { if (!v) return ''; if (v >= 99) return 'metric-good'; if (v >= 90) return 'metric-warn'; return 'metric-bad' }
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
.chart-section { padding: 16px; margin-bottom: 16px; }
.section-title { font-size: 14px; font-weight: 600; margin-bottom: 12px; color: var(--text); }
.mysql-table :deep(th) { white-space: nowrap; }
.text-muted { color: var(--text-muted); }
.dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 4px; }
.dot.up { background: var(--accent); box-shadow: 0 0 6px var(--accent-glow); }
.dot.down { background: var(--danger); }
.metric-good { color: var(--accent); }
.metric-warn { color: var(--warn); }
.metric-bad { color: var(--danger); }
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
