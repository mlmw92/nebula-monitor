<template>
  <div class="page-header">
    <div class="ph-text">
      <h2 class="ph-title">{{ title }}</h2>
      <p v-if="desc" class="ph-desc">{{ desc }}</p>
    </div>
    <div v-if="$slots.actions" class="ph-actions">
      <slot name="actions" />
    </div>
  </div>
</template>

<script setup>
// 全站统一页头：标题 + 描述 + 右侧操作区。
// 刻意不使用渐变文字——标题只是标题，层级靠字号与字重表达。
defineProps({
  title: { type: String, default: '' },
  desc: { type: String, default: '' },
})
</script>

<style scoped>
.page-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 16px;
  margin-bottom: 20px;
}
.ph-text {
  min-width: 0;
}
.ph-title {
  font-size: var(--fs-2xl);
  font-weight: 600;
  line-height: 1.25;
  letter-spacing: -0.01em;
  color: var(--t1);
}
.ph-desc {
  font-size: var(--fs-sm);
  color: var(--t3);
  margin-top: 4px;
}
.ph-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
}
</style>
