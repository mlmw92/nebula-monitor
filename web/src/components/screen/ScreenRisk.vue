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
    <div v-if="riskReasons.length" class="sr-reasons">
      <div class="sr-reasons-title">风险原因</div>
      <div v-for="item in riskReasons" :key="item.key" class="sr-reason">
        <span class="sr-reason-host">{{ item.name }}</span>
        <span>{{ item.reason }}</span>
      </div>
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

// 分级：故障=离线或有 critical 告警；预警=有 warning 告警或峰值>=70%；其余正常。
// 兼容原始节点(status)和大屏节点卡片(online)两种字段结构。
const evaluatedHosts = computed(() => {
  const firing = props.alerts.filter((a) => a.state === 'firing')
  return props.nodes.map((n) => {
    const online = typeof n.online === 'boolean' ? n.online : n.status === 'online'
    const na = firing.filter((a) => a.node === n.hostname || a.node === n.name)
    const hasCrit = na.some((a) => (a.severity || '').toLowerCase() === 'critical')
    const hasWarn = na.some((a) => (a.severity || '').toLowerCase() === 'warning')
    const m = props.metrics[n.hostname] || {}
    const cpu = Number(n.cpu ?? m.cpu ?? 0)
    const mem = Number(n.mem ?? m.mem ?? 0)
    const disk = Number(n.disk ?? m.disk ?? 0)
    const resources = []
    if (cpu >= 70) resources.push(`CPU ${Math.round(cpu)}%`)
    if (mem >= 80) resources.push(`内存 ${Math.round(mem)}%`)
    if (disk >= 85) resources.push(`磁盘 ${Math.round(disk)}%`)
    const resourceWarning = cpu >= 70 || mem >= 80 || disk >= 85
    let level = 'normal'
    if (!online || hasCrit) level = 'danger'
    else if (hasWarn || resourceWarning) level = 'warn'
    const reasons = []
    if (!online) reasons.push('主机离线')
    if (hasCrit) reasons.push('严重告警')
    if (hasWarn) reasons.push('预警告警')
    const alertReasons = [...new Set(na.map((a) => a.ruleName || a.summary).filter(Boolean))]
    if (alertReasons.length) reasons.push(`告警：${alertReasons.join('、')}`)
    if (resources.length) reasons.push(`资源超阈值：${resources.join('、')}`)
    return {
      key: n.hostname || n.name,
      name: n.label || n.displayName || n.hostname || n.name || '-',
      level,
      reason: reasons.join('；'),
    }
  })
})

const dist = computed(() => {
  const result = { normal: 0, warn: 0, danger: 0 }
  evaluatedHosts.value.forEach((host) => { result[host.level] += 1 })
  return result
})

const total = computed(() => props.nodes.length)
const riskCount = computed(() => dist.value.warn + dist.value.danger)
const riskReasons = computed(() => evaluatedHosts.value.filter((host) => host.level !== 'normal'))

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
.sr-reasons {
  margin-top: 8px;
  padding-top: 7px;
  border-top: 1px solid rgba(255, 255, 255, 0.06);
  max-height: 72px;
  overflow: auto;
  font-size: 11px;
  color: var(--text-dim);
}
.sr-reasons-title {
  margin-bottom: 4px;
  color: var(--warn);
}
.sr-reason {
  display: flex;
  gap: 6px;
  line-height: 18px;
}
.sr-reason-host {
  flex-shrink: 0;
  color: var(--text);
}
</style>
