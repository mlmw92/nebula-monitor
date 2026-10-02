<template>
  <div class="empty-state">
    <div v-if="icon" class="es-icon">
      <img v-if="typeof icon === 'string'" :src="icon" alt="" />
      <el-icon v-else :size="32"><component :is="icon" /></el-icon>
    </div>
    <div v-if="title" class="es-title">{{ title }}</div>
    <div v-if="hints && hints.length" class="es-hints">
      <p v-for="(h, i) in hints" :key="i" class="es-hint">{{ h }}</p>
    </div>
    <el-button v-if="actionText" class="es-action" size="small" @click="$emit('action')">
      {{ actionText }}
    </el-button>
  </div>
</template>

<script setup>
// 全站统一空状态。
// 设计要点：空状态不能只写"暂无数据"，必须告诉用户下一步做什么，
// 所以 hints 是排查建议（"检查 X / 前往 Y"），而不是同义反复。
defineProps({
  icon: { type: [Object, String], default: null },
  title: { type: String, default: '' },
  hints: { type: Array, default: () => [] },
  actionText: { type: String, default: '' },
})
defineEmits(['action'])
</script>

<style scoped>
.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  padding: 32px 16px;
  text-align: center;
}
.es-icon {
  color: var(--t3);
  margin-bottom: 4px;
  display: flex;
  align-items: center;
  justify-content: center;
}
.es-icon img {
  width: 32px;
  height: 32px;
  object-fit: contain;
  opacity: 0.6;
}
.es-title {
  font-size: var(--fs-base);
  font-weight: 600;
  color: var(--t2);
}
.es-hints {
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-width: 420px;
}
.es-hint {
  font-size: var(--fs-sm);
  color: var(--t3);
  line-height: 1.6;
}
.es-action {
  margin-top: 8px;
}
</style>
