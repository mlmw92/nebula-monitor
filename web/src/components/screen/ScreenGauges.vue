<template>
  <div class="glass panel-mini screen-gauges">
    <div class="sg-title">资源概况</div>
    <div class="sg-grid">
      <div class="sg-item" v-for="g in gauges" :key="g.key">
        <div class="sg-ring">
          <svg viewBox="0 0 80 80">
            <circle cx="40" cy="40" r="34" fill="none" stroke="rgba(255,255,255,0.06)" stroke-width="7" />
            <circle
              cx="40" cy="40" r="34" fill="none"
              :stroke="g.color" stroke-width="7" stroke-linecap="round"
              :stroke-dasharray="dash(g.value)" transform="rotate(-90 40 40)" class="sg-prog"
            />
          </svg>
          <div class="sg-center" :style="{ color: g.color }">{{ g.text }}</div>
        </div>
        <div class="sg-label">{{ g.label }}</div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue'

const props = defineProps({
  cpu: { type: Number, default: 0 },
  mem: { type: Number, default: 0 },
  disk: { type: Number, default: 0 },
  onlineRate: { type: Number, default: 0 },
})

const R = 34
const C = 2 * Math.PI * R

// 使用率分级配色：<70 accent，70-89 warn，>=90 danger；在线率反向（越高越好）
function usageColor(v) {
  if (v >= 90) return 'var(--danger)'
  if (v >= 70) return 'var(--warn)'
  return 'var(--accent)'
}
function rateColor(v) {
  if (v >= 90) return 'var(--accent)'
  if (v >= 60) return 'var(--warn)'
  return 'var(--danger)'
}

const gauges = computed(() => [
  { key: 'cpu', label: 'CPU 使用率', value: props.cpu, text: fmt(props.cpu), color: usageColor(props.cpu) },
  { key: 'mem', label: '内存使用率', value: props.mem, text: fmt(props.mem), color: usageColor(props.mem) },
  { key: 'disk', label: '磁盘使用率', value: props.disk, text: fmt(props.disk), color: usageColor(props.disk) },
  { key: 'online', label: '在线率', value: props.onlineRate, text: fmt(props.onlineRate), color: rateColor(props.onlineRate) },
])

function fmt(v) {
  return (Math.round((v || 0) * 10) / 10) + '%'
}
function dash(v) {
  const p = Math.max(0, Math.min(100, v || 0))
  return `${(C * p) / 100} ${C}`
}
</script>

<style scoped>
.screen-gauges {
  padding: 12px 14px;
}
.sg-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 10px;
}
.sg-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 10px 6px;
}
.sg-item {
  display: flex;
  flex-direction: column;
  align-items: center;
}
.sg-ring {
  position: relative;
  width: 68px;
  height: 68px;
}
.sg-ring svg {
  width: 100%;
  height: 100%;
}
.sg-prog {
  transition: stroke-dasharray 0.6s ease;
}
.sg-center {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 15px;
  font-weight: 700;
  font-family: var(--mono);
}
.sg-label {
  font-size: 11px;
  color: var(--text-dim);
  margin-top: 4px;
}
</style>
