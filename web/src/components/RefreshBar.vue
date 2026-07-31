<template>
  <div class="refresh-bar">
    <div class="rb-left">
      <el-switch v-model="auto" size="small" @change="restart" />
      <span class="rb-label">自动刷新</span>
      <el-select
        v-if="intervals && intervals.length"
        v-model="interval"
        size="small"
        style="width: 92px"
        @change="onIntervalChange"
      >
        <el-option v-for="o in intervals" :key="o.value" :label="o.label" :value="o.value" />
      </el-select>
      <span v-if="lastUpdate" class="rb-time">更新于 {{ lastUpdate }}</span>
    </div>
    <el-button size="small" :icon="RefreshIcon" :loading="loading" @click="trigger">刷新</el-button>
  </div>
</template>

<script setup>
import { ref, watch, onMounted, onUnmounted } from 'vue'
import { Refresh as RefreshIcon } from '@element-plus/icons-vue'

const props = defineProps({
  loading: { type: Boolean, default: false },
  intervals: { type: Array, default: null },
  interval: { type: Number, default: 15 },
})
const emit = defineEmits(['refresh', 'update:interval'])

const auto = ref(true)
const interval = ref(props.interval)
const lastUpdate = ref('')
let timer = null

watch(() => props.interval, (v) => { interval.value = v })

function trigger() {
  lastUpdate.value = new Date().toLocaleTimeString()
  emit('refresh')
}
function onIntervalChange(v) {
  emit('update:interval', v)
  restart()
}
function start() {
  stop()
  if (auto.value) timer = setInterval(trigger, interval.value * 1000)
}
function stop() {
  if (timer) { clearInterval(timer); timer = null }
}
function restart() { start() }

onMounted(start)
onUnmounted(stop)
</script>

<style scoped>
.refresh-bar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 16px;
}
.rb-left {
  display: flex;
  align-items: center;
  gap: 8px;
}
.rb-label {
  font-size: 13px;
  color: var(--text-dim);
}
.rb-time {
  font-size: 12px;
  color: var(--text-muted);
}
</style>
