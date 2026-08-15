<template>
  <div class="security-view">
    <RefreshBar :loading="loading" @refresh="refreshAll" />

    <!-- 顶部 KPI 概览 -->
    <div class="glass panel kpi-row" v-loading="loading">
      <div class="kpi-card" :class="scoreClass">
        <div class="kpi-ring" :style="ringStyle">
          <div class="kpi-ring-inner">
            <div class="kpi-score">{{ summary.score.toFixed(0) }}</div>
            <div class="kpi-ring-label">合规评分</div>
          </div>
        </div>
        <div class="kpi-meta">
          <div class="kpi-title">安全合规评分</div>
          <div class="kpi-sub">{{ summary.baselineHosts }} 台主机已评估</div>
        </div>
      </div>

      <div class="kpi-card">
        <div class="kpi-icon" style="--c: #FF5D6C">⚠</div>
        <div class="kpi-meta">
          <div class="kpi-value red">{{ summary.eventCount }}</div>
          <div class="kpi-title">安全事件</div>
          <div class="kpi-sub">累计监测记录</div>
        </div>
      </div>

      <div class="kpi-card">
        <div class="kpi-icon" style="--c: #FFB454">🛡</div>
        <div class="kpi-meta">
          <div class="kpi-value amber">{{ summary.riskNodes }}</div>
          <div class="kpi-title">风险主机</div>
          <div class="kpi-sub">含关键/警告事件</div>
        </div>
      </div>

      <div class="kpi-card">
        <div class="kpi-icon" style="--c: #4A9DF0">📄</div>
        <div class="kpi-meta">
          <div class="kpi-value cyan">{{ summary.fimChanges }}</div>
          <div class="kpi-title">文件完整性变化</div>
          <div class="kpi-sub">新增 / 修改 / 删除</div>
        </div>
      </div>
    </div>

    <!-- 入侵防御（受控 fail2ban 专属 SSH 防护） -->
    <div class="glass panel section" v-loading="defenseLoading">
      <div class="panel-title-row">
        <span class="panel-title">入侵防御</span>
        <span class="defense-tip">仅托管 nebula-monitor-sshd 专属 jail，不覆盖既有 fail2ban 配置</span>
      </div>

      <div class="defense-kpi" v-if="defenseSummary">
        <div class="dk-card"><span class="dk-val ok">{{ defenseSummary.protected }}</span><span class="dk-label">已防护</span></div>
        <div class="dk-card"><span class="dk-val">{{ defenseSummary.unmanaged }}</span><span class="dk-label">未启用</span></div>
        <div class="dk-card"><span class="dk-val warn">{{ defenseSummary.running }}</span><span class="dk-label">执行中</span></div>
        <div class="dk-card"><span class="dk-val bad">{{ defenseSummary.exception }}</span><span class="dk-label">异常</span></div>
      </div>

      <el-table :data="defenseStatuses" style="width: 100%; margin-top: 12px" empty-text="暂无节点" max-height="420">
        <el-table-column label="节点" min-width="180">
          <template #default="{ row }">
            <span class="node-cell">{{ row.node }}</span>
            <span v-if="!row.agentSupported" class="need-upgrade">需升级 Agent</span>
          </template>
        </el-table-column>
        <el-table-column label="防护状态" width="140">
          <template #default="{ row }">
            <span class="def-badge" :class="defenseBadgeClass(row)">{{ defenseStateText(row) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="jail" min-width="160">
          <template #default="{ row }">
            <span v-if="row.managedJail" class="jail-tag">nebula-monitor-sshd</span>
            <span v-else-if="row.jails && row.jails.length" class="dim">{{ row.jails.join(', ') }}</span>
            <span v-else class="muted">-</span>
          </template>
        </el-table-column>
        <el-table-column label="说明" min-width="200">
          <template #default="{ row }">
            <span class="dim">{{ row.message || (row.agentSupported ? '' : 'Agent 版本过低，不支持防护指令') }}</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="160" align="right">
          <template #default="{ row }">
            <el-button
              v-if="row.agentSupported && !row.managedJail"
              size="small" type="primary" plain
              :loading="actionId === row.node + '-enable'"
              @click="confirmDefense(row, 'enable')">启用防护</el-button>
            <el-button
              v-else-if="row.agentSupported && row.managedJail"
              size="small" type="warning" plain
              :loading="actionId === row.node + '-disable'"
              @click="confirmDefense(row, 'disable')">停用防护</el-button>
            <el-button v-else size="small" disabled>需升级 Agent</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 防护操作确认 -->
    <el-dialog v-model="defDialogVisible" title="入侵防御操作确认" width="460px">
      <div v-if="defTarget">
        <p>即将对节点 <b>{{ defTarget.node }}</b> 执行
          <b>{{ defTarget.action === 'enable' ? '启用' : '停用' }}</b> 防护。</p>
        <ul class="def-confirm">
          <li>仅托管 <code>nebula-monitor-sshd</code> 专属 jail，不会修改你的 jail.local 或既有 jail。</li>
          <li>启用后会自动将你的操作来源 IP 加入 fail2ban 白名单，避免误封自己。</li>
          <li v-if="defTarget.action === 'disable'">停用只移除 nebula 专属配置并重载，<b>不卸载 fail2ban 软件包</b>。</li>
          <li>任务将在节点下次上报后由 Agent 异步执行，可在状态栏观察结果。</li>
        </ul>
      </div>
      <template #footer>
        <el-button @click="defDialogVisible = false">取消</el-button>
        <el-button type="primary" @click="doDefenseAction">确认{{ defTarget && defTarget.action === 'enable' ? '启用' : '停用' }}</el-button>
      </template>
    </el-dialog>

    <!-- 安全事件区 -->
    <div class="glass panel section" v-loading="loading">
      <div class="panel-title-row">
        <span class="panel-title">安全事件</span>
        <div class="filters">
          <el-select v-model="filterCategory" placeholder="全部类别" clearable size="small" style="width: 160px" @change="loadEvents">
            <el-option v-for="c in categoryOptions" :key="c.value" :label="c.label" :value="c.value" />
          </el-select>
          <el-input v-model="filterNode" placeholder="按节点筛选" clearable size="small" style="width: 150px" @change="loadEvents" @clear="loadEvents" />
          <el-button size="small" @click="refreshAll">刷新</el-button>
        </div>
      </div>

      <el-table :data="pagedEvents" style="width: 100%" empty-text="暂无安全事件" :row-class-name="rowClass" max-height="460">
        <el-table-column label="级别" width="90">
          <template #default="{ row }">
            <span class="sev-tag" :class="'sev-' + row.severity">{{ sevLabel(row.severity) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="类别" width="130">
          <template #default="{ row }">
            <span class="cat-tag">{{ catLabel(row.category) }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="nodeIp" label="节点" width="150">
          <template #default="{ row }">
            <span :title="row.node">{{ row.nodeIp || row.node }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="message" label="描述" min-width="280" show-overflow-tooltip />
        <el-table-column label="来源 / 账户" width="190">
          <template #default="{ row }">
            <div v-if="row.sourceIp" class="src-block">
              <span class="dim">IP {{ row.sourceIp }}</span>
              <span v-if="row.sourceLocation" class="geo-tag" :title="row.sourceLocation">{{ row.sourceLocation }}</span>
            </div>
            <span v-if="row.user" class="dim">账户 {{ row.user }}</span>
            <span v-if="!row.sourceIp && !row.user" class="muted">-</span>
          </template>
        </el-table-column>
        <el-table-column label="时间" width="170">
          <template #default="{ row }">{{ fmtTime(row.timestamp) }}</template>
        </el-table-column>
      </el-table>
      <div style="margin-top: 12px; display: flex; justify-content: flex-end">
        <el-pagination
          v-model:current-page="evCurrentPage"
          v-model:page-size="evPageSize"
          :total="events.length"
          :page-sizes="[10, 20, 50, 100]"
          layout="total, sizes, prev, pager, next, jumper"
          background
        />
      </div>
    </div>

    <!-- 基线与 FIM 区 -->
    <div class="grid-2">
      <!-- 基线合规明细 -->
      <div class="glass panel section" v-loading="loading">
        <div class="panel-title-row">
          <span class="panel-title">基线合规明细</span>
        </div>
        <el-empty v-if="!baselines.length" description="暂无基线数据" :image-size="60" />
        <div v-for="b in baselines" :key="b.node" class="baseline-node">
          <div class="baseline-head">
            <span class="node-name">{{ b.displayName || b.node }}<span v-if="b.nodeIp" class="node-ip"> · {{ b.nodeIp }}</span></span>
            <span class="score-pill" :class="scorePillClass(b.score)">{{ b.score.toFixed(0) }}</span>
          </div>
          <div class="baseline-items">
            <div v-for="it in b.items" :key="it.key" class="bl-item" :class="it.pass ? 'pass' : 'fail'">
              <span class="bl-dot"></span>
              <span class="bl-name">{{ it.name }}</span>
              <span class="bl-state">{{ it.pass ? '通过' : '未通过' }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- FIM 变化时间线 -->
      <div class="glass panel section" v-loading="loading">
        <div class="panel-title-row">
          <span class="panel-title">文件完整性变化</span>
        </div>
        <el-empty v-if="!fimEvents.length" description="暂无文件变化记录" :image-size="60" />
        <div class="timeline">
          <div v-for="e in fimEvents" :key="e.id" class="tl-item">
            <span class="tl-dot" :class="'tl-' + e.detail.action"></span>
            <div class="tl-body">
              <div class="tl-msg">{{ e.message }}</div>
              <div class="tl-time">{{ fmtTime(e.timestamp) }} · {{ e.node }}</div>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, watch, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { getSecuritySummary, getSecurityEvents, getSecurityBaselines, getDefenseStatuses, postDefenseAction } from '../api/security'
import RefreshBar from './RefreshBar.vue'

const loading = ref(false)
const summary = ref({ score: 0, eventCount: 0, riskNodes: 0, fimChanges: 0, baselineHosts: 0 })
const events = ref([])
const baselines = ref([])
// 安全事件前端分页
const evCurrentPage = ref(1)
const evPageSize = ref(10)
const pagedEvents = computed(() => {
  const start = (evCurrentPage.value - 1) * evPageSize.value
  return events.value.slice(start, start + evPageSize.value)
})
watch(events, () => {
  const max = Math.max(1, Math.ceil(events.value.length / evPageSize.value))
  if (evCurrentPage.value > max) evCurrentPage.value = max
})
const filterCategory = ref('')
const filterNode = ref('')

// 受控入侵防御状态
const defenseLoading = ref(false)
const defenseStatuses = ref([])
const defDialogVisible = ref(false)
const defTarget = ref(null) // { node, action }
const actionId = ref('')

const defenseSummary = computed(() => {
  const s = { protected: 0, unmanaged: 0, running: 0, exception: 0 }
  for (const d of defenseStatuses.value) {
    if (!d.agentSupported) s.exception++
    else if (d.managedJail && d.running) s.protected++
    else if (d.managedJail && !d.running) s.unmanaged++
    else if (!d.supported) s.exception++
    else if (!d.managedJail) s.unmanaged++
  }
  return s
})

function defenseStateText(row) {
  if (!row.agentSupported) return '需升级 Agent'
  if (row.managedJail && row.running) return '已防护'
  if (row.managedJail && !row.running) return '已停用'
  if (row.agentSupported && !row.managedJail) return '未启用'
  if (!row.supported) return '不支持'
  return '未知'
}
function defenseBadgeClass(row) {
  if (!row.agentSupported) return 'bad'
  if (row.managedJail && row.running) return 'good'
  if (row.managedJail) return 'warn'
  return 'neutral'
}

async function loadDefense() {
  try {
    const d = await getDefenseStatuses()
    defenseStatuses.value = d.statuses || []
  } catch (e) {
    defenseStatuses.value = []
  }
}

function confirmDefense(row, action) {
  defTarget.value = { node: row.node, action }
  defDialogVisible.value = true
}
async function doDefenseAction() {
  const t = defTarget.value
  if (!t) return
  actionId.value = t.node + '-' + t.action
  try {
    await postDefenseAction(t.node, t.action)
    ElMessage.success(t.action === 'enable' ? '已提交启用任务，节点将在下次上报后执行' : '已提交停用任务')
    defDialogVisible.value = false
    setTimeout(loadDefense, 1500)
  } catch (e) {
    const need = e && e.response && e.response.data && e.response.data.needAgentUpgrade
    ElMessage.error((e && e.response && e.response.data && e.response.data.error) || '操作失败')
    if (need) setTimeout(loadDefense, 800)
  } finally {
    actionId.value = ''
  }
}

const categoryOptions = [
  { value: 'ssh_bruteforce', label: 'SSH 暴力破解' },
  { value: 'ssh_audit', label: 'SSH 登录审计' },
  { value: 'fim', label: '文件完整性' },
  { value: 'process_anomaly', label: '异常进程' },
  { value: 'sudo_audit', label: 'sudo 提权审计' },
]

const fimEvents = computed(() => events.value.filter((e) => e.category === 'fim').slice(0, 40))

const scoreClass = computed(() => {
  const s = summary.value.score
  if (s >= 80) return 'good'
  if (s >= 60) return 'warn'
  return 'bad'
})

const ringStyle = computed(() => {
  const pct = Math.max(0, Math.min(100, summary.value.score))
  const color = pct >= 80 ? '#00D9A3' : pct >= 60 ? '#FFB454' : '#FF5D6C'
  return {
    background: `conic-gradient(${color} ${pct * 3.6}deg, rgba(255,255,255,0.06) 0deg)`,
  }
})

function scorePillClass(score) {
  if (score >= 80) return 'good'
  if (score >= 60) return 'warn'
  return 'bad'
}

function sevLabel(s) {
  return { critical: '紧急', warning: '警告', info: '信息' }[s] || s
}
function catLabel(c) {
  const m = {
    ssh_bruteforce: 'SSH 暴力破解',
    ssh_audit: 'SSH 审计',
    fim: '文件完整性',
    process_anomaly: '异常进程',
    sudo_audit: 'sudo 审计',
  }
  return m[c] || c
}
function rowClass({ row }) {
  return 'row-' + row.severity
}
function fmtTime(ts) {
  if (!ts) return '-'
  const d = new Date(ts)
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

async function loadSummary() {
  try {
    const d = await getSecuritySummary()
    summary.value = {
      score: d.score || 0,
      eventCount: d.eventCount || 0,
      riskNodes: d.riskNodes || 0,
      fimChanges: d.fimChanges || 0,
      baselineHosts: d.baselineHosts || 0,
    }
  } catch (e) {
    /* 忽略：安全能力可能未启用 */
  }
}
async function loadEvents() {
  try {
    const d = await getSecurityEvents({ limit: 2000, category: filterCategory.value, node: filterNode.value })
    events.value = d.events || []
    evCurrentPage.value = 1
  } catch (e) {
    events.value = []
  }
}
async function loadBaselines() {
  try {
    const d = await getSecurityBaselines()
    baselines.value = d.baselines || []
  } catch (e) {
    baselines.value = []
  }
}
async function refreshAll() {
  loading.value = true
  await Promise.all([loadSummary(), loadEvents(), loadBaselines(), loadDefense()])
  loading.value = false
}

onMounted(() => {
  refreshAll()
})
</script>

<style scoped>
.security-view {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.kpi-row {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 16px;
  padding: 18px;
}
.kpi-card {
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 6px 4px;
}
.kpi-ring {
  width: 72px;
  height: 72px;
  border-radius: 50%;
  display: grid;
  place-items: center;
  flex-shrink: 0;
}
.kpi-ring-inner {
  width: 56px;
  height: 56px;
  border-radius: 50%;
  background: var(--bg-deep, #070D1A);
  display: grid;
  place-items: center;
  text-align: center;
}
.kpi-score {
  font-size: 22px;
  font-weight: 700;
  color: var(--text);
  line-height: 1;
}
.kpi-ring-label {
  font-size: 10px;
  color: var(--text-muted);
}
.kpi-icon {
  width: 44px;
  height: 44px;
  border-radius: 12px;
  display: grid;
  place-items: center;
  font-size: 20px;
  background: color-mix(in srgb, var(--c) 18%, transparent);
  box-shadow: 0 0 14px color-mix(in srgb, var(--c) 45%, transparent);
  flex-shrink: 0;
}
.kpi-meta {
  min-width: 0;
}
.kpi-value {
  font-size: 26px;
  font-weight: 700;
  line-height: 1.1;
}
.kpi-value.red { color: #FF5D6C; }
.kpi-value.amber { color: #FFB454; }
.kpi-value.cyan { color: #4A9DF0; }
.kpi-title {
  font-size: 13px;
  color: var(--text);
  margin-top: 2px;
}
.kpi-sub {
  font-size: 11px;
  color: var(--text-muted);
}
.kpi-card.good .kpi-score { color: #00D9A3; }
.kpi-card.warn .kpi-score { color: #FFB454; }
.kpi-card.bad .kpi-score { color: #FF5D6C; }

.panel-title-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}
.panel-title {
  font-size: 15px;
  font-weight: 600;
  color: var(--text);
  position: relative;
  padding-left: 12px;
}
.panel-title::before {
  content: '';
  position: absolute;
  left: 0;
  top: 50%;
  transform: translateY(-50%);
  width: 3px;
  height: 14px;
  background: linear-gradient(180deg, #4A9DF0, #2E7FD6);
  border-radius: 2px;
}
.filters {
  display: flex;
  gap: 8px;
  align-items: center;
}

.grid-2 {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
}
.section {
  padding: 16px;
}

.sev-tag {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 6px;
  font-size: 11px;
  font-weight: 600;
}
.sev-tag.sev-critical { background: rgba(255, 93, 108, 0.18); color: #FF5D6C; }
.sev-tag.sev-warning { background: rgba(255, 180, 84, 0.18); color: #FFB454; }
.sev-tag.sev-info { background: rgba(74, 157, 240, 0.18); color: #4A9DF0; }

.cat-tag {
  font-size: 12px;
  color: var(--text-dim);
}
.row-critical :deep(.el-table__row) { box-shadow: inset 3px 0 0 #FF5D6C; }
.row-warning :deep(tr) { border-left: 3px solid #FFB454; }

.dim { color: var(--text-dim); font-size: 12px; }
.muted { color: var(--text-muted); font-size: 12px; }

/* 来源 IP 属地标签 */
.src-block { display: flex; flex-direction: column; gap: 2px; }
.geo-tag {
  display: inline-block;
  align-self: flex-start;
  max-width: 100%;
  padding: 0 6px;
  border-radius: 4px;
  background: rgba(74, 157, 240, 0.15);
  color: #6DB3F2;
  font-size: 11px;
  line-height: 16px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* 基线 */
.baseline-node {
  border: 1px solid var(--border, rgba(255,255,255,0.08));
  border-radius: 10px;
  padding: 12px;
  margin-bottom: 12px;
  background: rgba(255,255,255,0.02);
}
.baseline-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 10px;
}
.node-name {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
}
.node-ip {
  font-size: 12px;
  font-weight: 400;
  color: var(--text-dim);
}
.score-pill {
  font-size: 13px;
  font-weight: 700;
  padding: 2px 10px;
  border-radius: 20px;
}
.score-pill.good { background: rgba(0, 217, 163, 0.16); color: #00D9A3; }
.score-pill.warn { background: rgba(255, 180, 84, 0.16); color: #FFB454; }
.score-pill.bad { background: rgba(255, 93, 108, 0.16); color: #FF5D6C; }

.baseline-items {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.bl-item {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  padding: 4px 6px;
  border-radius: 6px;
  transition: background 0.15s;
}
.bl-item:hover { background: rgba(255,255,255,0.04); }
.bl-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  flex-shrink: 0;
}
.bl-item.pass .bl-dot { background: #00D9A3; box-shadow: 0 0 8px rgba(0,217,163,0.6); }
.bl-item.fail .bl-dot { background: #FF5D6C; box-shadow: 0 0 8px rgba(255,93,108,0.6); }
.bl-name { color: var(--text-dim); flex: 1; }
.bl-item.fail .bl-name { color: #FFB454; }
.bl-state { font-size: 11px; color: var(--text-muted); }
.bl-item.fail .bl-state { color: #FF5D6C; }

/* FIM 时间线 */
.timeline {
  display: flex;
  flex-direction: column;
  gap: 0;
  max-height: 420px;
  overflow-y: auto;
}
.tl-item {
  display: flex;
  gap: 12px;
  padding: 8px 0;
  border-left: 2px solid var(--border, rgba(255,255,255,0.08));
  padding-left: 14px;
  position: relative;
}
.tl-dot {
  position: absolute;
  left: -7px;
  top: 12px;
  width: 12px;
  height: 12px;
  border-radius: 50%;
  border: 2px solid var(--bg-deep, #070D1A);
}
.tl-dot.tl-added { background: #4A9DF0; box-shadow: 0 0 8px rgba(74,157,240,0.7); }
.tl-dot.tl-modified { background: #FFB454; box-shadow: 0 0 8px rgba(255,180,84,0.7); }
.tl-dot.tl-deleted { background: #FF5D6C; box-shadow: 0 0 8px rgba(255,93,108,0.7); }
.tl-body { min-width: 0; }
.tl-msg {
  font-size: 12px;
  color: var(--text-dim);
  line-height: 1.4;
}
.tl-time {
  font-size: 11px;
  color: var(--text-muted);
  margin-top: 2px;
}

@media (max-width: 1100px) {
  .kpi-row { grid-template-columns: repeat(2, 1fr); }
  .grid-2 { grid-template-columns: 1fr; }
}

/* 入侵防御面板 */
.panel-title-row {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
}
.defense-tip {
  font-size: 12px;
  color: var(--text-muted);
}
.defense-kpi {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 12px;
}
.dk-card {
  background: rgba(255,255,255,0.03);
  border: 1px solid var(--border, rgba(255,255,255,0.08));
  border-radius: 10px;
  padding: 12px;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
}
.dk-val {
  font-size: 22px;
  font-weight: 700;
  color: var(--text);
}
.dk-val.ok { color: #00D9A3; }
.dk-val.warn { color: #FFB454; }
.dk-val.bad { color: #FF5D6C; }
.dk-label {
  font-size: 12px;
  color: var(--text-muted);
}
.def-badge {
  font-size: 12px;
  padding: 2px 10px;
  border-radius: 20px;
  font-weight: 600;
}
.def-badge.good { background: rgba(0,217,163,0.16); color: #00D9A3; }
.def-badge.warn { background: rgba(255,180,84,0.16); color: #FFB454; }
.def-badge.bad { background: rgba(255,93,108,0.16); color: #FF5D6C; }
.def-badge.neutral { background: rgba(255,255,255,0.08); color: var(--text-dim); }
.node-cell { font-weight: 600; color: var(--text); margin-right: 8px; }
.need-upgrade { font-size: 11px; color: #FFB454; border: 1px solid rgba(255,180,84,0.4); border-radius: 10px; padding: 1px 8px; }
.jail-tag {
  font-size: 11px;
  color: #00D9A3;
  background: rgba(0,217,163,0.12);
  border-radius: 8px;
  padding: 2px 8px;
}
.dim { color: var(--text-dim); }
.muted { color: var(--text-muted); }
.def-confirm {
  margin: 8px 0 0;
  padding-left: 18px;
  color: var(--text-dim);
  font-size: 12px;
  line-height: 1.7;
}
.def-confirm code {
  background: rgba(255,255,255,0.08);
  padding: 0 4px;
  border-radius: 4px;
  color: #4A9DF0;
}
</style>
