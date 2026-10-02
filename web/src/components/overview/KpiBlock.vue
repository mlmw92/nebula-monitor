<template>
  <div class="kpi-grid">
    <div v-for="k in kpis" :key="k.label" class="kpi-card">
      <div class="kpi-icon">
        <el-icon :size="18"><component :is="k.icon" /></el-icon>
      </div>
      <div class="kpi-main">
        <div class="kpi-k">{{ k.label }}</div>
        <div class="kpi-v" :class="{ bad: k.tone === 'bad' }">{{ k.value }}</div>
        <div class="kpi-f">{{ k.foot }}</div>
        <VChart
          v-if="series(k.label).length > 1"
          class="kpi-spark"
          :option="sparkOption(series(k.label))"
          autoresize
        />
      </div>
    </div>
    <div v-if="!kpis || kpis.length === 0" class="kpi-empty">
      <EmptyState
        :icon="DataLine"
        title="暂无指标数据"
        :hints="[
          '确认 Agent 已上报、且主机处于在线状态',
          '刚添加的主机，第一个采样点通常需要 1 分钟',
        ]"
      />
    </div>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import VChart from 'vue-echarts'
import { DataLine } from '@element-plus/icons-vue'
import EmptyState from '../common/EmptyState.vue'

const props = defineProps({
  kpis: { type: Array, default: () => [] },
})

/* =========================================================
 * 迷你趋势（sparkline）
 * 数据来源：每次首页刷新拿到的真实 KPI 值，滚动保存最近 N 个采样点。
 * 刻意不做插值、不伪造历史——所以刚打开页面时曲线是空的，跑几分钟后才成形。
 * 也不新增接口：/api/v1/nodes/latest 只给最新一个点，无法反推历史序列。
 * ========================================================= */
const HISTORY_KEY = 'nebula-kpi-history'
const MAX_POINTS = 24
const HISTORY_TTL = 24 * 60 * 60 * 1000

function loadHistory() {
  try {
    const raw = localStorage.getItem(HISTORY_KEY)
    const d = raw ? JSON.parse(raw) : null
    return d && typeof d === 'object' ? d : {}
  } catch (e) {
    return {}
  }
}

const history = ref(loadHistory())

watch(
  () => props.kpis,
  (list) => {
    if (!list || !list.length) return
    const now = Date.now()
    const next = { ...history.value }
    let changed = false
    for (const k of list) {
      const arr = (next[k.label] || []).filter((p) => p && now - p.t < HISTORY_TTL)
      const v = Number(k.value)
      if (Number.isNaN(v)) continue
      const last = arr[arr.length - 1]
      // 同一轮刷新（含 keep-alive 唤醒）不重复记点
      if (last && last.v === v && now - last.t < 5000) continue
      arr.push({ t: now, v })
      next[k.label] = arr.slice(-MAX_POINTS)
      changed = true
    }
    if (!changed) return
    history.value = next
    try {
      localStorage.setItem(HISTORY_KEY, JSON.stringify(next))
    } catch (e) {
      /* 容量错误忽略 */
    }
  },
  { deep: true, immediate: true }
)

function series(label) {
  const arr = history.value[label] || []
  return arr.map((p) => p.v)
}

/* ECharts 取不到 CSS 变量，只能读计算后的值；换肤后要重新读，否则颜色不跟随 */
const themeTick = ref(0)
function onThemeChanged() {
  themeTick.value += 1
}
const accent = computed(() => {
  themeTick.value // 依赖：换肤时重新取色
  if (typeof window === 'undefined' || !document.body) return '#4a9df0'
  const v = getComputedStyle(document.body).getPropertyValue('--accent').trim()
  return v || '#4a9df0'
})

onMounted(() => window.addEventListener('nebula:theme-changed', onThemeChanged))
onBeforeUnmount(() => window.removeEventListener('nebula:theme-changed', onThemeChanged))

function sparkOption(data) {
  const color = accent.value
  return {
    animation: false,
    grid: { left: 0, right: 0, top: 2, bottom: 0 },
    xAxis: { type: 'category', show: false, boundaryGap: false, data: data.map((_, i) => i) },
    yAxis: { type: 'value', show: false, scale: true },
    series: [
      {
        type: 'line',
        data,
        showSymbol: false,
        smooth: true,
        lineStyle: { width: 1.5, color },
        itemStyle: { color },
        areaStyle: { color, opacity: 0.12 },
      },
    ],
  }
}
</script>

<style scoped>
.kpi-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 12px;
  height: 100%;
}
/* 卡片嵌在"关键指标"区块卡内部，所以用 s2（比外层卡亮一级）表达层级 */
.kpi-card {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  min-height: 84px;
  padding: 14px 16px;
  border-radius: var(--r-md);
  border: 1px solid var(--bd);
  background: var(--s2);
  transition: border-color var(--dur-1) var(--ease);
}
.kpi-card:hover {
  border-color: var(--bd-hi);
}
/* 图标一律中性色：颜色只留给"真实异常"，不做分类配色 */
.kpi-icon {
  width: 36px;
  height: 36px;
  border-radius: var(--r-lg);
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  background: var(--fill-2);
  color: var(--t2);
}
.kpi-main {
  min-width: 0;
  flex: 1;
}
.kpi-k {
  font-size: var(--fs-sm);
  color: var(--t2);
}
.kpi-v {
  font-size: 26px;
  font-weight: 700;
  font-family: var(--mono);
  letter-spacing: -0.02em;
  line-height: 1.15;
  margin-top: 2px;
  color: var(--t1);
}
/* 只有真实异常（活跃告警里有 critical）才允许出现语义色 */
.kpi-v.bad {
  color: var(--danger);
}
.kpi-f {
  font-size: var(--fs-xs);
  color: var(--t3);
  margin-top: 1px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.kpi-spark {
  display: block;
  width: 100%;
  height: 36px;
  margin-top: 8px;
}
.kpi-empty {
  grid-column: 1 / -1;
}
</style>
