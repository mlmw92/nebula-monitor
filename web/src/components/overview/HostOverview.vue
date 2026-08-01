<template>
  <div class="host-overview">
    <div v-if="!groupedHosts || groupedHosts.length === 0" class="host-empty">暂无主机数据</div>
    <div v-for="g in groupedHosts" :key="g.group" class="host-group">
      <div class="group-head">
        <span class="group-name">{{ g.group }}</span>
        <span class="group-count">{{ g.hosts.length }}</span>
        <span class="group-meta">
          在线 {{ countByStatus(g.hosts, 'online') }} · 离线 {{ countByStatus(g.hosts, 'offline') }}
        </span>
      </div>
      <div class="host-grid">
        <el-popover
          v-for="h in g.hosts"
          :key="h.hostname"
          placement="top"
          :width="280"
          trigger="hover"
          :show-after="80"
          popper-class="host-popover"
        >
          <template #reference>
            <div
              class="host-cell"
              :class="cellCls(h)"
              :title="displayName(h) + ' · ' + (h.ip || h.hostname)"
              @click="goNode(h.hostname)"
            >
              <span class="hc-dot"></span>
              <span class="hc-label">{{ shortName(h) }}</span>
            </div>
          </template>
          <div class="hp-host">
            <div class="hp-title">
              <OsIcon :os="h.os" />
              <span class="hp-name">{{ displayName(h) }}</span>
              <span class="hp-status" :class="h.status === 'online' ? 'up' : 'down'">
                {{ h.status === 'online' ? '在线' : '离线' }}
              </span>
            </div>
            <div class="hp-ip">{{ h.ip || h.hostname }}</div>
            <div class="hp-metrics">
              <div class="hp-m">
                <span class="hp-m-label">CPU</span>
                <div class="hp-bar"><div class="hp-bar-fill" :style="barStyle(h.metrics.cpu, 70)"></div></div>
                <span class="hp-m-val">{{ pct(h.metrics.cpu) }}</span>
              </div>
              <div class="hp-m">
                <span class="hp-m-label">内存</span>
                <div class="hp-bar"><div class="hp-bar-fill" :style="barStyle(h.metrics.mem, 80)"></div></div>
                <span class="hp-m-val">{{ pct(h.metrics.mem) }}</span>
              </div>
              <div class="hp-m">
                <span class="hp-m-label">磁盘</span>
                <div class="hp-bar"><div class="hp-bar-fill" :style="barStyle(h.metrics.disk, 85)"></div></div>
                <span class="hp-m-val">{{ pct(h.metrics.disk) }}</span>
              </div>
            </div>
            <div class="hp-foot">点击查看主机详情 →</div>
          </div>
        </el-popover>
      </div>
    </div>
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
// 用于方块内的短名：取 displayName / hostname 前 4 个字符
function shortName(h) {
  const n = displayName(h) || h.hostname || '?'
  return n.length > 6 ? n.slice(0, 6) : n
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
// 根据负载+在线状态决定方块颜色：离线红、任意指标偏高橙、否则绿
function cellCls(h) {
  if (h.status !== 'online') return 'cell-down'
  const m = h.metrics || {}
  if ((m.cpu ?? 0) >= 85 || (m.mem ?? 0) >= 95 || (m.disk ?? 0) >= 100) return 'cell-bad'
  if ((m.cpu ?? 0) >= 70 || (m.mem ?? 0) >= 80 || (m.disk ?? 0) >= 85) return 'cell-warn'
  return 'cell-up'
}
function countByStatus(hosts, st) {
  return hosts.filter((h) => (st === 'online' ? h.status === 'online' : h.status !== 'online')).length
}
function goNode(name) {
  if (name) router.push('/node/' + name)
}
</script>

<style scoped>
.host-overview {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.group-head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 8px;
}
.group-name {
  font-size: 13px;
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
.group-meta {
  font-size: 11px;
  color: var(--text-muted);
  margin-left: auto;
}
.host-grid {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.host-cell {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 5px 10px;
  background: var(--bg-elev);
  border: 1px solid var(--border);
  border-radius: 6px;
  cursor: pointer;
  font-size: 12px;
  line-height: 1;
  user-select: none;
  transition: transform 0.12s, border-color 0.12s, box-shadow 0.12s;
}
.host-cell:hover {
  transform: translateY(-1px);
  border-color: var(--accent);
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.2);
  z-index: 1;
}
.hc-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  flex-shrink: 0;
}
.hc-label {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 90px;
  color: var(--text);
}
.cell-up {
  border-color: color-mix(in srgb, var(--chart-green) 40%, var(--border));
}
.cell-up .hc-dot {
  background: var(--chart-green);
  box-shadow: 0 0 6px var(--chart-green);
}
.cell-warn {
  border-color: color-mix(in srgb, var(--warn) 50%, var(--border));
}
.cell-warn .hc-dot {
  background: var(--warn);
  box-shadow: 0 0 6px var(--warn);
}
.cell-bad {
  border-color: color-mix(in srgb, var(--danger) 50%, var(--border));
}
.cell-bad .hc-dot {
  background: var(--danger);
  box-shadow: 0 0 6px var(--danger);
}
.cell-down {
  border-color: color-mix(in srgb, var(--danger) 50%, var(--border));
  opacity: 0.7;
}
.cell-down .hc-dot {
  background: var(--text-muted);
  box-shadow: none;
}
.cell-down .hc-label {
  color: var(--text-dim);
  text-decoration: line-through;
}
.host-empty {
  color: var(--text-muted);
  text-align: center;
  padding: 30px;
  font-size: 13px;
}
</style>

<style>
/* popover 浮层样式（不能 scoped） */
.host-popover {
  padding: 10px 12px !important;
  min-width: 240px;
}
.host-popover .hp-host {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.host-popover .hp-title {
  display: flex;
  align-items: center;
  gap: 8px;
}
.host-popover .hp-name {
  font-size: 14px;
  font-weight: 700;
  flex: 1;
}
.host-popover .hp-status {
  font-size: 11px;
  padding: 1px 7px;
  border-radius: 10px;
  font-weight: 600;
}
.host-popover .hp-status.up {
  color: var(--chart-green);
  background: color-mix(in srgb, var(--chart-green) 16%, transparent);
}
.host-popover .hp-status.down {
  color: var(--danger);
  background: color-mix(in srgb, var(--danger) 16%, transparent);
}
.host-popover .hp-ip {
  font-size: 11px;
  color: var(--text-dim);
  font-family: var(--mono);
}
.host-popover .hp-metrics {
  display: flex;
  flex-direction: column;
  gap: 5px;
  padding-top: 4px;
  border-top: 1px dashed var(--border);
}
.host-popover .hp-m {
  display: grid;
  grid-template-columns: 32px 1fr 38px;
  align-items: center;
  gap: 6px;
  font-size: 11px;
}
.host-popover .hp-m-label {
  color: var(--text-dim);
}
.host-popover .hp-bar {
  height: 5px;
  border-radius: 3px;
  background: var(--bg-elev);
  overflow: hidden;
}
.host-popover .hp-bar-fill {
  height: 100%;
  border-radius: 3px;
  transition: width 0.4s ease;
}
.host-popover .hp-m-val {
  text-align: right;
  color: var(--text);
  font-variant-numeric: tabular-nums;
}
.host-popover .hp-foot {
  font-size: 10px;
  color: var(--accent);
  text-align: right;
  margin-top: 2px;
}
</style>
