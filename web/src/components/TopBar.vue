<template>
  <header class="topbar">
    <div class="brand">
      <span class="logo-glyph">◈</span>
      <span class="brand-name">NebulaEye</span>
      <span class="brand-sub">监控指挥中心</span>
    </div>
    <nav class="nav">
      <button
        v-for="v in views"
        :key="v.key"
        class="nav-btn"
        :class="{ active: current === v.key }"
        @click="$emit('navigate', v.key)"
      >
        {{ v.label }}
      </button>
    </nav>
    <div class="topbar-right">
      <span class="clock">{{ clock }}</span>
      <button class="bell" @click="$emit('navigate', 'alerts')">
        🔔<span v-if="alertCount" class="badge">{{ alertCount }}</span>
      </button>
    </div>
  </header>
</template>

<script setup>
import { ref, onMounted, onUnmounted } from 'vue'

defineProps({ current: String, alertCount: Number })
defineEmits(['navigate'])

const views = [
  { key: 'overview', label: '集群总览' },
  { key: 'node', label: '主机详情' },
  { key: 'alerts', label: '告警中心' },
  { key: 'manage', label: '节点管理' },
]

const clock = ref('')
let timer = null
function tick() {
  clock.value = new Date().toLocaleTimeString('zh-CN', { hour12: false })
}
onMounted(() => {
  tick()
  timer = setInterval(tick, 1000)
})
onUnmounted(() => clearInterval(timer))
</script>
