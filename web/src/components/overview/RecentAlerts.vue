<template>
  <div class="recent-list">
    <div v-for="a in alerts" :key="a.id" class="recent-item" @click="goAlerts">
      <span class="dot" :class="'sev-' + (a.severity || '').toLowerCase()"></span>
      <div class="recent-body">
        <div class="recent-title">{{ a.ruleName }}</div>
        <div class="recent-sub">{{ a.node }} · {{ timeAgo(a.startsAt) }}</div>
      </div>
      <span class="recent-state" :class="'st-' + (a.state || '').toLowerCase()">{{ stateText(a.state) }}</span>
    </div>
    <div v-if="!alerts || alerts.length === 0" class="recent-empty">
      <span class="ok-icon">✓</span> 暂无告警记录
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
function stateText(s) {
  if (s === 'active') return '活跃'
  if (s === 'acked') return '已确认'
  if (s === 'resolved') return '已恢复'
  return s || '-'
}
function goAlerts() {
  router.push('/alerts')
}
</script>

<style scoped>
.recent-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  max-height: 320px;
  overflow: auto;
}
.recent-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  background: var(--bg-soft);
  border-radius: 8px;
  cursor: pointer;
  transition: background 0.2s;
}
.recent-item:hover {
  background: var(--bg-hover);
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
  background: var(--warning);
}
.sev-info {
  background: var(--info);
}
.recent-body {
  flex: 1;
  min-width: 0;
}
.recent-title {
  font-size: 13px;
  font-weight: 600;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.recent-sub {
  font-size: 11px;
  color: var(--text-dim);
}
.recent-state {
  font-size: 11px;
  padding: 2px 8px;
  border-radius: 10px;
  background: var(--bg-card);
  color: var(--text-dim);
  flex-shrink: 0;
}
.recent-state.st-active {
  color: var(--danger);
  background: var(--danger-dim);
}
.recent-state.st-acked {
  color: var(--warning);
}
.recent-state.st-resolved {
  color: var(--success);
}
.recent-empty {
  display: flex;
  align-items: center;
  gap: 8px;
  justify-content: center;
  padding: 30px;
  color: var(--success);
  font-size: 13px;
}
</style>
