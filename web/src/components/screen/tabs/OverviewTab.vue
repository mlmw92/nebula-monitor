<template>
  <div class="overview-tab">
    <!-- 左列：TopologyMap + CenterPanel -->
    <div class="ot-left">
      <div class="ot-topo" :class="{ 'ot-topo--empty': !topoHasNodes }">
        <TopologyMap :nodes="nodeCards" @has-nodes-change="topoHasNodes = $event" />
      </div>
      <div class="ot-center">
        <CenterPanel :nodes="nodeCards" :alerts="alerts" :health-score="healthScore" :health-level="healthLevel" />
      </div>
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
import { ref, computed } from 'vue'
import TopologyMap from '../TopologyMap.vue'
import CenterPanel from '../CenterPanel.vue'
import ScreenHealthScore from '../ScreenHealthScore.vue'
import ScrollTablePanel from '../ScrollTablePanel.vue'
import { rateShort } from '../../../charts/echarts'
import { buildNodeCards } from '../composables/useNodeCards'
import { calculateAlertScore } from '../../../composables/healthScore'

const props = defineProps({
  nodes: { type: Array, default: () => [] },
  metrics: { type: Object, default: () => ({}) },
  alerts: { type: Array, default: () => [] },
  healthScore: { type: Number, default: 100 },
})

const topoHasNodes = ref(false)

const nodeCards = computed(() => buildNodeCards(props.nodes, props.metrics))

const onlineNodes = computed(() => nodeCards.value.filter((n) => n.online !== false))
const onlineCount = computed(() => onlineNodes.value.length)
// 主机总数按去重后的卡片数统计，与列表展示保持一致
const totalCount = computed(() => nodeCards.value.length)

function avg(key) {
  const list = onlineNodes.value
  if (!list.length) return 0
  return list.reduce((s, n) => s + (n[key] || 0), 0) / list.length
}

const healthScore = computed(() => props.healthScore)

const healthLevel = computed(() => {
  if (healthScore.value >= 90) return 'green'
  if (healthScore.value >= 70) return 'amber'
  return 'red'
})

const onlineRate = computed(() => {
  return totalCount.value > 0
    ? Math.round((onlineCount.value / totalCount.value) * 100)
    : 100
})

const alertFreeRate = computed(() => {
  return calculateAlertScore((props.alerts || []).filter((a) => a.state === 'firing'))
})

const cpuHeadroom = computed(() => Math.round(Math.max(0, 100 - avg('cpu'))))
const diskHeadroom = computed(() => Math.round(Math.max(0, 100 - avg('disk'))))

// 主机列表
const hostColumns = [
  { key: 'name', label: '主机名', width: '1.3fr' },
  { key: 'ip', label: 'IP', width: '1.1fr' },
  { key: 'group', label: '分组', width: '0.9fr' },
  { key: 'cpu', label: 'CPU', width: '0.9fr', type: 'bar', align: 'right', max: (rows) => Math.max(...rows.map((r) => r.cpu), 100) },
  { key: 'mem', label: '内存', width: '0.9fr', type: 'bar', align: 'right', max: (rows) => Math.max(...rows.map((r) => r.mem), 100) },
  { key: 'disk', label: '磁盘', width: '0.9fr', type: 'bar', align: 'right', max: (rows) => Math.max(...rows.map((r) => r.disk), 100) },
  { key: 'netIn', label: '入流量', width: '0.9fr', type: 'num', align: 'right', fmt: (v) => rateShort(v) },
  { key: 'status', label: '状态', width: '0.6fr', type: 'badge', align: 'center' },
]
const hostRows = computed(() =>
  nodeCards.value.map((n) => ({
    key: n.hostname,
    name: n.label,
    ip: n.ip,
    group: n.group,
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

.ot-topo {
  flex: 1;
  min-height: 0;
  position: relative;
  transition: flex 0.25s ease, min-height 0.25s ease;
}
.ot-topo--empty {
  flex: 0 0 0px;
  min-height: 0;
  overflow: hidden;
}

.ot-center {
  flex: 1.25;
  min-height: 0;
  display: flex;
  flex-direction: column;
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
