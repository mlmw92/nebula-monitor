<template>
  <div class="mw-grid">
    <div
      v-for="s in summaries"
      :key="s.key"
      class="mw-card"
      :class="{ disabled: s.total === 0 }"
      @click="goTab(s.tab)"
    >
      <div class="mw-head">
        <img class="mw-icon" :src="s.icon" :alt="s.label" />
        <span class="mw-name">{{ s.label }}</span>
        <span class="mw-total">{{ s.total }}</span>
      </div>

      <div class="mw-body">
        <div class="mw-donut" :style="donutStyle(s)">
          <div class="mw-donut-inner">
            <span class="mw-online">{{ s.online }}</span>
            <span class="mw-donut-label">在线</span>
          </div>
        </div>
        <div class="mw-meta">
          <div class="mw-stat">
            <span class="mw-stat-val off" v-if="s.offline">{{ s.offline }}</span>
            <span class="mw-stat-val ok" v-else>0</span>
            <span class="mw-stat-label">离线/异常</span>
          </div>
          <div class="mw-top">
            <div class="mw-top-title">Top 实例</div>
            <div v-for="(t, i) in s.topN" :key="i" class="mw-top-row">
              <span class="mw-top-label" :title="t.label">{{ t.label }}</span>
              <span class="mw-top-val">{{ t.valueText }}</span>
            </div>
            <div v-if="s.topN.length === 0" class="mw-top-empty">暂无实例</div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { useRouter } from 'vue-router'

const props = defineProps({
  summaries: { type: Array, default: () => [] },
})
const router = useRouter()

function donutStyle(s) {
  const onlinePct = s.total > 0 ? (s.online / s.total) * 100 : 0
  return {
    background: `conic-gradient(var(--chart-green) 0 ${onlinePct}%, var(--danger) ${onlinePct}% 100%)`,
  }
}
function goTab(tab) {
  router.push({ path: '/middleware', query: { tab } })
}
</script>

<style scoped>
.mw-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 14px;
}
.mw-card {
  background: var(--bg-elev);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 14px;
  cursor: pointer;
  transition: transform 0.15s, border-color 0.15s, box-shadow 0.15s;
}
.mw-card:hover {
  transform: translateY(-2px);
  border-color: var(--accent);
  box-shadow: 0 6px 18px rgba(0, 0, 0, 0.25);
}
.mw-card.disabled {
  opacity: 0.55;
  cursor: default;
}
.mw-card.disabled:hover {
  transform: none;
  border-color: var(--border);
  box-shadow: none;
}
.mw-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
}
.mw-icon {
  width: 20px;
  height: 20px;
  object-fit: contain;
}
.mw-name {
  font-size: 14px;
  font-weight: 700;
  flex: 1;
}
.mw-total {
  font-size: 12px;
  color: var(--text-dim);
  background: var(--bg-card);
  border-radius: 10px;
  padding: 1px 8px;
}
.mw-body {
  display: flex;
  align-items: center;
  gap: 14px;
}
.mw-donut {
  width: 64px;
  height: 64px;
  border-radius: 50%;
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  position: relative;
}
.mw-donut::before {
  content: '';
  position: absolute;
  inset: 6px;
  background: var(--bg-elev);
  border-radius: 50%;
}
.mw-donut-inner {
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: center;
}
.mw-online {
  font-size: 18px;
  font-weight: 800;
  line-height: 1;
}
.mw-donut-label {
  font-size: 10px;
  color: var(--text-dim);
}
.mw-meta {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.mw-stat {
  display: flex;
  flex-direction: column;
}
.mw-stat-val {
  font-size: 16px;
  font-weight: 700;
}
.mw-stat-val.ok {
  color: var(--chart-green);
}
.mw-stat-val.off {
  color: var(--danger);
}
.mw-stat-label {
  font-size: 11px;
  color: var(--text-dim);
}
.mw-top-title {
  font-size: 11px;
  color: var(--text-dim);
  margin-bottom: 4px;
}
.mw-top-row {
  display: flex;
  justify-content: space-between;
  font-size: 11px;
  padding: 1px 0;
}
.mw-top-label {
  color: var(--text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 90px;
}
.mw-top-val {
  color: var(--accent);
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}
.mw-top-empty {
  font-size: 11px;
  color: var(--text-muted);
}
</style>
