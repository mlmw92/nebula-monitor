<template>
  <div class="health-block">
    <div class="hb-top">
      <div class="ring" :class="'score-' + rank(health.score)" :style="{ '--p': clamp(health.score) }">
        <div class="ring-inner">
          <span class="score">{{ Math.round(health.score) }}</span>
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

    <!-- 四个构成项：把"扣分来自哪里"直接摆出来，比只给一个分数有用 -->
    <div class="hb-facts">
      <StatusPill v-for="f in facts" :key="f.key" :tone="f.tone" dot>
        {{ f.label }} {{ f.value }}
      </StatusPill>
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue'
import StatusPill from '../common/StatusPill.vue'

const props = defineProps({
  health: { type: Object, required: true },
})

const RANK = { muted: 0, ok: 1, warn: 2, danger: 3 }
const worse = (a, b) => (RANK[a] >= RANK[b] ? a : b)

// 四个构成项与 healthScore.js 的扣分口径一致：
//   可用性＝在线率；性能＝CPU/内存均值是否超阈值；容量＝磁盘均值；安全＝告警级别分布
const facts = computed(() => {
  const h = props.health || {}
  const total = h.total || 0
  const offline = h.offline || 0
  const avail = total ? ((total - offline) / total) * 100 : 0
  const pressure = h.pressure || []
  const byKey = (k) => pressure.find((p) => p.key === k) || null
  const cpu = byKey('cpu')
  const mem = byKey('mem')
  const disk = byKey('disk')

  const toneOf = (p) =>
    !p || !p.count ? 'muted' : p.badCount > 0 ? 'danger' : p.warnCount > 0 ? 'warn' : 'ok'
  const pct = (p) => (p && p.count ? Math.round(p.rate) + '%' : '—')

  const crit = h.criticalAlerts || 0
  const warn = h.warningAlerts || 0

  return [
    {
      key: 'avail',
      label: '可用性',
      value: total ? avail.toFixed(1) + '%' : '—',
      tone: !total ? 'muted' : offline === 0 ? 'ok' : offline / total >= 0.3 ? 'danger' : 'warn',
    },
    {
      key: 'perf',
      label: '性能',
      value: `CPU ${pct(cpu)} · 内存 ${pct(mem)}`,
      tone: worse(toneOf(cpu), toneOf(mem)),
    },
    {
      key: 'capacity',
      label: '容量',
      value: `磁盘 ${pct(disk)}`,
      tone: toneOf(disk),
    },
    {
      key: 'security',
      label: '安全',
      value: crit > 0 ? `${crit} 条紧急` : warn > 0 ? `${warn} 条警告` : '无告警',
      tone: crit > 0 ? 'danger' : warn > 0 ? 'warn' : 'ok',
    },
  ]
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
  flex-direction: column;
  justify-content: center;
  gap: 14px;
  height: 100%;
  min-height: 120px;
}
.hb-top {
  display: flex;
  align-items: center;
  gap: 16px;
}
.ring {
  --p: 0;
  width: 88px;
  height: 88px;
  border-radius: 50%;
  background: conic-gradient(var(--ring-color) calc(var(--p) * 1%), var(--s2) 0);
  display: flex;
  align-items: center;
  justify-content: center;
  position: relative;
  flex-shrink: 0;
}
/* 内圆 9px 收缩 = 9px 环宽；底色必须与卡片一致（s1），否则会看出一个"洞" */
.ring::before {
  content: '';
  position: absolute;
  inset: 9px;
  background: var(--s1);
  border-radius: 50%;
}
.ring-inner {
  position: relative;
  display: flex;
  align-items: baseline;
  gap: 2px;
}
.score {
  font-size: var(--fs-3xl);
  font-weight: 800;
  line-height: 1;
  font-family: var(--mono);
  letter-spacing: -0.02em;
}
.ring.score-good {
  --ring-color: var(--ok);
}
.ring.score-warn {
  --ring-color: var(--warn);
}
.ring.score-bad {
  --ring-color: var(--danger);
}
.ring.score-unknown {
  --ring-color: var(--t3);
}
.hb-meta {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}
.hb-status {
  font-size: var(--fs-lg);
  font-weight: 700;
}
.hb-status.c-good {
  color: var(--ok);
}
.hb-status.c-warn {
  color: var(--warn);
}
.hb-status.c-bad {
  color: var(--danger);
}
.hb-status.c-unknown {
  color: var(--t3);
}
.hb-sub {
  font-size: var(--fs-sm);
  color: var(--t2);
}
.hb-sub .off {
  color: var(--danger);
}
.hb-facts {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
</style>
