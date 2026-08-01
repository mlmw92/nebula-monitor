<template>
  <div class="kpi-grid">
    <div v-for="k in kpis" :key="k.label" class="kpi-card" :class="toneCls(k.tone)">
      <div class="kpi-icon">
        <el-icon :size="18"><component :is="k.icon" /></el-icon>
      </div>
      <div class="kpi-body">
        <div class="kpi-num">{{ k.value }}</div>
        <div class="kpi-text">{{ k.label }}</div>
        <div class="kpi-foot">{{ k.foot }}</div>
      </div>
    </div>
    <div v-if="!kpis || kpis.length === 0" class="kpi-empty">暂无数据</div>
  </div>
</template>

<script setup>
const props = defineProps({
  kpis: { type: Array, default: () => [] },
})

function toneCls(tone) {
  if (tone === 'bad') return 'tone-alert'
  if (tone === 'warn') return 'tone-ops'
  return 'tone-ok'
}
</script>

<style scoped>
.kpi-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 12px;
  height: 100%;
}
.kpi-card {
  border-radius: var(--radius);
  padding: 14px 15px;
  min-height: 84px;
  display: flex;
  align-items: center;
  gap: 12px;
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
  width: 38px;
  height: 38px;
  border-radius: 10px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}
.kpi-body {
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.kpi-num {
  font-size: 22px;
  font-weight: 800;
  font-family: var(--mono);
  letter-spacing: -0.02em;
  line-height: 1.1;
}
.kpi-text {
  font-size: 11px;
  color: var(--text-dim);
  margin-top: 2px;
}
.kpi-foot {
  font-size: 10px;
  color: var(--text-muted);
  margin-top: 1px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.kpi-empty {
  grid-column: 1 / -1;
  color: var(--text-muted);
  font-size: 13px;
  text-align: center;
  padding: 20px;
}

/* 色调：顶栏渐变 + 图标底色（跟随主题 chart 色板） */
.tone-ok::before { background: linear-gradient(90deg, var(--chart-green), color-mix(in srgb, var(--chart-green) 65%, #000)); }
.tone-ok .kpi-icon { background: color-mix(in srgb, var(--chart-green) 16%, transparent); color: var(--chart-green); }

.tone-ops::before { background: linear-gradient(90deg, var(--chart-orange), color-mix(in srgb, var(--chart-orange) 65%, #000)); }
.tone-ops .kpi-icon { background: color-mix(in srgb, var(--chart-orange) 14%, transparent); color: var(--chart-orange); }

.tone-alert::before { background: linear-gradient(90deg, var(--chart-red), color-mix(in srgb, var(--chart-red) 65%, #000)); }
.tone-alert .kpi-icon { background: color-mix(in srgb, var(--chart-red) 18%, transparent); color: color-mix(in srgb, var(--chart-red) 85%, #fff); }
</style>
