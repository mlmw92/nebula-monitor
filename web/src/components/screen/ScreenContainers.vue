<template>
  <div class="glass panel-mini screen-containers">
    <div class="sc-title">容器状态</div>
    <div class="sc-empty" v-if="!stats.total">
      <span>暂无容器数据</span>
    </div>
    <div class="sc-body" v-else>
      <div class="sc-donut">
        <svg viewBox="0 0 120 120">
          <circle cx="60" cy="60" r="46" fill="none" stroke="rgba(255,255,255,0.05)" stroke-width="14" />
          <circle
            v-for="seg in segments"
            :key="seg.key"
            cx="60" cy="60" r="46" fill="none"
            :stroke="seg.color" stroke-width="14"
            :stroke-dasharray="seg.dash"
            :stroke-dashoffset="seg.offset"
            transform="rotate(-90 60 60)"
            class="sc-seg"
          />
        </svg>
        <div class="sc-donut-center">
          <span class="sc-total">{{ stats.total }}</span>
          <span class="sc-total-l">容器</span>
        </div>
      </div>
      <div class="sc-legend">
        <div class="sc-leg" v-for="seg in segments" :key="'l-' + seg.key">
          <span class="d" :style="{ background: seg.color }"></span>
          <span class="sc-leg-name">{{ seg.label }}</span>
          <b class="mono">{{ seg.value }}</b>
          <span class="sc-leg-pct">{{ pct(seg.value) }}%</span>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, reactive, computed, onMounted, onUnmounted } from 'vue'
import http from '../../api/http'

const emit = defineEmits(['summary'])

const stats = reactive({ total: 0, running: 0, stopped: 0, abnormal: 0 })
let timer = null
let visible = true

const R = 46
const C = 2 * Math.PI * R

const segments = computed(() => {
  const t = stats.total || 1
  const list = [
    { key: 'running', label: '运行中', value: stats.running, color: 'var(--accent)' },
    { key: 'stopped', label: '停止', value: stats.stopped, color: 'var(--text-dim)' },
    { key: 'abnormal', label: '异常', value: stats.abnormal, color: 'var(--danger)' },
  ]
  let acc = 0
  for (const s of list) {
    const len = C * (s.value / t)
    s.dash = `${len} ${C}`
    s.offset = -acc
    acc += len
  }
  return list
})

function pct(v) {
  if (!stats.total) return 0
  return Math.round((v / stats.total) * 100)
}

async function load() {
  if (!visible) return
  try {
    const res = await http.get('/api/v1/middleware/docker/containers').catch(() => ({ containers: [], hosts: [] }))
    const hosts = res.hosts || []
    const containers = res.containers || []
    // 主机汇总优先（含无容器但接入的 daemon）；异常 = 容器上报 up=false 的数量
    const running = hosts.reduce((s, h) => s + (h.containersRunning || 0), 0)
    const stopped = hosts.reduce((s, h) => s + (h.containersStopped || 0), 0)
    const total = hosts.reduce((s, h) => s + (h.containersTotal || 0), 0)
    const abnormal = containers.filter((c) => !c.up).length
    stats.running = running
    stats.stopped = stopped
    stats.abnormal = abnormal
    // total 至少覆盖 running+stopped；若 daemon 未上报 total 则用容器数兜底
    stats.total = Math.max(total, running + stopped, containers.length)
    emit('summary', { total: stats.total, running: stats.running, stopped: stats.stopped, abnormal: stats.abnormal })
  } catch (e) {
    /* ignore */
  }
}

function onVis() {
  visible = document.visibilityState === 'visible'
  if (visible) load()
}

onMounted(() => {
  load()
  timer = setInterval(load, 30000)
  document.addEventListener('visibilitychange', onVis)
})
onUnmounted(() => {
  timer && clearInterval(timer)
  document.removeEventListener('visibilitychange', onVis)
})
</script>

<style scoped>
.screen-containers {
  padding: 12px 14px;
  display: flex;
  flex-direction: column;
}
.sc-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 8px;
}
.sc-empty {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--text-dim);
  font-size: 12px;
}
.sc-body {
  display: flex;
  align-items: center;
  gap: 12px;
  flex: 1;
}
.sc-donut {
  position: relative;
  width: 104px;
  height: 104px;
  flex-shrink: 0;
}
.sc-donut svg {
  width: 100%;
  height: 100%;
}
.sc-seg {
  transition: stroke-dasharray 0.6s ease, stroke-dashoffset 0.6s ease;
}
.sc-donut-center {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
}
.sc-total {
  font-size: 24px;
  font-weight: 800;
  font-family: var(--mono);
  color: var(--text);
  line-height: 1;
}
.sc-total-l {
  font-size: 10px;
  color: var(--text-dim);
  margin-top: 2px;
}
.sc-legend {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
}
.sc-leg {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--text-dim);
}
.sc-leg .d {
  width: 8px;
  height: 8px;
  border-radius: 2px;
  flex-shrink: 0;
}
.sc-leg-name {
  color: var(--text);
}
.sc-leg b {
  margin-left: auto;
  color: var(--text);
}
.sc-leg-pct {
  width: 34px;
  text-align: right;
}
</style>
