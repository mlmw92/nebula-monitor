<template>
  <div class="hosts-view">
    <!-- 顶部操作栏 -->
    <div class="glass panel toolbar">
      <div class="toolbar-left">
        <el-radio-group v-model="statusFilter" size="small">
          <el-radio-button value="">全部 ({{ nodes.length }})</el-radio-button>
          <el-radio-button value="online">在线 ({{ onlineCount }})</el-radio-button>
          <el-radio-button value="offline">离线 ({{ offlineCount }})</el-radio-button>
          <el-radio-button value="warning">异常 ({{ warningCount }})</el-radio-button>
        </el-radio-group>
      </div>
      <div class="toolbar-right">
        <el-select v-model="groupFilter" placeholder="分组" clearable size="small" style="width: 120px">
          <el-option v-for="g in groups" :key="g.name" :value="g.name" :label="g.name" />
        </el-select>
        <el-button :icon="Setting" size="small" plain @click="showGroupManage = true">分组管理</el-button>
        <el-input
          v-model="keyword"
          placeholder="搜索主机名 / IP"
          size="small"
          clearable
          style="width: 200px"
        >
          <template #prefix><el-icon><Search /></el-icon></template>
        </el-input>
        <el-button type="primary" :icon="Plus" size="small" @click="openAddNode">添加主机</el-button>
      </div>
    </div>

    <!-- 刷新控制条 -->
    <div class="glass panel refresh-bar">
      <div class="refresh-left">
        <el-tooltip :content="refreshInterval === 0 ? '已暂停自动刷新' : `${countdown}s 后刷新`" placement="top">
          <el-button :icon="Refresh" size="small" circle @click="manualRefresh" />
        </el-tooltip>
        <span class="refresh-text" v-if="lastRefresh">上次刷新：{{ lastRefresh }}</span>
        <el-tag v-if="loadError" type="danger" size="small" effect="dark">{{ loadError }}</el-tag>
      </div>
      <div class="refresh-right">
        <span class="refresh-label">自动刷新</span>
        <el-select v-model="refreshInterval" size="small" style="width: 100px" @change="onIntervalChange">
          <el-option :value="0" label="关闭" />
          <el-option :value="10" label="10 秒" />
          <el-option :value="20" label="20 秒" />
          <el-option :value="30" label="30 秒" />
          <el-option :value="60" label="60 秒" />
        </el-select>
        <span class="countdown" v-if="refreshInterval > 0">{{ countdown }}s</span>
      </div>
    </div>

    <!-- 主机列表 -->
    <div class="glass panel">
      <el-table
        :data="pagedNodes"
        stripe
        style="width: 100%"
        empty-text="暂无主机"
        @row-click="(r) => goDetail(r)"
        class="host-table"
        :row-class-name="rowClass"
      >
        <el-table-column label="主机名称 / IP" min-width="220">
          <template #default="{ row }">
            <div class="host-name">
              <div class="hn-top">
                <OsIcon :os="row.os" />
                <span class="hn-name">{{ row.hostname }}</span>
              </div>
              <span class="hn-ip">{{ row.ip || '-' }}</span>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <span class="status-led" :class="row.status === 'online' ? 'on' : 'off'"></span>
            <span :class="['status-text', row.status === 'online' ? 'on' : 'off']">
              {{ row.status === 'online' ? '在线' : '离线' }}
            </span>
          </template>
        </el-table-column>

        <el-table-column label="分组" min-width="120">
          <template #default="{ row }">
            <el-dropdown trigger="click" @command="(cmd) => changeGroup(row, cmd)" @click.stop>
              <span class="group-tag clickable" @click.stop>
                {{ row.group || 'default' }}
                <el-icon class="group-arrow"><ArrowDown /></el-icon>
              </span>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item
                    v-for="g in groups"
                    :key="g.name"
                    :command="g.name"
                    :disabled="g.name === (row.group || 'default')"
                  >
                    {{ g.name }}
                  </el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
          </template>
        </el-table-column>

        <el-table-column label="Agent 版本" min-width="110">
          <template #default="{ row }">
            <span class="ver-cell" :class="verClass(row.version)">{{ row.version || '-' }}</span>
          </template>
        </el-table-column>

        <el-table-column label="磁盘使用" min-width="120">
          <template #default="{ row }">
            <div class="usage-cell" v-if="hasMetric(row)">
              <div class="mini-bar">
                <div class="mini-bar-fill" :class="rateClass(m(row).disk)" :style="{ width: pct(m(row).disk) + '%' }"></div>
              </div>
              <span :class="['rate-sm', rateClass(m(row).disk)]">{{ fmtNum(m(row).disk) }}%</span>
            </div>
            <span v-else class="dim">--</span>
          </template>
        </el-table-column>

        <el-table-column label="CPU" min-width="110">
          <template #default="{ row }">
            <div class="usage-cell" v-if="hasMetric(row)">
              <div class="mini-bar">
                <div class="mini-bar-fill" :class="rateClass(m(row).cpu)" :style="{ width: pct(m(row).cpu) + '%' }"></div>
              </div>
              <span :class="['rate-sm', rateClass(m(row).cpu)]">{{ fmtNum(m(row).cpu) }}%</span>
            </div>
            <span v-else class="dim">--</span>
          </template>
        </el-table-column>

        <el-table-column label="内存" min-width="110">
          <template #default="{ row }">
            <div class="usage-cell" v-if="hasMetric(row)">
              <div class="mini-bar">
                <div class="mini-bar-fill" :class="rateClass(m(row).mem)" :style="{ width: pct(m(row).mem) + '%' }"></div>
              </div>
              <span :class="['rate-sm', rateClass(m(row).mem)]">{{ fmtNum(m(row).mem) }}%</span>
            </div>
            <span v-else class="dim">--</span>
          </template>
        </el-table-column>

        <el-table-column label="流量↓" min-width="100">
          <template #default="{ row }">
            <span class="rate mono">{{ fmtRate(m(row).netIn) }}/s</span>
          </template>
        </el-table-column>

        <el-table-column label="负载" min-width="80">
          <template #default="{ row }">
            <span :class="['rate-sm', loadClass(row)]">{{ fmtNum(m(row).load1, 2) }}</span>
          </template>
        </el-table-column>

        <el-table-column label="磁盘读写" min-width="130">
          <template #default="{ row }">
            <span class="rate mono sm">
              R{{ fmtRate(m(row).diskRead) }}
              <small class="sep">/</small>
              W{{ fmtRate(m(row).diskWr) }}
            </span>
          </template>
        </el-table-column>

        <el-table-column label="操作" width="180" fixed="right">
          <template #default="{ row }">
            <el-button link size="small" @click.stop="goDetail(row)">详情</el-button>
            <el-button link size="small" type="warning" @click.stop="upgrade(row)">升级</el-button>
            <el-button link size="small" type="danger" @click.stop="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <div class="pagination-wrap">
        <el-pagination
          v-model:current-page="currentPage"
          :page-size="pageSize"
          :page-sizes="[20, 50, 100]"
          layout="total, sizes, prev, pager, next, jumper"
          :total="filteredNodes.length"
          background
          @size-change="(s) => (pageSize = s)"
        />
      </div>
    </div>

    <!-- 添加主机弹窗 -->
    <el-dialog v-model="showAddModal" title="添加主机" width="680px">
      <p style="color: var(--text-dim); margin-bottom: 14px; font-size: 13px">
        在目标机器上执行以下命令，Agent 会自动注册并上报：
      </p>
      <el-input
        v-model="installCommand"
        type="textarea"
        :rows="4"
        readonly
        resize="none"
        class="cmd-box"
      />
      <template #footer>
        <el-button @click="showAddModal = false">关闭</el-button>
        <el-button type="primary" :icon="CopyDocument" @click="copyCmd">一键复制</el-button>
      </template>
    </el-dialog>

    <!-- 分组管理弹窗 -->
    <GroupManage
      v-if="showGroupManage"
      :groups="groups"
      :nodes="nodes"
      @close="showGroupManage = false"
      @changed="load"
    />
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { Plus, Search, CopyDocument, Setting, ArrowDown, Refresh } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import http from '../api/http'
import OsIcon from './OsIcon.vue'
import GroupManage from './GroupManage.vue'

const router = useRouter()
const nodes = ref([])
const metrics = ref({})
const groups = ref([])
const statusFilter = ref('')
const groupFilter = ref('')
const keyword = ref('')
const currentPage = ref(1)
const pageSize = ref(20)

// 刷新控制
const refreshInterval = ref(20) // 0=关闭, >0=秒
const countdown = ref(20)
const lastRefresh = ref('')
const loadError = ref('')

const showAddModal = ref(false)
const showGroupManage = ref(false)
const installCommand = ref('')

let loadTimer = null
let countdownTimer = null
let visible = true

const onlineCount = computed(() => nodes.value.filter((n) => n.status === 'online').length)
const offlineCount = computed(() => nodes.value.filter((n) => n.status !== 'online').length)
const warningCount = computed(() => nodes.value.filter((n) => nodeSeverity(n) >= 50 && n.status === 'online').length)

// 过滤 + 排序（异常置顶）
const filteredNodes = computed(() => {
  let arr = nodes.value
  if (statusFilter.value === 'online') arr = arr.filter((n) => n.status === 'online')
  else if (statusFilter.value === 'offline') arr = arr.filter((n) => n.status !== 'online')
  else if (statusFilter.value === 'warning') arr = arr.filter((n) => nodeSeverity(n) >= 50)
  if (groupFilter.value) arr = arr.filter((n) => (n.group || 'default') === groupFilter.value)
  if (keyword.value) {
    const k = keyword.value.toLowerCase()
    arr = arr.filter((n) => n.hostname.toLowerCase().includes(k) || (n.ip || '').toLowerCase().includes(k))
  }
  return arr.sort((a, b) => nodeSeverity(b) - nodeSeverity(a))
})

const pagedNodes = computed(() => {
  const start = (currentPage.value - 1) * pageSize.value
  return filteredNodes.value.slice(start, start + pageSize.value)
})

// 过滤条件变化时回到第一页
watch([statusFilter, groupFilter, keyword], () => {
  currentPage.value = 1
})

function nodeSeverity(n) {
  if (n.status !== 'online') return 100
  const mm = metrics.value[n.hostname]
  if (!mm) return 0
  let s = 0
  for (const k of ['cpu', 'mem', 'disk']) {
    const v = mm[k]
    if (typeof v === 'number') {
      if (v >= 90) s = Math.max(s, 80)
      else if (v >= 70) s = Math.max(s, 50)
    }
  }
  return s
}

function rowClass({ row }) {
  if (row.status !== 'online') return 'row-offline'
  if (nodeSeverity(row) >= 80) return 'row-critical'
  if (nodeSeverity(row) >= 50) return 'row-warning'
  return ''
}

function verClass(v) {
  if (!v || v === 'dev') return 'dev'
  return 'release'
}

function m(row) {
  return metrics.value[row.hostname] || {}
}
function hasMetric(row) {
  const v = metrics.value[row.hostname]
  return v && typeof v.disk === 'number'
}
function pct(v) {
  if (typeof v !== 'number') return 0
  return Math.min(100, Math.max(0, v))
}
function rateClass(v) {
  if (typeof v !== 'number') return ''
  if (v >= 90) return 'red'
  if (v >= 70) return 'amber'
  return 'green'
}
function loadClass(row) {
  const v = m(row).load1
  if (typeof v !== 'number') return ''
  if (v >= 8) return 'red'
  if (v >= 4) return 'amber'
  return 'green'
}
function fmtNum(v, d = 1) {
  if (typeof v !== 'number') return '--'
  return v.toFixed(d)
}
function fmtRate(v) {
  if (typeof v !== 'number') return '--'
  if (v >= 1048576) return (v / 1048576).toFixed(2) + 'M'
  if (v >= 1024) return (v / 1024).toFixed(1) + 'K'
  return v.toFixed(0) + 'B'
}

async function load() {
  if (!visible) return
  loadError.value = ''
  try {
    const [nd, md, gd] = await Promise.all([
      http.get('/api/v1/nodes'),
      http.get('/api/v1/nodes/latest'),
      http.get('/api/v1/groups'),
    ])
    nodes.value = nd.nodes || []
    metrics.value = md.metrics || {}
    groups.value = gd.groups || []
    lastRefresh.value = new Date().toLocaleTimeString('zh-CN', { hour12: false })
  } catch (e) {
    loadError.value = '数据加载失败：' + (e.message || '未知错误')
    console.error('[HostsView] load error:', e)
  }
}

function manualRefresh() {
  load()
  countdown.value = refreshInterval.value
}

function onIntervalChange() {
  countdown.value = refreshInterval.value
  restartTimers()
}

function restartTimers() {
  if (loadTimer) { clearInterval(loadTimer); loadTimer = null }
  if (countdownTimer) { clearInterval(countdownTimer); countdownTimer = null }
  if (refreshInterval.value > 0) {
    countdown.value = refreshInterval.value
    loadTimer = setInterval(() => {
      if (visible) load()
      countdown.value = refreshInterval.value
    }, refreshInterval.value * 1000)
    countdownTimer = setInterval(() => {
      if (countdown.value > 0) countdown.value--
    }, 1000)
  }
}

function openAddNode() {
  showAddModal.value = true
  http
    .get('/api/v1/install-info')
    .then((info) => (installCommand.value = info.command || '获取安装命令失败'))
    .catch(() => (installCommand.value = '获取安装命令失败，请检查 Server 是否正常'))
}

async function changeGroup(row, group) {
  if (group === (row.group || 'default')) return
  try {
    await http.put('/api/v1/nodes/' + row.hostname + '/group', { group })
    ElMessage.success(`${row.hostname} 已移入分组「${group}」`)
    load()
  } catch (e) {
    ElMessage.error('修改分组失败：' + (e.message || ''))
  }
}

async function copyCmd() {
  if (!installCommand.value) return
  try {
    await navigator.clipboard.writeText(installCommand.value)
    ElMessage.success('已复制到剪贴板')
  } catch (e) {
    const ta = document.createElement('textarea')
    ta.value = installCommand.value
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    document.body.removeChild(ta)
    ElMessage.success('已复制到剪贴板')
  }
}

async function remove(row) {
  try {
    await ElMessageBox.confirm('确认删除主机 ' + row.hostname + '？', '提示', {
      type: 'warning',
      confirmButtonText: '删除',
      cancelButtonText: '取消',
    })
    await http.del('/api/v1/nodes/' + row.hostname)
    ElMessage.success('已删除')
    load()
  } catch (e) {
    /* 取消 */
  }
}

async function upgrade(row) {
  try {
    await ElMessageBox.confirm(
      '确认升级主机 ' + row.hostname + ' 的 Agent？\n升级期间 Agent 会短暂离线后自动恢复。',
      'Agent 升级',
      { type: 'warning', confirmButtonText: '升级', cancelButtonText: '取消' }
    )
    await http.post('/api/v1/nodes/' + row.hostname + '/upgrade', {})
    ElMessage.success('升级任务已下发，Agent 将在下次心跳时执行（约 15-30s 内生效）')
  } catch (e) {
    if (e && e.message && !e.message.includes('cancel')) {
      ElMessage.error('升级失败：' + e.message)
    }
  }
}

function goDetail(row) {
  router.push('/node/' + row.hostname)
}

function onVis() {
  visible = document.visibilityState === 'visible'
  if (visible) {
    load()
    restartTimers()
  } else {
    if (loadTimer) { clearInterval(loadTimer); loadTimer = null }
    if (countdownTimer) { clearInterval(countdownTimer); countdownTimer = null }
  }
}

onMounted(() => {
  load()
  restartTimers()
  document.addEventListener('visibilitychange', onVis)
})
onUnmounted(() => {
  if (loadTimer) clearInterval(loadTimer)
  if (countdownTimer) clearInterval(countdownTimer)
  document.removeEventListener('visibilitychange', onVis)
})

defineExpose({ reload: load })
</script>

<style scoped>
.hosts-view {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 16px;
  flex-wrap: wrap;
  gap: 10px;
}
.toolbar-left,
.toolbar-right {
  display: flex;
  gap: 10px;
  align-items: center;
  flex-wrap: wrap;
}

/* 刷新控制条 */
.refresh-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 16px;
}
.refresh-left,
.refresh-right {
  display: flex;
  align-items: center;
  gap: 10px;
}
.refresh-text {
  font-size: 12px;
  color: var(--text-dim);
  font-family: var(--mono);
}
.refresh-label {
  font-size: 12px;
  color: var(--text-dim);
}
.countdown {
  font-size: 12px;
  font-family: var(--mono);
  color: var(--accent);
  min-width: 28px;
  text-align: right;
}

/* 主机名/IP */
.host-name {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.hn-top {
  display: flex;
  align-items: center;
  gap: 6px;
}
.hn-name {
  color: var(--text);
  font-weight: 600;
  font-size: 13px;
}
.hn-ip {
  color: var(--text-dim);
  font-size: 11px;
  font-family: var(--mono);
}
.ver-cell {
  display: inline-block;
  font-size: 11px;
  font-family: var(--mono);
  padding: 1px 7px;
  border-radius: 4px;
  line-height: 18px;
}
.ver-cell.dev {
  color: var(--text-muted);
  background: rgba(255, 255, 255, 0.04);
}
.ver-cell.release {
  color: var(--accent);
  background: var(--accent-dim);
}

/* 状态指示灯 */
.status-led {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  margin-right: 6px;
  vertical-align: middle;
}
.status-led.on {
  background: var(--accent);
  box-shadow: 0 0 6px var(--accent-glow);
}
.status-led.off {
  background: var(--danger);
}
.status-text {
  font-size: 12px;
  vertical-align: middle;
}
.status-text.on { color: var(--accent); }
.status-text.off { color: var(--danger); }

/* 分组标签 */
.group-tag {
  font-size: 12px;
  color: var(--text-dim);
}
.group-tag.clickable {
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  gap: 2px;
  padding: 2px 8px;
  border-radius: 4px;
  transition: background 0.15s;
}
.group-tag.clickable:hover {
  background: rgba(255, 255, 255, 0.06);
  color: var(--text);
}
.group-arrow {
  font-size: 10px;
  opacity: 0.6;
}

/* 迷你进度条 */
.usage-cell {
  display: flex;
  align-items: center;
  gap: 6px;
}
.mini-bar {
  flex: 1;
  height: 5px;
  background: rgba(255, 255, 255, 0.06);
  border-radius: 3px;
  overflow: hidden;
  min-width: 40px;
}
.mini-bar-fill {
  height: 100%;
  border-radius: 3px;
  transition: width 0.3s;
}
.mini-bar-fill.green { background: var(--accent); }
.mini-bar-fill.amber { background: var(--warn); }
.mini-bar-fill.red { background: var(--danger); }

/* 数值 */
.rate {
  font-family: var(--mono);
  font-size: 13px;
  color: var(--text);
}
.rate.sm { font-size: 11px; }
.rate.mono { font-family: var(--mono); }
.rate-sm {
  font-family: var(--mono);
  font-size: 11px;
  color: var(--text-dim);
  white-space: nowrap;
}
.rate-sm.green { color: var(--accent); }
.rate-sm.amber { color: var(--warn); }
.rate-sm.red { color: var(--danger); }
.rate small {
  font-size: 10px;
  margin-left: 1px;
  color: var(--text-dim);
}
.rate .sep {
  margin: 0 3px;
  color: var(--text-muted);
}
.dim {
  color: var(--text-muted);
  font-size: 12px;
}

/* 行状态高亮 */
.host-table :deep(.el-table__row) {
  cursor: pointer;
}
.host-table :deep(.row-offline) {
  background: var(--danger-dim) !important;
}
.host-table :deep(.row-critical) {
  background: rgba(244, 63, 94, 0.08) !important;
}
.host-table :deep(.row-warning) {
  background: var(--warn-dim) !important;
}
.pagination-wrap {
  margin-top: 14px;
  display: flex;
  justify-content: flex-end;
}
.cmd-box :deep(.el-textarea__inner) {
  font-family: var(--mono);
  font-size: 12px;
  color: var(--accent);
  background: rgba(0, 0, 0, 0.35) !important;
}
</style>
