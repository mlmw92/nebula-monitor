<template>
  <span class="mw-role-group">
    <span v-for="e in entries" :key="e.label" class="mw-role" :class="'mw-role-' + e.cls">{{ e.label }}</span>
  </span>
</template>

<script setup>
import { computed } from 'vue'

const props = defineProps({
  // 角色/拓扑标识，如 master/slave/sentinel/broker/standalone 等。
  // K8s 节点可能同时带多个角色（标准控制面就是 control-plane,master），
  // 因此这里按逗号拆开、每个角色各渲染一个标签——与 `kubectl get nodes` 的 ROLES 列一致。
  role: { type: String, default: '' },
})

const MAP = {
  master: { label: 'Master', cls: 'master' },
  primary: { label: 'Primary', cls: 'master' },
  leader: { label: 'Leader', cls: 'master' },
  slave: { label: 'Slave', cls: 'slave' },
  replica: { label: 'Replica', cls: 'slave' },
  secondary: { label: 'Secondary', cls: 'slave' },
  standby: { label: 'Standby', cls: 'slave' },
  follower: { label: 'Follower', cls: 'slave' },
  sentinel: { label: 'Sentinel', cls: 'sentinel' },
  controller: { label: 'Controller', cls: 'sentinel' },
  'control-plane': { label: 'Control-Plane', cls: 'sentinel' },
  broker: { label: 'Broker', cls: 'broker' },
  nameserver: { label: 'NameServer', cls: 'broker' },
  arbiter: { label: 'Arbiter', cls: 'sentinel' },
  mongos: { label: 'Mongos', cls: 'broker' },
  standalone: { label: '单机', cls: 'standalone' },
  node: { label: 'Node', cls: 'standalone' },
  worker: { label: 'Worker', cls: 'standalone' },
  unknown: { label: '未知', cls: 'unknown' },
}

const entries = computed(() => {
  const raw = (props.role || '').trim()
  if (!raw) return [MAP.unknown]
  const list = raw
    .split(',')
    .map((part) => {
      const key = part.trim().toLowerCase()
      if (!key) return null
      // 没登记过的角色原样显示（用灰色），好过显示成"未知"把真实取值藏掉。
      return MAP[key] || { label: part.trim(), cls: 'unknown' }
    })
    .filter(Boolean)
  return list.length ? list : [MAP.unknown]
})
</script>

<style scoped>
/* 外层用 inline-flex + 允许换行：多个角色时逐个排开，列宽不够就换行，
   而不是被单元格的 overflow 裁掉半截（这正是"角色显示不全"的成因）。 */
.mw-role-group {
  display: inline-flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px;
}
.mw-role {
  display: inline-block;
  padding: 2px 10px;
  border-radius: 999px;
  font-size: 13px;
  font-weight: 600;
  line-height: 1.5;
  white-space: nowrap;
}
.mw-role-master {
  background: rgba(220, 56, 45, 0.15);
  color: #ff6b6b;
}
.mw-role-slave {
  background: rgba(34, 197, 94, 0.15);
  color: var(--ok);
}
.mw-role-sentinel {
  background: rgba(245, 158, 11, 0.15);
  color: var(--warn);
}
.mw-role-broker {
  background: rgba(59, 130, 246, 0.15);
  color: #60a5fa;
}
.mw-role-standalone {
  background: rgba(139, 92, 246, 0.15);
  color: #a78bfa;
}
.mw-role-unknown {
  background: rgba(107, 124, 147, 0.15);
  color: var(--t3);
}
</style>
