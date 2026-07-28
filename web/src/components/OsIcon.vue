<template>
  <img :src="iconSrc" :alt="osType" class="os-icon" :title="title" />
</template>

<script setup>
import { computed } from 'vue'

import windowsIcon from '../assets/img/windows.svg'
import kylinIcon from '../assets/img/Kylin.svg'
import uosIcon from '../assets/img/uos.svg'
import centosIcon from '../assets/img/centos.svg'
import ubuntuIcon from '../assets/img/ubuntu.svg'
import linuxIcon from '../assets/img/linux.svg'

const props = defineProps({
  os: { type: String, default: '' },
})

const osType = computed(() => {
  const s = (props.os || '').toLowerCase()
  if (s.includes('win')) return 'windows'
  if (s.includes('kylin')) return 'kylin'
  if (s.includes('uos')) return 'uos'
  if (s.includes('centos')) return 'centos'
  if (s.includes('ubuntu')) return 'ubuntu'
  return 'linux'
})

const iconMap = {
  windows: windowsIcon,
  kylin: kylinIcon,
  uos: uosIcon,
  centos: centosIcon,
  ubuntu: ubuntuIcon,
  linux: linuxIcon,
}

const iconSrc = computed(() => iconMap[osType.value])

const title = computed(() => props.os || osType.value)
</script>

<style scoped>
.os-icon {
  display: inline-block;
  width: 20px;
  height: 20px;
  object-fit: contain;
  vertical-align: middle;
  flex-shrink: 0;
}
</style>
