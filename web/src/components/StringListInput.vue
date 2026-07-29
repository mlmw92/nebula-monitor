<template>
  <div class="str-list">
    <div class="row" v-for="(v, i) in modelValue" :key="i">
      <el-input
        v-model="modelValue[i]"
        :placeholder="placeholder"
        :type="secret ? 'password' : 'text'"
        :show-password="secret"
        size="default"
        clearable
      />
      <el-button type="danger" link :icon="Delete" @click="remove(i)" />
    </div>
    <el-button type="primary" link :icon="Plus" @click="add">{{ addText }}</el-button>
  </div>
</template>

<script setup>
import { Delete, Plus } from '@element-plus/icons-vue'

const props = defineProps({
  modelValue: { type: Array, default: () => [] },
  placeholder: { type: String, default: '' },
  secret: { type: Boolean, default: false },
  addText: { type: String, default: '添加' },
})
const emit = defineEmits(['update:modelValue'])

function add() {
  emit('update:modelValue', [...props.modelValue, ''])
}
function remove(i) {
  emit('update:modelValue', props.modelValue.filter((_, idx) => idx !== i))
}
</script>

<style scoped>
.str-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.row {
  display: flex;
  align-items: center;
  gap: 8px;
}
.row .el-input {
  flex: 1;
}
</style>
