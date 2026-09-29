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
      <h2 class="empty-title">尚未采集到 {{ spec.label }} 实例</h2>
      <p class="empty-desc">请在运行 Agent 的节点上配置 {{ spec.label }} 地址（agent.yaml 对应实例段），并确认服务可达。</p>
      <p class="empty-hint">配置完成后约 15-30 秒数据将出现在此页面。</p>
    </div>

    <template v-if="instances.length > 0">
      <!-- KPI 卡片行 -->
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
        <KpiCard v-for="k in spec.kpis" :key="k.metric + k.agg" :label="k.label" :value="formatNum(kpiValue(k))" :tone="k.tone || 'ops'">
          <template #icon><el-icon :size="20"><DataLine /></el-icon></template>
        </KpiCard>
      </div>

      <!-- 实例列表 -->
      <div class="mw-list glass">
        <div class="mw-list-title">实例列表</div>
        <el-table :data="pagedInstances" size="small" stripe @row-click="openDetail" :row-class-name="rowClass">
          <el-table-column label="实例" min-width="170" show-overflow-tooltip>
            <template #default="{ row }">
              <span class="dot" :class="row.up ? 'up' : 'down'"></span>
              <span class="mono">{{ row.instance }}</span>
            </template>
          </el-table-column>
          <el-table-column prop="name" label="名称" min-width="110" show-overflow-tooltip />
          <el-table-column prop="node" label="节点" min-width="140" show-overflow-tooltip />
          <el-table-column
            v-for="col in spec.columns"
            :key="col.key"
            :label="col.label"
            min-width="110"
            sortable
            :sort-method="(a, b) => sortByMetric(a, b, col.key)"
          >
            <template #default="{ row }">{{ metricValue(row, col.key) }}</template>
          </el-table-column>
          <el-table-column label="状态" min-width="80">
            <template #default="{ row }">
              <MwStatusDot :status="row.up ? 'normal' : 'abnormal'" :label="row.up ? '正常' : '离线'" />
            </template>
          </el-table-column>
        </el-table>
        <div class="pager">
          <el-pagination background layout="total, sizes, prev, pager, next, jumper" :total="instances.length" :page-size="pageSize" :current-page="currentPage" :page-sizes="[10, 20, 50, 100]" @current-change="v => currentPage = v" @size-change="v => { pageSize = v; currentPage = 1 }" />
        </div>
      </div>
    </template>

    <!-- 详情抽屉 -->
    <el-drawer v-model="drawer" :title="`实例 ${selected?.instance || ''}`" size="45%" :destroy-on-close="true">
      <div v-if="selected" class="detail-content">
        <div class="detail-meta">
          <div class="meta-item"><span class="meta-label">实例</span><span class="mono">{{ selected.instance }}</span></div>
          <div class="meta-item"><span class="meta-label">节点</span>{{ selected.node }}</div>
          <div class="meta-item" v-if="selected.name"><span class="meta-label">名称</span>{{ selected.name }}</div>
          <div class="meta-item" v-if="selected.role"><span class="meta-label">角色</span>{{ selected.role }}</div>
          <div class="meta-item" v-if="selected.version"><span class="meta-label">版本</span>{{ selected.version }}</div>
          <div class="meta-item"><span class="meta-label">状态</span>{{ selected.up ? '在线' : '离线' }}</div>
        </div>
        <div class="metric-grid">
          <div v-for="m in selected.metrics" :key="m.key" class="metric-cell">
            <div class="mc-label">{{ m.label }}</div>
            <div class="mc-value">{{ m.value }}<small v-if="m.unit"> {{ m.unit }}</small></div>
          </div>
        </div>
        <div v-for="t in spec.trends" :key="t.metric" class="trend-block">
          <div class="trend-title">{{ t.name }}（最近 1 小时）</div>
          <div :ref="(el) => setChartRef(el, t.metric)" class="trend-chart"></div>
        </div>
      </div>
    </el-drawer>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onBeforeUnmount, nextTick } from 'vue'
import * as echarts from 'echarts'
import { Grid, CircleCheck, CircleClose, DataLine } from '@element-plus/icons-vue'
import http from '../../api/http'
import RefreshBar from '../RefreshBar.vue'
import KpiCard from '../KpiCard.vue'
import MwStatusDot from './MwStatusDot.vue'
import './mw.css'
import { builtinSpec } from './builtinSpecs'

const props = defineProps({
  type: { type: String, required: true },
})

const spec = computed(() => builtinSpec(props.type))
const instances = ref([])
const loading = ref(false)
const drawer = ref(false)
const selected = ref(null)
const currentPage = ref(1)
const pageSize = ref(20)

const stats = computed(() => {
  const up = instances.value.filter((i) => i.up).length
  return { total: instances.value.length, up, down: instances.value.length - up }
})

const pagedInstances = computed(() => {
  const s = (currentPage.value - 1) * pageSize.value
  return instances.value.slice(s, s + pageSize.value)
})

function metricOf(row, key) {
  const hit = (row.metrics || []).find((m) => m.key === key)
  return hit ? hit.value : null
}
function metricValue(row, key) {
  const v = metricOf(row, key)
  return v === null ? '-' : v
}
function sortByMetric(a, b, key) {
  const va = metricOf(a, key), vb = metricOf(b, key)
  return (va ?? 0) - (vb ?? 0)
}
function formatNum(v) {
  if (v === null || v === undefined) return '-'
  if (typeof v !== 'number') return v
  return v.toLocaleString('zh-CN', { maximumFractionDigits: 2 })
}
function kpiValue(k) {
  const vals = instances.value.map((i) => metricOf(i, k.metric)).filter((v) => v !== null)
  if (!vals.length) return '-'
  if (k.agg === 'max') return Math.max(...vals)
  if (k.agg === 'avg') return vals.reduce((s, v) => s + v, 0) / vals.length
  return vals.reduce((s, v) => s + v, 0)
}
function rowClass({ row }) {
  return row.up ? '' : 'row-down'
}

async function load() {
  loading.value = true
  try {
    const data = await http.get(`/api/v1/middleware/${encodeURIComponent(props.type)}/instances`)
    instances.value = data.instances || []
  } catch (e) {
    console.error('加载实例失败', e)
  } finally {
    loading.value = false
  }
}

// ---- 详情抽屉趋势图 ----
const chartRefs = {}
let charts = []
function setChartRef(el, key) {
  if (el) chartRefs[key] = el
}
function openDetail(row) {
  selected.value = row
  drawer.value = true
  nextTick(() => renderTrends(row))
}
async function renderTrends(row) {
  charts.forEach((c) => c.dispose())
  charts = []
  const end = Date.now()
  const start = end - 3600 * 1000
  for (const t of spec.value.trends) {
    const el = chartRefs[t.metric]
    if (!el) continue
    const chart = echarts.init(el)
    charts.push(chart)
    try {
      const data = await http.get(
        `/api/v1/query/range?node=${encodeURIComponent(row.node)}&metric=${encodeURIComponent(t.metric)}&start=${start}&end=${end}&step=60`
      )
      const series = []
      for (const s of data.series || []) {
        if (s.labels?.instance !== row.instance) continue
        series.push({
          name: t.name,
          type: 'line',
          data: (s.points || []).map((p) => [p.timestamp, p.value]),
          smooth: true,
          showSymbol: false,
          areaStyle: { opacity: 0.15 },
        })
      }
      chart.setOption({
        tooltip: { trigger: 'axis' },
        grid: { left: 55, right: 20, top: 20, bottom: 28 },
        xAxis: { type: 'time' },
        yAxis: { type: 'value' },
        series,
      })
    } catch (e) {
      console.error('趋势查询失败', e)
    }
  }
}

let timer = null
onMounted(() => {
  load()
  timer = setInterval(load, 30000)
})
onBeforeUnmount(() => {
  clearInterval(timer)
  charts.forEach((c) => c.dispose())
})
</script>

<style scoped>
.kpi-row { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 12px; margin-bottom: 14px; }
.mw-list { border-radius: var(--radius); padding: 12px; }
.mw-list-title { font-size: 14px; font-weight: 600; margin-bottom: 10px; }
.pager { margin-top: 10px; display: flex; justify-content: flex-end; }
.dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 8px; }
.dot.up { background: #4ade80; box-shadow: 0 0 6px rgba(74, 222, 128, 0.5); }
.dot.down { background: #f87171; }
.mono { font-family: 'JetBrains Mono', ui-monospace, monospace; }
:deep(.clickable) { cursor: pointer; }
:deep(.row-down) { opacity: 0.55; }
.detail-meta { display: grid; grid-template-columns: repeat(auto-fill, minmax(180px, 1fr)); gap: 10px; margin-bottom: 14px; }
.meta-item { background: rgba(255, 255, 255, 0.04); border-radius: 8px; padding: 8px 10px; font-size: 13px; }
.meta-label { color: var(--text-dim); margin-right: 8px; font-size: 12px; }
.metric-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(160px, 1fr)); gap: 10px; margin-bottom: 16px; }
.metric-cell { background: rgba(255, 255, 255, 0.04); border-radius: 8px; padding: 10px 12px; }
.mc-label { font-size: 12px; color: var(--text-dim); margin-bottom: 4px; }
.mc-value { font-size: 18px; font-weight: 600; font-family: 'JetBrains Mono', ui-monospace, monospace; }
.trend-block { margin-bottom: 18px; }
.trend-title { font-size: 13px; color: var(--text-dim); margin-bottom: 6px; }
.trend-chart { height: 180px; }
</style>
