<template>
  <el-dialog
    :model-value="visible"
    @update:model-value="$emit('update:visible', $event)"
    :title="'主机详情 · ' + displayName"
    width="560px"
    class="host-detail-dialog"
    append-to-body
    :close-on-click-modal="true"
  >
    <div v-if="host" class="hdd-body">
      <div class="hdd-head">
        <span :class="['hdd-dot', host.online ? 'up' : 'down']"></span>
        <div class="hdd-name-wrap">
          <strong class="hdd-name">{{ displayName }}</strong>
          <span class="hdd-hostname">{{ host.hostname }}</span>
        </div>
        <span :class="['hdd-status', host.online ? 'up' : 'down']">{{ host.online ? '在线' : '离线' }}</span>
      </div>
      <div class="hdd-meta">
        <div class="hdd-meta-item"><span>IP</span><b>{{ host.ip || '-' }}</b></div>
        <div class="hdd-meta-item"><span>分组</span><b>{{ host.group || '默认分组' }}</b></div>
      </div>
      <div class="hdd-section-title">实时资源</div>
      <div class="hdd-metrics">
        <div v-for="metric in resourceMetrics" :key="metric.key" class="hdd-metric">
          <span class="hdd-m-label">{{ metric.label }}</span>
          <span class="hdd-m-value" :class="{ warn: metric.warn }">{{ metric.value }}</span>
        </div>
      </div>
      <div class="hdd-section-title hdd-section-extra">运行指标</div>
      <div class="hdd-metrics">
        <div v-for="metric in runtimeMetrics" :key="metric.key" class="hdd-metric">
          <span class="hdd-m-label">{{ metric.label }}</span>
          <span class="hdd-m-value">{{ metric.value }}</span>
        </div>
      </div>
    </div>
  </el-dialog>
</template>

<script setup>
import { computed } from 'vue'
import { rateShort } from '../../charts/echarts'

const props = defineProps({
  visible: { type: Boolean, default: false },
  host: { type: Object, default: null },
})
defineEmits(['update:visible'])

const host = computed(() => props.host || null)
const displayName = computed(() => host.value?.displayName || host.value?.hostname || '-')

function pct(value) { return value == null ? '--' : Number(value).toFixed(1) + '%' }
function load(value) { return value == null ? '--' : Number(value).toFixed(2) }
function count(value) { return value == null ? '--' : Math.round(Number(value)).toLocaleString('en-US') }
function iops(read, write) { return `${Math.round(Number(read) || 0)}/${Math.round(Number(write) || 0)} IOPS` }
function loss(value) { return value == null ? '--' : Number(value).toFixed(1) + '/s' }

const resourceMetrics = computed(() => {
  const h = host.value
  if (!h) return []
  return [
    { key: 'cpu', label: 'CPU 使用率', value: pct(h.cpu), warn: h.cpu >= 70 },
    { key: 'mem', label: '内存使用率', value: pct(h.mem), warn: h.mem >= 70 },
    { key: 'disk', label: '磁盘使用率', value: pct(h.disk), warn: h.disk >= 70 },
    { key: 'memUsed', label: '已用内存', value: formatBytes(h.memUsed) },
    { key: 'netIn', label: '实时入流量', value: rateShort(h.netIn || 0) },
    { key: 'netOut', label: '实时出流量', value: rateShort(h.netOut || 0) },
  ]
})

const runtimeMetrics = computed(() => {
  const h = host.value
  if (!h) return []
  return [
    { key: 'load', label: '系统负载（1m）', value: load(h.load1) },
    { key: 'iops', label: '磁盘读/写', value: iops(h.diskIopsR, h.diskIopsW) },
    { key: 'netDrop', label: '丢包', value: loss(h.netDrop) },
    { key: 'tcpRetrans', label: 'TCP 重传', value: loss(h.tcpRetrans) },
    { key: 'procCount', label: '进程数', value: count(h.procCount) },
  ]
})

function formatBytes(value) {
  if (value == null || Number(value) <= 0) return '--'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let n = Number(value); let i = 0
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++ }
  return `${n >= 10 ? n.toFixed(0) : n.toFixed(1)} ${units[i]}`
}
</script>

<style scoped>
.host-detail-dialog :deep(.el-dialog__body) { padding: 12px 18px 18px; }
.hdd-body { color: #e5e7eb; font-size: 13px; }
.hdd-head { display: flex; align-items: center; gap: 9px; margin-bottom: 10px; }
.hdd-dot { width: 10px; height: 10px; border-radius: 50%; flex-shrink: 0; }
.hdd-dot.up { background: #22c55e; box-shadow: 0 0 6px #22c55e; }
.hdd-dot.down { background: #ef4444; box-shadow: 0 0 6px rgba(239, 68, 68, .45); }
.hdd-name-wrap { min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.hdd-name { color: #f8fafc; font-size: 15px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.hdd-hostname { color: #64748b; font: 11px ui-monospace, 'SFMono-Regular', Menlo, Consolas, monospace; }
.hdd-status { margin-left: auto; padding: 3px 8px; border-radius: 4px; font-size: 11px; }
.hdd-status.up { color: #4ade80; background: rgba(34, 197, 94, .12); }
.hdd-status.down { color: #fca5a5; background: rgba(239, 68, 68, .14); }
.hdd-meta { display: flex; flex-wrap: wrap; gap: 8px 22px; padding: 10px 0; border-top: 1px solid rgba(255, 255, 255, .06); border-bottom: 1px solid rgba(255, 255, 255, .06); }
.hdd-meta-item { color: #94a3b8; font-size: 12px; }
.hdd-meta-item b { color: #e5e7eb; font-weight: 600; margin-left: 6px; }
.hdd-section-title { margin: 14px 0 8px; color: #94a3b8; font-size: 12px; letter-spacing: .04em; }
.hdd-section-extra { margin-top: 16px; }
.hdd-metrics { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; }
.hdd-metric { min-width: 0; padding: 9px 10px; background: rgba(255, 255, 255, .04); border: 1px solid rgba(255, 255, 255, .07); border-radius: 6px; }
.hdd-m-label { display: block; margin-bottom: 4px; color: #94a3b8; font-size: 11px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.hdd-m-value { color: #e5e7eb; font: 600 14px ui-monospace, 'SFMono-Regular', Menlo, Consolas, monospace; white-space: nowrap; }
.hdd-m-value.warn { color: #f59e0b; }
@media (max-width: 560px) { .hdd-metrics { grid-template-columns: repeat(2, 1fr); } }
</style>
