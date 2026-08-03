<template>
  <div class="glass screen-gauges">
    <div class="sg-title">{{ title }}</div>
    <div class="sg-grid" :style="{ gridTemplateColumns: `repeat(${cols}, 1fr)` }">
      <div class="sg-item" v-for="g in gauges" :key="g.key">
        <template v-if="g.type === 'text'">
          <div class="sg-text-card" :style="{ borderColor: g.color, boxShadow: `0 0 12px ${g.color}33` }">
            <div class="sg-text-value" :style="{ color: g.color }">{{ g.text }}</div>
          </div>
        </template>
        <template v-else>
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
        </template>
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
    type: g.type || 'gauge',
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
.sg-text-card {
  width: 68px;
  height: 68px;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(0, 0, 0, 0.25);
  border: 1.5px solid var(--accent);
  transition: all 0.4s ease;
}
.sg-text-value {
  font-size: 14px;
  font-weight: 700;
  font-family: var(--mono);
  text-align: center;
  line-height: 1.1;
}
.sg-label {
  font-size: 11px;
  color: var(--text-dim);
  margin-top: 4px;
}

/* 宽屏 / 4K 适配 */
@media (min-width: 2400px) {
  .screen-gauges { padding: 18px 20px; }
  .sg-title { font-size: 17px; margin-bottom: 14px; }
  .sg-grid { gap: 16px 8px; }
  .sg-ring { width: 96px; height: 96px; }
  .sg-center { font-size: 18px; }
  .sg-text-card { width: 96px; height: 96px; }
  .sg-text-value { font-size: 18px; }
  .sg-label { font-size: 15px; margin-top: 6px; }
}
@media (min-width: 3440px) {
  .screen-gauges { padding: 24px 26px; }
  .sg-title { font-size: 22px; margin-bottom: 18px; }
  .sg-grid { gap: 22px 12px; }
  .sg-ring { width: 120px; height: 120px; }
  .sg-center { font-size: 24px; }
  .sg-text-card { width: 120px; height: 120px; }
  .sg-text-value { font-size: 24px; }
  .sg-label { font-size: 19px; margin-top: 8px; }
}
</style>
