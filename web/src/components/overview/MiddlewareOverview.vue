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
        <span v-if="s.total > 0" class="mw-total">{{ s.total }}</span>
        <span v-else class="mw-badge">未配置</span>
      </div>

      <div v-if="s.total > 0" class="mw-body">
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
          </div>
        </div>
      </div>

      <div v-else class="mw-empty">
        <el-icon :size="20"><InfoFilled /></el-icon>
        <div class="mw-empty-title">尚未配置监控</div>
        <div class="mw-empty-sub">点击前往 {{ s.label }} 页面了解接入方式</div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { InfoFilled } from '@element-plus/icons-vue'
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
  display: flex;
  flex-direction: column;
}
.mw-card:hover {
  transform: translateY(-2px);
  border-color: var(--accent);
  box-shadow: 0 6px 18px rgba(0, 0, 0, 0.25);
}
.mw-card.disabled {
  opacity: 0.78;
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
.mw-badge {
  font-size: 10px;
  font-weight: 600;
  color: var(--text-muted);
  background: var(--bg-card);
  border: 1px dashed var(--border);
  border-radius: 10px;
  padding: 1px 7px;
  letter-spacing: 0.04em;
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
.mw-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  padding: 18px 0 8px;
  color: var(--text-muted);
  text-align: center;
}
.mw-empty-title {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-dim);
}
.mw-empty-sub {
  font-size: 10px;
  color: var(--text-muted);
  line-height: 1.5;
}
</style>
