<template>
  <div class="explore">
    <div class="left">
      <div class="left-head-row">
        <div class="left-head">指标目录</div>
        <span v-if="activeLoaded" class="discover-count">{{ activeCount }}/{{ metricCount }} 有数据</span>
        <span v-else class="discover-count muted">检测中…</span>
      </div>
      <el-input v-model="kw" placeholder="搜索指标名/中文名" size="small" clearable class="kw" />
      <el-tree
        :data="treeData"
        :props="{ label: 'label', children: 'children' }"
        node-key="key"
        highlight-current
        @node-click="onNodeClick"
        default-expand-all
      >
        <template #default="{ data }">
          <span class="metric-tree-node">
            <span>{{ data.label }}</span>
            <span v-if="data.meta" :class="['metric-state', data.meta.active ? 'online' : 'offline']">
              {{ data.meta.active ? '有数据' : '暂无数据' }}
            </span>
          </span>
        </template>
      </el-tree>
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
import http, { get as httpGet, metricCatalog, metricActive } from '../../api/http'
import { useDashboards } from '../../composables/useDashboards'

const kw = ref('')
const catalog = ref({})
const categories = ref([])
const activeMap = ref({})
const activeLoaded = ref(false)
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
      children: items.map((m) => ({
        key: 'm:' + m.name,
        label: `${m.title} (${m.name})`,
        meta: { ...m, active: activeMap.value[m.name] === true },
      })),
    })
  }
  return list
})

const metricCount = computed(() => Object.values(catalog.value).reduce((n, list) => n + (list || []).length, 0))
const activeCount = computed(() => Object.values(activeMap.value).filter(Boolean).length)

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
  if (!chartEl.value) {
    ElMessage.error('图表容器未准备好，请稍后重试')
    return
  }
  try {
    if (!chart || (typeof chart.isDisposed === 'function' && chart.isDisposed())) {
      chart = initChart(chartEl.value)
    }
  } catch (e) {
    ElMessage.error('图表初始化失败：' + (e.message || e))
    return
  }
  const { start, end, step } = rangeBounds('1h')
  let d
  try {
    d = await httpGet(`/api/v1/query/range?metric=${encodeURIComponent(selected.value.name)}&start=${start}&end=${end}&step=${step}`)
  } catch (e) {
    ElMessage.error('查询失败：' + (e.message || e))
    return
  }

  try {
    const data = (d.series || d.data || []).map((s, index) => {
      const labels = s.labels || {}
      const suffix = labels.instance || labels.node || labels.host || `序列 ${index + 1}`
      return {
        name: `${selected.value.name}·${suffix}`,
        data: (s.points || []).map((p) => [Number(p.timestamp), Number(p.value)]),
        color: COLORS.cyan,
      }
    })
    chart.clear()
    chart.setOption(monitorOption({
      xMin: start,
      xMax: end,
      series: data,
      area: true,
    }), true)
  } catch (e) {
    ElMessage.error('图表渲染失败：' + (e.message || e))
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
  try {
    const d = await metricCatalog()
    catalog.value = d.catalog || {}
    categories.value = d.categories || []
    try {
      const a = await metricActive()
      activeMap.value = Object.fromEntries((a.items || []).map((item) => [item.name, item.active === true]))
    } catch (e) {
      // 自动发现失败不影响指标目录浏览，状态保持为“暂无数据/未检测”。
      ElMessage.warning('自动发现状态暂时不可用')
    }
  } catch (e) {
    ElMessage.error('加载指标目录失败：' + (e.message || e))
  } finally {
    activeLoaded.value = true
  }
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
.left-head-row { display: flex; align-items: center; justify-content: space-between; margin-bottom: 8px; }
.left-head { font-size: 15px; font-weight: 700; color: #e5edf7; }
.discover-count { font-size: 11px; color: #34d399; }
.discover-count.muted { color: #64748b; }
.metric-tree-node { display: flex; align-items: center; justify-content: space-between; gap: 8px; width: 100%; min-width: 0; }
.metric-state { flex: none; font-size: 10px; }
.metric-state.online { color: #34d399; }
.metric-state.offline { color: #64748b; }
.kw { margin-bottom: 8px; }
.right { flex: 1; padding: 16px; }
.empty { color: #64748b; margin-top: 40px; text-align: center; }
.right-head { display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 10px; }
.m-title { font-size: 16px; font-weight: 700; color: #e5edf7; }
.m-name { font-size: 12px; color: #64748b; font-weight: 400; }
.m-meta { font-size: 12px; color: #94a3b8; margin-top: 4px; }
.chart { height: calc(100% - 70px); min-height: 320px; background: rgba(15,23,42,0.4); border-radius: 10px; }
</style>
