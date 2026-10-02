<template>
  <span class="pill" :class="[tone, size]">
    <i v-if="dot" class="pill-dot"></i>
    <slot />
  </span>
</template>

<script setup>
// 12px 轻量药丸：用于高密度列表的状态表达。
// tone 只允许表达真实状态（ok/warn/danger/info），muted 用于"无状态"。
defineProps({
  tone: { type: String, default: 'muted' }, // ok | warn | danger | info | muted
  dot: { type: Boolean, default: false },
  size: { type: String, default: 'sm' }, // sm | md
})
</script>

<style scoped>
.pill {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 2px 8px;
  border-radius: var(--r-pill);
  font-size: var(--fs-xs);
  font-weight: 600;
  line-height: 1.6;
  white-space: nowrap;
  border: 1px solid transparent;
}
.pill.md {
  font-size: var(--fs-sm);
  padding: 3px 10px;
}
.pill-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
  flex-shrink: 0;
}

.pill.ok {
  background: var(--ok-dim);
  color: var(--ok);
  border-color: var(--ok-bd);
}
.pill.warn {
  background: var(--warn-dim);
  color: var(--warn);
  border-color: var(--warn-bd);
}
.pill.danger {
  background: var(--danger-dim);
  color: var(--danger);
  border-color: var(--danger-bd);
}
.pill.info {
  background: var(--info-dim);
  color: var(--info);
  border-color: var(--info-bd);
}
.pill.muted {
  background: var(--fill-2);
  color: var(--t3);
  border-color: var(--bd);
}
</style>
