<template>
  <div class="glass screen-gauges">
    <div class="sg-title">{{ title }}</div>
    <div class="sg-grid" :style="{ gridTemplateColumns: `repeat(${cols}, 1fr)` }">
      <div class="sg-item" v-for="g in items" :key="g.key">
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
  title: { type: String, default: '资源概况' },
  // items: [{ key, label, value(0-100), text(环中心显示), color }]
  items: { type: Array, default: () => [] },
  cols: { type: Number, default: 4 },
})

const R = 34
const C = 2 * Math.PI * R

const gauges = computed(() =>
  props.items.map((g) => ({
    key: g.key,
    label: g.label,
    value: Math.max(0, Math.min(100, g.value ?? 0)),
    text: g.text ?? Math.round((g.value ?? 0)) + '%',
    color: g.color || 'var(--accent)',
  }))
)

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
  letter-spacing: 0.04em;
}
.sg-grid {
  display: grid;
  gap: 10px 4px;
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
  filter: drop-shadow(0 0 4px currentColor);
}
.sg-center {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 14px;
  font-weight: 700;
  font-family: var(--mono);
}
.sg-label {
  font-size: 11px;
  color: var(--text-dim);
  margin-top: 4px;
}
</style>
