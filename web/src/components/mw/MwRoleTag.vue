<template>
  <span class="mw-role" :class="'mw-role-' + entry.cls">{{ entry.label }}</span>
</template>

<script setup>
import { computed } from 'vue'

const props = defineProps({
  // 角色/拓扑标识，如 master/slave/sentinel/broker/standalone 等
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

const entry = computed(() => {
  const r = (props.role || '').trim().toLowerCase()
  if (MAP[r]) return MAP[r]
  return { label: props.role || '未知', cls: 'unknown' }
})
</script>

<style scoped>
.mw-role {
  display: inline-block;
  padding: 2px 10px;
  border-radius: 999px;
  font-size: 12px;
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
  color: #4ade80;
}
.mw-role-sentinel {
  background: rgba(245, 158, 11, 0.15);
  color: #fbbf24;
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
  color: #94a3b8;
}
</style>
