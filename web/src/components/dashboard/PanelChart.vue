<template>
  <div class="panel-chart">
    <div class="panel-head">
      <span class="panel-title">{{ panel.title || panel.metric }}</span>
      <div class="panel-actions">
        <el-button size="small" text bg @click="$emit('edit')">编辑</el-button>
        <el-button size="small" text bg type="primary" @click="onExport">导出CSV</el-button>
        <el-button size="small" text bg type="danger" @click="$emit('remove')">删除</el-button>
      </div>
    </div>
    <div ref="el" class="chart"></div>
    <div v-if="error" class="panel-error">{{ error }}</div>
  </div>
</template>

<script setup>
import { ref, onMounted, onBeforeUnmount, watch, nextTick } from 'vue'
import { initChart, monitorOption, setHistory, COLORS } from '../../charts/echarts'
import http from '../../api/http'

const props = defineProps({
  panel: { type: Object, required: true },
})
const emit = defineEmits(['edit', 'remove'])

const el = ref(null)
let chart = null
const error = ref('')

function rangeBounds(r) {
  const now = Date.now()
  switch (r || '1h') {
    case '6h': return { start: now - 6 * 3600000, end: now, step: 60000 }
    case '24h': return { start: now - 24 * 3600000, end: now, step: 300000 }
    case '7d': return { start: now - 7 * 86400000, end: now, step: 1800000 }
    case '1h':
    default: return { start: now - 3600000, end: now, step: 60000 }
  }
}

async function load() {
  if (!chart) return
  error.value = ''
  const { start, end, step } = rangeBounds(props.panel.range)
  const params = new URLSearchParams({
    metric: props.panel.metric,
    start,
    end,
    step,
  })
  if (props.panel.node) params.set('node', props.panel.node)
  if (props.panel.instance) params.set('instance', props.panel.instance)
  for (const k in (props.panel.labels || {})) params.set('labels', `${k}=${props.panel.labels[k]}`)
  try {
    const d = await http.get('/api/v1/query/range?' + params.toString())
    const series = d.series || d.data || []
    // 按指标名归类到单序列（面板只选一个指标）
    const data = {}
    ;(series || []).forEach((s) => {
      const label = s.labels && s.labels.instance ? `${props.panel.metric}·${s.labels.instance}` : props.panel.metric
      data[label] = (s.points || []).map((p) => [p.timestamp, p.value])
    })
    const names = Object.keys(data)
    chart.setOption({
      series: names.map((n, i) => ({
        name: n,
        data: data[n] || [],
        type: 'line',
        smooth: true,
        showSymbol: false,
        lineStyle: { color: COLORS.cyan, width: 2 },
        areaStyle: { color: 'rgba(34,211,238,0.12)' },
      })),
    })
  } catch (e) {
    error.value = '加载失败：' + (e.message || e)
  }
}

function onExport() {
  const { start, end } = rangeBounds(props.panel.range)
  http.exportMetricCSV({
    metric: props.panel.metric,
    node: props.panel.node,
    instance: props.panel.instance,
    start,
    end,
    step: 60000,
    labels: Object.entries(props.panel.labels || {}).map(([k, v]) => `${k}=${v}`).join(','),
    filename: `metric_${props.panel.metric}.csv`,
  }).catch((e) => {
    error.value = '导出失败：' + (e.message || e)
  })
}

function resize() {
  chart && chart.resize()
}

onMounted(async () => {
  await nextTick()
  chart = initChart(el.value)
  chart.setOption(monitorOption({ series: [{ name: props.panel.metric, color: COLORS.cyan, data: [] }] }))
  load()
  window.addEventListener('resize', resize)
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', resize)
  chart && chart.dispose()
})

watch(() => props.panel, () => load(), { deep: true })
</script>

<style scoped>
.panel-chart {
  background: rgba(15, 23, 42, 0.6);
  border: 1px solid rgba(34, 211, 238, 0.15);
  border-radius: 10px;
  padding: 10px 12px;
  display: flex;
  flex-direction: column;
  min-height: 240px;
}
.panel-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 6px;
}
.panel-title {
  font-size: 14px;
  font-weight: 600;
  color: #e5edf7;
}
.chart {
  flex: 1;
  min-height: 180px;
}
.panel-error {
  color: #f87171;
  font-size: 12px;
  margin-top: 4px;
}
</style>
