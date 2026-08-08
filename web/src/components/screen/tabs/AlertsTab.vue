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
import { calculateAlertScore } from '../../../composables/healthScore'
import { buildNodeCards } from '../composables/useNodeCards'

const props = defineProps({
  nodes: { type: Array, default: () => [] },
  metrics: { type: Object, default: () => ({}) },
  alerts: { type: Array, default: () => [] },
  healthScore: { type: Number, default: 100 },
})

const router = useRouter()

const nodeCards = computed(() => buildNodeCards(props.nodes, props.metrics))

const activeAlerts = computed(() => (props.alerts || []).filter((a) => a.state === 'firing'))
const onlineNodes = computed(() => nodeCards.value.filter((n) => n.online !== false))
const onlineCount = computed(() => onlineNodes.value.length)

function avg(key) {
  const list = onlineNodes.value
  if (!list.length) return 0
  return list.reduce((s, n) => s + (n[key] || 0), 0) / list.length
}

const healthScore = computed(() => props.healthScore)

const onlineRate = computed(() => {
  return props.nodes.length > 0
    ? Math.round((onlineCount.value / props.nodes.length) * 100)
    : 100
})

const alertFreeRate = computed(() => {
  return calculateAlertScore(activeAlerts.value)
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
