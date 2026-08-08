<template>
  <div class="glass panel-mini screen-health">
    <div class="sh-title">服务健康评分</div>
    <div class="sh-body">
      <div class="sh-hex" :class="level">
        <svg viewBox="0 0 120 120">
          <polygon :points="hexPoints" class="sh-hex-bg" />
          <polygon :points="hexPoints" class="sh-hex-line" :stroke="hexColor" />
        </svg>
        <div class="sh-hex-center">
          <span class="sh-num" :style="{ color: hexColor }">{{ score }}</span>
          <span class="sh-unit">分</span>
          <span class="sh-lv">{{ levelLabel }}</span>
        </div>
      </div>
      <div class="sh-bars">
        <div class="sh-bar" v-for="b in bars" :key="b.key">
          <span class="sh-bar-l">{{ b.label }}</span>
          <span class="sh-bar-track">
            <span class="sh-bar-fill" :style="{ width: b.value + '%', background: b.color }"></span>
          </span>
          <b class="sh-bar-v mono">{{ b.value }}</b>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue'

const props = defineProps({
  score: { type: Number, default: 100 },
  onlineRate: { type: Number, default: 0 },
  alertFreeRate: { type: Number, default: 100 }, // 无告警率
  cpuHeadroom: { type: Number, default: 100 },   // CPU 余量 = 100 - 均值
  diskHeadroom: { type: Number, default: 100 },  // 磁盘余量 = 100 - 均值
})

const level = computed(() => {
  if (props.score >= 90) return 'green'
  if (props.score >= 70) return 'amber'
  return 'red'
})
const levelLabel = computed(() => ({ green: '优秀', amber: '一般', red: '风险' }[level.value]))
const hexColor = computed(() => ({ green: 'var(--accent)', amber: 'var(--warn)', red: 'var(--danger)' }[level.value]))

// 正六边形顶点（中心 60,60，半径 52，尖角朝上）
const hexPoints = computed(() => {
  const cx = 60, cy = 60, r = 52
  const pts = []
  for (let i = 0; i < 6; i++) {
    const a = (Math.PI / 180) * (60 * i - 90)
    pts.push(`${(cx + r * Math.cos(a)).toFixed(1)},${(cy + r * Math.sin(a)).toFixed(1)}`)
  }
  return pts.join(' ')
})

function barColor(v) {
  if (v >= 90) return 'var(--accent)'
  if (v >= 70) return 'var(--warn)'
  return 'var(--danger)'
}
const bars = computed(() => [
  { key: 'online', label: '在线率', value: Math.round(props.onlineRate), color: barColor(props.onlineRate) },
  { key: 'alertFree', label: '告警健康度', value: Math.round(props.alertFreeRate), color: barColor(props.alertFreeRate) },
  { key: 'cpu', label: 'CPU 余量', value: Math.round(props.cpuHeadroom), color: barColor(props.cpuHeadroom) },
  { key: 'disk', label: '磁盘余量', value: Math.round(props.diskHeadroom), color: barColor(props.diskHeadroom) },
])
</script>

<style scoped>
.screen-health {
  padding: 12px 14px;
  display: flex;
  flex-direction: column;
}
.sh-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 8px;
}
.sh-body {
  display: flex;
  align-items: center;
  gap: 14px;
  flex: 1;
}
.sh-hex {
  position: relative;
  width: 110px;
  height: 110px;
  flex-shrink: 0;
}
.sh-hex svg { width: 100%; height: 100%; }
.sh-hex-bg {
  fill: rgba(56, 189, 248, 0.06);
  stroke: rgba(255, 255, 255, 0.08);
  stroke-width: 1;
}
.sh-hex-line {
  fill: none;
  stroke-width: 2.5;
  filter: drop-shadow(0 0 6px currentColor);
  opacity: 0.9;
}
.sh-hex-center {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 0;
}
.sh-num {
  font-size: 30px;
  font-weight: 800;
  font-family: var(--mono);
  line-height: 1;
}
.sh-unit {
  font-size: 11px;
  color: var(--text-dim);
}
.sh-lv {
  font-size: 11px;
  color: var(--text-dim);
  margin-top: 3px;
}
.sh-bars {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 9px;
  min-width: 0;
}
.sh-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
}
.sh-bar-l {
  width: 56px;
  flex-shrink: 0;
  color: var(--text-dim);
}
.sh-bar-track {
  flex: 1;
  height: 6px;
  background: rgba(255, 255, 255, 0.06);
  border-radius: 3px;
  overflow: hidden;
}
.sh-bar-fill {
  display: block;
  height: 100%;
  border-radius: 3px;
  transition: width 0.6s ease;
}
.sh-bar-v {
  width: 28px;
  flex-shrink: 0;
  text-align: right;
  color: var(--text);
}
.sh-bar-l {
  white-space: nowrap;
}
</style>
