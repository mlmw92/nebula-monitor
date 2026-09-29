<template>
  <div class="builtin-tab">
    <!-- 统计条 -->
    <div class="stat-row glass">
      <div class="stat-item">
        <span class="stat-num">{{ stats.total }}</span>
        <span class="stat-label">实例总数</span>
      </div>
      <div class="stat-item ok">
        <span class="stat-num">{{ stats.up }}</span>
        <span class="stat-label">在线</span>
      </div>
      <div class="stat-item bad">
        <span class="stat-num">{{ stats.down }}</span>
        <span class="stat-label">离线</span>
      </div>
      <el-button size="small" :icon="Refresh" circle style="margin-left: auto" @click="load" :loading="loading" />
    </div>

    <!-- 实例列表 -->
    <div class="mw-list glass">
      <el-table :data="instances" size="small" @row-click="openDetail" row-class-name="clickable">
        <el-table-column label="实例" min-width="170">
          <template #default="{ row }">
            <span class="dot" :class="row.up ? 'up' : 'down'"></span>
            <span class="mono">{{ row.instance }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="name" label="名称" min-width="110" />
        <el-table-column prop="node" label="节点" min-width="140" />
        <el-table-column
          v-for="col in spec.columns"
          :key="col.key"
          :label="col.label"
          min-width="110"
        >
          <template #default="{ row }">{{ metricValue(row, col.key) }}</template>
        </el-table-column>
        <el-table-column label="状态" width="80">
          <template #default="{ row }">
            <el-tag :type="row.up ? 'success' : 'danger'" size="small">{{ row.up ? '在线' : '离线' }}</el-tag>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 详情抽屉 -->
    <el-drawer v-model="drawer" :title="`实例 ${selected?.instance || ''}`" size="45%">
      <template v-if="selected">
        <div class="meta-grid">
          <div><span class="meta-k">节点</span>{{ selected.node }}</div>
          <div><span class="meta-k">名称</span>{{ selected.name }}</div>
          <div v-if="selected.role"><span class="meta-k">角色</span>{{ selected.role }}</div>
          <div v-if="selected.version"><span class="meta-k">版本</span>{{ selected.version }}</div>
          <div><span class="meta-k">状态</span>{{ selected.up ? '在线' : '离线' }}</div>
        </div>
        <div class="drawer-metrics" v-if="(selected.metrics || []).length">
          <div v-for="m in selected.metrics" :key="m.key" class="drawer-metric">
            <span class="meta-k">{{ m.label }}</span><b>{{ m.value }}</b>
          </div>
        </div>
        <div v-for="t in spec.trends" :key="t.metric" class="trend-block">
          <div class="trend-title">{{ t.name }}（最近 1 小时）</div>
          <div :ref="(el) => setChartRef(el, t.metric)" class="trend-chart"></div>
        </div>
      </template>
    </el-drawer>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onBeforeUnmount, nextTick } from 'vue'
import * as echarts from 'echarts'
import { Refresh } from '@element-plus/icons-vue'
import http from '../../api/http'
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
const stats = computed(() => {
  const up = instances.value.filter((i) => i.up).length
  return { total: instances.value.length, up, down: instances.value.length - up }
})

function metricValue(row, key) {
  const hit = (row.metrics || []).find((m) => m.key === key)
  return hit ? hit.value : '-'
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
.builtin-tab { padding: 14px; }
.stat-row {
  display: flex;
  gap: 28px;
  align-items: center;
  padding: 12px 18px;
  margin-bottom: 14px;
  border-radius: var(--radius);
}
.stat-item { display: flex; flex-direction: column; align-items: center; }
.stat-num { font-size: 22px; font-weight: 700; }
.stat-item.ok .stat-num { color: var(--ok, #4ade80); }
.stat-item.bad .stat-num { color: var(--danger, #f87171); }
.stat-label { font-size: 12px; color: var(--text-dim); }
.mw-list { border-radius: var(--radius); padding: 8px; }
.dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 8px; }
.dot.up { background: #4ade80; box-shadow: 0 0 6px rgba(74, 222, 128, 0.5); }
.dot.down { background: #f87171; }
.mono { font-family: 'JetBrains Mono', ui-monospace, monospace; }
:deep(.clickable) { cursor: pointer; }
.meta-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(180px, 1fr)); gap: 10px; margin-bottom: 14px; }
.meta-grid > div { background: rgba(255, 255, 255, 0.04); border-radius: 8px; padding: 8px 10px; font-size: 13px; }
.meta-k { color: var(--text-dim); margin-right: 8px; font-size: 12px; }
.drawer-metrics { display: grid; grid-template-columns: repeat(auto-fill, minmax(170px, 1fr)); gap: 8px; margin-bottom: 16px; }
.drawer-metric { background: rgba(255, 255, 255, 0.04); border-radius: 8px; padding: 8px 10px; font-size: 13px; }
.trend-block { margin-bottom: 18px; }
.trend-title { font-size: 13px; color: var(--text-dim); margin-bottom: 6px; }
.trend-chart { height: 180px; }
</style>
