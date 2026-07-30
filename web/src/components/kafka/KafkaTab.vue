<template>
  <div class="mw-tab">
    <div v-if="!loading && instances.length === 0" class="empty-guide glass">
      <div class="empty-icon">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="64" height="64">
          <circle cx="12" cy="12" r="10"/>
          <path d="M12 2v20M2 12h20"/>
        </svg>
      </div>
      <h2 class="empty-title">尚未配置 Kafka 监控</h2>
      <p class="empty-desc">当前没有已采集的 Kafka 实例。请在运行 Agent 的节点上配置 Kafka Broker 地址。</p>
      <p class="empty-hint">配置完成后约 15-30 秒数据将出现在此页面。</p>
    </div>

    <template v-if="instances.length > 0">
      <div class="kpi-row">
        <div class="kpi-card gradient-total"><div class="kpi-body"><div class="kpi-num">{{ stats.total }}</div><div class="kpi-text">实例总数</div></div></div>
        <div class="kpi-card gradient-up"><div class="kpi-body"><div class="kpi-num">{{ stats.up }}</div><div class="kpi-text">在线实例</div></div></div>
        <div class="kpi-card gradient-down"><div class="kpi-body"><div class="kpi-num">{{ stats.down }}</div><div class="kpi-text">离线实例</div></div></div>
        <div class="kpi-card gradient-conn"><div class="kpi-body"><div class="kpi-num">{{ stats.totalBrokers }}</div><div class="kpi-text">总 Broker 数</div></div></div>
        <div class="kpi-card gradient-ops"><div class="kpi-body"><div class="kpi-num">{{ stats.totalTopics }}</div><div class="kpi-text">总 Topic 数</div></div></div>
        <div class="kpi-card gradient-mem"><div class="kpi-body"><div class="kpi-num">{{ formatNum(stats.totalLag) }}</div><div class="kpi-text">总消费延迟</div></div></div>
      </div>

      <div class="chart-section glass">
        <div class="section-title">实例列表</div>
        <el-table :data="instances" style="width: 100%" @row-click="openDetail" :row-class-name="rowClass">
          <el-table-column prop="instance" label="Broker 地址" min-width="160" />
          <el-table-column prop="name" label="名称" min-width="100" />
          <el-table-column prop="role" label="角色" width="80">
            <template #default="{ row }"><el-tag size="small">{{ row.role }}</el-tag></template>
          </el-table-column>
          <el-table-column prop="version" label="版本" width="100" />
          <el-table-column label="状态" width="80">
            <template #default="{ row }"><span :class="['dot', row.up ? 'up' : 'down']"></span>{{ row.up ? '在线' : '离线' }}</template>
          </el-table-column>
          <el-table-column prop="brokerCount" label="Broker 数" width="90" sortable />
          <el-table-column prop="topicCount" label="Topic 数" width="90" sortable />
          <el-table-column prop="partitionCount" label="分区数" width="80" sortable />
          <el-table-column prop="underReplicatedPartitions" label="未复制分区" width="110" sortable />
          <el-table-column prop="consumerGroupCount" label="消费组" width="90" sortable />
          <el-table-column prop="consumerLag" label="消费延迟" width="100" sortable />
        </el-table>
      </div>
    </template>

    <el-drawer v-model="drawerVisible" :title="detailTitle" size="50%" :destroy-on-close="true">
      <div v-if="selected" class="detail-content">
        <div class="detail-meta">
          <div class="meta-item"><span class="meta-label">实例</span><span class="mono">{{ selected.instance }}</span></div>
          <div class="meta-item"><span class="meta-label">节点</span>{{ selected.node }}</div>
          <div class="meta-item"><span class="meta-label">角色</span>{{ selected.role }}</div>
          <div class="meta-item"><span class="meta-label">版本</span>{{ selected.version }}</div>
          <div class="meta-item"><span class="meta-label">分组</span>{{ selected.group }}</div>
        </div>
        <div class="metric-grid">
          <div class="metric-cell"><div class="mc-label">Broker 数</div><div class="mc-value">{{ selected.brokerCount }}</div></div>
          <div class="metric-cell"><div class="mc-label">Topic 数</div><div class="mc-value">{{ selected.topicCount }}</div></div>
          <div class="metric-cell"><div class="mc-label">分区数</div><div class="mc-value">{{ selected.partitionCount }}</div></div>
          <div class="metric-cell"><div class="mc-label">未复制分区</div><div class="mc-value" :class="{'metric-bad': selected.underReplicatedPartitions > 0}">{{ selected.underReplicatedPartitions }}</div></div>
          <div class="metric-cell"><div class="mc-label">离线分区</div><div class="mc-value" :class="{'metric-bad': selected.offlinePartitions > 0}">{{ selected.offlinePartitions }}</div></div>
          <div class="metric-cell"><div class="mc-label">消费组数</div><div class="mc-value">{{ selected.consumerGroupCount }}</div></div>
          <div class="metric-cell"><div class="mc-label">消费延迟</div><div class="mc-value">{{ formatNum(selected.consumerLag) }}</div></div>
          <div class="metric-cell"><div class="mc-label">最大延迟</div><div class="mc-value">{{ formatNum(selected.consumerLagMax) }}</div></div>
          <div class="metric-cell"><div class="mc-label">控制器数</div><div class="mc-value">{{ selected.activeControllerCount }}</div></div>
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
const instances = ref([])
const drawerVisible = ref(false)
const selected = ref(null)
const chartRef = ref(null)
let chartInstance = null

const stats = computed(() => {
  const s = { total: 0, up: 0, down: 0, totalBrokers: 0, totalTopics: 0, totalLag: 0 }
  for (const i of instances.value) {
    s.total++
    if (i.up) s.up++; else s.down++
    s.totalBrokers += i.brokerCount || 0
    s.totalTopics += i.topicCount || 0
    s.totalLag += i.consumerLag || 0
  }
  return s
})

const detailTitle = computed(() => selected.value ? `Kafka 详情 - ${selected.value.name || selected.value.instance}` : '详情')

async function load() {
  loading.value = true
  try {
    const data = await http.get('/api/v1/middleware/kafka/instances')
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
    const lagData = await http.get(`/api/v1/query/range?node=${row.node}&metric=kafka_consumer_lag&start=${start}&end=${end}&step=60`)
    const series = []
    if (lagData.series) for (const s of lagData.series) {
      if (s.labels?.instance === row.instance) series.push({ name: '消费延迟', type: 'line', data: s.points.map(p => [p.timestamp, p.value]), smooth: true, areaStyle: { opacity: 0.2 } })
    }
    chartInstance.setOption({
      tooltip: { trigger: 'axis' },
      legend: { data: ['消费延迟'], textStyle: { color: '#8b949e' } },
      grid: { left: 60, right: 30, top: 40, bottom: 30 },
      xAxis: { type: 'time' },
      yAxis: { type: 'value', name: '延迟' },
      series,
    })
  } catch (e) { console.error(e) }
}

function formatNum(n) { return n != null ? Number(n).toLocaleString() : '-' }
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
.dot.up { background: #3fb950; }
.dot.down { background: #dc382d; }
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
