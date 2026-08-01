<template>
  <div class="host-overview">
    <div v-for="g in groupedHosts" :key="g.group" class="host-group">
      <div class="group-head">
        <span class="group-name">{{ g.group }}</span>
        <span class="group-count">{{ g.hosts.length }}</span>
      </div>
      <div class="host-wrap">
        <div
          v-for="h in g.hosts"
          :key="h.hostname"
          class="host-card"
          :class="{ down: h.status !== 'online' }"
          @click="goNode(h.hostname)"
        >
          <div class="hc-top">
            <OsIcon :os="h.os" />
            <span class="hc-name" :title="h.hostname">{{ displayName(h) }}</span>
            <span class="hc-dot" :class="h.status === 'online' ? 'up' : 'down'"></span>
          </div>
          <div class="hc-ip">{{ h.ip || h.hostname }}</div>
          <div class="hc-metrics">
            <div class="hc-m">
              <span class="hc-m-label">CPU</span>
              <div class="hc-bar">
                <div class="hc-bar-fill" :style="barStyle(h.metrics.cpu, 70)"></div>
              </div>
              <span class="hc-m-val">{{ pct(h.metrics.cpu) }}</span>
            </div>
            <div class="hc-m">
              <span class="hc-m-label">内存</span>
              <div class="hc-bar">
                <div class="hc-bar-fill" :style="barStyle(h.metrics.mem, 80)"></div>
              </div>
              <span class="hc-m-val">{{ pct(h.metrics.mem) }}</span>
            </div>
            <div class="hc-m">
              <span class="hc-m-label">磁盘</span>
              <div class="hc-bar">
                <div class="hc-bar-fill" :style="barStyle(h.metrics.disk, 85)"></div>
              </div>
              <span class="hc-m-val">{{ pct(h.metrics.disk) }}</span>
            </div>
          </div>
        </div>
      </div>
    </div>
    <div v-if="!groupedHosts || groupedHosts.length === 0" class="host-empty">暂无主机数据</div>
  </div>
</template>

<script setup>
import { useRouter } from 'vue-router'
import OsIcon from '../OsIcon.vue'

const props = defineProps({
  groupedHosts: { type: Array, default: () => [] },
})
const router = useRouter()

function displayName(h) {
  return h.displayName && h.displayName.trim() ? h.displayName : h.hostname
}
function pct(v) {
  return v == null || isNaN(v) ? '-' : Math.round(v) + '%'
}
function barStyle(v, warnAt) {
  const n = v == null || isNaN(v) ? 0 : Math.max(0, Math.min(100, v))
  let color = 'var(--chart-green)'
  if (n >= warnAt + 15) color = 'var(--danger)'
  else if (n >= warnAt) color = 'var(--warn)'
  return { width: n + '%', background: color }
}
function goNode(name) {
  if (name) router.push('/node/' + name)
}
</script>

<style scoped>
.host-overview {
  display: flex;
  flex-direction: column;
  gap: 18px;
}
.group-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 10px;
}
.group-name {
  font-size: 14px;
  font-weight: 700;
  color: var(--text);
}
.group-count {
  font-size: 11px;
  color: var(--text-dim);
  background: var(--bg-elev);
  border-radius: 10px;
  padding: 1px 8px;
}
.host-wrap {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
}
.host-card {
  width: 220px;
  background: var(--bg-elev);
  border-radius: 10px;
  padding: 12px;
  border: 1px solid var(--border);
  cursor: pointer;
  transition: transform 0.15s, border-color 0.15s, box-shadow 0.15s;
}
.host-card:hover {
  transform: translateY(-2px);
  border-color: var(--accent);
  box-shadow: 0 6px 18px rgba(0, 0, 0, 0.25);
}
.host-card.down {
  opacity: 0.7;
}
.hc-top {
  display: flex;
  align-items: center;
  gap: 8px;
}
.hc-name {
  font-size: 14px;
  font-weight: 700;
  flex: 1;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.hc-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}
.hc-dot.up {
  background: var(--chart-green);
  box-shadow: 0 0 6px var(--chart-green);
}
.hc-dot.down {
  background: var(--danger);
  box-shadow: 0 0 6px var(--danger-dim);
}
.hc-ip {
  font-size: 11px;
  color: var(--text-dim);
  margin: 2px 0 10px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.hc-metrics {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.hc-m {
  display: grid;
  grid-template-columns: 32px 1fr 36px;
  align-items: center;
  gap: 6px;
  font-size: 11px;
}
.hc-m-label {
  color: var(--text-dim);
}
.hc-bar {
  height: 5px;
  border-radius: 3px;
  background: var(--bg-card);
  overflow: hidden;
}
.hc-bar-fill {
  height: 100%;
  border-radius: 3px;
  transition: width 0.4s ease;
}
.hc-m-val {
  text-align: right;
  color: var(--text);
  font-variant-numeric: tabular-nums;
}
.host-empty {
  color: var(--text-muted);
  text-align: center;
  padding: 30px;
}
</style>
