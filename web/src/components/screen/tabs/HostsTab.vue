<template>
  <div class="hosts-tab">
    <HostMonitorPanel :nodes="nodeCards" />
  </div>
</template>

<script setup>
import { computed } from 'vue'
import HostMonitorPanel from '../HostMonitorPanel.vue'
import { buildNodeCards } from '../composables/useNodeCards'

const props = defineProps({
  nodes: { type: Array, default: () => [] },
  metrics: { type: Object, default: () => ({}) },
})

const nodeCards = computed(() => buildNodeCards(props.nodes, props.metrics))
</script>

<style scoped>
.hosts-tab {
  height: 100%;
  min-height: 0;
}
</style>