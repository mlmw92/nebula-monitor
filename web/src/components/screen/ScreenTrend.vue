<template>
  <div class="screen-trend glass" :class="{ compact }">
    <div class="trend-head">
      <span class="trend-title">{{ title }}</span>
      <span class="trend-cur" :style="{ color: hexColor }">{{ curText }}</span>
    </div>
    <div ref="chartEl" class="trend-chart"></div>
  </div>
</template>

<script setup>
import { ref, onMounted, onUnmounted, watch } from 'vue'
import { initChart, monitorOption, COLORS, rateShort } from '../../charts/echarts'

const props = defineProps({
  title: { type: String, default: '' },
  // series: [{ name, color, data:[[ts,val],...] }]
  series: { type: Array, default: () => [] },
  unit: { type: String, default: '%' }, // '%' | 'rate'
  color: { type: String, default: COLORS.cyan },
  compact: { type: Boolean, default: false },
})

const chartEl = ref(null)
let chart = null
let ro = null

const hexColor = props.color
const curText = ref('--')

function isRate() {
  return props.unit === 'rate'
}

function fmtCur() {
  const s = props.series[0]
  if (!s || !s.data || !s.data.length) {
    curText.value = '--'
    return
  }
  const v = s.data[s.data.length - 1][1]
  if (v == null) {
    curText.value = '--'
    return
  }
  curText.value = isRate() ? rateShort(v) : v.toFixed(1) + '%'
}

function render() {
  if (!chart) return
  const rate = isRate()
  const opt = monitorOption({
    yMin: 0,
    yMax: rate ? undefined : 100,
    yFormatter: rate ? (v) => rateShort(v) : (v) => v + '%',
    tipFormatter: rate ? (v) => rateShort(v) : (v) => (v == null ? '-' : v.toFixed(1) + '%'),
    series: props.series.map((s) => ({ name: s.name, color: s.color, data: s.data })),
  })
  chart.setOption(opt, true)
  fmtCur()
}

onMounted(() => {
  chart = initChart(chartEl.value)
  render()
  ro = new ResizeObserver(() => chart && chart.resize())
  ro.observe(chartEl.value)
})
onUnmounted(() => {
  ro && ro.disconnect()
  chart && chart.dispose()
  chart = null
})

watch(() => props.series, render, { deep: true })
</script>

<style scoped>
.screen-trend {
  display: flex;
  flex-direction: column;
  padding: 10px 12px 6px;
  min-height: 0;
}
.trend-head {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  margin-bottom: 4px;
}
.trend-title {
  font-size: 12px;
  color: var(--text-dim);
  letter-spacing: 0.03em;
}
.trend-cur {
  font-size: 16px;
  font-weight: 700;
  font-family: var(--mono);
}
.trend-chart {
  flex: 1;
  min-height: 90px;
  width: 100%;
}

/* 紧凑模式：嵌入到资源趋势卡内，去掉嵌套背景与边框 */
.screen-trend.compact {
  padding: 2px 4px;
  background: none;
  border: none;
  box-shadow: none;
  backdrop-filter: none;
}
.screen-trend.compact .trend-head {
  margin-bottom: 0;
}
.screen-trend.compact .trend-title {
  font-size: 11px;
}
.screen-trend.compact .trend-cur {
  font-size: 13px;
}
.screen-trend.compact .trend-chart {
  min-height: 30px;
}
</style>
