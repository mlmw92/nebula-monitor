<template>
  <div class="crit-list">
    <div v-for="a in alerts" :key="a.id" class="crit-item" @click="goAlerts">
      <span class="dot" :class="'sev-' + (a.severity || '').toLowerCase()"></span>
      <div class="crit-body">
        <div class="crit-title">{{ a.ruleName }}</div>
        <div class="crit-sub">{{ a.node }} · {{ timeAgo(a.startsAt) }}</div>
      </div>
    </div>
    <div v-if="!alerts || alerts.length === 0" class="crit-empty">
      <span class="ok-icon">✓</span> 当前无紧急告警
    </div>
  </div>
</template>

<script setup>
import { useRouter } from 'vue-router'
const props = defineProps({
  alerts: { type: Array, default: () => [] },
})
const router = useRouter()

function timeAgo(t) {
  if (!t) return ''
  const d = new Date(t)
  if (isNaN(d)) return ''
  const s = Math.floor((Date.now() - d.getTime()) / 1000)
  if (s < 60) return s + ' 秒前'
  if (s < 3600) return Math.floor(s / 60) + ' 分钟前'
  if (s < 86400) return Math.floor(s / 3600) + ' 小时前'
  return Math.floor(s / 86400) + ' 天前'
}
function goAlerts() {
  router.push('/alerts')
}
</script>

<style scoped>
.crit-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  height: 100%;
  overflow: auto;
}
.crit-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  background: var(--bg-elev);
  border-radius: 8px;
  cursor: pointer;
  transition: background 0.2s;
}
.crit-item:hover {
  background: var(--accent-dim);
}
.dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}
.sev-critical {
  background: var(--danger);
  box-shadow: 0 0 8px var(--danger-dim);
}
.sev-warning {
  background: var(--warn);
}
.sev-info {
  background: var(--info);
}
.crit-body {
  min-width: 0;
}
.crit-title {
  font-size: 13px;
  font-weight: 600;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.crit-sub {
  font-size: 11px;
  color: var(--text-dim);
}
.crit-empty {
  display: flex;
  align-items: center;
  gap: 8px;
  justify-content: center;
  height: 100%;
  color: var(--chart-green);
  font-size: 13px;
}
</style>
