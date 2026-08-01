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
      <div v-if="!reasons || reasons.length === 0" class="hb-ok">
        <el-icon :size="14"><CircleCheckFilled /></el-icon>
        <span>所有指标运行正常</span>
      </div>
      <ul v-else class="hb-reasons">
        <li v-for="r in reasons" :key="r.key" :class="'r-' + r.severity">
          <span class="r-dot"></span>
          <span class="r-label">{{ r.label }}</span>
          <span class="r-value">{{ r.value }}</span>
        </li>
      </ul>
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue'
import { CircleCheckFilled } from '@element-plus/icons-vue'

const props = defineProps({
  health: { type: Object, required: true },
})

// 派生"扣分原因"列表：与 OverviewView 的 score 计算口径严格一致
const reasons = computed(() => {
  const list = []
  const h = props.health
  if (!h) return list

  // 1. 离线主机（最关键）
  if (h.offline > 0) {
    list.push({
      key: 'offline',
      severity: h.offline / Math.max(1, h.total) >= 0.3 ? 'bad' : 'warn',
      label: '主机离线',
      value: h.offline + ' 台未上报',
    })
  }

  // 2. 资源压力：只有超过告警阈值才展示（与 score 扣分条件一致）
  const pressure = h.pressure || []
  pressure.forEach((p) => {
    if (!p || typeof p.avgOver !== 'number' || isNaN(p.avgOver) || p.avgOver <= 0) return
    const isBad = p.badCount > 0
    const severity = isBad ? 'bad' : 'warn'
    let valueText = Math.round(p.avgOver) + '% 平均超阈值'
    if (p.count > 1) {
      valueText = `${p.warnCount}/${p.count} 台偏高`
    }
    list.push({
      key: p.key,
      severity,
      label: p.label + (isBad ? ' 严重偏高' : ' 平均偏高'),
      value: valueText,
    })
  })

  // 3. 告警联动
  if (h.criticalAlerts > 0) {
    list.push({
      key: 'crit',
      severity: 'bad',
      label: '紧急告警',
      value: h.criticalAlerts + ' 条',
    })
  }
  if (h.warningAlerts > 0) {
    list.push({
      key: 'warn',
      severity: 'warn',
      label: '警告告警',
      value: h.warningAlerts + ' 条',
    })
  }

  return list.slice(0, 4)
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
  gap: 18px;
  height: 100%;
  min-height: 120px;
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
  min-width: 0;
  display: flex;
  align-items: center;
}
.hb-ok {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--chart-green);
  font-size: 13px;
  font-weight: 600;
}
.hb-reasons {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
  width: 100%;
}
.hb-reasons li {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  padding: 5px 8px;
  background: var(--bg-elev);
  border-radius: 6px;
  border-left: 2px solid var(--text-muted);
}
.hb-reasons li.r-warn {
  border-left-color: var(--warn);
}
.hb-reasons li.r-bad {
  border-left-color: var(--danger);
}
.r-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--text-muted);
  flex-shrink: 0;
}
.hb-reasons li.r-warn .r-dot {
  background: var(--warn);
  box-shadow: 0 0 6px var(--warn);
}
.hb-reasons li.r-bad .r-dot {
  background: var(--danger);
  box-shadow: 0 0 6px var(--danger);
}
.r-label {
  color: var(--text);
  flex: 1;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.r-value {
  color: var(--text-dim);
  font-weight: 700;
  font-variant-numeric: tabular-nums;
}
.hb-reasons li.r-warn .r-value {
  color: var(--warn);
}
.hb-reasons li.r-bad .r-value {
  color: var(--danger);
}
</style>
