<template>
  <!-- 选中若干行后才出现的操作条。
       做成共享组件而不是各页各写一份：资产台账与节点操作两页都要它，
       抄第二遍就必然出现"两页的批量条间距/文案不一样"这类无意义差异。 -->
  <div class="batch-bar">
    <span class="bb-count">已选中 <b>{{ count }}</b> 条</span>
    <slot />
    <span class="bb-spacer" />
    <el-button size="small" text @click="$emit('clear')">取消选择</el-button>
  </div>
</template>

<script setup>
defineProps({
  // count 为已选中的条数（组件只负责显示，选中状态由调用方持有）。
  count: { type: Number, default: 0 },
})
defineEmits(['clear'])
</script>

<style scoped>
.batch-bar {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
  margin-top: 8px;
  padding: 8px 12px;
  border: 1px solid var(--border-strong);
  border-radius: 8px;
  background: var(--accent-dim);
  font-size: 13px;
}
.bb-count b {
  color: var(--accent);
  font-variant-numeric: tabular-nums;
}
/* 把「取消选择」推到右侧：破坏性动作（删除/忽略）与它分开，避免误点 */
.bb-spacer {
  flex: 1;
}
</style>
