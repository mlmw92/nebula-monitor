<template>
  <div class="alerts-tab">
    <div class="at-left">
      <ScreenAlertLevels :alerts="activeAlerts" />
      <ScreenHealthScore
        :score="healthScore"
        :onlineRate="onlineRate"
        :alertFreeRate="alertFreeRate"
        :cpuHeadroom="cpuHeadroom"
        :diskHeadroom="diskHeadroom"
      />
    </div>
    <div class="at-right">
      <ScreenRisk :alerts="activeAlerts" :nodes="nodeCards" />
      <ScrollTablePanel
        class="at-table"
        title="实时告警事件"
        :columns="alertColumns"
        :rows="alertRows"
        :speed="0.3"
      />
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import ScreenAlertLevels from '../ScreenAlertLevels.vue'
import ScreenHealthScore from '../ScreenHealthScore.vue'
import ScreenRisk from '../ScreenRisk.vue'
import ScrollTablePanel from '../ScrollTablePanel.vue'

const props = defineProps({
  nodes: { type: Array, default: () => [] },
  metrics: { type: Object, default: () => ({}) },
  alerts: { type: Array, default: () => [] },
})

const router = useRouter()

const nodeCards = computed(() =>
  props.nodes.map((n) => ({
    name: n.hostname,
    ip: n.ip || '-',
    online: n.status === 'online',
    cpu: props.metrics[n.hostname]?.cpu || 0,
    mem: props.metrics[n.hostname]?.mem || 0,
    disk: props.metrics[n.hostname]?.disk || 0,
    load1: props.metrics[n.hostname]?.load1 || 0,
    load: props.metrics[n.hostname]?.load1 || 0,
    netIn: props.metrics[n.hostname]?.netIn || 0,
    netOut: props.metrics[n.hostname]?.netOut || 0,
    memTotal: props.metrics[n.hostname]?.memTotal || 0,
    procCount: props.metrics[n.hostname]?.procCount || 0,
  }))
)

const activeAlerts = computed(() => (props.alerts || []).filter((a) => a.state === 'firing'))
const onlineNodes = computed(() => nodeCards.value.filter((n) => n.online !== false))
const onlineCount = computed(() => onlineNodes.value.length)

function avg(key) {
  const list = onlineNodes.value
  if (!list.length) return 0
  return list.reduce((s, n) => s + (n[key] || 0), 0) / list.length
}

const healthScore = computed(() => {
  const total = props.nodes.length
  const online = onlineCount.value
  const firing = activeAlerts.value.length
  const onlineScore = total > 0 ? (online / total) * 100 : 100
  const cpuScore = Math.max(0, 100 - avg('cpu'))
  const memScore = Math.max(0, 100 - avg('mem'))
  const diskScore = Math.max(0, 100 - avg('disk'))
  const alertScore = Math.max(0, 100 - firing * 10)
  return Math.round((onlineScore + cpuScore + memScore + diskScore + alertScore) / 5)
})

const onlineRate = computed(() => {
  return props.nodes.length > 0
    ? Math.round((onlineCount.value / props.nodes.length) * 100)
    : 100
})

const alertFreeRate = computed(() => {
  const firing = activeAlerts.value.length
  return Math.max(0, 100 - firing * 10)
})

const cpuHeadroom = computed(() => Math.round(Math.max(0, 100 - avg('cpu'))))
const diskHeadroom = computed(() => Math.round(Math.max(0, 100 - avg('disk'))))

// 告警列表
function fmtShort(ts) {
  if (!ts) return '--'
  const d = new Date(ts)
  const p = (x) => String(x).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

const alertColumns = [
  { key: 'severity', label: '级别', width: '0.7fr', type: 'badge', align: 'center' },
  { key: 'node', label: '节点', width: '1.1fr' },
  { key: 'rule', label: '告警规则', width: '1.8fr' },
  { key: 'time', label: '时间', width: '1.1fr', align: 'right' },
]

const alertRows = computed(() =>
  activeAlerts.value.map((a) => ({
    severity: a.severity === 'critical' ? '故障' : a.severity === 'warning' ? '预警' : '提示',
    node: a.node || '-',
    rule: a.ruleName || a.summary || '未知告警',
    time: fmtShort(a.startsAt),
  }))
)
</script>

<style scoped>
.alerts-tab {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
  height: 100%;
  min-height: 0;
}

.at-left, .at-right {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-height: 0;
}

.at-table {
  flex: 1;
  min-height: 0;
}

/* 4K 适配 */
@media (min-width: 2400px) {
  .alerts-tab { gap: 16px; }
  .at-left, .at-right { gap: 16px; }
}

@media (min-width: 3440px) {
  .alerts-tab { gap: 22px; }
  .at-left, .at-right { gap: 22px; }
}
</style>