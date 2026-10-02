<template>
  <div class="kpi-card" :class="`tone-${tone}`">
    <div v-if="$slots.icon" class="kpi-icon"><slot name="icon" /></div>
    <div class="kpi-body">
      <div class="kpi-num">{{ value }}</div>
      <div class="kpi-text">{{ label }}</div>
      <!-- hint 可选：仅在需要的页面（如资产台账的健康度）补充一行口径说明 -->
      <div v-if="hint" class="kpi-hint">{{ hint }}</div>
    </div>
  </div>
</template>

<script setup>
defineProps({
  value: { type: [String, Number], default: '' },
  label: { type: String, default: '' },
  tone: { type: String, default: 'total' },
  hint: { type: String, default: '' },
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
  transform: translateY(-1px);
  box-shadow: var(--sh-2);
}
.kpi-card::before {
  content: '';
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  height: 1px;
  background: var(--bd);
}
.kpi-icon {
  width: 36px;
  height: 36px;
  border-radius: 9px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  /* 默认中性：颜色只留给真实异常，不用来给 KPI 分类 */
  background: var(--fill-2);
  color: var(--t2);
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
  font-size: 20px;
  font-weight: 700;
  font-family: var(--mono);
  letter-spacing: -0.02em;
  line-height: 1.15;
}
.kpi-text {
  font-size: 13px;
  color: var(--text-dim);
  margin-top: 1px;
  white-space: nowrap;
}
.kpi-hint {
  font-size: 12px;
  color: var(--text-muted);
  margin-top: 2px;
  white-space: nowrap;
}

/* =========================================================
 * 色调：只区分「异常」与「正常」两档，不给 KPI 做分类配色。
 * 原来 8 个 tone 各配一种颜色（青/绿/红/紫/蓝/橙/靛），是典型的
 * 彩色图标网格——颜色变成了装饰，真正的异常反而不显眼。
 * 现在：默认中性；只有 down / alert（真实故障）上红色。
 * ========================================================= */
.tone-down .kpi-icon,
.tone-alert .kpi-icon {
  background: var(--danger-dim);
  color: var(--danger);
}
.tone-down::before,
.tone-alert::before {
  height: 2px;
  background: var(--danger);
}
.tone-down .kpi-num,
.tone-alert .kpi-num {
  color: var(--danger);
}
/* warn 级：琥珀，用于"接近阈值"这类需要看一眼但不是故障的指标 */
.tone-warn .kpi-icon {
  background: var(--warn-dim);
  color: var(--warn);
}
.tone-warn::before {
  height: 2px;
  background: var(--warn);
}
</style>
