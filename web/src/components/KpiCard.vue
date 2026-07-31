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

/* 色调：顶栏渐变 + 图标底色 */
.tone-total::before { background: linear-gradient(90deg, #22d3ee, #3b82f6); }
.tone-total .kpi-icon { background: rgba(56, 189, 248, 0.12); color: #38bdf8; }

.tone-host::before { background: linear-gradient(90deg, #0ea5e9, #0284c7); }
.tone-host .kpi-icon { background: rgba(14, 165, 233, 0.15); color: #38bdf8; }

.tone-up::before { background: linear-gradient(90deg, #22c55e, #16a34a); }
.tone-up .kpi-icon { background: rgba(34, 197, 94, 0.15); color: #22c55e; }

.tone-down::before { background: linear-gradient(90deg, #ef4444, #dc2626); }
.tone-down .kpi-icon { background: rgba(239, 68, 68, 0.15); color: #ef4444; }

.tone-mem::before { background: linear-gradient(90deg, #a855f7, #7c3aed); }
.tone-mem .kpi-icon { background: rgba(100, 116, 139, 0.15); color: #94a3b8; }

.tone-conn::before { background: linear-gradient(90deg, #3b82f6, #2563eb); }
.tone-conn .kpi-icon { background: rgba(56, 189, 248, 0.12); color: #38bdf8; }

.tone-ops::before { background: linear-gradient(90deg, #f59e0b, #d97706); }
.tone-ops .kpi-icon { background: rgba(245, 158, 11, 0.12); color: #f59e0b; }

.tone-cluster::before { background: linear-gradient(90deg, #6366f1, #8b5cf6); }
.tone-cluster .kpi-icon { background: rgba(99, 102, 241, 0.15); color: #818cf8; }

.tone-alert::before { background: linear-gradient(90deg, #ef4444, #dc2626); }
.tone-alert .kpi-icon { background: rgba(239, 68, 68, 0.18); color: #f87171; }

.tone-ok::before { background: linear-gradient(90deg, #22c55e, #16a34a); }
.tone-ok .kpi-icon { background: rgba(34, 197, 94, 0.15); color: #22c55e; }
</style>
