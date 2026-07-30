<template>
  <div class="node-view">
    <!-- 面包屑 + 状态 -->
    <div class="breadcrumb" v-if="current">
      <el-icon class="bc-home"><HomeFilled /></el-icon>
      <span class="bc-item">主机监控</span>
      <el-icon class="bc-sep"><ArrowRight /></el-icon>
      <span class="bc-item bc-cur">{{ current.hostname }}<em v-if="current.ip"> · {{ current.ip }}</em></span>
      <span class="status-pill" :class="currentStatus">
        <i class="dot"></i>{{ currentStatus === 'online' ? '在线' : '离线' }}
      </span>
    </div>

    <!-- 顶部：主机选择 + 概要 -->
    <div class="head-panel glass">
      <div class="head-left">
        <el-select
          v-model="selected"
          filterable
          placeholder="选择主机"
          style="width: 260px"
          @change="onSelect"
        >
          <el-option
            v-for="n in nodes"
            :key="n.hostname"
            :value="n.hostname"
            :label="n.hostname + ' (' + (n.group || 'default') + ')'"
          />
        </el-select>
      </div>
      <div class="head-info" v-if="current">
        <span class="copyable mono"><Connection /> {{ current.ip || '-' }}
          <el-icon class="copy-btn" title="复制 IP" @click="copyText(current.ip)"><DocumentCopy /></el-icon>
        </span>
        <span><OsIcon :os="current.os" /> {{ current.os || '-' }}</span>
        <span v-if="current.version" class="mono"><el-icon><Cpu /></el-icon> Agent v{{ current.version }}</span>
        <span class="refresh-switch">
          <el-switch v-model="autoRefresh" inline-prompt active-text="刷新" inactive-text="暂停" @change="onAutoRefresh" />
        </span>
      </div>
    </div>

    <el-tabs v-model="activeTab" type="border-card" class="node-tabs">
      <!-- ============ Tab 1：主机概览 ============ -->
      <el-tab-pane label="主机概览" name="overview">
        <!-- 设备信息 -->
        <div class="section-title">设备信息</div>
        <div class="device-grid">
          <div class="dev-item"><span>主机名</span><strong class="with-copy">{{ current?.hostname || '-' }}<el-icon class="copy-btn" title="复制" @click="copyText(current?.hostname)"><DocumentCopy /></el-icon></strong></div>
          <div class="dev-item"><span>IP 地址</span><strong class="with-copy mono">{{ current?.ip || '-' }}<el-icon class="copy-btn" title="复制" @click="copyText(current?.ip)"><DocumentCopy /></el-icon></strong></div>
          <div class="dev-item"><span>操作系统</span><strong>{{ current?.os || '-' }}</strong></div>
          <div class="dev-item"><span>运行天数</span><strong class="mono">{{ uptimeDays }} 天（{{ bootTimeText }}）</strong></div>
          <div class="dev-item"><span>Agent 版本</span><strong class="mono">v{{ current?.version || '-' }}</strong></div>
          <div class="dev-item"><span>CPU 型号</span><strong :title="hostInfo?.cpuModel">{{ hostInfo?.cpuModel || '-' }}</strong></div>
          <div class="dev-item"><span>CPU 核数</span><strong class="mono">{{ hostInfo?.cpuCores || '-' }} 核</strong></div>
          <div class="dev-item">
            <span>系统负载</span>
            <strong class="mono">{{ rt.load1 }} / {{ rt.load5 }} / {{ rt.load15 }}</strong>
          </div>
        </div>

        <!-- 系统情况：环形图 -->
        <div class="section-title">系统情况</div>
        <div class="gauge-row">
          <div class="gauge-card">
            <div :ref="(el) => setRef(el, 'cpuGauge')" class="gauge"></div>
            <div class="gauge-label">CPU 使用率</div>
            <div class="gauge-sub">{{ hostInfo?.cpuCores || '-' }} 核</div>
          </div>
          <div class="gauge-card">
            <div :ref="(el) => setRef(el, 'memGauge')" class="gauge"></div>
            <div class="gauge-label">内存使用率</div>
            <div class="gauge-sub">{{ memUsedText }} / {{ memTotalText }}</div>
          </div>
          <div class="gauge-card">
            <div :ref="(el) => setRef(el, 'diskGauge')" class="gauge"></div>
            <div class="gauge-label">磁盘使用率</div>
            <div class="gauge-sub">{{ diskUsedText }} / {{ diskTotalText }}</div>
          </div>
          <div class="gauge-card">
            <div :ref="(el) => setRef(el, 'swapGauge')" class="gauge"></div>
            <div class="gauge-label">SWAP 使用率</div>
            <div class="gauge-sub">{{ swapUsedText }} / {{ swapTotalText }}</div>
          </div>
        </div>

        <!-- 端口状态 -->
        <div v-if="portStatuses.length > 0" class="port-section">
          <div class="section-title">端口状态</div>
          <div class="port-list">
            <div v-for="ps in portStatuses" :key="ps.port" class="port-item" :class="{ up: ps.up, down: !ps.up }">
              <span class="port-dot"></span>
              <span class="port-num">{{ ps.port }}</span>
              <span class="port-state">{{ ps.up ? '在线' : '离线' }}</span>
              <span v-if="ps.latency" class="port-latency">{{ ps.latency.toFixed(1) }}ms</span>
            </div>
          </div>
        </div>

        <!-- 实时趋势 + IO -->
        <div class="section-title">实时趋势</div>
        <div class="panel-hint">说明：以下为通过 WebSocket 实时上报的采样（每秒 1 条，横轴为采样时刻，仅保留最近 60 个点）。</div>
        <div class="metric-grid">
          <div class="metric-card">
            <div class="mc-head"><span class="mc-label">CPU 使用率</span><span class="mc-value" :class="rateClass(rt.cpu)">{{ rt.cpu }}<small>%</small></span></div>
            <div :ref="(el) => setRef(el, 'cpu')" class="mc-chart"></div>
            <div class="mc-desc">{{ rtInfo.cpu }}</div>
          </div>
          <div class="metric-card">
            <div class="mc-head"><span class="mc-label">内存使用率</span><span class="mc-value" :class="rateClass(rt.mem)">{{ rt.mem }}<small>%</small></span></div>
            <div :ref="(el) => setRef(el, 'mem')" class="mc-chart"></div>
            <div class="mc-desc">{{ rtInfo.mem }}</div>
          </div>
          <div class="metric-card">
            <div class="mc-head">
              <span class="mc-label">磁盘 IO</span>
              <div class="mc-stats">
                <span class="mc-stat"><i class="dot" style="background:#3b82f6"></i>读取 <b style="color:#3b82f6">{{ rt.diskRead }}</b></span>
                <span class="mc-stat"><i class="dot" style="background:#f59e0b"></i>写入 <b style="color:#f59e0b">{{ rt.diskWrite }}</b></span>
              </div>
            </div>
            <div :ref="(el) => setRef(el, 'diskio')" class="mc-chart"></div>
            <div class="mc-desc">{{ rtInfo.diskio }}</div>
          </div>
          <div class="metric-card">
            <div class="mc-head">
              <span class="mc-label">网络 IO</span>
              <div class="mc-stats">
                <span class="mc-stat"><i class="dot" style="background:#22c55e"></i>接收 <b style="color:#22c55e">{{ rt.net }}</b></span>
                <span class="mc-stat"><i class="dot" style="background:#a78bfa"></i>发送 <b style="color:#a78bfa">{{ rt.netSent }}</b></span>
              </div>
            </div>
            <div :ref="(el) => setRef(el, 'netio')" class="mc-chart"></div>
            <div class="mc-desc">{{ rtInfo.netio }}</div>
          </div>
        </div>

        <!-- 进程 TOP10 + 在线用户 + 告警 -->
        <div class="bottom-row">
          <div class="bottom-col">
            <div class="section-title section-head">
              进程占用 Top 10
              <el-input v-model="procSearch" placeholder="搜索进程名 / PID" clearable size="small" :prefix-icon="Search" class="proc-search" />
            </div>
            <el-table :data="filteredProcs" stripe size="small" max-height="320" :default-sort="{ prop: 'cpu', order: 'descending' }">
              <el-table-column prop="pid" label="PID" width="90" sortable />
              <el-table-column prop="name" label="进程名" min-width="160" show-overflow-tooltip />
              <el-table-column label="CPU %" width="120" sortable :sort-method="(a, b) => a.cpu - b.cpu">
                <template #default="{ row }">
                  <div class="proc-bar">
                    <div class="bar"><div class="bar-fill cyan" :style="{ width: Math.min(row.cpu, 100) + '%' }"></div></div>
                    <span class="mono">{{ row.cpu.toFixed(1) }}</span>
                  </div>
                </template>
              </el-table-column>
              <el-table-column label="MEM %" width="120" sortable :sort-method="(a, b) => a.mem - b.mem">
                <template #default="{ row }">
                  <div class="proc-bar">
                    <div class="bar"><div class="bar-fill" :class="memClass(row.mem)" :style="{ width: Math.min(row.mem, 100) + '%' }"></div></div>
                    <span class="mono">{{ row.mem.toFixed(1) }}</span>
                  </div>
                </template>
              </el-table-column>
            </el-table>
            <el-empty v-if="!filteredProcs.length" description="无进程数据" :image-size="50" />
          </div>
          <div class="bottom-col">
            <div class="section-title">
              在线 SSH 用户
              <el-tag size="small" type="info" effect="plain" style="margin-left: 8px">{{ hostInfo?.onlineUsers?.length || 0 }} 人</el-tag>
            </div>
            <el-table v-if="hostInfo?.onlineUsers?.length" :data="hostInfo.onlineUsers" stripe size="small" class="user-table">
              <el-table-column prop="user" label="用户" width="120" />
              <el-table-column prop="terminal" label="终端" width="110" />
              <el-table-column prop="loginAt" label="登录时间" min-width="150" />
              <el-table-column prop="from" label="来源 IP" min-width="130" show-overflow-tooltip />
            </el-table>
            <el-empty v-else description="无在线用户" :image-size="60" />

            <div class="section-title" style="margin-top: 16px">告警事件</div>
            <el-table :data="alertEvents" stripe size="small" max-height="220">
              <el-table-column label="时间" width="160">
                <template #default="{ row }">{{ fmtTime(row.startsAt || row.endsAt) }}</template>
              </el-table-column>
              <el-table-column prop="ruleName" label="规则" min-width="120" show-overflow-tooltip />
              <el-table-column label="状态" width="80">
                <template #default="{ row }">
                  <el-tag :type="row.state === 'firing' ? 'danger' : 'success'" size="small" effect="dark">{{ row.state === 'firing' ? '触发' : '恢复' }}</el-tag>
                </template>
              </el-table-column>
              <el-table-column label="级别" width="80">
                <template #default="{ row }">
                  <el-tag :type="sevType(row.severity)" size="small" effect="dark">{{ sevLabel(row.severity) }}</el-tag>
                </template>
              </el-table-column>
            </el-table>
            <el-empty v-if="!alertEvents.length" description="暂无告警" :image-size="50" />
          </div>
        </div>
      </el-tab-pane>

      <!-- ============ Tab 2：基础监控 ============ -->
      <el-tab-pane label="基础监控" name="monitor">
        <div class="tab-header">
          <span class="panel-title" style="margin: 0">基础监控</span>
        </div>
        <div class="panel-hint">说明：今日 / 昨天展示当天 0–24 点真实曲线；近 7 天 / 近 30 天展示每日平均值（每个点代表一天）。每个指标可独立切换时间范围。</div>
        <div class="monitor-grid">
          <div class="monitor-panel" v-for="p in monitorPanels" :key="p.key">
            <div class="monitor-panel-head">
              <span class="monitor-panel-title">{{ p.title }}</span>
              <el-select v-if="p.nic" v-model="netIface" size="small" @change="onNetIfaceChange" style="width: 120px">
                <el-option v-for="i in netIfaces" :key="i" :value="i" :label="i === 'all' ? '全部网卡' : i" />
              </el-select>
            </div>
            <div class="monitor-panel-tools">
              <el-radio-group v-model="panelRange[p.key]" size="small" @change="() => onPanelRangeChange(p.key)">
                <el-radio-button value="today">今日</el-radio-button>
                <el-radio-button value="yesterday">昨天</el-radio-button>
                <el-radio-button value="week">近7天</el-radio-button>
                <el-radio-button value="month">近30天</el-radio-button>
              </el-radio-group>
            </div>
            <div class="monitor-panel-desc">{{ p.desc }}</div>
            <div :ref="(el) => setMonitorRef(el, p.key)" class="monitor-chart"></div>
          </div>
        </div>
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<script setup>
import { ref, computed, reactive, onMounted, onUnmounted, watch, nextTick } from 'vue'
import { Connection, Cpu, HomeFilled, ArrowRight, DocumentCopy, Search } from '@element-plus/icons-vue'
import OsIcon from './OsIcon.vue'
import { ElMessage } from 'element-plus'
import http from '../api/http'
import { initChart, areaOption, areaMultiOption, monitorOption, gaugeOption, COLORS, rateShort } from '../charts/echarts'

let socket = null
let reconnectTimer = null
let nodeTimer = null
let alertTimer = null

const nodes = ref([])
const selected = ref('')
const activeTab = ref('overview')
const procs = ref([])
const alertEvents = ref([])
const netIface = ref('all')
const netIfaces = ref(['all'])
const loadingNode = ref(false)
const autoRefresh = ref(true)
const procSearch = ref('')
const portStatuses = ref([])

const rt = reactive({
  cpu: 0, mem: 0, disk: 0, net: '0 B/s', netSent: '0 B/s',
  netRecvTotal: '0 B', netSentTotal: '0 B',
  swap: 0, diskRead: '0 B/s', diskWrite: '0 B/s',
  load1: 0, load5: 0, load15: 0,
})

const nameToKey = {
  cpu_usage: 'cpu', mem_used_percent: 'mem', disk_used_percent: 'disk',
  network_recv_rate: 'net', network_sent_rate: 'netSent', swap_used_percent: 'swap',
  network_recv_total: 'netRecvTotal', network_sent_total: 'netSentTotal',
  disk_read_rate: 'diskRead', disk_write_rate: 'diskWrite',
  load1: 'load1', load5: 'load5', load15: 'load15',
}
const ioKeys = ['net', 'netSent', 'diskRead', 'diskWrite']
const byteKeys = ['netRecvTotal', 'netSentTotal']

// 实时趋势图缓冲：按指标拆分（磁盘 IO=读取/写入，网络 IO=接收/发送）
const buffers = { cpu: [], mem: [], diskRead: [], diskWrite: [], net: [], netSent: [] }
const charts = {}
const refs = {}
const setRef = (el, key) => { if (el) refs[key] = el }

// 实时趋势图定义：单序列(cpu/mem) 与 多序列(磁盘 IO/网络 IO)
const rtChartKeys = ['cpu', 'mem', 'diskio', 'netio']
const rtChartSeries = {
  cpu: ['cpu'],
  mem: ['mem'],
  diskio: ['diskRead', 'diskWrite'],
  netio: ['net', 'netSent'],
}
// 统一刷新所有实时趋势图（根据各自缓冲序列更新 series）
function refreshRealtime() {
  for (const k of rtChartKeys) {
    const c = charts[k]
    if (!c) continue
    c.setOption({ series: rtChartSeries[k].map((b) => ({ data: buffers[b] })) })
  }
}

const monitorPanels = [
  { key: 'cpu', title: 'CPU 使用率', type: 'percent', metrics: [{ name: 'cpu_usage', label: 'CPU' }], desc: 'CPU 占用百分比；今日/昨天为当天真实曲线，近7/30天为每日平均值。' },
  { key: 'mem', title: '内存使用率', type: 'percent', metrics: [{ name: 'mem_used_percent', label: '内存' }], desc: '物理内存占用百分比；横轴与时间维度同 CPU。' },
  { key: 'load', title: '系统负载', type: 'load', metrics: [{ name: 'load1', label: '1分钟' }, { name: 'load5', label: '5分钟' }, { name: 'load15', label: '15分钟' }], desc: '系统平均负载三条折线（1/5/15 分钟），数值约等或超过 CPU 核心数表示偏忙。' },
  { key: 'swap', title: 'SWAP 使用率', type: 'percent', metrics: [{ name: 'swap_used_percent', label: 'SWAP' }], desc: '交换分区占用百分比。' },
  { key: 'disk', title: '磁盘占用率', type: 'percent', desc: '所有磁盘空间汇总占用百分比。' },
  { key: 'diskio', title: '磁盘 IO', type: 'rate', sumDevices: true, metrics: [{ name: 'disk_read_rate', label: '读取' }, { name: 'disk_write_rate', label: '写入' }], desc: '磁盘读取/写入速率（所有磁盘汇总），纵轴自适应 KB/s·MB/s。' },
  { key: 'net', title: '网络流量', type: 'rate', nic: true, metrics: [{ name: 'network_recv_rate', label: '接收' }, { name: 'network_sent_rate', label: '发送' }], desc: '网络接收/发送速率，右上角可切换网卡查看。' },
]
const panelColors = {
  cpu: COLORS.cyan,
  mem: COLORS.purple,
  load: [COLORS.cyan, COLORS.amber, COLORS.red],
  swap: COLORS.amber,
  disk: COLORS.blue,
  diskio: [COLORS.blue, COLORS.amber],
  net: [COLORS.green, COLORS.purple],
}
// 每个面板独立的时间范围（今日/昨天/近7天/近30天）
const panelRange = reactive({})
monitorPanels.forEach((p) => { panelRange[p.key] = 'today' })
const monitorEls = {}
const monitorCharts = {}
// 实时趋势卡片含义说明（横轴为最近约 60 次采样的时间，每秒一条）
const rtInfo = {
  cpu: '实时 CPU 占用百分比，最近 60 秒（每秒采样）。',
  mem: '实时物理内存占用百分比，最近 60 秒。',
  diskio: '实时磁盘读取 / 写入速率，最近 60 秒（蓝=读取，橙=写入）。',
  netio: '实时网络接收 / 发送速率，最近 60 秒（绿=接收，紫=发送）。',
}
function ensureMonitorChart(key) {
  if (monitorEls[key] && !monitorCharts[key]) {
    const c = initChart(monitorEls[key])
    c.setOption(monitorOption({}))
    monitorCharts[key] = c
  }
  return monitorCharts[key]
}
const setMonitorRef = (el, key) => {
  if (el) {
    monitorEls[key] = el
    if (activeTab.value === 'monitor') {
      nextTick(() => {
        ensureMonitorChart(key)
        if (monitorCharts[key]) monitorCharts[key].resize()
        loadMonitor(selected.value)
      })
    }
  } else {
    delete monitorEls[key]
    if (monitorCharts[key]) { monitorCharts[key].dispose(); delete monitorCharts[key] }
  }
}

const current = computed(() => nodes.value.find((n) => n.hostname === selected.value) || null)
const currentStatus = computed(() => (current.value?.status === 'online' ? 'online' : 'offline'))
const hostInfo = computed(() => current.value?.hostInfo || null)
const uptimeDays = computed(() => {
  const bt = hostInfo.value?.bootTime
  if (!bt) return '-'
  return Math.floor((Date.now() / 1000 - bt) / 86400)
})
const bootTimeText = computed(() => {
  const bt = hostInfo.value?.bootTime
  if (!bt) return '-'
  return fmtTime(bt)
})

// 环形图子标签：已用 / 总量
const memTotalText = computed(() => hostInfo.value?.memoryTotal ? fmtBytes(hostInfo.value.memoryTotal) : '-')
const memUsedText = computed(() => {
  const t = hostInfo.value?.memoryTotal
  return t ? fmtBytes(t * num(rt.mem) / 100) : '-'
})
const diskTotalText = computed(() => hostInfo.value?.diskTotal ? fmtBytes(hostInfo.value.diskTotal) : '-')
const diskUsedText = computed(() => hostInfo.value?.diskUsed ? fmtBytes(hostInfo.value.diskUsed) : '-')
const swapTotalText = computed(() => '')
const swapUsedText = computed(() => (num(rt.swap) > 0 ? round1(rt.swap) + '%' : '-'))

// 进程搜索过滤
const filteredProcs = computed(() => {
  const q = (procSearch.value || '').trim().toLowerCase()
  if (!q) return procs.value
  return procs.value.filter((p) => (p.name || '').toLowerCase().includes(q) || String(p.pid).includes(q))
})

// 环形图阈值配色：0-60 绿 / 60-80 橙 / 80-100 红
function gaugeColor(v) {
  const n = num(v)
  return n >= 80 ? COLORS.red : n >= 60 ? COLORS.amber : COLORS.green
}

// 一键复制
function copyText(t) {
  if (!t) return
  const s = String(t)
  if (navigator.clipboard?.writeText) {
    navigator.clipboard.writeText(s).then(() => ElMessage.success('已复制: ' + s)).catch(() => {})
  } else {
    ElMessage.info(s)
  }
}

function num(v) { const n = Number(v); return isFinite(n) ? n : 0 }
function round1(v) { return Number((Number(v) || 0).toFixed(1)) }
function fmtRate(bps) {
  const b = Number(bps || 0)
  if (b >= 1 << 30) return (b / (1 << 30)).toFixed(2) + ' GB/s'
  if (b >= 1 << 20) return (b / (1 << 20)).toFixed(2) + ' MB/s'
  if (b >= 1 << 10) return (b / (1 << 10)).toFixed(2) + ' KB/s'
  return b.toFixed(0) + ' B/s'
}
function fmtBytes(bytes) {
  const b = Number(bytes || 0)
  if (b >= 1 << 30) return (b / (1 << 30)).toFixed(2) + ' GB'
  if (b >= 1 << 20) return (b / (1 << 20)).toFixed(2) + ' MB'
  if (b >= 1 << 10) return (b / (1 << 10)).toFixed(2) + ' KB'
  return b.toFixed(0) + ' B'
}
function fmtTime(ts) {
  if (!ts) return '-'
  // 同时兼容 Unix 秒（如 bootTime）与 Unix 毫秒（如告警事件 startsAt/endsAt）：
  // 数值 < 1e12 视为秒（10 位 ~2025 年）；否则视为毫秒（13 位）。
  let n = num(ts)
  if (n < 1e12) n *= 1000
  const d = new Date(n)
  if (isNaN(d.getTime())) return '-'
  const p = (x) => String(x).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}
function sevType(s) {
  return { critical: 'danger', warning: 'warning', info: 'info' }[s] || 'info'
}
function sevLabel(s) {
  return { critical: '紧急', warning: '警告', info: '信息' }[s] || s
}
function rateClass(v) { const n = num(v); return n >= 90 ? 'red' : n >= 70 ? 'amber' : 'green' }
function memClass(v) { return num(v) >= 50 ? 'amber' : 'green' }

// ---------- WebSocket 实时指标 ----------
function connectWS(name) {
  if (socket) { try { socket.close() } catch (e) {} socket = null }
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  const url = `${proto}://${location.host}/ws?topic=metrics&node=${encodeURIComponent(name)}`
  socket = new WebSocket(url)
  socket.onmessage = (ev) => {
    try {
      const msg = JSON.parse(ev.data)
      if (msg.type !== 'metrics' || !msg.data) return
      msg.data.forEach((d) => {
        const key = nameToKey[d.name]
        if (!key) return
        const val = d.value
        if (ioKeys.includes(key)) rt[key] = fmtRate(val)
        else if (byteKeys.includes(key)) rt[key] = fmtBytes(val)
        else rt[key] = round1(val)
        if (buffers[key]) {
          buffers[key].push([d.timestamp / 1e6, val])
          if (buffers[key].length > 60) buffers[key].shift()
        }
      })
      refreshRealtime()
      updateGauges()
    } catch (e) { /* ignore */ }
  }
  socket.onclose = () => {
    if (autoRefresh.value) reconnectTimer = setTimeout(() => { if (selected.value && autoRefresh.value) connectWS(selected.value) }, 3000)
  }
  socket.onerror = () => { try { socket.close() } catch (e) {} }
}

function updateGauges() {
  const setGauge = (chart, v) => {
    if (!chart) return
    const c = gaugeColor(v)
    chart.setOption({ series: [{ data: [{ value: num(v) }], progress: { itemStyle: { color: c } }, detail: { color: c } }] })
  }
  setGauge(charts.cpuGauge, rt.cpu)
  setGauge(charts.memGauge, rt.mem)
  setGauge(charts.diskGauge, rt.disk)
  setGauge(charts.swapGauge, rt.swap)
}

// ---------- 数据加载 ----------
async function loadNodes() {
  try {
    const data = await http.get('/api/v1/nodes')
    const list = data.nodes || []
    if (!selected.value && list.length) selected.value = list[0].hostname
    nodes.value = list
  } catch (e) { /* ignore */ }
}

async function loadProcesses(name) {
  try {
    const data = await http.get('/api/v1/processes?hostname=' + encodeURIComponent(name))
    procs.value = (data.processes || []).slice(0, 10)
  } catch (e) { /* ignore */ }
}

async function loadAlerts(name) {
  try {
    const data = await http.get('/api/v1/alerts?node=' + encodeURIComponent(name) + '&limit=50')
    alertEvents.value = data.alerts || []
  } catch (e) { /* ignore */ }
}

async function loadPortStatuses(name) {
  try {
    const upData = await http.get(`/api/v1/query/latest?node=${encodeURIComponent(name)}&metric=port_up`)
    const latData = await http.get(`/api/v1/query/latest?node=${encodeURIComponent(name)}&metric=port_latency`)
    const upMap = {}
    if (upData.series) for (const s of upData.series) {
      const port = s.labels?.port
      if (port && s.points?.length > 0) upMap[port] = s.points[s.points.length - 1].value > 0
    }
    const latMap = {}
    if (latData.series) for (const s of latData.series) {
      const port = s.labels?.port
      if (port && s.points?.length > 0) latMap[port] = s.points[s.points.length - 1].value
    }
    portStatuses.value = Object.keys(upMap).map(port => ({
      port,
      up: upMap[port],
      latency: latMap[port] || 0,
    })).sort((a, b) => a.port.localeCompare(b.port))
  } catch (e) { portStatuses.value = [] }
}

function onSelect(name) {
  selected.value = name
  connectWS(name)
  loadProcesses(name)
  loadAlerts(name)
  loadPortStatuses(name)
  if (activeTab.value === 'monitor') {
    nextTick(() => {
      initMonitorCharts()
      loadMonitor(name)
      setTimeout(() => Object.values(monitorCharts).forEach((c) => c.resize()), 200)
    })
  }
}

// ---------- 基础监控历史 ----------
function round2(v) { return Number((Number(v) || 0).toFixed(2)) }

function rangeBounds(mode) {
  const now = Date.now()
  const ds = new Date()
  ds.setHours(0, 0, 0, 0)
  const dayStart = ds.getTime()
  switch (mode) {
    case 'today': return [dayStart, now]
    case 'yesterday': return [dayStart - 86400000, dayStart]
    case 'week': return [dayStart - 6 * 86400000, now]
    case 'month': return [dayStart - 29 * 86400000, now]
  }
  return [now - 86400000, now]
}

// 抓取单指标原始序列（时间戳由纳秒转为毫秒）
async function fetchRaw(name, metric, start, end, step) {
  const d = await http.get(`/api/v1/query/range?node=${encodeURIComponent(name)}&metric=${encodeURIComponent(metric)}&start=${start}&end=${end}&step=${step}`)
  return (d.series || []).map((s) => ({
    labels: s.labels || {},
    points: (s.points || []).map((pt) => [pt.timestamp / 1e6, Number(pt.value)]),
  }))
}

// 多序列按时间戳求和合并为一条
function sumSeriesByTs(list) {
  const m = new Map()
  for (const s of list) for (const [ts, v] of s.points) m.set(ts, (m.get(ts) || 0) + Number(v))
  return Array.from(m.entries()).map(([ts, v]) => [ts, round2(v)]).sort((a, b) => a[0] - b[0])
}

// 按自然日分桶求均值（用于近7/30天视图）
function dailyAvg(points) {
  const m = new Map()
  for (const [ts, v] of points) {
    const d = new Date(ts)
    d.setHours(0, 0, 0, 0)
    const k = d.getTime()
    const e = m.get(k) || [0, 0]
    e[0] += Number(v)
    e[1] += 1
    m.set(k, e)
  }
  return Array.from(m.entries()).map(([k, e]) => [k, round2(e[0] / e[1])]).sort((a, b) => a[0] - b[0])
}

async function loadDiskOccupancy(name, start, end, step) {
  const [usedD, totalD] = await Promise.all([
    http.get(`/api/v1/query/range?node=${encodeURIComponent(name)}&metric=disk_used&start=${start}&end=${end}&step=${step}`),
    http.get(`/api/v1/query/range?node=${encodeURIComponent(name)}&metric=disk_total&start=${start}&end=${end}&step=${step}`),
  ])
  const sumByTs = (series) => {
    const m = new Map()
    for (const s of series || []) for (const p of s.points || []) {
      const ts = p.timestamp / 1e6
      m.set(ts, (m.get(ts) || 0) + Number(p.value))
    }
    return m
  }
  const used = sumByTs(usedD.series)
  const total = sumByTs(totalD.series)
  const pts = []
  for (const [ts, t] of total) if (t > 0) pts.push([ts, Number(((used.get(ts) || 0) / t * 100).toFixed(2))])
  pts.sort((a, b) => a[0] - b[0])
  return pts
}

// 网络流量：按网卡过滤/汇总，返回接收、发送两条序列
async function loadNetSeries(name, start, end, step) {
  const raw = []
  for (const m of ['network_recv_rate', 'network_sent_rate']) {
    const d = await http.get(`/api/v1/query/range?node=${encodeURIComponent(name)}&metric=${m}&start=${start}&end=${end}&step=${step}`)
    ;(d.series || []).forEach((s) => raw.push({
      name: m,
      labels: s.labels || {},
      points: (s.points || []).map((pt) => [pt.timestamp / 1e6, Number(pt.value)]),
    }))
  }
  const ifaces = new Set()
  raw.forEach((s) => { if (s.labels.iface) ifaces.add(s.labels.iface) })
  netIfaces.value = ['all', ...Array.from(ifaces).sort()]
  if (netIface.value !== 'all' && !ifaces.has(netIface.value)) netIface.value = 'all'
  const pick = (mname) => {
    let ss = raw.filter((s) => s.name === mname)
    if (netIface.value !== 'all') ss = ss.filter((s) => s.labels.iface === netIface.value)
    return sumSeriesByTs(ss)
  }
  return [
    { name: '接收', data: pick('network_recv_rate') },
    { name: '发送', data: pick('network_sent_rate') },
  ]
}

function xFormatterFor(daily) {
  if (daily) {
    return (val) => { const d = new Date(val); const z = (x) => String(x).padStart(2, '0'); return `${z(d.getMonth() + 1)}-${z(d.getDate())}` }
  }
  return (val) => { const d = new Date(val); const z = (x) => String(x).padStart(2, '0'); return `${z(d.getHours())}:${z(d.getMinutes())}` }
}

async function loadMonitor(name, onlyKey) {
  if (!name) return
  for (const p of monitorPanels) {
    if (onlyKey && p.key !== onlyKey) continue
    const chart = monitorCharts[p.key]
    if (!chart) continue
    const mode = panelRange[p.key] || 'today'
    const [start, end] = rangeBounds(mode)
    const daily = mode === 'week' || mode === 'month'
    const step = daily ? 3600000 : 1800000 // 毫秒
    try {
      let series = []
      if (p.key === 'disk') {
        series = [{ name: '磁盘', data: await loadDiskOccupancy(name, start, end, step) }]
      } else if (p.key === 'net') {
        series = await loadNetSeries(name, start, end, step)
      } else {
        const rawByMetric = {}
        for (const m of p.metrics) rawByMetric[m.name] = await fetchRaw(name, m.name, start, end, step)
        series = p.metrics.map((m) => {
          const list = rawByMetric[m.name] || []
          const data = p.sumDevices ? sumSeriesByTs(list) : (list.length ? list[0].points : [])
          return { name: m.label, data }
        })
      }
      // daily 模式直接用原始小时级数据（step=1h），不再按日聚合——
      // 部署初期只有 1-2 天数据时，dailyAvg 会坍缩成单点导致折线无法渲染。
      // 近7天≈168点、近30天≈720点，ECharts time 轴可平滑处理。
      const colors = panelColors[p.key]
      const yFormatter = p.type === 'percent' ? (v) => v + '%'
        : p.type === 'rate' ? rateShort
        : (v) => round1(v)
      const tipFmt = p.type === 'percent' ? (v) => (v == null ? '-' : round1(v) + '%')
        : p.type === 'rate' ? (v) => (v == null ? '-' : fmtRate(v))
        : (v) => (v == null ? '-' : round1(v))
      chart.setOption(monitorOption({
        yMin: 0,
        yMax: p.type === 'percent' ? 100 : undefined,
        xMin: daily ? start : undefined,
        xMax: daily ? end : undefined,
        yFormatter,
        tipFormatter: tipFmt,
        xFormatter: xFormatterFor(daily),
        series: series.map((s, i) => ({ name: s.name, color: Array.isArray(colors) ? colors[i] : colors, data: s.data })),
      }), true)
    } catch (e) { console.warn('[monitor] 加载失败', p.key, mode, e) }
  }
}

function onNetIfaceChange() {
  if (activeTab.value === 'monitor') loadMonitor(selected.value, 'net')
}

function initMonitorCharts() {
  for (const p of monitorPanels) {
    ensureMonitorChart(p.key)
  }
  Object.values(monitorCharts).forEach((c) => c.resize())
}

function onPanelRangeChange(key) {
  if (activeTab.value === 'monitor') loadMonitor(selected.value, key)
}

// 自动刷新开关：开启则重连 WS + 轮询，关闭则停止
function onAutoRefresh(val) {
  if (val) {
    if (selected.value) connectWS(selected.value)
    nodeTimer = setInterval(loadNodes, 15000)
    alertTimer = setInterval(() => { if (selected.value) loadAlerts(selected.value) }, 30000)
  } else {
    if (socket) { try { socket.close() } catch (e) {} socket = null }
    if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null }
    if (nodeTimer) { clearInterval(nodeTimer); nodeTimer = null }
    if (alertTimer) { clearInterval(alertTimer); alertTimer = null }
  }
}

watch(activeTab, (t) => {
  if (t === 'monitor') {
    nextTick(() => {
      initMonitorCharts()
      loadMonitor(selected.value)
      setTimeout(() => Object.values(monitorCharts).forEach((c) => c.resize()), 200)
    })
  } else if (t === 'overview') {
    // 切回概览页时重建实时图/环形图（v-if 会销毁旧 DOM，需重新绑定）
    nextTick(() => {
      initRealtimeCharts()
      setTimeout(() => rtChartKeys.forEach((k) => charts[k] && charts[k].resize()), 200)
    })
  }
})

function initRealtimeCharts() {
  // 先释放已有实例（切回概览页重复初始化时避免泄漏/空白）
  for (const k of rtChartKeys) {
    if (charts[k]) { charts[k].dispose(); delete charts[k] }
  }
  const defs = {
    cpu: { multi: false, color: COLORS.cyan, unit: '%' },
    mem: { multi: false, color: COLORS.purple, unit: '%' },
    diskio: { multi: true, defs: [{ name: '读取', color: COLORS.blue }, { name: '写入', color: COLORS.amber }] },
    netio: { multi: true, defs: [{ name: '接收', color: COLORS.green }, { name: '发送', color: COLORS.purple }] },
  }
  for (const k of rtChartKeys) {
    if (refs[k]) {
      charts[k] = initChart(refs[k])
      const d = defs[k]
      charts[k].setOption(d.multi ? areaMultiOption(d.defs) : areaOption(d.color, d.unit))
    }
  }
  const gaugeColors = { cpuGauge: COLORS.green, memGauge: COLORS.green, diskGauge: COLORS.green, swapGauge: COLORS.green }
  for (const g of ['cpuGauge', 'memGauge', 'diskGauge', 'swapGauge']) {
    if (charts[g]) { charts[g].dispose(); delete charts[g] }
    if (refs[g]) {
      charts[g] = initChart(refs[g])
      charts[g].setOption(gaugeOption(gaugeColors[g], ''))
    }
  }
  updateGauges()
}

onMounted(async () => {
  await loadNodes()
  if (selected.value) {
    connectWS(selected.value)
    loadProcesses(selected.value)
    loadAlerts(selected.value)
    loadPortStatuses(selected.value)
  }
  await nextTick()
  initRealtimeCharts()
  nodeTimer = setInterval(loadNodes, 15000)
  alertTimer = setInterval(() => { if (selected.value) loadAlerts(selected.value) }, 30000)
})

onUnmounted(() => {
  if (socket) { try { socket.close() } catch (e) {} }
  if (reconnectTimer) clearTimeout(reconnectTimer)
  if (nodeTimer) clearInterval(nodeTimer)
  if (alertTimer) clearInterval(alertTimer)
  Object.values(charts).forEach((c) => c.dispose && c.dispose())
  Object.values(monitorCharts).forEach((c) => c.dispose && c.dispose())
})
</script>

<style scoped>
.node-view { display: flex; flex-direction: column; gap: 16px; }
.head-panel {
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-wrap: wrap;
  gap: 12px;
  padding: 12px 16px;
}
.head-left { display: flex; align-items: center; gap: 12px; }
.head-info { display: flex; gap: 18px; font-size: 12px; color: var(--text-dim); }
.head-info span { display: flex; align-items: center; gap: 4px; }

/* Tabs */
.node-tabs { border-radius: 8px; overflow: hidden; }
.node-tabs :deep(.el-tabs__header) { background: rgba(255,255,255,0.04); margin: 0; }
.node-tabs :deep(.el-tabs__content) { padding: 16px 20px; }
.tab-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 14px; }
.port-section { margin-top: 16px; }
.port-list { display: flex; flex-wrap: wrap; gap: 8px; }
.port-item { display: inline-flex; align-items: center; gap: 6px; padding: 6px 12px; border-radius: 6px; font-size: 13px; background: rgba(255,255,255,0.04); }
.port-item.up { border-left: 3px solid #3fb950; }
.port-item.down { border-left: 3px solid #dc382d; opacity: 0.7; }
.port-dot { width: 6px; height: 6px; border-radius: 50%; }
.port-item.up .port-dot { background: #3fb950; box-shadow: 0 0 4px rgba(63,185,80,0.5); }
.port-item.down .port-dot { background: #dc382d; }
.port-num { font-family: var(--mono); font-weight: 600; }
.port-state { color: var(--text-dim); font-size: 12px; }
.port-latency { color: var(--text-muted); font-size: 11px; }

.section-title {
  margin: 18px 0 10px;
  font-size: 14px;
  font-weight: 600;
  color: var(--text-main);
  padding-left: 10px;
  border-left: 3px solid var(--el-color-primary, #409eff);
}
.section-head { display: flex; align-items: center; justify-content: space-between; }
.proc-search { width: 220px; }

/* 设备信息 */
.device-grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; }
.dev-item {
  min-width: 0;
  padding: 10px 12px;
  border-radius: 6px;
  background: rgba(255,255,255,0.035);
  border: 1px solid var(--border-soft);
}
.dev-item span { display: block; margin-bottom: 6px; font-size: 13px; font-weight: 400; color: var(--label); }
.dev-item strong {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 14px;
  font-weight: 600;
  color: var(--text-main);
  font-variant-numeric: tabular-nums;
}

/* 环形图 */
/* 面包屑 */
.breadcrumb { display: flex; align-items: center; gap: 6px; font-size: 13px; color: var(--text-dim); padding: 0 2px; }
.breadcrumb .bc-home { color: var(--el-color-primary); }
.breadcrumb .bc-sep { font-size: 12px; opacity: 0.6; }
.breadcrumb .bc-item { color: var(--text-dim); }
.breadcrumb .bc-cur { color: var(--text-main); font-weight: 600; }
.breadcrumb .bc-cur em { font-style: normal; color: var(--text-dim); font-weight: 400; }
.status-pill { display: inline-flex; align-items: center; gap: 6px; margin-left: 10px; padding: 3px 10px; border-radius: 999px; font-size: 12px; font-weight: 600; }
.status-pill .dot { width: 8px; height: 8px; border-radius: 50%; box-shadow: 0 0 6px currentColor; }
.status-pill.online { color: var(--accent); background: rgba(34,197,94,0.12); }
.status-pill.offline { color: var(--text-dim); background: rgba(255,255,255,0.06); }
.status-pill.offline .dot { background: var(--text-dim); }

/* 复制按钮 */
.copyable { display: inline-flex; align-items: center; gap: 4px; }
.copy-btn { cursor: pointer; opacity: 0.45; transition: opacity 0.15s, color 0.15s; }
.copy-btn:hover { opacity: 1; color: var(--el-color-primary); }
.with-copy { display: flex; align-items: center; gap: 6px; }
.refresh-switch { margin-left: auto; display: inline-flex; align-items: center; }

.gauge-row { display: grid; grid-template-columns: repeat(4, 1fr); gap: 16px; }
.gauge-card {
  display: flex;
  flex-direction: column;
  align-items: center;
  padding: 12px;
  border-radius: 8px;
  background: rgba(255,255,255,0.03);
  border: 1px solid var(--border-soft);
}
.gauge { width: 100%; height: 130px; }
.gauge-label { text-align: center; margin-top: 4px; font-size: 13px; color: var(--text-dim); }
.gauge-sub { text-align: center; margin-top: 2px; font-size: 12px; color: var(--text-main); opacity: 0.85; font-variant-numeric: tabular-nums; }

/* 实时趋势 */
.metric-grid { display: grid; grid-template-columns: repeat(4, 1fr); gap: 16px; }
.metric-card { padding: 14px 16px; border-radius: 8px; background: rgba(255,255,255,0.03); border: 1px solid var(--border-soft); }
.mc-head { display: flex; justify-content: space-between; align-items: baseline; margin-bottom: 8px; }
.mc-label { font-size: 14px; font-weight: 400; color: var(--label); letter-spacing: 0.2px; }
.mc-value { font-size: 26px; font-weight: 700; font-family: var(--mono); line-height: 1.1; }
.mc-value small { font-size: 12px; font-weight: 400; margin-left: 2px; }
.mc-value.green { color: var(--accent); }
.mc-value.amber { color: var(--warn); }
.mc-value.red { color: var(--danger); }
.mc-value.cyan { color: var(--info); }
.mc-stats { display: flex; gap: 16px; align-items: baseline; }
.mc-stat { display: inline-flex; align-items: baseline; gap: 5px; font-size: 13px; color: var(--text-dim); }
.mc-stat b { font-family: var(--mono); font-weight: 700; font-size: 15px; }
.mc-stat .dot { width: 8px; height: 8px; border-radius: 50%; display: inline-block; }
.mc-chart { height: 100px; }
.mc-desc { margin-top: 8px; font-size: 11px; line-height: 1.4; color: var(--text-dim); opacity: 0.8; }

/* 说明文字 */
.panel-hint { margin: 8px 0 12px; font-size: 12px; line-height: 1.5; color: var(--text-dim); opacity: 0.85; }

/* IO */

.cyan { color: var(--info); }
.amber { color: var(--warn); }

/* 底部：进程 + 用户/告警 */
.bottom-row { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; }
.user-list { display: flex; flex-wrap: wrap; gap: 8px; padding: 8px 0; }
.user-tag { font-family: var(--mono); }
.proc-bar { display: flex; align-items: center; gap: 8px; }
.proc-bar .bar { flex: 1; height: 5px; background: rgba(255,255,255,0.08); border-radius: 3px; overflow: hidden; }
.bar-fill { height: 100%; border-radius: 3px; }
.bar-fill.green { background: var(--accent); }
.bar-fill.amber { background: var(--warn); }
.bar-fill.cyan { background: var(--info); }
.proc-bar span { width: 40px; text-align: right; font-size: 11px; color: var(--text-dim); }

/* 基础监控 */
.monitor-grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 16px; }
.monitor-panel { padding: 12px 14px; border-radius: 8px; background: rgba(255,255,255,0.03); border: 1px solid var(--border-soft); }
.monitor-panel-head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; gap: 8px; }
.monitor-panel-tools { margin-bottom: 6px; }
.monitor-panel-title { font-size: 13px; color: var(--text-main); font-weight: 600; }
.monitor-panel-desc { margin-bottom: 6px; font-size: 11px; line-height: 1.4; color: var(--text-dim); opacity: 0.8; }
.monitor-chart { height: 240px; }

@media (max-width: 1100px) {
  .device-grid, .gauge-row, .metric-grid { grid-template-columns: repeat(2, 1fr); }
  .bottom-row, .monitor-grid { grid-template-columns: 1fr; }
}
@media (max-width: 640px) {
  .device-grid, .gauge-row, .metric-grid { grid-template-columns: 1fr; }
}
</style>
