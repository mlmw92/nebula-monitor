<template>
  <div class="glass geo-map">
    <div class="gm-head">
      <span class="gm-title">请求来源地理分布</span>
      <div class="gm-scope">
        <button :class="{ on: scope === 'cn' }" @click="change('cn')">中国</button>
        <button :class="{ on: scope === 'world' }" @click="change('world')">世界</button>
      </div>
    </div>
    <div class="gm-tip" v-if="!hasData">暂无 access log 数据，请在 agent.yaml 配置 nginx 实例 accessLog 路径</div>
    <div ref="mapChart" class="gm-chart" :class="{ dim: !hasData }"></div>
  </div>
</template>

<script setup>
import { ref, watch, onMounted, onUnmounted } from 'vue'
import { initChart, mapGeoOption } from '../../charts/echarts'
import { registerMaps, mapName } from '../../charts/geoData'

const props = defineProps({
  scope: { type: String, default: 'cn' },
  data: { type: Object, default: () => null },
})
const emit = defineEmits(['update:scope'])

const mapChart = ref(null)
let chart = null
let ro = null

const hasData = ref(false)

// ip2region 对内网/保留地址返回 Reserved，视为无效地理点
function isValidPoint(p) {
  return p && p.name && p.name !== 'Reserved' && p.name !== '0'
}

// 后端 geo 响应 → mapGeoOption 数据：名称按 scope 归一化（世界地图用英文国家名）
function toGeo(d) {
  if (!d) return {}
  const scope = props.scope
  return {
    points: (d.points || []).filter(isValidPoint).map((p) => ({ name: mapName(scope, p), requests: p.requests, bytes: p.bytes })),
    deployPoints: (d.deployPoints || []).map((p) => ({ name: mapName(scope, p), requests: p.requests })),
    lines: (d.lines || []).map((l) => ({
      fromName: mapName(scope, { name: l.from, countryEn: l.fromEn }),
      toName: mapName(scope, { name: l.to, countryEn: l.toEn }),
      value: l.value,
    })),
  }
}

function render() {
  if (!chart) return
  const d = props.data
  const pts = (d?.points || []).filter(isValidPoint)
  hasData.value = !!(d && (pts.length || d.lines?.length || d.deployPoints?.length))
  chart.setOption(mapGeoOption(props.scope, toGeo(d)), true)
}

function change(s) {
  if (s === props.scope) return
  emit('update:scope', s)
}

onMounted(() => {
  registerMaps()
  chart = initChart(mapChart.value)
  render()
  ro = new ResizeObserver(() => chart && chart.resize())
  ro.observe(mapChart.value)
})
onUnmounted(() => {
  ro && ro.disconnect()
  chart && chart.dispose()
  chart = null
})

watch(() => props.data, render, { deep: false })
watch(() => props.scope, render)
</script>

<style scoped>
.geo-map {
  display: flex;
  flex-direction: column;
  padding: 10px 12px 6px;
  min-height: 0;
  position: relative;
}
.gm-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 4px;
}
.gm-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
}
.gm-scope {
  display: flex;
  gap: 6px;
}
.gm-scope button {
  background: transparent;
  border: 1px solid var(--border);
  color: var(--text-dim);
  font-size: 11px;
  padding: 3px 12px;
  border-radius: 12px;
  cursor: pointer;
  transition: all 0.15s;
}
.gm-scope button:hover {
  border-color: var(--accent);
  color: var(--accent);
}
.gm-scope button.on {
  background: var(--accent-dim);
  border-color: var(--accent);
  color: var(--accent);
}
.gm-chart {
  flex: 1;
  min-height: 0;
  width: 100%;
  transition: opacity 0.3s;
}
.gm-chart.dim {
  opacity: 0.35;
}
.gm-tip {
  position: absolute;
  top: 40px;
  left: 50%;
  transform: translateX(-50%);
  z-index: 2;
  background: rgba(6, 11, 22, 0.85);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 6px 14px;
  font-size: 12px;
  color: var(--text-dim);
  white-space: nowrap;
}
</style>
