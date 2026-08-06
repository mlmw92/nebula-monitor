<template>
  <div class="explore">
    <div class="left">
      <div class="left-head">指标目录</div>
      <el-input v-model="kw" placeholder="搜索指标名/中文名" size="small" clearable class="kw" />
      <el-tree
        :data="treeData"
        :props="{ label: 'label', children: 'children' }"
        node-key="key"
        highlight-current
        @node-click="onNodeClick"
        default-expand-all
      />
    </div>
    <div class="right">
      <div v-if="!selected" class="empty">从左侧选择指标查看趋势</div>
      <template v-else>
        <div class="right-head">
          <div>
            <div class="m-title">{{ selected.title }} <span class="m-name">{{ selected.name }}</span></div>
            <div class="m-meta">分类：{{ selected.category }} ｜ 单位：{{ selected.unit || '-' }} ｜ 推荐图表：{{ selected.chart }}</div>
          </div>
          <div class="right-actions">
            <el-button size="small" @click="onExport">导出 CSV</el-button>
            <el-button size="small" type="primary" @click="addToDash">加入仪表盘</el-button>
          </div>
        </div>
        <div ref="chartEl" class="chart"></div>
      </template>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onBeforeUnmount, nextTick } from 'vue'
import { ElMessage } from 'element-plus'
import { initChart, monitorOption, COLORS } from '../../charts/echarts'
import http from '../../api/http'
import { useDashboards } from '../../composables/useDashboards'

const kw = ref('')
const catalog = ref({})
const categories = ref([])
const selected = ref(null)
const chartEl = ref(null)
let chart = null
const dash = useDashboards()

const treeData = computed(() => {
  const list = []
  const cats = categories.value
  for (const cat of cats) {
    const items = (catalog.value[cat] || []).filter((m) => {
      if (!kw.value) return true
      const k = kw.value.toLowerCase()
      return m.name.toLowerCase().includes(k) || (m.title || '').toLowerCase().includes(k)
    })
    if (!items.length) continue
    list.push({
      key: 'cat:' + cat,
      label: cat,
      children: items.map((m) => ({ key: 'm:' + m.name, label: `${m.title} (${m.name})`, meta: m })),
    })
  }
  return list
})

function onNodeClick(node) {
  if (node.meta) {
    selected.value = node.meta
    nextTick(() => renderChart())
  }
}

function rangeBounds(r) {
  const now = Date.now()
  switch (r || '1h') {
    case '6h': return { start: now - 6 * 3600000, end: now, step: 60000 }
    case '24h': return { start: now - 24 * 3600000, end: now, step: 300000 }
    case '7d': return { start: now - 7 * 86400000, end: now, step: 1800000 }
    default: return { start: now - 3600000, end: now, step: 60000 }
  }
}

async function renderChart() {
  if (!selected.value) return
  await nextTick()
  if (!chart) chart = initChart(chartEl.value)
  const { start, end, step } = rangeBounds('1h')
  try {
    const d = await http.get(`/api/v1/query/range?metric=${encodeURIComponent(selected.value.name)}&start=${start}&end=${end}&step=${step}`)
    const series = d.series || d.data || []
    const data = {}
    ;(series || []).forEach((s) => {
      const label = s.labels && s.labels.instance ? `${selected.value.name}·${s.labels.instance}` : selected.value.name
      data[label] = (s.points || []).map((p) => [p.timestamp, p.value])
    })
    chart.setOption({
      series: Object.keys(data).map((n) => ({
        name: n, data: data[n] || [], type: 'line', smooth: true, showSymbol: false,
        lineStyle: { color: COLORS.cyan, width: 2 }, areaStyle: { color: 'rgba(34,211,238,0.12)' },
      })),
    })
  } catch (e) {
    ElMessage.error('查询失败：' + (e.message || e))
  }
}

function onExport() {
  if (!selected.value) return
  const { start, end } = rangeBounds('1h')
  http.exportMetricCSV({ metric: selected.value.name, start, end, step: 60000, filename: `metric_${selected.value.name}.csv` })
    .catch((e) => ElMessage.error('导出失败：' + (e.message || e)))
}

async function addToDash() {
  if (!selected.value) return
  const name = '我的看板'
  // 尝试找一个同名看板，否则新建
  const list = await dash.load(true)
  const exist = (list || []).find((d) => d.name === name)
  const panel = {
    title: selected.value.title,
    chartType: selected.value.chart || 'line',
    metric: selected.value.name,
    range: '1h',
    step: 0,
    labels: {},
  }
  if (exist) {
    const panels = (exist.panels || []).concat(panel)
    await dash.update(exist.id, exist.name, panels)
    ElMessage.success('已添加到「' + name + '」')
  } else {
    await dash.create(name, [panel])
    ElMessage.success('已创建「' + name + '」并加入指标')
  }
}

function resize() { chart && chart.resize() }
onMounted(async () => {
  const d = await http.metricCatalog()
  catalog.value = d.catalog || {}
  categories.value = d.categories || []
  window.addEventListener('resize', resize)
})
onBeforeUnmount(() => {
  window.removeEventListener('resize', resize)
  chart && chart.dispose()
})
</script>

<style scoped>
.explore { display: flex; height: calc(100vh - 140px); }
.left { width: 320px; border-right: 1px solid rgba(34,211,238,0.12); padding: 12px; overflow: auto; }
.left-head { font-size: 15px; font-weight: 700; color: #e5edf7; margin-bottom: 8px; }
.kw { margin-bottom: 8px; }
.right { flex: 1; padding: 16px; }
.empty { color: #64748b; margin-top: 40px; text-align: center; }
.right-head { display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 10px; }
.m-title { font-size: 16px; font-weight: 700; color: #e5edf7; }
.m-name { font-size: 12px; color: #64748b; font-weight: 400; }
.m-meta { font-size: 12px; color: #94a3b8; margin-top: 4px; }
.chart { height: calc(100% - 70px); min-height: 320px; background: rgba(15,23,42,0.4); border-radius: 10px; }
</style>
