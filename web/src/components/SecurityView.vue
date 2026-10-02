<template>
  <div class="security-view">
    <PageHeader title="安全中心" desc="入侵防御、基线合规、文件完整性与安全事件的统一视图" />

    <RefreshBar :loading="refreshing" @refresh="refreshAll" />

    <!-- 加载失败提示：与「能力未启用」区分开，避免把失败当空数据 -->
    <el-alert
      v-if="loadError"
      type="error"
      :closable="false"
      show-icon
      class="sec-load-error"
    >
      <template #title>安全模块数据加载失败：{{ loadError }}
        <el-button link type="primary" @click="refreshAll">重试</el-button>
      </template>
    </el-alert>

    <!-- 顶部 KPI 概览 -->
    <div class="card kpi-row" v-loading="loading">
      <div class="kpi-card score-kpi" :class="scoreClass">
        <div class="kpi-ring" :style="ringStyle">
          <div class="kpi-score">{{ summary.score.toFixed(0) }}</div>
        </div>
        <div class="kpi-meta">
          <div class="kpi-title">安全合规评分</div>
          <div class="kpi-sub">{{ summary.baselineHosts }} 台主机已评估</div>
        </div>
      </div>

      <KpiCard :value="summary.eventCount" label="安全事件" :tone="summary.eventCount > 0 ? 'alert' : 'total'">
        <template #icon>⚠</template>
      </KpiCard>

      <KpiCard :value="summary.riskNodes" label="风险主机" tone="warn">
        <template #icon>🛡</template>
      </KpiCard>

      <KpiCard :value="summary.fimChanges" label="文件完整性变化" tone="conn">
        <template #icon>📄</template>
      </KpiCard>
    </div>

    <!-- 入侵防御（受控 fail2ban 专属 SSH 防护） -->
    <SectionCard title="入侵防御" dense v-loading="defenseLoading">
      <template #actions>
        <span class="defense-tip">仅托管 nebula-monitor-sshd 专属 jail，不覆盖既有 fail2ban 配置</span>
      </template>

      <div class="defense-kpi" v-if="defenseSummary">
        <div class="dk-card"><span class="dk-val ok">{{ defenseSummary.protected }}</span><span class="dk-label">已防护</span></div>
        <div class="dk-card"><span class="dk-val">{{ defenseSummary.unmanaged }}</span><span class="dk-label">未启用</span></div>
        <div class="dk-card"><span class="dk-val warn">{{ defenseSummary.running }}</span><span class="dk-label">执行中</span></div>
        <div class="dk-card"><span class="dk-val bad">{{ defenseSummary.exception }}</span><span class="dk-label">异常</span></div>
      </div>

      <el-table :data="pagedDefenses" style="width: 100%; margin-top: 12px">
        <template #empty>
          <EmptyState
            :icon="Lock"
            title="暂无可防护节点"
            :hints="['Agent 需先上报才能托管专属 jail', '确认目标机已安装 fail2ban，且 Agent 以 root 运行']"
          />
        </template>
        <el-table-column label="节点" min-width="200">
          <template #default="{ row }">
            <div class="node-cell">
              <span class="node-ip">{{ row.nodeIp || row.node }}</span>
              <span v-if="row.displayName && row.displayName !== row.node" class="node-alias">· {{ row.displayName }}</span>
            </div>
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
            <div v-if="row.firewallAction" class="dim defense-action">{{ row.firewallAction }}</div>
          </template>
        </el-table-column>
        <el-table-column label="当前封禁" min-width="180">
          <template #default="{ row }">
            <template v-if="row.managedJail && row.firewallVerified">
              <span v-if="row.bannedIps && row.bannedIps.length" class="ban-count">{{ row.bannedIps.length }} 个 IP</span>
              <span v-else class="dim">当前无封禁</span>
              <div v-if="row.bannedIps && row.bannedIps.length" class="dim ban-ips" :title="row.bannedIps.join(', ')">{{ row.bannedIps.join(', ') }}</div>
            </template>
            <span v-else class="dim">未验证实际封禁</span>
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
      <div style="margin-top: 12px; display: flex; justify-content: flex-end">
        <el-pagination
          v-model:current-page="defCurrentPage"
          v-model:page-size="defPageSize"
          :total="sortedDefenses.length"
          :page-sizes="[10, 20, 50, 100]"
          layout="total, sizes, prev, pager, next, jumper"
          background
        />
      </div>
    </SectionCard>

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
    <SectionCard title="安全事件" dense v-loading="loading">
      <template #actions>
        <div class="filters">
          <el-select v-model="filterCategory" placeholder="全部类别" clearable size="small" style="width: 160px" @change="loadEvents">
            <el-option v-for="c in categoryOptions" :key="c.value" :label="c.label" :value="c.value" />
          </el-select>
          <el-input v-model="filterNode" placeholder="按节点筛选" clearable size="small" style="width: 150px" @change="loadEvents" @clear="loadEvents" />
          <el-button size="small" @click="refreshAll">刷新</el-button>
        </div>
      </template>

      <el-table :data="pagedEvents" style="width: 100%" :row-class-name="rowClass" max-height="460">
        <template #empty>
          <EmptyState
            :icon="Lock"
            :title="filterCategory || filterNode ? '当前筛选下没有安全事件' : '暂无安全事件'"
            :hints="eventHints"
          />
        </template>
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
    </SectionCard>

    <!-- 基线与 FIM 区 -->
    <div class="grid-2">
      <!-- 基线合规明细 -->
      <SectionCard title="基线合规明细" dense v-loading="loading">
        <template #actions>
          <div class="panel-tools">
            <el-switch
              v-model="onlyFailedBaseline"
              size="small"
              inline-prompt
              active-text="仅未通过"
              inactive-text="全部"
            />
          </div>
        </template>
        <EmptyState
          v-if="!filteredBaselines.length"
          :icon="Lock"
          :title="baselines.length ? '没有未通过的节点' : '暂无基线数据'"
          :hints="
            baselines.length
              ? ['当前所有节点都通过基线检查，关闭「仅未通过」可看全量明细']
              : ['基线数据来自配置巡检的 L2 快照，先跑一次巡检生成基线', '未产生快照的资产不会出现在这里']
          "
        />
        <div class="baseline-scroll">
          <div v-for="b in filteredBaselines" :key="b.node" class="baseline-node">
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
      </SectionCard>

      <!-- FIM 变化时间线 -->
      <SectionCard title="文件完整性变化" dense v-loading="loading">
        <EmptyState
          v-if="!fimEvents.length"
          :icon="Lock"
          title="暂无文件变化记录"
          :hints="['Agent 每轮上报时比对配置文件的 md5，变化才产生记录', '确认该节点已开启文件完整性采集']"
        />
        <div class="timeline">
          <div v-for="e in fimEvents" :key="e.id" class="tl-item">
            <span class="tl-dot" :class="'tl-' + e.detail.action"></span>
            <div class="tl-body">
              <div class="tl-msg">{{ e.message }}</div>
              <div class="tl-time">{{ fmtTime(e.timestamp) }} · {{ e.node }}</div>
            </div>
          </div>
        </div>
      </SectionCard>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, watch, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { getSecuritySummary, getSecurityEvents, getSecurityBaselines, getDefenseStatuses, postDefenseAction } from '../api/security'
import { Lock } from '@element-plus/icons-vue'
import RefreshBar from './RefreshBar.vue'
import KpiCard from './KpiCard.vue'
import PageHeader from './common/PageHeader.vue'
import SectionCard from './common/SectionCard.vue'
import EmptyState from './common/EmptyState.vue'

const loading = ref(false)
const refreshing = ref(false)
const loadError = ref('')
const summary = ref({ score: 0, eventCount: 0, riskNodes: 0, fimChanges: 0, baselineHosts: 0 })
const events = ref([])
const baselines = ref([])

// 安全事件空态：区分「确实没有事件」与「被筛选掉了」
const eventHints = computed(() => {
  if (filterCategory.value || filterNode.value) {
    return ['清空「类别」与「节点」筛选后再看一次', '也可以直接到「告警中心」「操作审计」交叉查证']
  }
  return [
    '没有事件通常是好事：安全模块未检测到异常',
    '若刚部署，确认目标机 Agent 已上报且安全采集项已开启',
  ]
})
const onlyFailedBaseline = ref(false)
const filteredBaselines = computed(() => {
  if (!onlyFailedBaseline.value) return baselines.value
  return baselines.value.filter((b) => !b.items || b.items.some((it) => !it.pass))
})
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
    else if (d.managedJail && d.running && d.firewallVerified) s.protected++
    else if (d.managedJail && d.running) s.running++
    else if (!d.supported) s.exception++
    else s.unmanaged++
  }
  return s
})

function defenseStateText(row) {
  if (!row.agentSupported) return '需升级 Agent'
  if (row.managedJail && row.running && row.firewallVerified) return '已防护'
  if (row.managedJail && row.running) return '仅审计/未验证'
  if (row.managedJail && !row.running) return '已停用'
  if (row.agentSupported && !row.managedJail) return '未启用'
  if (!row.supported) return '不支持'
  return '未知'
}
function defenseBadgeClass(row) {
  if (!row.agentSupported || !row.supported) return 'bad'
  if (row.managedJail && row.running && row.firewallVerified) return 'good'
  if (row.managedJail && row.running) return 'warn'
  if (row.managedJail) return 'warn'
  return 'neutral'
}

async function loadDefense() {
  try {
    const d = await getDefenseStatuses()
    defenseStatuses.value = d.statuses || []
    loadError.value = ''
  } catch (e) {
    defenseStatuses.value = []
    if (e && e.message) loadError.value = e.message
  }
}

// IP 升序比较：点分十进制按四段数值比较，非标准 IP 退化为字符串比较
function ipCompare(a, b) {
  const pa = String(a || '').split('.')
  const pb = String(b || '').split('.')
  const isNum = (p) => p.length === 4 && p.every((x) => /^\d+$/.test(x))
  if (isNum(pa) && isNum(pb)) {
    for (let i = 0; i < 4; i++) {
      const x = +pa[i]
      const y = +pb[i]
      if (x !== y) return x - y
    }
    return 0
  }
  return String(a || '').localeCompare(String(b || ''))
}

// 稳定默认顺序：参考主机列表，按节点 IP 升序（不依赖后端顺序，刷新后保持一致）
const sortedDefenses = computed(() =>
  defenseStatuses.value.slice().sort((a, b) => ipCompare(a.nodeIp, b.nodeIp))
)

// 入侵防御列表前端分页
const defCurrentPage = ref(1)
const defPageSize = ref(10)
const pagedDefenses = computed(() => {
  const start = (defCurrentPage.value - 1) * defPageSize.value
  return sortedDefenses.value.slice(start, start + defPageSize.value)
})
watch(sortedDefenses, () => {
  const max = Math.max(1, Math.ceil(sortedDefenses.value.length / defPageSize.value))
  if (defCurrentPage.value > max) defCurrentPage.value = max
})

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
  { value: 'cat_ban', label: 'fail2ban 封禁' },
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
  const color = pct >= 80 ? 'var(--ok)' : pct >= 60 ? 'var(--warn)' : 'var(--danger)'
  return {
    background: `conic-gradient(${color} ${pct * 3.6}deg, var(--fill-2) 0deg)`,
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
    cat_ban: 'fail2ban 封禁',
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
    loadError.value = ''
  } catch (e) {
    if (e && e.message) loadError.value = e.message
  }
}
async function loadEvents() {
  try {
    const d = await getSecurityEvents({ limit: 2000, category: filterCategory.value, node: filterNode.value })
    events.value = d.events || []
    evCurrentPage.value = 1
    loadError.value = ''
  } catch (e) {
    events.value = []
    if (e && e.message) loadError.value = e.message
  }
}
async function loadBaselines() {
  try {
    const d = await getSecurityBaselines()
    baselines.value = d.baselines || []
    loadError.value = ''
  } catch (e) {
    baselines.value = []
    if (e && e.message) loadError.value = e.message
  }
}
async function refreshAll() {
  refreshing.value = true
  await Promise.all([loadSummary(), loadEvents(), loadBaselines(), loadDefense()])
  refreshing.value = false
}

onMounted(async () => {
  loading.value = true
  await refreshAll()
  loading.value = false
})
</script>

<style scoped>
.security-view {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.sec-load-error {
  margin-bottom: 0;
}
.sec-load-error :deep(.el-button--primary) { font-size: 13px; }
.kpi-row {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 16px;
  padding: 18px;
}
.kpi-row > :deep(.kpi-card) {
  height: 100%;
  box-sizing: border-box;
}

.score-kpi {
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 6px 4px;
}
.score-kpi .kpi-ring {
  width: 72px;
  height: 72px;
  border-radius: 50%;
  display: grid;
  place-items: center;
  flex-shrink: 0;
}
.score-kpi .kpi-score {
  width: 56px;
  height: 56px;
  border-radius: 50%;
  background: var(--s0);
  display: grid;
  place-items: center;
  font-size: 22px;
  font-weight: 700;
  color: var(--text);
  line-height: 1;
}
.score-kpi .kpi-meta {
  min-width: 0;
}
.score-kpi .kpi-title {
  font-size: 14px;
  color: var(--text);
  margin-top: 2px;
}
.score-kpi .kpi-sub {
  font-size: 13px;
  color: var(--text-muted);
}
.score-kpi.good .kpi-score { color: var(--ok); }
.score-kpi.warn .kpi-score { color: var(--warn); }
.score-kpi.bad .kpi-score { color: var(--danger); }

/* 卡片标题统一走 SectionCard，不再自写 .panel-title / .panel-title-row */
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
  font-size: 13px;
  font-weight: 600;
}
.sev-tag.sev-critical { background: var(--danger-dim); color: var(--danger); }
.sev-tag.sev-warning { background: var(--warn-dim); color: var(--warn); }
.sev-tag.sev-info { background: var(--accent-dim); color: var(--accent); }

.cat-tag {
  font-size: 13px;
  color: var(--text-dim);
}
.row-critical :deep(.el-table__row) { box-shadow: inset 3px 0 0 var(--danger); }
.row-warning :deep(tr) { border-left: 3px solid var(--warn); }

.dim { color: var(--text-dim); font-size: 13px; }
.muted { color: var(--text-muted); font-size: 13px; }

/* 来源 IP 属地标签 */
.src-block { display: flex; flex-direction: column; gap: 2px; }
.geo-tag {
  display: inline-block;
  align-self: flex-start;
  max-width: 100%;
  padding: 0 6px;
  border-radius: 4px;
  background: rgba(74, 157, 240, 0.15);
  color: var(--accent);
  font-size: 13px;
  line-height: 18px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* 基线 */
.panel-tools {
  margin-left: auto;
  display: flex;
  align-items: center;
}
.baseline-scroll {
  max-height: 460px;
  overflow-y: auto;
  overflow-x: hidden;
  padding-right: 4px;
}
.baseline-node {
  border: 1px solid var(--bd);
  border-radius: 10px;
  padding: 12px;
  margin-bottom: 12px;
  background: var(--fill-1);
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
  font-size: 13px;
  font-weight: 400;
  color: var(--text-dim);
}
.score-pill {
  font-size: 13px;
  font-weight: 700;
  padding: 2px 10px;
  border-radius: 20px;
}
.score-pill.good { background: var(--ok-dim); color: var(--ok); }
.score-pill.warn { background: var(--warn-dim); color: var(--warn); }
.score-pill.bad { background: var(--danger-dim); color: var(--danger); }

.baseline-items {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.bl-item {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  padding: 4px 6px;
  border-radius: 6px;
  transition: background 0.15s;
}
.bl-item:hover { background: var(--fill-1); }
.bl-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  flex-shrink: 0;
}
.bl-item.pass .bl-dot { background: var(--ok); box-shadow: 0 0 8px var(--ok-dim); }
.bl-item.fail .bl-dot { background: var(--danger); box-shadow: 0 0 8px var(--danger-dim); }
.bl-name { color: var(--text-dim); flex: 1; }
.bl-item.fail .bl-name { color: var(--warn); }
.bl-state { font-size: 13px; color: var(--text-muted); }
.bl-item.fail .bl-state { color: var(--danger); }

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
  border-left: 2px solid var(--bd);
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
  border: 2px solid var(--s0);
}
.tl-dot.tl-added { background: var(--accent); box-shadow: 0 0 8px var(--accent-dim); }
.tl-dot.tl-modified { background: var(--warn); box-shadow: 0 0 8px var(--warn-dim); }
.tl-dot.tl-deleted { background: var(--danger); box-shadow: 0 0 8px rgba(255,93,108,0.7); }
.tl-body { min-width: 0; }
.tl-msg {
  font-size: 13px;
  color: var(--text-dim);
  line-height: 1.4;
}
.tl-time {
  font-size: 13px;
  color: var(--text-muted);
  margin-top: 2px;
}

@media (max-width: 1100px) {
  .kpi-row { grid-template-columns: repeat(2, 1fr); }
  .grid-2 { grid-template-columns: 1fr; }
}

/* 网格里的卡片等高，避免左右两张卡底部参差 */
.grid-2 :deep(.sec-card) {
  height: 100%;
}
.defense-tip {
  font-size: 13px;
  color: var(--text-muted);
}
.defense-kpi {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 12px;
}
.dk-card {
  background: var(--fill-1);
  border: 1px solid var(--bd);
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
.dk-val.ok { color: var(--ok); }
.dk-val.warn { color: var(--warn); }
.dk-val.bad { color: var(--danger); }
.dk-label {
  font-size: 13px;
  color: var(--text-muted);
}
.def-badge {
  font-size: 13px;
  padding: 2px 10px;
  border-radius: 20px;
  font-weight: 600;
}
.def-badge.good { background: var(--ok-dim); color: var(--ok); }
.def-badge.warn { background: var(--warn-dim); color: var(--warn); }
.def-badge.bad { background: var(--danger-dim); color: var(--danger); }
.def-badge.neutral { background: var(--fill-3); color: var(--text-dim); }
.node-cell { font-weight: 600; color: var(--text); margin-right: 8px; }
.need-upgrade { font-size: 13px; color: var(--warn); border: 1px solid var(--warn-bd); border-radius: 10px; padding: 1px 8px; }
.jail-tag {
  font-size: 13px;
  color: var(--ok);
  background: rgba(0,217,163,0.12);
  border-radius: 8px;
  padding: 2px 8px;
}
.defense-action, .ban-ips { margin-top: 4px; font-size: 13px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.ban-count { color: var(--warn); font-size: 13px; font-weight: 600; }
.dim { color: var(--text-dim); }
.muted { color: var(--text-muted); }
.def-confirm {
  margin: 8px 0 0;
  padding-left: 18px;
  color: var(--text-dim);
  font-size: 13px;
  line-height: 1.7;
}
.def-confirm code {
  background: var(--fill-3);
  padding: 0 4px;
  border-radius: 4px;
  color: var(--accent);
}
</style>
