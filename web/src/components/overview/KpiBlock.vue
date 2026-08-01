<template>
  <div class="kpi-grid">
    <div v-for="k in kpis" :key="k.label" class="kpi-cell" :class="'c-' + k.tone">
      <div class="kpi-label">{{ k.label }}</div>
      <div class="kpi-value">{{ k.value }}</div>
      <div class="kpi-foot">{{ k.foot }}</div>
    </div>
    <div v-if="!kpis || kpis.length === 0" class="kpi-empty">暂无数据</div>
  </div>
</template>

<script setup>
defineProps({
  kpis: { type: Array, default: () => [] },
})
</script>

<style scoped>
.kpi-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 12px;
  height: 100%;
}
.kpi-cell {
  background: var(--bg-soft);
  border-radius: 10px;
  padding: 12px 14px;
  display: flex;
  flex-direction: column;
  gap: 4px;
  border-left: 3px solid var(--accent);
}
.kpi-cell.c-good {
  border-left-color: var(--success);
}
.kpi-cell.c-warn {
  border-left-color: var(--warning);
}
.kpi-cell.c-bad {
  border-left-color: var(--danger);
}
.kpi-label {
  font-size: 12px;
  color: var(--text-dim);
}
.kpi-value {
  font-size: 26px;
  font-weight: 800;
  line-height: 1.1;
}
.kpi-foot {
  font-size: 11px;
  color: var(--text-muted);
}
.kpi-empty {
  grid-column: 1 / -1;
  color: var(--text-muted);
  font-size: 13px;
  text-align: center;
  padding: 20px;
}
</style>
