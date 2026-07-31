<template>
  <div class="glass panel-mini screen-alertlevels">
    <div class="al-title">告警级别统计</div>
    <div class="al-list">
      <div class="al-row" v-for="r in rows" :key="r.key">
        <span class="al-dot" :style="{ background: r.color }"></span>
        <span class="al-name">{{ r.label }}</span>
        <span class="al-track">
          <span class="al-fill" :style="{ width: r.pct + '%', background: r.color }"></span>
        </span>
        <b class="al-val mono">{{ r.value }}</b>
      </div>
    </div>
    <div class="al-foot">
      活跃告警 <b class="mono">{{ total }}</b> 条 · 今日新增 <b class="mono">{{ todayCount }}</b>
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue'

const props = defineProps({
  alerts: { type: Array, default: () => [] }, // 活跃(firing)告警
})

const counts = computed(() => {
  const c = { critical: 0, warning: 0, info: 0 }
  for (const a of props.alerts) {
    if (a.severity === 'critical') c.critical += 1
    else if (a.severity === 'warning') c.warning += 1
    else c.info += 1
  }
  return c
})
const total = computed(() => props.alerts.length)
const max = computed(() => Math.max(1, counts.value.critical, counts.value.warning, counts.value.info))

const todayCount = computed(() => {
  const start = new Date()
  start.setHours(0, 0, 0, 0)
  const t0 = start.getTime()
  return props.alerts.filter((a) => a.startsAt && new Date(a.startsAt).getTime() >= t0).length
})

const rows = computed(() => [
  { key: 'critical', label: '严重', value: counts.value.critical, color: 'var(--danger)' },
  { key: 'warning', label: '警告', value: counts.value.warning, color: 'var(--warn)' },
  { key: 'info', label: '提示', value: counts.value.info, color: 'var(--info)' },
].map((r) => ({ ...r, pct: Math.round((r.value / max.value) * 100) })))
</script>

<style scoped>
.screen-alertlevels {
  padding: 12px 14px;
  display: flex;
  flex-direction: column;
}
.al-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 12px;
}
.al-list {
  flex: 1;
  display: flex;
  flex-direction: column;
  justify-content: center;
  gap: 14px;
}
.al-row {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
}
.al-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}
.al-name {
  width: 36px;
  flex-shrink: 0;
  color: var(--text);
}
.al-track {
  flex: 1;
  height: 8px;
  background: rgba(255, 255, 255, 0.06);
  border-radius: 4px;
  overflow: hidden;
}
.al-fill {
  display: block;
  height: 100%;
  border-radius: 4px;
  transition: width 0.6s ease;
}
.al-val {
  width: 34px;
  text-align: right;
  font-weight: 700;
  color: var(--text);
}
.al-foot {
  margin-top: 12px;
  padding-top: 8px;
  border-top: 1px solid rgba(255, 255, 255, 0.06);
  font-size: 12px;
  color: var(--text-dim);
}
.al-foot b {
  color: var(--text);
}
</style>
