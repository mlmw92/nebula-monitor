<template>
  <section class="card sec-card">
    <header v-if="title || $slots.actions" class="sec-head">
      <div class="sec-title-wrap">
        <span v-if="title" class="accent-bar"></span>
        <div class="sec-title-text">
          <h3 v-if="title" class="sec-title">{{ title }}</h3>
          <p v-if="subtitle" class="sec-sub">{{ subtitle }}</p>
        </div>
      </div>
      <div class="sec-actions">
        <slot name="actions" />
      </div>
    </header>

    <div class="sec-body" :class="{ dense }">
      <slot />
    </div>

    <footer v-if="$slots.footer" class="sec-foot">
      <slot name="footer" />
    </footer>
  </section>
</template>

<script setup>
// 全站统一容器：替代 .glass / .kpi / .ov-card / .panel 四种并存写法。
// dense 用于表格类高密度区域（内边距 16px → 12px）。
defineProps({
  title: { type: String, default: '' },
  subtitle: { type: String, default: '' },
  dense: { type: Boolean, default: false },
})
</script>

<style scoped>
.sec-card {
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.sec-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 12px 16px;
  border-bottom: 1px solid var(--bd);
}
.sec-title-wrap {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.accent-bar {
  width: 3px;
  height: 14px;
  border-radius: 2px;
  background: var(--accent);
  flex-shrink: 0;
}
.sec-title-text {
  min-width: 0;
}
.sec-title {
  font-size: var(--fs-base);
  font-weight: 600;
  color: var(--t1);
  letter-spacing: 0.01em;
}
.sec-sub {
  font-size: var(--fs-xs);
  color: var(--t3);
  margin-top: 2px;
}
.sec-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
}
.sec-body {
  padding: 16px;
  min-width: 0;
  flex: 1;
}
.sec-body.dense {
  padding: 12px;
}
.sec-foot {
  padding: 9px 16px;
  border-top: 1px solid var(--bd);
  font-size: var(--fs-xs);
  color: var(--t3);
}
</style>
