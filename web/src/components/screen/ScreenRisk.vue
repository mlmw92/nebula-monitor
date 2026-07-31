<template>
  <div class="glass panel-mini screen-risk">
    <div class="sr-title">风险等级分布</div>
    <div class="sr-body">
      <div class="sr-donut">
        <svg viewBox="0 0 120 120">
          <circle cx="60" cy="60" r="46" fill="none" stroke="rgba(255,255,255,0.05)" stroke-width="14" />
          <circle
            v-for="seg in segments"
            :key="seg.key"
            cx="60" cy="60" r="46" fill="none"
            :stroke="seg.color" stroke-width="14"
            :stroke-dasharray="seg.dash"
            :stroke-dashoffset="seg.offset"
            transform="rotate(-90 60 60)"
            class="sr-seg"
          />
        </svg>
        <div class="sr-donut-center">
          <span class="sr-total">{{ total }}</span>
          <span class="sr-total-l">主机</span>
        </div>
      </div>
      <div class="sr-legend">
        <div class="sr-leg" v-for="seg in segments" :key="'l-' + seg.key">
          <span class="d" :style="{ background: seg.color }"></span>
          <span class="sr-leg-name">{{ seg.label }}</span>
          <b class="mono">{{ seg.value }}</b>
          <span class="sr-leg-pct">{{ pct(seg.value) }}%</span>
        </div>
      </div>
    </div>
    <div class="sr-foot">
      风险主机 <b class="mono">{{ riskCount }}</b> 台 · 占比 <b class="mono">{{ pct(riskCount) }}%</b>
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue'

const props = defineProps({
  nodes: { type: Array, default: () => [] },
  metrics: { type: Object, default: () => ({}) },
  alerts: { type: Array, default: () => [] },
})

const R = 46
const C = 2 * Math.PI * R

// 分级：故障=离线或有 critical 告警；预警=有 warning 告警或峰值>=70%；其余正常
const dist = computed(() => {
  let normal = 0, warn = 0, danger = 0
  const firing = props.alerts.filter((a) => a.state === 'firing')
  for (const n of props.nodes) {
    const online = n.status === 'online'
    const na = firing.filter((a) => a.node === n.hostname)
    const hasCrit = na.some((a) => a.severity === 'critical')
    const hasWarn = na.some((a) => a.severity === 'warning')
    const m = props.metrics[n.hostname] || {}
    const peak = Math.max(m.cpu || 0, m.mem || 0, m.disk || 0)
    if (!online || hasCrit) danger += 1
    else if (hasWarn || peak >= 70) warn += 1
    else normal += 1
  }
  return { normal, warn, danger }
})

const total = computed(() => props.nodes.length)
const riskCount = computed(() => dist.value.warn + dist.value.danger)

const segments = computed(() => {
  const t = total.value || 1
  const list = [
    { key: 'normal', label: '正常', value: dist.value.normal, color: 'var(--accent)' },
    { key: 'warn', label: '预警', value: dist.value.warn, color: 'var(--warn)' },
    { key: 'danger', label: '故障', value: dist.value.danger, color: 'var(--danger)' },
  ]
  let acc = 0
  for (const s of list) {
    const frac = s.value / t
    const len = C * frac
    s.dash = `${len} ${C}`
    s.offset = -acc
    acc += len
  }
  return list
})

function pct(v) {
  if (!total.value) return 0
  return Math.round((v / total.value) * 100)
}
</script>

<style scoped>
.screen-risk {
  padding: 12px 14px;
  display: flex;
  flex-direction: column;
}
.sr-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 8px;
}
.sr-body {
  display: flex;
  align-items: center;
  gap: 12px;
  flex: 1;
}
.sr-donut {
  position: relative;
  width: 104px;
  height: 104px;
  flex-shrink: 0;
}
.sr-donut svg {
  width: 100%;
  height: 100%;
}
.sr-seg {
  transition: stroke-dasharray 0.6s ease, stroke-dashoffset 0.6s ease;
}
.sr-donut-center {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
}
.sr-total {
  font-size: 24px;
  font-weight: 800;
  font-family: var(--mono);
  color: var(--text);
  line-height: 1;
}
.sr-total-l {
  font-size: 10px;
  color: var(--text-dim);
  margin-top: 2px;
}
.sr-legend {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
}
.sr-leg {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--text-dim);
}
.sr-leg .d {
  width: 8px;
  height: 8px;
  border-radius: 2px;
  flex-shrink: 0;
}
.sr-leg-name {
  color: var(--text);
}
.sr-leg b {
  margin-left: auto;
  color: var(--text);
}
.sr-leg-pct {
  width: 34px;
  text-align: right;
}
.sr-foot {
  margin-top: 10px;
  padding-top: 8px;
  border-top: 1px solid rgba(255, 255, 255, 0.06);
  font-size: 12px;
  color: var(--text-dim);
}
.sr-foot b {
  color: var(--warn);
}
</style>
