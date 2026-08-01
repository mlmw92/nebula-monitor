<template>
  <div class="kpi-card" :class="`tone-${tone}`">
    <div v-if="$slots.icon" class="kpi-icon"><slot name="icon" /></div>
    <div class="kpi-body">
      <div class="kpi-num">{{ value }}</div>
      <div class="kpi-text">{{ label }}</div>
    </div>
  </div>
</template>

<script setup>
defineProps({
  value: { type: [String, Number], default: '' },
  label: { type: String, default: '' },
  tone: { type: String, default: 'total' },
})
</script>

<style scoped>
.kpi-card {
  border-radius: var(--radius);
  padding: 15px 15px;
  min-height: 70px;
  display: flex;
  align-items: center;
  gap: 11px;
  border: 1px solid var(--border);
  background: var(--bg-card);
  position: relative;
  overflow: hidden;
  transition: transform 0.2s, box-shadow 0.2s;
}
.kpi-card:hover {
  transform: translateY(-2px);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.3);
}
.kpi-card::before {
  content: '';
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  height: 2px;
  opacity: 0.8;
}
.kpi-icon {
  width: 36px;
  height: 36px;
  border-radius: 9px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}
.kpi-icon :deep(svg) {
  width: 19px;
  height: 19px;
}
.kpi-body {
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.kpi-num {
  font-size: 18px;
  font-weight: 700;
  font-family: var(--mono);
  letter-spacing: -0.02em;
  line-height: 1.15;
}
.kpi-text {
  font-size: 10px;
  color: var(--text-dim);
  margin-top: 1px;
  white-space: nowrap;
}

/* 色调：顶栏渐变 + 图标底色（跟随主题 chart 色板） */
.tone-total::before { background: linear-gradient(90deg, var(--chart-cyan), var(--chart-blue)); }
.tone-total .kpi-icon { background: color-mix(in srgb, var(--chart-cyan) 14%, transparent); color: var(--chart-cyan); }

.tone-host::before { background: linear-gradient(90deg, var(--chart-cyan), color-mix(in srgb, var(--chart-blue) 70%, #000)); }
.tone-host .kpi-icon { background: color-mix(in srgb, var(--chart-cyan) 16%, transparent); color: var(--chart-cyan); }

.tone-up::before { background: linear-gradient(90deg, var(--chart-green), color-mix(in srgb, var(--chart-green) 65%, #000)); }
.tone-up .kpi-icon { background: color-mix(in srgb, var(--chart-green) 16%, transparent); color: var(--chart-green); }

.tone-down::before { background: linear-gradient(90deg, var(--chart-red), color-mix(in srgb, var(--chart-red) 65%, #000)); }
.tone-down .kpi-icon { background: color-mix(in srgb, var(--chart-red) 16%, transparent); color: var(--chart-red); }

.tone-mem::before { background: linear-gradient(90deg, var(--chart-purple), color-mix(in srgb, var(--chart-purple) 65%, #000)); }
.tone-mem .kpi-icon { background: color-mix(in srgb, var(--chart-purple) 15%, transparent); color: var(--chart-purple); }

.tone-conn::before { background: linear-gradient(90deg, var(--chart-blue), color-mix(in srgb, var(--chart-blue) 70%, #000)); }
.tone-conn .kpi-icon { background: color-mix(in srgb, var(--chart-cyan) 14%, transparent); color: var(--chart-cyan); }

.tone-ops::before { background: linear-gradient(90deg, var(--chart-orange), color-mix(in srgb, var(--chart-orange) 65%, #000)); }
.tone-ops .kpi-icon { background: color-mix(in srgb, var(--chart-orange) 14%, transparent); color: var(--chart-orange); }

.tone-cluster::before { background: linear-gradient(90deg, var(--chart-indigo), var(--chart-purple)); }
.tone-cluster .kpi-icon { background: color-mix(in srgb, var(--chart-indigo) 16%, transparent); color: var(--chart-indigo); }

.tone-alert::before { background: linear-gradient(90deg, var(--chart-red), color-mix(in srgb, var(--chart-red) 65%, #000)); }
.tone-alert .kpi-icon { background: color-mix(in srgb, var(--chart-red) 18%, transparent); color: color-mix(in srgb, var(--chart-red) 85%, #fff); }

.tone-ok::before { background: linear-gradient(90deg, var(--chart-green), color-mix(in srgb, var(--chart-green) 65%, #000)); }
.tone-ok .kpi-icon { background: color-mix(in srgb, var(--chart-green) 16%, transparent); color: var(--chart-green); }
</style>
