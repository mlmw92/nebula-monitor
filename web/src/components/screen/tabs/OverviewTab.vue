<template>
  <div class="overview-tab">
    <!-- 左列：TopologyMap + CenterPanel -->
    <div class="ot-left">
      <TopologyMap :nodes="nodeCards" :health-score="healthScore" :health-level="healthLevel" />
      <CenterPanel :nodes="nodeCards" :alerts="alerts" />
    </div>
    <!-- 右列：HealthScore + 主机列表 -->
    <div class="ot-right">
      <ScreenHealthScore
        :score="healthScore"
        :onlineRate="onlineRate"
        :alertFreeRate="alertFreeRate"
        :cpuHeadroom="cpuHeadroom"
        :diskHeadroom="diskHeadroom"
      />
      <ScrollTablePanel
        class="ot-table"
        title="主机监控实时列表"
        :columns="hostColumns"
        :rows="hostRows"
        :speed="0.4"
      />
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue'
import TopologyMap from '../TopologyMap.vue'
import CenterPanel from '../CenterPanel.vue'
import ScreenHealthScore from '../ScreenHealthScore.vue'
import ScrollTablePanel from '../ScrollTablePanel.vue'
import { rateShort } from '../../../charts/echarts'

const props = defineProps({
  nodes: { type: Array, default: () => [] },
  metrics: { type: Object, default: () => ({}) },
  alerts: { type: Array, default: () => [] },
})

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

const onlineNodes = computed(() => nodeCards.value.filter((n) => n.online !== false))
const onlineCount = computed(() => onlineNodes.value.length)

function avg(key) {
  const list = onlineNodes.value
  if (!list.length) return 0
  return list.reduce((s, n) => s + (n[key] || 0), 0) / list.length
}

// 健康评分计算
const healthScore = computed(() => {
  const cpu = avg('cpu')
  const mem = avg('mem')
  const disk = avg('disk')
  const total = props.nodes.length
  const online = onlineCount.value
  const firing = (props.alerts || []).filter((a) => a.state === 'firing').length
  const onlineScore = total > 0 ? (online / total) * 100 : 100
  const cpuScore = Math.max(0, 100 - cpu)
  const memScore = Math.max(0, 100 - mem)
  const diskScore = Math.max(0, 100 - disk)
  const alertScore = Math.max(0, 100 - firing * 10)
  return Math.round((onlineScore + cpuScore + memScore + diskScore + alertScore) / 5)
})

const healthLevel = computed(() => {
  if (healthScore.value >= 80) return 'green'
  if (healthScore.value >= 60) return 'amber'
  return 'red'
})

const onlineRate = computed(() => {
  return props.nodes.length > 0
    ? Math.round((onlineCount.value / props.nodes.length) * 100)
    : 100
})

const alertFreeRate = computed(() => {
  const firing = (props.alerts || []).filter((a) => a.state === 'firing').length
  return Math.max(0, 100 - firing * 10)
})

const cpuHeadroom = computed(() => Math.round(Math.max(0, 100 - avg('cpu'))))
const diskHeadroom = computed(() => Math.round(Math.max(0, 100 - avg('disk'))))

// 主机列表
const hostColumns = [
  { key: 'name', label: '主机名', width: '1.4fr' },
  { key: 'cpu', label: 'CPU', width: '1fr', type: 'bar', align: 'right', max: (rows) => Math.max(...rows.map((r) => r.cpu), 100) },
  { key: 'mem', label: '内存', width: '1fr', type: 'bar', align: 'right', max: (rows) => Math.max(...rows.map((r) => r.mem), 100) },
  { key: 'disk', label: '磁盘', width: '1fr', type: 'bar', align: 'right', max: (rows) => Math.max(...rows.map((r) => r.disk), 100) },
  { key: 'netIn', label: '入流量', width: '0.9fr', type: 'num', align: 'right', fmt: (v) => rateShort(v) },
  { key: 'status', label: '状态', width: '0.7fr', type: 'badge', align: 'center' },
]
const hostRows = computed(() =>
  nodeCards.value.map((n) => ({
    name: n.name,
    cpu: n.cpu,
    mem: n.mem,
    disk: n.disk,
    netIn: n.netIn,
    status: n.online ? '在线' : '离线',
  }))
)
</script>

<style scoped>
.overview-tab {
  display: grid;
  grid-template-columns: 1.5fr minmax(260px, 1fr);
  gap: 12px;
  height: 100%;
  min-height: 0;
}

.ot-left {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-height: 0;
}

.ot-right {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-height: 0;
}

.ot-table {
  flex: 1;
  min-height: 0;
}

/* 4K 适配 */
@media (min-width: 2400px) {
  .overview-tab {
    gap: 16px;
  }
  .ot-left, .ot-right {
    gap: 16px;
  }
}

@media (min-width: 3440px) {
  .overview-tab {
    gap: 22px;
  }
  .ot-left, .ot-right {
    gap: 22px;
  }
}
</style>