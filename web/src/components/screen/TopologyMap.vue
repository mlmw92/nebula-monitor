<template>
  <div ref="wrap" class="topo-wrap">
    <svg class="topo-svg" :viewBox="`0 0 ${VB.w} ${VB.h}`" preserveAspectRatio="xMidYMid meet">
      <!-- 连线（中心 -> 各节点） -->
      <g class="links">
        <path
          v-for="n in placedNodes"
          :key="'ln-' + n.id"
          :d="linkPath(n)"
          class="link"
          :class="n.tone"
          fill="none"
        />
        <!-- 流光点：沿连线运动 -->
        <circle
          v-for="n in placedNodes"
          :key="'fl-' + n.id"
          r="2.6"
          class="flow"
          :class="n.tone"
        >
          <animateMotion :dur="n.tone === 'danger' ? '3.2s' : '2s'" repeatCount="indefinite" :path="linkPath(n)" />
        </circle>
      </g>

    </svg>

    <!-- 节点框（DOM 覆盖层，百分比定位） -->
    <div
      v-for="n in placedNodes"
      :key="'box-' + n.id"
      class="topo-node glass"
      :class="[n.tone, { alerting: n.alerting }]"
      :style="{ left: n.px + '%', top: n.py + '%' }"
      @click="n.onClick && n.onClick()"
      @mouseenter="hover = n"
      @mouseleave="hover = null"
    >
      <div class="tn-title">
        <span class="tn-dot" :class="n.tone"></span>
        {{ n.title }}
      </div>
      <div class="tn-meta">{{ n.meta }}</div>
    </div>

    <!-- hover 提示 -->
    <div v-if="hover" class="topo-tip glass" :style="{ left: hover.px + '%', top: hover.py + '%' }">
      <div class="tt-title">{{ hover.title }}</div>
      <div class="tt-line" v-for="(l, i) in hover.tip" :key="i">{{ l }}</div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed } from 'vue'
import { useRouter } from 'vue-router'

const props = defineProps({
  nodes: { type: Array, default: () => [] },
  metrics: { type: Object, default: () => ({}) },
  alerts: { type: Array, default: () => [] },
  redisStats: { type: Object, default: () => ({}) }, // { total, up, down, clusterCount, alertCount }
  dockerStats: { type: Object, default: () => ({}) }, // { total, running, stopped, abnormal }
})
const emit = defineEmits(['select-node', 'select-redis', 'select-docker'])
const router = useRouter()

// viewBox 逻辑坐标系（与 DOM 百分比定位共用同一比例）
const VB = { w: 1000, h: 640 }
const CX = VB.w / 2
const CY = VB.h / 2

const hover = ref(null)

// 主机按 group 聚合
const hostGroups = computed(() => {
  const map = new Map()
  for (const n of props.nodes) {
    const g = n.group || '默认分组'
    const item = map.get(g) || { name: g, total: 0, online: 0, offline: 0, alerting: 0 }
    item.total += 1
    if (n.status === 'online') item.online += 1
    else item.offline += 1
    if (props.alerts.some((a) => a.node === n.hostname && a.state === 'firing')) item.alerting += 1
    map.set(g, item)
  }
  return [...map.values()]
})

const firing = computed(() => props.alerts.filter((a) => a.state === 'firing'))
const critCount = computed(() => firing.value.filter((a) => a.severity === 'critical').length)
const warnCount = computed(() => firing.value.filter((a) => a.severity === 'warning').length)

// 组装环绕节点：主机组 + Redis + 告警，并按圆周均匀布点
const placedNodes = computed(() => {
  const items = []

  // 主机组（最多取 6 个，其余合并计数在最后一个上体现总量）
  const groups = hostGroups.value.slice(0, 6)
  groups.forEach((g, i) => {
    let tone = 'ok'
    if (g.offline > 0) tone = 'danger'
    else if (g.alerting > 0) tone = 'warn'
    items.push({
      id: 'g' + i,
      kind: 'group',
      title: g.name,
      meta: `在线 ${g.online} / 离线 ${g.offline}`,
      tone,
      alerting: g.alerting > 0,
      tip: [`主机 ${g.total} 台`, `在线 ${g.online} · 离线 ${g.offline}`, g.alerting ? `告警 ${g.alerting}` : '无告警'],
      onClick: () => router.push('/hosts'),
    })
  })

  // Redis 中间件节点
  const rs = props.redisStats || {}
  if (rs.total) {
    let tone = 'ok'
    if (rs.down > 0) tone = 'danger'
    else if (rs.alertCount > 0) tone = 'warn'
    items.push({
      id: 'redis',
      kind: 'redis',
      title: 'Redis 集群',
      meta: `实例 ${rs.up || 0}/${rs.total} · 组 ${rs.clusterCount || 0}`,
      tone,
      alerting: (rs.alertCount || 0) > 0,
      tip: [`实例 ${rs.total} 个`, `在线 ${rs.up || 0} · 离线 ${rs.down || 0}`, `集群/哨兵 ${rs.clusterCount || 0} 组`],
      onClick: () => emit('select-redis'),
    })
  }

  // Docker 容器节点
  const ds = props.dockerStats || {}
  if (ds.total) {
    let tone = 'ok'
    if ((ds.abnormal || 0) > 0) tone = 'danger'
    else if ((ds.stopped || 0) > 0) tone = 'warn'
    items.push({
      id: 'docker',
      kind: 'docker',
      title: '容器集群',
      meta: `运行 ${ds.running || 0}/${ds.total}`,
      tone,
      alerting: (ds.abnormal || 0) > 0,
      tip: [`容器 ${ds.total} 个`, `运行 ${ds.running || 0} · 停止 ${ds.stopped || 0}`, (ds.abnormal || 0) ? `异常 ${ds.abnormal}` : '无异常'],
      onClick: () => emit('select-docker'),
    })
  }

  // 安全/告警节点
  const alertTone = critCount.value > 0 ? 'danger' : warnCount.value > 0 ? 'warn' : 'ok'
  items.push({
    id: 'alerts',
    kind: 'alerts',
    title: '告警中心',
    meta: `活跃 ${firing.value.length} 条`,
    tone: alertTone,
    alerting: firing.value.length > 0,
    tip: [`活跃告警 ${firing.value.length} 条`, `严重 ${critCount.value} · 警告 ${warnCount.value}`],
    onClick: () => router.push('/alerts'),
  })

  // 圆周布点：椭圆半径，避免与四周面板重叠
  const rx = 360
  const ry = 210
  const n = items.length
  items.forEach((it, i) => {
    // 从正上方开始顺时针，留出上下均衡
    const ang = -Math.PI / 2 + (i / n) * Math.PI * 2
    const x = CX + rx * Math.cos(ang)
    const y = CY + ry * Math.sin(ang)
    it.x = x
    it.y = y
    it.px = (x / VB.w) * 100
    it.py = (y / VB.h) * 100
  })
  return items
})

// 中心到节点的三次贝塞尔曲线
function linkPath(n) {
  const mx = (CX + n.x) / 2
  const my = (CY + n.y) / 2
  // 控制点向中心法线方向偏移，形成柔和弧线
  const dx = n.x - CX
  const dy = n.y - CY
  const c1x = CX + dx * 0.3
  const c1y = CY + dy * 0.05
  const c2x = mx
  const c2y = my
  return `M ${CX} ${CY} C ${c1x} ${c1y}, ${c2x} ${c2y}, ${n.x} ${n.y}`
}
</script>

<style scoped>
.topo-wrap {
  position: absolute;
  inset: 0;
  z-index: 1;
}
.topo-svg {
  width: 100%;
  height: 100%;
  display: block;
}

/* 连线与流光 */
.link {
  stroke-width: 1.4;
  opacity: 0.35;
  stroke-dasharray: 5 7;
  animation: dash 1.4s linear infinite;
}
.link.ok { stroke: var(--accent); }
.link.warn { stroke: var(--warn); }
.link.danger { stroke: var(--danger); }
@keyframes dash {
  to { stroke-dashoffset: -24; }
}
.flow.ok { fill: var(--accent); }
.flow.warn { fill: var(--warn); }
.flow.danger { fill: var(--danger); }

/* 节点框：百分比定位并居中于坐标点 */
.topo-node {
  position: absolute;
  transform: translate(-50%, -50%);
  min-width: 120px;
  padding: 8px 12px;
  cursor: pointer;
  transition: transform 0.15s ease, box-shadow 0.15s ease;
  z-index: 2;
}
.topo-node:hover {
  transform: translate(-50%, -50%) scale(1.06);
}
.topo-node.danger { box-shadow: 0 0 0 1px rgba(244, 63, 94, 0.4), 0 4px 18px rgba(244, 63, 94, 0.12); }
.topo-node.warn { box-shadow: 0 0 0 1px rgba(245, 158, 11, 0.35); }
.topo-node.alerting { animation: node-pulse 1.6s ease-in-out infinite; }
@keyframes node-pulse {
  0%, 100% { box-shadow: 0 0 0 1px rgba(244, 63, 94, 0.4); }
  50% { box-shadow: 0 0 0 3px rgba(244, 63, 94, 0.12); }
}
.tn-title {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
  white-space: nowrap;
}
.tn-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
}
.tn-dot.ok { background: var(--accent); }
.tn-dot.warn { background: var(--warn); }
.tn-dot.danger { background: var(--danger); }
.tn-meta {
  font-size: 11px;
  color: var(--text-dim);
  margin-top: 3px;
  font-family: var(--mono);
}

/* hover 提示 */
.topo-tip {
  position: absolute;
  transform: translate(-50%, -125%);
  padding: 8px 10px;
  z-index: 5;
  pointer-events: none;
  min-width: 130px;
}
.tt-title {
  font-size: 12px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 4px;
}
.tt-line {
  font-size: 11px;
  color: var(--text-dim);
  font-family: var(--mono);
}

@media (max-width: 1100px) {
  .topo-node { min-width: 96px; padding: 6px 9px; }
  .tn-title { font-size: 12px; }
}
</style>
