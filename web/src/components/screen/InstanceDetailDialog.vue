<template>
  <el-dialog
    :model-value="visible"
    @update:model-value="$emit('update:visible', $event)"
    :title="title"
    width="540px"
    class="instance-detail-dialog"
    append-to-body
    :close-on-click-modal="true"
  >
    <div v-if="inst" class="idd-body">
      <div class="idd-head">
        <span :class="['idd-dot', inst.up ? 'up' : 'down']"></span>
        <span class="idd-name mono">{{ displayName }}</span>
      </div>
      <div class="idd-tags" v-if="inst.role || inst.topology">
        <span v-if="inst.role" class="idd-tag" :class="inst.role">{{ roleLabel(inst.role) }}</span>
        <span v-if="inst.topology" class="idd-tag topo">{{ topoLabel(inst.topology) }}</span>
      </div>
      <div class="idd-meta">
        <div class="idd-meta-item" v-if="inst.node"><span>节点</span><b>{{ inst.node }}</b></div>
        <div class="idd-meta-item" v-if="inst.ip"><span>IP</span><b>{{ inst.ip }}</b></div>
        <div class="idd-meta-item" v-if="inst.version"><span>版本</span><b>{{ inst.version }}</b></div>
        <div class="idd-meta-item" v-if="inst.group"><span>归属</span><b>{{ inst.group }}</b></div>
        <div class="idd-meta-item" v-if="inst.uptime"><span>运行</span><b>{{ formatUptime(inst.uptime) }}</b></div>
      </div>
      <div class="idd-metrics" v-if="metricFields.length">
        <div class="idd-metric" v-for="f in metricFields" :key="f.key">
          <span class="idd-m-label">{{ f.label }}</span>
          <span class="idd-m-value" :class="{ warn: f.warn }">{{ f.value }}</span>
        </div>
      </div>
      <div v-else class="idd-empty">暂无更多指标数据</div>
    </div>
  </el-dialog>
</template>

<script setup>
import { computed } from 'vue'

const props = defineProps({
  visible: { type: Boolean, default: false },
  instance: { type: Object, default: null },
})
defineEmits(['update:visible'])

// 结构/标识类字段不进入指标区（已单独展示或无需展示）
const SKIP = new Set([
  'up', 'role', 'topology', 'name', 'instance', 'node', 'ip', 'group', 'version', 'uptime',
  'container', 'type', '_rowKey', 'status', 'alert', 'alerts', 'replicaOf', 'cluster',
  'displayName', 'key', 'children', 'slaves', 'masters', 'sentinals', 'sentinels',
  'expand', 'level', 'parent', 'id', 'selected', 'loading',
])

const LABELS = {
  usedMem: '内存使用', memLimit: '内存上限', usedMemory: '内存使用',
  mem: '内存使用率', memPercent: '内存使用率', memoryUsage: '内存使用率',
  clients: '客户端连接', connections: '连接数', conns: '连接数',
  connectionsUsed: '已用连接', connectionsFree: '空闲连接',
  ops: '操作速率', qps: 'QPS', hitRate: '缓存命中率',
  keys: '键数量', queries: '查询数', slow: '慢查询数', slowLog: '慢查询数',
  inserts: '插入数', online: '在线', latencyMs: '延迟(ms)', latency: '延迟(ms)',
  cpu: 'CPU使用率', cpuUsage: 'CPU使用率', cpuPercent: 'CPU使用率',
  replicationLag: '复制延迟(s)', replicationOffset: '复制偏移',
  blocked: '阻塞客户端', evicted: '淘汰键', expired: '过期键',
  rejected: '拒绝连接', fragmentation: '碎片率', connectedSlaves: '从节点数',
  throughput: '吞吐', messages: '消息数', partitions: '分区数',
  brokers: 'Broker数', topics: 'Topic数', consumers: '消费者组',
  containers: '容器数', images: '镜像数', pods: 'Pod数',
  namespaces: '命名空间数', nodesNum: '节点数', totalNodes: '节点数',
}

const inst = computed(() => props.instance || null)
const displayName = computed(() => {
  const i = inst.value
  if (!i) return ''
  return i.name || i.instance || i.container || i.ip || i.node || '-'
})
const title = computed(() => '实例详情 · ' + displayName.value)

function isByte(k) { return /mem|memory|size|bytes|disk/i.test(k) }
function isPct(k) { return /percent|rate|usage|hitrate|ratio/i.test(k) }
function isTime(k) { return /uptime/i.test(k) }

const metricFields = computed(() => {
  const i = inst.value
  if (!i) return []
  const out = []
  for (const k of Object.keys(i)) {
    if (SKIP.has(k)) continue
    const v = i[k]
    if (v === null || v === undefined || v === '') continue
    if (typeof v === 'object') continue
    let val
    if (typeof v === 'number') {
      if (isByte(k)) val = formatBytes(v)
      else if (isPct(k)) val = v + '%'
      else if (isTime(k)) val = formatUptime(v)
      else val = formatNum(v)
    } else {
      val = String(v)
    }
    out.push({ key: k, label: LABELS[k] || prettyKey(k), value: val })
  }
  return out
})

function prettyKey(k) { return k.replace(/([A-Z])/g, ' $1').replace(/^./, (c) => c.toUpperCase()) }
function roleLabel(r) { return ({ master: '主节点', slave: '从节点', replica: '从节点', sentinel: '哨兵' })[r] || r }
function topoLabel(t) { return ({ cluster: '集群', replication: '主从', sentinel: '哨兵', standalone: '单机' })[t] || t }
function formatNum(n) { return typeof n === 'number' ? n.toLocaleString('en-US') : (n == null ? '' : String(n)) }
function formatBytes(n) {
  if (n == null || isNaN(n)) return '-'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0, v = n
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return (v >= 10 ? v.toFixed(0) : v.toFixed(1)) + u[i]
}
function formatUptime(s) {
  if (!s || s < 0) return '-'
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60)
  if (d > 0) return d + '天' + h + '小时'
  if (h > 0) return h + '小时' + m + '分'
  return m + '分'
}
</script>

<style scoped>
.instance-detail-dialog :deep(.el-dialog__body) { padding: 12px 18px 18px; }
.idd-body { color: #e5e7eb; font-size: 13px; }
.idd-head { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
.idd-dot { width: 10px; height: 10px; border-radius: 50%; flex-shrink: 0; }
.idd-dot.up { background: #22c55e; box-shadow: 0 0 6px #22c55e; }
.idd-dot.down { background: #ef4444; }
.idd-name { font-size: 15px; font-weight: 600; color: #f8fafc; }
.mono { font-family: ui-monospace, 'SFMono-Regular', Menlo, Consolas, monospace; }
.idd-tags { display: flex; gap: 6px; margin-bottom: 10px; }
.idd-tag { font-size: 11px; padding: 2px 8px; border-radius: 4px; border: 1px solid rgba(255, 255, 255, .12); }
.idd-tag.master { background: rgba(220, 56, 45, .18); color: #ff8a80; border-color: rgba(220, 56, 45, .35); }
.idd-tag.slave { background: rgba(34, 197, 94, .15); color: #4ade80; border-color: rgba(34, 197, 94, .3); }
.idd-tag.topo { background: rgba(56, 189, 248, .12); color: #7dd3fc; border-color: rgba(56, 189, 248, .3); }
.idd-meta { display: flex; flex-wrap: wrap; gap: 8px 18px; padding: 10px 0; border-top: 1px solid rgba(255, 255, 255, .06); border-bottom: 1px solid rgba(255, 255, 255, .06); margin-bottom: 10px; }
.idd-meta-item { font-size: 12px; color: #94a3b8; }
.idd-meta-item b { color: #e5e7eb; font-weight: 600; margin-left: 6px; }
.idd-metrics { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
.idd-metric { background: rgba(255, 255, 255, .04); border: 1px solid rgba(255, 255, 255, .07); border-radius: 6px; padding: 8px 10px; }
.idd-m-label { display: block; font-size: 11px; color: #94a3b8; margin-bottom: 3px; }
.idd-m-value { font-size: 14px; font-weight: 600; color: #e5e7eb; }
.idd-m-value.warn { color: #f59e0b; }
.idd-empty { color: #64748b; font-size: 12px; padding: 10px 0; }
</style>
