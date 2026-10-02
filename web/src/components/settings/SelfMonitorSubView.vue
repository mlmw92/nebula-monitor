<template>
  <div class="selfmon-view">
    <div class="toolbar">
      <span class="field-hint">
        自监控指标以 <code v-pre>self_*</code> 前缀写入时序库，可直接用「指标浏览」查询、
        用告警规则监控 Server 自身；探针 <code v-pre>/healthz</code>、<code v-pre>/readyz</code> 免登录。
      </span>
      <div class="toolbar-actions">
        <el-switch v-model="autoRefresh" active-text="自动刷新" size="small" />
        <el-button size="small" :loading="loading" @click="load">刷新</el-button>
      </div>
    </div>

    <!-- 健康探针 -->
    <div class="probe-row">
      <div class="probe">
        <span class="probe-label">存活 /healthz</span>
        <el-tag :type="health.ok ? 'success' : 'danger'" size="small" effect="dark">
          {{ health.ok ? '正常' : '异常' }}
        </el-tag>
        <span class="field-hint inline">
          版本 {{ health.body.version || '—' }} · 运行 {{ uptimeText }}
        </span>
      </div>
      <div class="probe">
        <span class="probe-label">就绪 /readyz</span>
        <el-tag :type="ready.ok ? 'success' : 'danger'" size="small" effect="dark">
          {{ ready.ok ? '就绪' : '降级' }}
        </el-tag>
        <span v-for="(c, name) in ready.body.checks || {}" :key="name" class="field-hint inline">
          <el-tag :type="c.ok ? 'info' : 'danger'" size="small" effect="plain">{{ name }}</el-tag>
          <span v-if="!c.ok" class="bad-detail">{{ c.detail }}</span>
        </span>
      </div>
    </div>

    <el-alert
      v-if="error"
      :title="error"
      type="warning"
      :closable="false"
      show-icon
      class="err"
    />

    <!-- 关键指标 -->
    <el-descriptions :column="4" border size="small" class="grid">
      <el-descriptions-item label="goroutine">{{ m.goroutines ?? '—' }}</el-descriptions-item>
      <el-descriptions-item label="堆内存">{{ mb(m.heapAllocBytes) }}</el-descriptions-item>
      <el-descriptions-item label="GC 次数">{{ m.gcCount ?? '—' }}</el-descriptions-item>
      <el-descriptions-item label="WebSocket 连接">{{ m.wsConnections ?? '—' }}</el-descriptions-item>

      <el-descriptions-item label="HTTP 请求">{{ m.http?.requests ?? '—' }}</el-descriptions-item>
      <el-descriptions-item label="HTTP 错误">
        <span :class="{ bad: (m.http?.errors || 0) > 0 }">{{ m.http?.errors ?? '—' }}</span>
      </el-descriptions-item>
      <el-descriptions-item label="TSDB 写入">{{ m.tsdb?.writeTotal ?? '—' }}</el-descriptions-item>
      <el-descriptions-item label="TSDB 写入失败">
        <span :class="{ bad: (m.tsdb?.writeErrors || 0) > 0 }">{{ m.tsdb?.writeErrors ?? '—' }}</span>
      </el-descriptions-item>

      <el-descriptions-item label="TSDB 查询">{{ m.tsdb?.queryTotal ?? '—' }}</el-descriptions-item>
      <el-descriptions-item label="TSDB 查询失败">
        <span :class="{ bad: (m.tsdb?.queryErrors || 0) > 0 }">{{ m.tsdb?.queryErrors ?? '—' }}</span>
      </el-descriptions-item>
      <el-descriptions-item label="通知成功">{{ m.notify?.sent ?? '—' }}</el-descriptions-item>
      <el-descriptions-item label="通知失败">
        <span :class="{ bad: (m.notify?.failed || 0) > 0 }">{{ m.notify?.failed ?? '—' }}</span>
      </el-descriptions-item>

      <el-descriptions-item label="告警评估轮次">{{ m.alert?.evalTotal ?? '—' }}</el-descriptions-item>
      <el-descriptions-item label="评估节拍">
        <span :class="{ bad: evalStale }">{{ evalAgeText }}</span>
      </el-descriptions-item>
      <el-descriptions-item label="活跃告警">
        {{ m.alert?.firing ?? '—' }}（抑制 {{ m.alert?.suppressed ?? 0 }}）
      </el-descriptions-item>
      <el-descriptions-item label="节点在线">
        {{ m.nodes?.online ?? '—' }} / {{ m.nodes?.total ?? '—' }}
      </el-descriptions-item>
    </el-descriptions>

    <!-- 按渠道通知 -->
    <div v-if="channels.length" class="channels">
      <div class="section-title">按渠道通知</div>
      <el-table :data="channels" size="small" stripe>
        <el-table-column prop="channel" label="渠道" min-width="120" />
        <el-table-column prop="sent" label="成功" width="100" />
        <el-table-column label="失败" width="100">
          <template #default="{ row }">
            <span :class="{ bad: row.failed > 0 }">{{ row.failed }}</span>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 趋势 -->
    <div class="trend">
      <div class="section-title">
        趋势（近 1 小时）
        <span class="field-hint inline">
          数据来自时序库 <code v-pre>self_goroutines</code> / <code v-pre>self_heap_alloc_bytes</code>
        </span>
      </div>
      <div ref="chartRef" class="chart" />
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted, nextTick } from 'vue'
import * as echarts from 'echarts'
import http from '../../api/http'
import { tokenColor } from '../../charts/echarts'
const AXIS_COLOR = tokenColor('t2', '#9fb3c8')

const loading = ref(false)
const error = ref('')
const autoRefresh = ref(true)
const m = ref({})
const nodeLabel = ref('')
const health = ref({ ok: false, body: {} })
const ready = ref({ ok: false, body: {} })
const chartRef = ref(null)
let chartInstance = null
let timer = null

const channels = computed(() => {
  const by = m.value.notify?.byChannel || {}
  return Object.entries(by).map(([channel, c]) => ({ channel, sent: c.sent, failed: c.failed }))
})

const uptimeText = computed(() => formatDuration(health.value.body.uptimeSeconds ?? m.value.uptimeSeconds))
const evalAgeText = computed(() => {
  const age = m.value.alert?.evalAgeSeconds
  if (age === undefined || age === null) return '—'
  if (age < 0) return '尚未评估'
  return age + ' 秒前'
})
// 评估周期 3 倍仍未评估即视为停摆（与 /readyz 同口径）
const evalStale = computed(() => {
  const age = m.value.alert?.evalAgeSeconds
  const iv = m.value.alert?.evalIntervalSeconds || 0
  if (!iv || age === undefined || age === null) return false
  return age < 0 || age > iv * 3 + 5
})

function formatDuration(sec) {
  if (sec === undefined || sec === null) return '—'
  const s = Number(sec)
  if (s < 60) return s + ' 秒'
  if (s < 3600) return Math.floor(s / 60) + ' 分钟'
  const h = Math.floor(s / 3600)
  const mm = Math.floor((s % 3600) / 60)
  return mm ? `${h} 小时 ${mm} 分` : `${h} 小时`
}

function mb(bytes) {
  if (bytes === undefined || bytes === null) return '—'
  const v = Number(bytes)
  if (v < 1024 * 1024) return (v / 1024).toFixed(1) + ' KB'
  return (v / 1024 / 1024).toFixed(1) + ' MB'
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const probe = await http.probeHealth()
    health.value = probe.health
    ready.value = probe.ready
  } catch (e) {
    error.value = '探针请求失败：' + e.message
  }
  try {
    const status = await http.getSelfStatus()
    m.value = status.metrics || {}
    nodeLabel.value = status.nodeLabel || status.metrics?.node || ''
  } catch (e) {
    // 缺 dashboard:read 时只看到探针部分，仍应给出明确原因而不是空白页
    error.value = error.value || '读取自监控快照失败：' + e.message
  }
  loading.value = false
  await nextTick()
  renderTrend()
}

async function renderTrend() {
  if (!chartRef.value || !nodeLabel.value) return
  const end = Date.now()
  const start = end - 3600 * 1000
  const query = (metric) =>
    http.get(`/api/v1/query/range?node=${encodeURIComponent(nodeLabel.value)}&metric=${metric}&start=${start}&end=${end}&step=30`)
  try {
    const [goroutines, heap] = await Promise.all([
      query('self_goroutines').catch(() => ({})),
      query('self_heap_alloc_bytes').catch(() => ({})),
    ])
    const series = []
    for (const s of goroutines.series || []) {
      series.push({ name: 'goroutine', type: 'line', smooth: true, showSymbol: false, data: s.points.map((p) => [p.timestamp, p.value]) })
    }
    for (const s of heap.series || []) {
      series.push({
        name: '堆内存 (MB)', type: 'line', yAxisIndex: 1, smooth: true, showSymbol: false,
        data: s.points.map((p) => [p.timestamp, +(p.value / 1024 / 1024).toFixed(1)]),
      })
    }
    if (chartInstance) { chartInstance.dispose(); chartInstance = null }
    chartInstance = echarts.init(chartRef.value)
    chartInstance.setOption({
      tooltip: { trigger: 'axis' },
      legend: { data: series.map((s) => s.name), textStyle: { color: AXIS_COLOR } },
      grid: { left: 55, right: 60, top: 40, bottom: 30 },
      xAxis: { type: 'time' },
      yAxis: [
        { type: 'value', name: 'goroutine' },
        { type: 'value', name: 'MB' },
      ],
      series,
    })
  } catch (e) {
    // 趋势仅作参考，失败不影响指标明细展示
  }
}

onMounted(() => {
  load()
  timer = setInterval(() => { if (autoRefresh.value) load() }, 10000)
})

onUnmounted(() => {
  if (timer) clearInterval(timer)
  if (chartInstance) { chartInstance.dispose(); chartInstance = null }
})
</script>

<style scoped>
.selfmon-view {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
}
.toolbar-actions {
  display: flex;
  align-items: center;
  gap: 10px;
}
.probe-row {
  display: flex;
  gap: 28px;
  flex-wrap: wrap;
}
.probe {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.probe-label {
  font-size: 13px;
  font-weight: 600;
}
.field-hint {
  font-size: 12px;
  color: var(--text-dim);
}
.field-hint.inline {
  margin-left: 4px;
}
.bad-detail {
  margin-left: 6px;
  font-size: 12px;
  color: var(--el-color-danger, #f56c6c);
}
.bad {
  color: var(--el-color-danger, #f56c6c);
  font-weight: 600;
}
.err {
  margin-bottom: 4px;
}
.section-title {
  font-size: 13px;
  font-weight: 600;
  margin-bottom: 8px;
}
.chart {
  width: 100%;
  height: 260px;
}
.trend {
  margin-top: 4px;
}
code {
  font-family: var(--mono);
  font-size: 12px;
}
</style>
