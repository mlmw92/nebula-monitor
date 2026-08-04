<template>
  <div class="scroll-table glass">
    <div class="st-head">
      <div class="st-title">
        <i class="st-bar"></i>
        <span>{{ title }}</span>
      </div>
      <span class="st-count" v-if="rows.length">{{ rows.length }} 条</span>
    </div>
    <div class="st-grid" :style="gridStyle">
      <div class="st-col-head" v-for="(col, i) in columns" :key="i" :style="colStyle(col)">
        {{ col.label }}
      </div>
    </div>
    <div class="st-body" ref="bodyRef" @mouseenter="paused = true" @mouseleave="paused = false">
      <div class="st-scroll" :class="{ pause: paused }" v-if="displayRows.length">
        <div class="st-row" v-for="(row, ri) in displayRows" :key="ri" :style="gridStyle">
          <div class="st-cell" v-for="(col, ci) in columns" :key="ci" :style="colStyle(col)">
            <span v-if="col.type === 'dot'" class="st-dot" :class="row[col.key] ? 'on' : 'off'"></span>
            <span v-else-if="col.type === 'badge'" :class="['st-badge', badgeClass(row, col)]">{{ row[col.key] }}</span>
            <span v-else-if="col.type === 'bar'" class="st-bar-val">
              <i class="st-bar-track"><i class="st-bar-fill" :style="barStyle(row, col)"></i></i>
              <span>{{ col.fmt ? col.fmt(row[col.key]) : row[col.key] }}</span>
            </span>
            <span v-else :class="{ 'st-num': col.type === 'num' }">{{ col.fmt ? col.fmt(row[col.key]) : row[col.key] }}</span>
          </div>
        </div>
      </div>
      <div class="st-empty" v-else>暂无数据</div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'

const props = defineProps({
  title: { type: String, default: '' },
  columns: { type: Array, default: () => [] },
  rows: { type: Array, default: () => [] },
  speed: { type: Number, default: 0.5 },
  rowHeight: { type: Number, default: 34 },
})

const bodyRef = ref(null)
const paused = ref(false)
let offset = 0
let raf = null

const displayRows = computed(() => {
  const r = props.rows || []
  if (!r.length) return []
  return [...r, ...r]
})

const gridStyle = computed(() => ({
  display: 'grid',
  gridTemplateColumns: props.columns.map((c) => c.width || '1fr').join(' '),
  gap: '0',
}))

function colStyle(col) {
  const s = {}
  if (col.align) s.textAlign = col.align
  return s
}

function badgeClass(row, col) {
  const v = row[col.key]
  if (col.badgeMap) return col.badgeMap(v, row)
  if (v === '故障' || v === 'critical') return 'danger'
  if (v === '预警' || v === 'warning') return 'warn'
  if (v === '提示' || v === 'info') return 'info'
  if (v === '在线' || v === 'up' || v === true || v === '健康') return 'on'
  if (v === '离线' || v === 'down' || v === false) return 'off'
  return 'tag'
}

function barStyle(row, col) {
  const v = row[col.key]
  const max = col.max ? col.max(props.rows) : 100
  const pct = max > 0 ? Math.min((v / max) * 100, 100) : 0
  let color = 'var(--accent)'
  if (col.color) {
    color = typeof col.color === 'function' ? col.color(v, row) : col.color
  } else if (pct >= 90) color = 'var(--danger)'
  else if (pct >= 70) color = 'var(--warn)'
  return { width: pct + '%', background: color }
}

function tick() {
  if (!paused.value && bodyRef.value) {
    const el = bodyRef.value
    const half = el.scrollHeight / 2
    offset += props.speed
    if (offset >= half) offset = 0
    el.scrollTop = offset
  }
  raf = requestAnimationFrame(tick)
}

onMounted(() => {
  raf = requestAnimationFrame(tick)
})
onUnmounted(() => {
  raf && cancelAnimationFrame(raf)
})

watch(
  () => props.rows,
  () => {
    if (bodyRef.value) bodyRef.value.scrollTop = 0
    offset = 0
  }
)
</script>

<style scoped>
.scroll-table {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  padding: 10px 12px;
}
.st-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 6px;
  flex-shrink: 0;
}
.st-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 14px;
  font-weight: 600;
  color: var(--text);
  letter-spacing: 0.04em;
}
.st-bar {
  width: 3px;
  height: 14px;
  background: var(--accent);
  border-radius: 2px;
  box-shadow: 0 0 8px var(--accent-glow);
}
.st-count {
  font-size: 11px;
  color: var(--text-muted);
  font-family: var(--mono);
}
.st-grid {
  flex-shrink: 0;
}
.st-col-head {
  padding: 6px 8px;
  font-size: 11px;
  color: var(--text-muted);
  border-bottom: 1px solid var(--border);
  text-transform: uppercase;
  letter-spacing: 0.05em;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.st-body {
  flex: 1;
  min-height: 0;
  overflow: hidden;
  position: relative;
}
.st-scroll {
  will-change: transform;
}
.st-row {
  display: grid;
  align-items: center;
  height: 34px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.04);
  transition: background 0.15s;
}
.st-row:hover {
  background: var(--accent-dim);
}
.st-cell {
  padding: 0 8px;
  font-size: 12px;
  color: var(--text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.st-num {
  font-family: var(--mono);
  text-align: right;
}
.st-dot {
  display: inline-block;
  width: 7px;
  height: 7px;
  border-radius: 50%;
}
.st-dot.on {
  background: var(--chart-green);
  box-shadow: 0 0 6px var(--chart-green);
}
.st-dot.off {
  background: var(--text-muted);
}
.st-badge {
  display: inline-block;
  padding: 1px 8px;
  border-radius: 8px;
  font-size: 10px;
  font-weight: 600;
}
.st-badge.on {
  background: rgba(34, 197, 94, 0.15);
  color: var(--chart-green);
}
.st-badge.off {
  background: rgba(239, 68, 68, 0.15);
  color: var(--danger);
}
.st-badge.danger {
  background: rgba(239, 68, 68, 0.18);
  color: var(--danger);
  box-shadow: 0 0 8px rgba(239, 68, 68, 0.3);
}
.st-badge.warn {
  background: rgba(245, 158, 11, 0.15);
  color: var(--warn);
}
.st-badge.info {
  background: rgba(59, 130, 246, 0.15);
  color: var(--info);
}
.st-badge.tag {
  background: rgba(34, 211, 238, 0.12);
  color: var(--accent);
  border: 1px solid rgba(34, 211, 238, 0.25);
}
.st-bar-val {
  display: flex;
  align-items: center;
  gap: 6px;
  font-family: var(--mono);
  font-size: 11px;
}
.st-bar-track {
  flex: 1;
  height: 6px;
  background: rgba(255, 255, 255, 0.06);
  border-radius: 3px;
  overflow: hidden;
  display: block;
  min-width: 40px;
}
.st-bar-fill {
  display: block;
  height: 100%;
  border-radius: 3px;
  transition: width 0.6s ease;
}
.st-empty {
  padding: 30px 0;
  text-align: center;
  color: var(--text-muted);
  font-size: 12px;
}
</style>
