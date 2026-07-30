<template>
  <div class="redis-tab">
    <!-- 空状态：无实例时引导用户配置 -->
    <div v-if="!loading && instances.length === 0" class="empty-guide glass">
      <div class="empty-icon">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="64" height="64">
          <ellipse cx="12" cy="5" rx="9" ry="3"/>
          <path d="M3 5v6c0 1.66 4 3 9 3s9-1.34 9-3V5"/>
          <path d="M3 11v6c0 1.66 4 3 9 3s9-1.34 9-3v-6"/>
        </svg>
      </div>
      <h2 class="empty-title">尚未配置 Redis 监控</h2>
      <p class="empty-desc">当前没有已采集的 Redis 实例。请在运行 Agent 的节点上执行以下命令，按引导配置 Redis 实例：</p>
      <div class="empty-cmd">
        <code>{{ redisInstallCmd }}</code>
        <button class="copy-btn" @click="copyCmd" title="复制命令">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="14" height="14">
            <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
            <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
          </svg>
        </button>
      </div>
      <p class="empty-hint">若节点没有本地脚本，先下载到本地再交互运行：<code>{{ redisInstallCmdAlt }}</code></p>
      <p class="empty-hint">配置完成后约 15-30 秒数据将出现在此页面。详细配置说明请参阅 README.md。</p>
    </div>

    <!-- 有数据时的正常布局 -->
    <template v-if="instances.length > 0">
    <!-- ===== 区块1：统计概览卡片 ===== -->
    <div class="kpi-row">
      <div class="kpi-card gradient-total">
        <div class="kpi-icon"><DatabaseIcon /></div>
        <div class="kpi-body">
          <div class="kpi-num">{{ stats.total }}</div>
          <div class="kpi-text">实例总数</div>
        </div>
      </div>
      <div class="kpi-card gradient-up">
        <div class="kpi-icon"><CheckCircleIcon /></div>
        <div class="kpi-body">
          <div class="kpi-num">{{ stats.up }}</div>
          <div class="kpi-text">在线实例</div>
        </div>
      </div>
      <div class="kpi-card gradient-down">
        <div class="kpi-icon"><XCircleIcon /></div>
        <div class="kpi-body">
          <div class="kpi-num">{{ stats.down }}</div>
          <div class="kpi-text">离线实例</div>
        </div>
      </div>
      <div class="kpi-card gradient-mem">
        <div class="kpi-icon"><MemoryIcon /></div>
        <div class="kpi-body">
          <div class="kpi-num">{{ formatBytes(stats.totalMemory) }}</div>
          <div class="kpi-text">总内存使用</div>
        </div>
      </div>
      <div class="kpi-card gradient-conn">
        <div class="kpi-icon"><ConnectionIcon /></div>
        <div class="kpi-body">
          <div class="kpi-num">{{ stats.totalClients }}</div>
          <div class="kpi-text">总连接客户端</div>
        </div>
      </div>
      <div class="kpi-card gradient-ops">
        <div class="kpi-icon"><ActivityIcon /></div>
        <div class="kpi-body">
          <div class="kpi-num">{{ formatNum(stats.totalOps) }}</div>
          <div class="kpi-text">总 OPS</div>
        </div>
      </div>
    </div>

    <!-- ===== 区块2：分布可视化（环形图）===== -->
    <div class="chart-section glass">
      <div class="section-title">分布概览</div>
      <div class="pie-row">
        <div class="pie-item">
          <div :ref="el => setChartRef(el, 'topologyPie')" class="pie-chart"></div>
          <div class="pie-title">部署拓扑分布</div>
        </div>
        <div class="pie-item">
          <div :ref="el => setChartRef(el, 'rolePie')" class="pie-chart"></div>
          <div class="pie-title">角色分布</div>
        </div>
        <div class="pie-item">
          <div :ref="el => setChartRef(el, 'statusPie')" class="pie-chart"></div>
          <div class="pie-title">在线状态</div>
        </div>
      </div>
    </div>

    <!-- ===== 区块3：性能排行（横向柱状图）===== -->
    <div class="chart-section glass">
      <div class="section-title">性能排行 Top 10</div>
      <div class="bar-row">
        <div class="bar-item">
          <div class="bar-sub-title">内存使用量 Top 10</div>
          <div :ref="el => setChartRef(el, 'memBar')" class="bar-chart"></div>
        </div>
        <div class="bar-item">
          <div class="bar-sub-title">OPS Top 10</div>
          <div :ref="el => setChartRef(el, 'opsBar')" class="bar-chart"></div>
        </div>
      </div>
    </div>

    <!-- ===== 区块4：缓存命中率 ===== -->
    <div class="chart-section glass">
      <div class="section-title">缓存命中率</div>
      <div :ref="el => setChartRef(el, 'hitRateBar')" class="hitrate-chart"></div>
    </div>

    <!-- ===== 区块5：实例列表 ===== -->
    <div class="table-section glass">
      <div class="table-toolbar">
        <div class="section-title no-bar">实例列表</div>
        <div class="toolbar-right">
          <el-select v-model="filterStatus" placeholder="状态" clearable size="small" style="width: 100px">
            <el-option label="全部" value="" />
            <el-option label="在线" value="up" />
            <el-option label="离线" value="down" />
          </el-select>
          <el-select v-model="filterTopology" placeholder="拓扑" clearable size="small" style="width: 120px">
            <el-option label="全部" value="" />
            <el-option label="单机" value="standalone" />
            <el-option label="主从" value="replication" />
            <el-option label="哨兵" value="sentinel" />
            <el-option label="集群" value="cluster" />
          </el-select>
          <el-input v-model="searchText" placeholder="搜索实例/节点" clearable size="small" style="width: 200px" :prefix-icon="SearchIcon" />
          <el-select v-model="refreshInterval" size="small" style="width: 110px" @change="onRefreshChange">
            <el-option label="不刷新" :value="0" />
            <el-option label="10秒" :value="10" />
            <el-option label="30秒" :value="30" />
            <el-option label="60秒" :value="60" />
          </el-select>
          <el-button size="small" @click="loadInstances" :icon="RefreshIcon">刷新</el-button>
        </div>
      </div>
      <el-table :data="filteredInstances" style="width: 100%" @row-click="openDetail" :row-class-name="rowClass" size="small" stripe>
        <el-table-column label="节点" prop="node" width="140" show-overflow-tooltip />
        <el-table-column label="实例地址" prop="instance" width="200" show-overflow-tooltip>
          <template #default="{ row }">
            <span class="mono">{{ row.instance }}</span>
          </template>
        </el-table-column>
        <el-table-column label="角色" width="100">
          <template #default="{ row }">
            <span class="role-tag" :class="row.role">{{ roleLabel(row.role) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="拓扑" width="90">
          <template #default="{ row }">
            <span class="topo-tag">{{ topoLabel(row.topology) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="版本" prop="version" width="90" />
        <el-table-column label="状态" width="80">
          <template #default="{ row }">
            <span class="status-dot" :class="row.up ? 'up' : 'down'"></span>
            {{ row.up ? '在线' : '离线' }}
          </template>
        </el-table-column>
        <el-table-column label="客户端数" width="90" align="right">
          <template #default="{ row }">
            <span class="mono">{{ formatNum(row.clients) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="内存使用" min-width="160">
          <template #default="{ row }">
            <div class="mem-cell">
              <div class="mem-text mono">{{ formatBytes(row.usedMemory) }}</div>
              <div class="bar" v-if="row.memPercent > 0">
                <div class="bar-fill" :class="memBarClass(row.memPercent)" :style="{ width: row.memPercent + '%' }"></div>
              </div>
              <div class="mem-pct" v-if="row.memPercent > 0">{{ row.memPercent }}%</div>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="OPS" width="90" align="right">
          <template #default="{ row }">
            <span class="mono">{{ formatNum(row.ops) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="命中率" width="80" align="right">
          <template #default="{ row }">
            <span class="mono" :class="hitRateClass(row.hitRate)">{{ row.hitRate }}%</span>
          </template>
        </el-table-column>
        <el-table-column label="运行时长" width="100">
          <template #default="{ row }">
            <span class="mono">{{ formatUptime(row.uptime) }}</span>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- ===== 区块6：实例详情抽屉 ===== -->
    <el-drawer v-model="detailVisible" size="60%" :with-header="false" direction="rtl" class="detail-drawer">
      <div class="detail-content" v-if="selected">
        <!-- 实例元信息卡 -->
        <div class="detail-header">
          <div class="dh-left">
            <div class="dh-title">
              <span class="status-dot lg" :class="selected.up ? 'up' : 'down'"></span>
              <span class="mono">{{ selected.instance }}</span>
            </div>
            <div class="dh-meta">
              <span class="role-tag" :class="selected.role">{{ roleLabel(selected.role) }}</span>
              <span class="topo-tag">{{ topoLabel(selected.topology) }}</span>
              <span class="meta-item">节点：{{ selected.node }}</span>
              <span class="meta-item" v-if="selected.version">版本：{{ selected.version }}</span>
              <span class="meta-item" v-if="selected.uptime > 0">运行：{{ formatUptime(selected.uptime) }}</span>
            </div>
          </div>
          <div class="dh-right">
            <div class="range-tabs">
              <button v-for="r in ranges" :key="r.value" :class="{ active: range === r.value }" @click="changeRange(r.value)">{{ r.label }}</button>
            </div>
          </div>
        </div>

        <!-- 趋势图网格 -->
        <div class="chart-grid">
          <div class="trend-card" v-for="chart in trendCharts" :key="chart.key">
            <div class="tc-head">
              <span class="tc-label">{{ chart.label }}</span>
              <span class="tc-value" v-if="chart.current !== null" :class="chart.valueClass">{{ chart.currentText }}</span>
            </div>
            <div :ref="el => setTrendRef(el, chart.key)" class="tc-chart"></div>
          </div>
        </div>
      </div>
    </el-drawer>
    </template><!-- /有数据时的正常布局 -->
  </div>
</template>

<script setup>
import { ref, reactive, computed, onMounted, onUnmounted, nextTick, watch } from 'vue'
import { Search, Refresh } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import http from '../../api/http'
import { echarts, initChart, COLORS } from '../../charts/echarts'

// ---- 图标组件（内联 SVG，避免依赖 lucide） ----
const DatabaseIcon = { template: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5v6c0 1.66 4 3 9 3s9-1.34 9-3V5"/><path d="M3 11v6c0 1.66 4 3 9 3s9-1.34 9-3v-6"/></svg>' }
const CheckCircleIcon = { template: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><polyline points="9 12 12 15 16 10"/></svg>' }
const XCircleIcon = { template: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><line x1="15" y1="9" x2="9" y2="15"/><line x1="9" y1="9" x2="15" y2="15"/></svg>' }
const MemoryIcon = { template: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 19v-3"/><path d="M10 19v-3"/><path d="M14 19v-3"/><path d="M18 19v-3"/><path d="M8 11V9"/><path d="M16 11V9"/><path d="M12 11V9"/><path d="M2 15h20"/><path d="M2 7a2 2 0 0 1 2-2h16a2 2 0 0 1 2 2v1.1a2 2 0 0 0 0 3.837V17a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2v-5.063a2 2 0 0 0 0-3.837Z"/></svg>' }
const ConnectionIcon = { template: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M13.144 10.144a4 4 0 1 0-5.742 0"/><path d="M11 14.48V17"/><circle cx="11" cy="19" r="2"/><path d="M16 9a5 5 0 0 1 4.516 2.861"/><path d="M19.922 12.633a5 5 0 0 1-.39 4.155"/><circle cx="21" cy="18" r="2"/></svg>' }
const ActivityIcon = { template: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="22 12 18 12 15 21 9 3 6 12 2 12"/></svg>' }
const SearchIcon = Search
const RefreshIcon = Refresh

// ---- 数据 ----
const instances = ref([])
const loading = ref(false)
const filterStatus = ref('')
const filterTopology = ref('')
const searchText = ref('')
const refreshInterval = ref(30)
let refreshTimer = null

// Server 真实地址（取自 /api/v1/install-info，参考「添加主机」功能）
const serverURL = ref('')
// 主命令：节点上已有 agent-install.sh（agent 安装时已自拷贝到 /etc/monitor-agent/），
// 直接本地运行可进入交互式向导（stdin 为终端，能正常 read）。
const redisInstallCmd = computed(() => 'bash /etc/monitor-agent/agent-install.sh redis')
// 备选命令：节点没有本地脚本时，先下载到本地再以交互方式运行。
// 注意：不要用 curl ... | bash 管道方式（管道会占用 stdin，交互向导无法输入）。
const redisInstallCmdAlt = computed(() =>
  (serverURL.value
    ? `curl -fsSL ${serverURL.value}/install/agent-install.sh`
    : 'curl -fsSL http://<server>:8080/install/agent-install.sh') +
  ' -o /tmp/agent-install.sh && bash /tmp/agent-install.sh redis'
)
function loadServerURL() {
  http
    .get('/api/v1/install-info')
    .then((info) => { if (info && info.serverURL) serverURL.value = info.serverURL })
    .catch(() => {})
}

// 统计
const stats = reactive({ total: 0, up: 0, down: 0, totalMemory: 0, totalClients: 0, totalOps: 0 })

// 过滤
const filteredInstances = computed(() => {
  return instances.value.filter(i => {
    if (filterStatus.value === 'up' && !i.up) return false
    if (filterStatus.value === 'down' && i.up) return false
    if (filterTopology.value && i.topology !== filterTopology.value) return false
    if (searchText.value) {
      const s = searchText.value.toLowerCase()
      if (!i.instance.toLowerCase().includes(s) && !i.node.toLowerCase().includes(s)) return false
    }
    return true
  })
})

// ---- 图表引用 ----
const chartRefs = {}
const chartInstances = {}
const trendRefs = {}
const trendChartsMap = {}

function setChartRef(el, key) {
  if (el) chartRefs[key] = el
}
function setTrendRef(el, key) {
  if (el) trendRefs[key] = el
}

// ---- 加载数据 ----
async function loadInstances() {
  loading.value = true
  try {
    const data = await http.get('/api/v1/middleware/redis/instances')
    instances.value = data.instances || []
    computeStats()
    await nextTick()
    renderCharts()
  } catch (e) {
    console.error('加载 Redis 实例失败', e)
  } finally {
    loading.value = false
  }
}

function computeStats() {
  const list = instances.value
  stats.total = list.length
  stats.up = list.filter(i => i.up).length
  stats.down = list.filter(i => !i.up).length
  stats.totalMemory = list.reduce((s, i) => s + (i.usedMemory || 0), 0)
  stats.totalClients = list.reduce((s, i) => s + (i.clients || 0), 0)
  stats.totalOps = list.reduce((s, i) => s + (i.ops || 0), 0)
}

// ---- 渲染概览图表 ----
function renderCharts() {
  renderTopologyPie()
  renderRolePie()
  renderStatusPie()
  renderMemBar()
  renderOpsBar()
  renderHitRateBar()
}

function getOrCreate(key) {
  if (chartInstances[key]) {
    chartInstances[key].dispose()
  }
  if (!chartRefs[key]) return null
  chartInstances[key] = initChart(chartRefs[key])
  return chartInstances[key]
}

function renderTopologyPie() {
  const chart = getOrCreate('topologyPie')
  if (!chart) return
  const counts = {}
  instances.value.forEach(i => {
    const t = i.topology || 'unknown'
    counts[t] = (counts[t] || 0) + 1
  })
  const data = Object.entries(counts).map(([name, value]) => ({ name: topoLabel(name), value }))
  chart.setOption({
    tooltip: { trigger: 'item', backgroundColor: 'rgba(11,17,32,0.92)', borderColor: 'rgba(34,211,238,0.3)', textStyle: { color: '#e5edf7' } },
    legend: { bottom: 0, textStyle: { color: '#9fb3c8', fontSize: 11 }, itemWidth: 10, itemHeight: 6 },
    series: [{
      type: 'pie', radius: ['45%', '72%'], center: ['50%', '42%'],
      avoidLabelOverlap: true, itemStyle: { borderColor: '#0a0e14', borderWidth: 2 },
      label: { show: true, color: '#e5edf7', fontSize: 12, formatter: '{c}' },
      labelLine: { lineStyle: { color: '#9fb3c8' } },
      color: ['#22d3ee', '#3b82f6', '#a855f7', '#f59e0b', '#22c55e'],
      data,
    }],
  })
}

function renderRolePie() {
  const chart = getOrCreate('rolePie')
  if (!chart) return
  const counts = {}
  instances.value.forEach(i => {
    const r = i.role || 'unknown'
    counts[r] = (counts[r] || 0) + 1
  })
  const data = Object.entries(counts).map(([name, value]) => ({ name: roleLabel(name), value }))
  chart.setOption({
    tooltip: { trigger: 'item', backgroundColor: 'rgba(11,17,32,0.92)', borderColor: 'rgba(34,211,238,0.3)', textStyle: { color: '#e5edf7' } },
    legend: { bottom: 0, textStyle: { color: '#9fb3c8', fontSize: 11 }, itemWidth: 10, itemHeight: 6 },
    series: [{
      type: 'pie', radius: ['45%', '72%'], center: ['50%', '42%'],
      avoidLabelOverlap: true, itemStyle: { borderColor: '#0a0e14', borderWidth: 2 },
      label: { show: true, color: '#e5edf7', fontSize: 12, formatter: '{c}' },
      labelLine: { lineStyle: { color: '#9fb3c8' } },
      color: ['#dc382d', '#22c55e', '#f59e0b', '#6b7c93'],
      data,
    }],
  })
}

function renderStatusPie() {
  const chart = getOrCreate('statusPie')
  if (!chart) return
  const up = stats.up
  const down = stats.down
  chart.setOption({
    tooltip: { trigger: 'item', backgroundColor: 'rgba(11,17,32,0.92)', borderColor: 'rgba(34,211,238,0.3)', textStyle: { color: '#e5edf7' } },
    legend: { bottom: 0, textStyle: { color: '#9fb3c8', fontSize: 11 }, itemWidth: 10, itemHeight: 6 },
    series: [{
      type: 'pie', radius: ['45%', '72%'], center: ['50%', '42%'],
      itemStyle: { borderColor: '#0a0e14', borderWidth: 2 },
      label: { show: true, color: '#e5edf7', fontSize: 12, formatter: '{c}' },
      labelLine: { lineStyle: { color: '#9fb3c8' } },
      color: ['#22c55e', '#ef4444'],
      data: [{ name: '在线', value: up }, { name: '离线', value: down }],
    }],
  })
}

function renderMemBar() {
  const chart = getOrCreate('memBar')
  if (!chart) return
  const sorted = [...instances.value].filter(i => i.usedMemory > 0).sort((a, b) => b.usedMemory - a.usedMemory).slice(0, 10).reverse()
  chart.setOption({
    tooltip: {
      trigger: 'axis', axisPointer: { type: 'shadow' },
      backgroundColor: 'rgba(11,17,32,0.92)', borderColor: 'rgba(34,211,238,0.3)', textStyle: { color: '#e5edf7' },
      formatter: (p) => `${p[0].name}<br/>${formatBytes(p[0].value)}`,
    },
    grid: { left: 10, right: 60, top: 8, bottom: 8, containLabel: true },
    xAxis: { type: 'value', axisLabel: { color: '#9fb3c8', fontSize: 10, formatter: (v) => formatBytesShort(v) }, splitLine: { lineStyle: { color: 'rgba(34,211,238,0.08)' } } },
    yAxis: { type: 'category', data: sorted.map(i => i.name || i.instance), axisLabel: { color: '#9fb3c8', fontSize: 11, width: 120, overflow: 'truncate' }, axisLine: { lineStyle: { color: '#9fb3c8' } } },
    series: [{
      type: 'bar', data: sorted.map(i => i.usedMemory), barWidth: '55%',
      itemStyle: {
        borderRadius: [0, 4, 4, 0],
        color: (params) => {
          const val = params.value
          const max = sorted.length > 0 ? sorted[sorted.length - 1].usedMemory : 1
          const ratio = val / max
          if (ratio > 0.8) return '#ef4444'
          if (ratio > 0.6) return '#f59e0b'
          return '#22d3ee'
        },
      },
    }],
  })
}

function renderOpsBar() {
  const chart = getOrCreate('opsBar')
  if (!chart) return
  const sorted = [...instances.value].filter(i => i.ops > 0).sort((a, b) => b.ops - a.ops).slice(0, 10).reverse()
  chart.setOption({
    tooltip: {
      trigger: 'axis', axisPointer: { type: 'shadow' },
      backgroundColor: 'rgba(11,17,32,0.92)', borderColor: 'rgba(34,211,238,0.3)', textStyle: { color: '#e5edf7' },
      formatter: (p) => `${p[0].name}<br/>${formatNum(p[0].value)} ops/s`,
    },
    grid: { left: 10, right: 50, top: 8, bottom: 8, containLabel: true },
    xAxis: { type: 'value', axisLabel: { color: '#9fb3c8', fontSize: 10, formatter: (v) => formatNum(v) }, splitLine: { lineStyle: { color: 'rgba(34,211,238,0.08)' } } },
    yAxis: { type: 'category', data: sorted.map(i => i.name || i.instance), axisLabel: { color: '#9fb3c8', fontSize: 11, width: 120, overflow: 'truncate' }, axisLine: { lineStyle: { color: '#9fb3c8' } } },
    series: [{
      type: 'bar', data: sorted.map(i => i.ops), barWidth: '55%',
      itemStyle: { borderRadius: [0, 4, 4, 0], color: '#3b82f6' },
    }],
  })
}

function renderHitRateBar() {
  const chart = getOrCreate('hitRateBar')
  if (!chart) return
  const sorted = [...instances.value].filter(i => i.hitRate > 0).sort((a, b) => a.hitRate - b.hitRate)
  chart.setOption({
    tooltip: {
      trigger: 'axis', axisPointer: { type: 'shadow' },
      backgroundColor: 'rgba(11,17,32,0.92)', borderColor: 'rgba(34,211,238,0.3)', textStyle: { color: '#e5edf7' },
      formatter: (p) => `${p[0].name}<br/>命中率 ${p[0].value}%`,
    },
    grid: { left: 10, right: 30, top: 8, bottom: 8, containLabel: true },
    xAxis: { type: 'category', data: sorted.map(i => i.name || i.instance), axisLabel: { color: '#9fb3c8', fontSize: 10, interval: 0, rotate: sorted.length > 8 ? 30 : 0 }, axisLine: { lineStyle: { color: '#9fb3c8' } } },
    yAxis: { type: 'value', min: 0, max: 100, axisLabel: { color: '#9fb3c8', fontSize: 10, formatter: '{value}%' }, splitLine: { lineStyle: { color: 'rgba(34,211,238,0.08)' } } },
    series: [{
      type: 'bar', data: sorted.map(i => i.hitRate), barWidth: '40%',
      itemStyle: {
        borderRadius: [4, 4, 0, 0],
        color: (params) => {
          const v = params.value
          if (v < 50) return '#ef4444'
          if (v < 80) return '#f59e0b'
          return '#22c55e'
        },
      },
    }],
  })
}

// ---- 详情抽屉 ----
const detailVisible = ref(false)
const selected = ref(null)
const range = ref('1h')
const ranges = [
  { label: '近1小时', value: '1h' },
  { label: '今日', value: 'today' },
  { label: '昨日', value: 'yesterday' },
  { label: '近7天', value: '7d' },
  { label: '近30天', value: '30d' },
]

// 趋势图配置
const trendCharts = ref([])

function openDetail(row) {
  selected.value = row
  detailVisible.value = true
  buildTrendCharts(row)
  nextTick(() => loadTrendData())
}

function buildTrendCharts(row) {
  const list = [
    { key: 'mem', label: '内存使用率', metric: 'redis_used_memory_percent', unit: '%', color: COLORS.purple, current: row.memPercent, currentText: row.memPercent + '%' },
    { key: 'clients', label: '连接客户端数', metric: 'redis_connected_clients', unit: '', color: COLORS.blue, current: row.clients, currentText: formatNum(row.clients) },
    { key: 'ops', label: '命令速率(OPS)', metric: 'redis_ops_per_sec', unit: '', color: COLORS.cyan, current: row.ops, currentText: formatNum(row.ops) },
    { key: 'hitrate', label: '缓存命中率', metric: 'redis_hit_rate', unit: '%', color: COLORS.green, current: row.hitRate, currentText: row.hitRate + '%' },
    { key: 'keys', label: '键数量', metric: 'redis_keys', unit: '', color: COLORS.amber, current: row.keys, currentText: formatNum(row.keys) },
    { key: 'frag', label: '内存碎片率', metric: 'redis_memory_fragmentation_ratio', unit: '', color: '#a855f7', current: null, currentText: '-' },
    { key: 'evicted', label: '淘汰键数', metric: 'redis_evicted_keys', unit: '', color: COLORS.red, current: null, currentText: '-' },
    { key: 'uptime', label: '运行时长', metric: 'redis_uptime_in_seconds', unit: 's', color: '#6b7c93', current: row.uptime, currentText: formatUptime(row.uptime) },
  ]
  if (row.role === 'slave') {
    list.push({ key: 'lag', label: '复制延迟(秒)', metric: 'redis_replication_lag', unit: 's', color: COLORS.red, current: null, currentText: '-' })
  }
  trendCharts.value = list
}

function changeRange(r) {
  range.value = r
  if (detailVisible.value) loadTrendData()
}

function getRangeMs() {
  const now = Date.now()
  switch (range.value) {
    case '1h': return { start: now - 3600000, end: now, step: 60000 }
    case 'today': {
      const d = new Date()
      return { start: new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime(), end: now, step: 300000 }
    }
    case 'yesterday': {
      const d = new Date()
      const yStart = new Date(d.getFullYear(), d.getMonth(), d.getDate() - 1).getTime()
      return { start: yStart, end: yStart + 86400000, step: 300000 }
    }
    case '7d': return { start: now - 7 * 86400000, end: now, step: 1800000 }
    case '30d': return { start: now - 30 * 86400000, end: now, step: 3600000 }
    default: return { start: now - 3600000, end: now, step: 60000 }
  }
}

async function loadTrendData() {
  if (!selected.value) return
  const { start, end, step } = getRangeMs()
  for (const chart of trendCharts.value) {
    try {
      const data = await http.get(`/api/v1/query/range?node=${selected.value.node}&metric=${chart.metric}&labels.instance=${selected.value.instance}&start=${start}&end=${end}&step=${step}`)
      const series = data.series || []
      let points = []
      if (series.length > 0) {
        points = (series[0].points || []).map(p => [p.timestamp, p.value])
      }
      const c = getOrCreateTrend(chart.key)
      if (c) {
        c.setOption({
          grid: { left: 48, right: 14, top: 8, bottom: 22, containLabel: true },
          tooltip: { trigger: 'axis', backgroundColor: 'rgba(11,17,32,0.92)', borderColor: chart.color, textStyle: { color: '#e5edf7' } },
          xAxis: { type: 'time', axisLine: { lineStyle: { color: '#9fb3c8' } }, axisLabel: { color: '#9fb3c8', fontSize: 10, hideOverlap: true }, splitLine: { show: false } },
          yAxis: { type: 'value', min: 0, axisLabel: { color: '#9fb3c8', fontSize: 10 }, splitLine: { lineStyle: { color: 'rgba(34,211,238,0.08)' } } },
          series: [{
            type: 'line', smooth: true, showSymbol: false, data: points,
            lineStyle: { color: chart.color, width: 2, shadowColor: chart.color, shadowBlur: 8 },
            areaStyle: { color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [{ offset: 0, color: chart.color + '55' }, { offset: 1, color: chart.color + '03' }]) },
          }],
        })
      }
    } catch (e) {
      console.error('加载趋势数据失败', chart.key, e)
    }
  }
}

function getOrCreateTrend(key) {
  if (trendChartsMap[key]) trendChartsMap[key].dispose()
  if (!trendRefs[key]) return null
  trendChartsMap[key] = initChart(trendRefs[key])
  return trendChartsMap[key]
}

// ---- 刷新 ----
function onRefreshChange() {
  if (refreshTimer) clearInterval(refreshTimer)
  if (refreshInterval.value > 0) {
    refreshTimer = setInterval(loadInstances, refreshInterval.value * 1000)
  }
}

// ---- 格式化工具 ----
function formatBytes(b) {
  if (!b || b <= 0) return '0 B'
  if (b >= 1 << 30) return (b / (1 << 30)).toFixed(2) + ' GB'
  if (b >= 1 << 20) return (b / (1 << 20)).toFixed(2) + ' MB'
  if (b >= 1 << 10) return (b / (1 << 10)).toFixed(1) + ' KB'
  return b.toFixed(0) + ' B'
}
function formatBytesShort(b) {
  if (b >= 1 << 30) return (b / (1 << 30)).toFixed(0) + 'G'
  if (b >= 1 << 20) return (b / (1 << 20)).toFixed(0) + 'M'
  if (b >= 1 << 10) return (b / (1 << 10)).toFixed(0) + 'K'
  return b.toFixed(0) + 'B'
}
function formatNum(n) {
  if (!n) return '0'
  if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M'
  if (n >= 1000) return (n / 1000).toFixed(1) + 'K'
  return n.toFixed(0)
}
function formatUptime(s) {
  if (!s || s <= 0) return '-'
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  if (d > 0) return `${d}天${h}时`
  const m = Math.floor((s % 3600) / 60)
  if (h > 0) return `${h}时${m}分`
  return `${m}分`
}
function roleLabel(r) {
  return { master: 'Master', slave: 'Slave', sentinel: 'Sentinel', unknown: '未知' }[r] || r
}
function topoLabel(t) {
  return { standalone: '单机', replication: '主从', sentinel: '哨兵', cluster: '集群', unknown: '未知' }[t] || t
}
function memBarClass(p) {
  if (p > 80) return 'red'
  if (p > 60) return 'amber'
  return 'green'
}
function hitRateClass(r) {
  if (r < 50) return 'sev-critical'
  if (r < 80) return 'sev-warning'
  return 'ok-text'
}
function rowClass({ row }) {
  return row.up ? '' : 'row-down'
}

// 复制空状态引导命令到剪贴板
function copyCmd() {
  const cmd = redisInstallCmd.value
  if (navigator.clipboard) {
    navigator.clipboard.writeText(cmd).then(() => {
      ElMessage.success('命令已复制到剪贴板')
    }).catch(() => {})
  } else {
    // 降级：创建临时 textarea
    const ta = document.createElement('textarea')
    ta.value = cmd
    document.body.appendChild(ta)
    ta.select()
    try { document.execCommand('copy'); ElMessage.success('命令已复制到剪贴板') } catch (e) {}
    document.body.removeChild(ta)
  }
}

// ---- 生命周期 ----
onMounted(() => {
  loadInstances()
  loadServerURL()
  onRefreshChange()
  window.addEventListener('resize', handleResize)
})
onUnmounted(() => {
  if (refreshTimer) clearInterval(refreshTimer)
  window.removeEventListener('resize', handleResize)
  Object.values(chartInstances).forEach(c => c && c.dispose())
  Object.values(trendChartsMap).forEach(c => c && c.dispose())
})

function handleResize() {
  Object.values(chartInstances).forEach(c => c && c.resize())
  Object.values(trendChartsMap).forEach(c => c && c.resize())
}
</script>

<style scoped>
.redis-tab {
  padding: 16px;
}

/* 区块1：KPI 卡片 */
.kpi-row {
  display: grid;
  grid-template-columns: repeat(6, 1fr);
  gap: 12px;
  margin-bottom: 16px;
}
.kpi-card {
  border-radius: var(--radius);
  padding: 16px 14px;
  display: flex;
  align-items: center;
  gap: 12px;
  border: 1px solid var(--border);
  position: relative;
  overflow: hidden;
  transition: transform 0.2s, box-shadow 0.2s;
}
.kpi-card:hover {
  transform: translateY(-2px);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.3);
}
.kpi-card::before {
  content: '';
  position: absolute;
  top: 0; left: 0; right: 0;
  height: 2px;
  opacity: 0.8;
}
.gradient-total::before { background: linear-gradient(90deg, #22d3ee, #3b82f6); }
.gradient-up::before { background: linear-gradient(90deg, #22c55e, #16a34a); }
.gradient-down::before { background: linear-gradient(90deg, #ef4444, #dc2626); }
.gradient-mem::before { background: linear-gradient(90deg, #a855f7, #7c3aed); }
.gradient-conn::before { background: linear-gradient(90deg, #3b82f6, #2563eb); }
.gradient-ops::before { background: linear-gradient(90deg, #f59e0b, #d97706); }
.kpi-icon {
  width: 38px;
  height: 38px;
  border-radius: 10px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}
.kpi-icon svg { width: 20px; height: 20px; }
.gradient-total .kpi-icon { background: rgba(34, 211, 238, 0.15); color: #22d3ee; }
.gradient-up .kpi-icon { background: rgba(34, 197, 94, 0.15); color: #22c55e; }
.gradient-down .kpi-icon { background: rgba(239, 68, 68, 0.15); color: #ef4444; }
.gradient-mem .kpi-icon { background: rgba(168, 85, 247, 0.15); color: #a855f7; }
.gradient-conn .kpi-icon { background: rgba(59, 130, 246, 0.15); color: #3b82f6; }
.gradient-ops .kpi-icon { background: rgba(245, 158, 11, 0.15); color: #f59e0b; }
.kpi-num {
  font-size: 22px;
  font-weight: 700;
  font-family: var(--mono);
  letter-spacing: -0.02em;
  line-height: 1.2;
}
.kpi-text {
  font-size: 11px;
  color: var(--text-dim);
  margin-top: 2px;
}

/* 通用 section */
.chart-section {
  padding: 16px 18px;
  margin-bottom: 16px;
}
.section-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 14px;
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
.section-title.no-bar::before { display: none; }

/* 区块2：环形图 */
.pie-row {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 16px;
}
.pie-item {
  display: flex;
  flex-direction: column;
  align-items: center;
}
.pie-chart {
  width: 100%;
  height: 220px;
}
.pie-title {
  font-size: 12px;
  color: var(--text-dim);
  margin-top: -8px;
}

/* 区块3：柱状图 */
.bar-row {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 16px;
}
.bar-item {}
.bar-sub-title {
  font-size: 12px;
  color: var(--text-dim);
  margin-bottom: 8px;
}
.bar-chart {
  width: 100%;
  height: 280px;
}

/* 区块4：命中率 */
.hitrate-chart {
  width: 100%;
  height: 260px;
}

/* 区块5：表格 */
.table-section {
  padding: 16px 18px;
}
.table-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}
.toolbar-right {
  display: flex;
  gap: 8px;
  align-items: center;
}
.role-tag {
  display: inline-block;
  padding: 2px 10px;
  border-radius: 12px;
  font-size: 11px;
  font-weight: 600;
}
.role-tag.master { background: rgba(220, 56, 45, 0.15); color: #ff6b6b; }
.role-tag.slave { background: rgba(34, 197, 94, 0.15); color: #4ade80; }
.role-tag.sentinel { background: rgba(245, 158, 11, 0.15); color: #fbbf24; }
.role-tag.unknown { background: rgba(107, 124, 147, 0.15); color: #94a3b8; }
.topo-tag {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
  background: rgba(56, 189, 248, 0.12);
  color: var(--info);
}
.status-dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  margin-right: 6px;
  vertical-align: middle;
}
.status-dot.up {
  background: var(--accent);
  box-shadow: 0 0 6px var(--accent-glow);
}
.status-dot.down {
  background: var(--danger);
}
.status-dot.lg {
  width: 12px;
  height: 12px;
}
.mem-cell {
  display: flex;
  align-items: center;
  gap: 8px;
}
.mem-cell .bar {
  flex: 1;
  height: 5px;
  background: rgba(255, 255, 255, 0.06);
  border-radius: 3px;
  overflow: hidden;
}
.mem-cell .bar-fill {
  height: 100%;
  border-radius: 3px;
}
.bar-fill.green { background: var(--accent); }
.bar-fill.amber { background: var(--warn); }
.bar-fill.red { background: var(--danger); }
.mem-text {
  font-size: 12px;
  min-width: 60px;
}
.mem-pct {
  font-size: 11px;
  color: var(--text-dim);
  min-width: 36px;
  text-align: right;
}
:deep(.row-down) {
  opacity: 0.6;
}
:deep(.el-table) {
  cursor: pointer;
}

/* 区块6：详情抽屉 */
:deep(.detail-drawer .el-drawer__body) {
  padding: 0;
}
.detail-content {
  padding: 20px 24px;
  height: 100%;
  overflow-y: auto;
}
.detail-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 20px;
  padding-bottom: 16px;
  border-bottom: 1px solid var(--border);
}
.dh-title {
  font-size: 18px;
  font-weight: 700;
  display: flex;
  align-items: center;
  gap: 10px;
}
.dh-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  margin-top: 10px;
  font-size: 12px;
  color: var(--text-dim);
}
.meta-item {}
.chart-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 14px;
}
.trend-card {
  background: rgba(255, 255, 255, 0.02);
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 12px 14px;
}
.tc-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 6px;
}
.tc-label {
  font-size: 12px;
  color: var(--text-dim);
}
.tc-value {
  font-size: 16px;
  font-weight: 700;
  font-family: var(--mono);
}
.tc-chart {
  width: 100%;
  height: 160px;
}

/* 空状态引导 */
.empty-guide {
  text-align: center;
  padding: 60px 24px;
  margin-bottom: 16px;
}
.empty-icon {
  color: var(--text-dim);
  opacity: 0.4;
  margin-bottom: 16px;
}
.empty-title {
  font-size: 18px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 8px;
}
.empty-desc {
  font-size: 13px;
  color: var(--text-dim);
  margin-bottom: 20px;
  max-width: 600px;
  margin-left: auto;
  margin-right: auto;
  line-height: 1.6;
}
.empty-cmd {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  background: rgba(0, 0, 0, 0.3);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 10px 16px;
  margin-bottom: 16px;
  max-width: 100%;
  overflow-x: auto;
}
.empty-cmd code {
  font-family: var(--mono);
  font-size: 12px;
  color: var(--accent);
  white-space: nowrap;
}
.copy-btn {
  background: none;
  border: none;
  color: var(--text-dim);
  cursor: pointer;
  padding: 4px;
  border-radius: 4px;
  display: flex;
  align-items: center;
  transition: color 0.15s, background 0.15s;
}
.copy-btn:hover {
  color: var(--accent);
  background: rgba(34, 211, 238, 0.1);
}
.empty-hint {
  font-size: 12px;
  color: var(--text-muted);
  margin-top: 8px;
  line-height: 1.6;
}
.empty-hint code {
  font-family: var(--mono);
  font-size: 11px;
  color: var(--text-dim);
  background: rgba(255, 255, 255, 0.05);
  padding: 1px 6px;
  border-radius: 3px;
}

/* 响应式 */
@media (max-width: 1200px) {
  .kpi-row { grid-template-columns: repeat(3, 1fr); }
  .pie-row { grid-template-columns: 1fr; }
  .bar-row { grid-template-columns: 1fr; }
  .chart-grid { grid-template-columns: 1fr; }
}
</style>
