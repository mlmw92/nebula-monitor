<template>
  <div class="mw-tab">
    <RefreshBar :loading="loading" @refresh="load" />
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
        <KpiCard label="实例总数" :value="stats.total" tone="total" />
        <KpiCard label="在线实例" :value="stats.up" tone="up" />
        <KpiCard label="离线实例" :value="stats.down" tone="down" />
        <KpiCard label="总 Broker 数" :value="stats.totalBrokers" tone="cluster" />
        <KpiCard label="总 Topic 数" :value="stats.totalTopics" tone="ops" />
        <KpiCard label="总消费延迟" :value="formatNum(stats.totalLag)" tone="alert" />
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

      <div class="chart-section glass">
        <div class="section-title">实例列表</div>
        <el-table :data="pagedInstances" style="width: 100%" @row-click="openDetail" :row-class-name="rowClass" size="small" stripe @sort-change="onSortChange">
          <el-table-column prop="instance" label="Broker 地址" min-width="160" show-overflow-tooltip />
          <el-table-column prop="name" label="名称" min-width="100" show-overflow-tooltip />
          <el-table-column prop="role" label="角色" width="80">
            <template #default="{ row }"><el-tag size="small">{{ row.role }}</el-tag></template>
          </el-table-column>
          <el-table-column prop="version" label="版本" width="100" />
          <el-table-column label="状态" width="90">
            <template #default="{ row }"><span class="status-text"><span class="status-dot" :class="row.up ? 'up' : 'down'"></span>{{ row.up ? '在线' : '离线' }}</span></template>
          </el-table-column>
          <el-table-column prop="brokerCount" label="Broker 数" width="90" sortable />
          <el-table-column prop="topicCount" label="Topic 数" width="90" sortable />
          <el-table-column prop="partitionCount" label="分区数" width="80" sortable />
          <el-table-column prop="underReplicatedPartitions" label="未复制分区" width="110" sortable />
          <el-table-column prop="consumerGroupCount" label="消费组" width="90" sortable />
          <el-table-column prop="consumerLag" label="消费延迟" width="100" sortable />
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
import { ref, onMounted, computed, nextTick, watch } from 'vue'
import * as echarts from 'echarts'
import http from '../../api/http'
import RefreshBar from '../RefreshBar.vue'
import KpiCard from '../KpiCard.vue'

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
