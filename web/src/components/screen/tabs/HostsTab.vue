<template>
  <div class="hosts-tab">
    <HostMonitorPanel :nodes="nodeCards" />
  </div>
</template>

<script setup>
import { computed } from 'vue'
import HostMonitorPanel from '../HostMonitorPanel.vue'

const props = defineProps({
  nodes: { type: Array, default: () => [] },
  metrics: { type: Object, default: () => ({}) },
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
    load5: props.metrics[n.hostname]?.load5 || 0,
    load15: props.metrics[n.hostname]?.load15 || 0,
    netIn: props.metrics[n.hostname]?.netIn || 0,
    netOut: props.metrics[n.hostname]?.netOut || 0,
    diskIopsR: props.metrics[n.hostname]?.diskIopsR || 0,
    diskIopsW: props.metrics[n.hostname]?.diskIopsW || 0,
    netDrop: props.metrics[n.hostname]?.netDrop || 0,
    tcpRetrans: props.metrics[n.hostname]?.tcpRetrans || 0,
    memTotal: props.metrics[n.hostname]?.memTotal || 0,
    procCount: props.metrics[n.hostname]?.procCount || 0,
  }))
)
</script>

<style scoped>
.hosts-tab {
  height: 100%;
  min-height: 0;
}
</style>