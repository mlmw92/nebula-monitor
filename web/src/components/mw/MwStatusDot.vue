<template>
  <span class="mw-status" :class="[type, { lg: large }]" :title="tooltip || undefined">
    <span class="mw-status-dot"></span>
    <span class="mw-status-label">{{ label ?? defaultLabel }}</span>
  </span>
</template>

<script setup>
import { computed } from 'vue'

const props = defineProps({
  // 'normal' | 'abnormal'
  status: { type: String, default: 'normal' },
  // 自定义文案（默认 正常/异常）
  label: { type: String, default: null },
  // 大号（用于详情头部）
  large: { type: Boolean, default: false },
  // 悬浮提示
  tooltip: { type: String, default: '' },
})

const type = computed(() => (props.status === 'normal' ? 'normal' : 'abnormal'))
const defaultLabel = computed(() => (type.value === 'normal' ? '正常' : '异常'))
</script>

<style scoped>
.mw-status {
  display: inline-flex;
  align-items: center;
  font-size: 13px;
  font-weight: 500;
  line-height: 1;
}
.mw-status-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  margin-right: 6px;
  display: inline-block;
  flex-shrink: 0;
}
.mw-status-label {
  white-space: nowrap;
}
/* 正常：绿灯 + 绿色字体 */
.mw-status.normal {
  color: #4ade80;
}
.mw-status.normal .mw-status-dot {
  background: #4ade80;
  box-shadow: 0 0 6px rgba(74, 222, 128, 0.5);
}
/* 异常：黄灯 + 黄色字体 */
.mw-status.abnormal {
  color: #fbbf24;
}
.mw-status.abnormal .mw-status-dot {
  background: #fbbf24;
  box-shadow: 0 0 6px rgba(245, 158, 11, 0.55);
}
/* 大号 */
.mw-status.lg {
  font-size: 15px;
}
.mw-status.lg .mw-status-dot {
  width: 12px;
  height: 12px;
}
</style>
