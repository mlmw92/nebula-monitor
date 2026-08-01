<template>
  <div class="health-block">
    <div class="hb-left">
      <div class="ring" :class="'score-' + rank(health.score)" :style="{ '--p': clamp(health.score) }">
        <div class="ring-inner">
          <span class="score">{{ Math.round(health.score) }}</span>
          <span class="score-unit">分</span>
        </div>
      </div>
      <div class="hb-meta">
        <div class="hb-status" :class="'c-' + rank(health.score)">{{ health.statusText }}</div>
        <div class="hb-sub">
          在线 {{ health.online }} / 共 {{ health.total }}
          <template v-if="health.offline"> · <span class="off">离线 {{ health.offline }}</span></template>
        </div>
      </div>
    </div>
    <div class="hb-right">
      <div v-for="p in health.parts" :key="p.label" class="hb-metric">
        <div class="hb-metric-top">
          <span class="hb-label">{{ p.label }}</span>
          <span class="hb-val" :style="{ color: p.color }">{{ p.rate }}%</span>
        </div>
        <div class="hb-bar">
          <div class="hb-bar-fill" :style="{ width: clamp(p.rate) + '%', background: p.color }"></div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
const props = defineProps({
  health: { type: Object, required: true },
})

function clamp(v) {
  const n = Number(v)
  if (isNaN(n)) return 0
  return Math.max(0, Math.min(100, n))
}
function rank(score) {
  const s = Number(score)
  if (isNaN(s)) return 'unknown'
  if (s >= 90) return 'good'
  if (s >= 70) return 'warn'
  return 'bad'
}
</script>

<style scoped>
.health-block {
  display: flex;
  align-items: center;
  gap: 20px;
  height: 100%;
}
.hb-left {
  display: flex;
  align-items: center;
  gap: 14px;
  flex-shrink: 0;
}
.ring {
  --p: 0;
  width: 92px;
  height: 92px;
  border-radius: 50%;
  background: conic-gradient(var(--ring-color) calc(var(--p) * 1%), var(--bg-elev) 0);
  display: flex;
  align-items: center;
  justify-content: center;
  position: relative;
  flex-shrink: 0;
}
.ring::before {
  content: '';
  position: absolute;
  inset: 8px;
  background: var(--bg-card);
  border-radius: 50%;
}
.ring-inner {
  position: relative;
  display: flex;
  align-items: baseline;
  gap: 2px;
}
.score {
  font-size: 30px;
  font-weight: 800;
  line-height: 1;
}
.score-unit {
  font-size: 12px;
  color: var(--text-dim);
}
.ring.score-good {
  --ring-color: var(--chart-green);
}
.ring.score-warn {
  --ring-color: var(--warn);
}
.ring.score-bad {
  --ring-color: var(--danger);
}
.ring.score-unknown {
  --ring-color: var(--text-muted);
}
.hb-meta {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.hb-status {
  font-size: 16px;
  font-weight: 700;
}
.hb-status.c-good {
  color: var(--chart-green);
}
.hb-status.c-warn {
  color: var(--warn);
}
.hb-status.c-bad {
  color: var(--danger);
}
.hb-status.c-unknown {
  color: var(--text-muted);
}
.hb-sub {
  font-size: 12px;
  color: var(--text-dim);
}
.hb-sub .off {
  color: var(--danger);
}
.hb-right {
  flex: 1;
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 10px 18px;
  min-width: 0;
}
.hb-metric-top {
  display: flex;
  justify-content: space-between;
  font-size: 12px;
  margin-bottom: 4px;
}
.hb-label {
  color: var(--text-dim);
}
.hb-val {
  font-weight: 700;
}
.hb-bar {
  height: 5px;
  border-radius: 3px;
  background: var(--bg-elev);
  overflow: hidden;
}
.hb-bar-fill {
  height: 100%;
  border-radius: 3px;
  transition: width 0.4s ease;
}
</style>
