<template>
  <div>
    <!-- 告警统计看板 -->
    <div class="glass panel" style="margin-bottom: 16px">
      <div class="panel-title" style="margin-bottom: 12px">告警概览</div>
      <div class="alert-stats">
        <div class="glass panel kpi">
          <div class="kpi-label">活跃告警</div>
          <div class="kpi-value red">{{ stats.firing }}</div>
        </div>
        <div class="glass panel kpi">
          <div class="kpi-label">紧急</div>
          <div class="kpi-value red">{{ stats.bySeverity.critical }}</div>
        </div>
        <div class="glass panel kpi">
          <div class="kpi-label">警告</div>
          <div class="kpi-value amber">{{ stats.bySeverity.warning }}</div>
        </div>
        <div class="glass panel kpi">
          <div class="kpi-label">已抑制</div>
          <div class="kpi-value gray">{{ stats.suppressed }}</div>
        </div>
        <div class="glass panel kpi">
          <div class="kpi-label">24h 事件</div>
          <div class="kpi-value cyan">{{ stats.total }}</div>
        </div>
      </div>
      <div class="stats-extra">
        <div class="stats-col">
          <div class="mini-title">级别分布（活跃）</div>
          <div class="sev-bar-wrap">
            <span class="sev-dot danger"></span>紧急 {{ stats.bySeverity.critical }}
            <span class="sev-dot warning"></span>警告 {{ stats.bySeverity.warning }}
            <span class="sev-dot info"></span>信息 {{ stats.bySeverity.info }}
          </div>
        </div>
        <div class="stats-col">
          <div class="mini-title">Top 规则</div>
          <div v-if="!stats.topRules.length" class="muted">暂无数据</div>
          <div v-for="r in stats.topRules" :key="r.ruleId" class="top-rule">
            <span class="top-name">{{ r.name }}</span>
            <span class="top-badge">共 {{ r.total }} / 活跃 {{ r.firing }}</span>
          </div>
        </div>
      </div>
    </div>

    <!-- 维护窗口 -->
    <div class="glass panel" style="margin-bottom: 16px">
      <div class="panel-title-row">
        <span class="panel-title" style="margin-bottom: 0">维护窗口</span>
        <el-switch
          v-model="maintenance.enabled"
          active-text="已开启"
          inactive-text="已关闭"
          @change="saveMaintenance"
        />
      </div>
      <template v-if="maintenance.enabled">
        <div class="maintenance-row">
          <div class="maintenance-item">
            <span class="maintenance-label">开始时间</span>
            <el-date-picker
              v-model="maintenanceStart"
              type="datetime"
              placeholder="选择开始时间"
              format="YYYY-MM-DD HH:mm"
              value-format="x"
              @change="saveMaintenance"
            />
          </div>
          <div class="maintenance-item">
            <span class="maintenance-label">结束时间</span>
            <el-date-picker
              v-model="maintenanceEnd"
              type="datetime"
              placeholder="选择结束时间"
              format="YYYY-MM-DD HH:mm"
              value-format="x"
              @change="saveMaintenance"
            />
          </div>
          <div class="maintenance-item" style="flex: 1">
            <span class="maintenance-label">原因</span>
            <el-input v-model="maintenance.reason" placeholder="如：版本升级、数据迁移..." @blur="saveMaintenance" />
          </div>
        </div>
        <div class="maintenance-hint">维护窗口期间，所有告警通知将被抑制，告警事件仍会正常记录</div>
      </template>
    </div>

    <!-- 告警规则 -->
    <div class="glass panel" style="margin-bottom: 16px">
      <div class="panel-title-row">
        <span class="panel-title" style="margin-bottom: 0">告警规则</span>
        <el-dropdown split-button type="primary" size="small" @command="onTemplateCmd">
          <span @click="newRule">新建规则</span>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item command="__blank">空白规则</el-dropdown-item>
              <el-dropdown-item v-for="t in templates" :key="t.name" :command="t.name">{{ t.name }}</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </div>
      <el-table :data="rules" stripe style="width: 100%" empty-text="暂无规则">
        <el-table-column prop="name" label="名称" min-width="140" />
        <el-table-column label="触发条件" min-width="180">
          <template #default="{ row }">
            <span class="mono">{{ row.metric }} {{ row.operator }} {{ row.threshold }}</span>
          </template>
        </el-table-column>
        <el-table-column label="级别" width="90">
          <template #default="{ row }">
            <el-tag :type="sevType(row.severity)" size="small" effect="dark">{{ sevLabel(row.severity) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="通知渠道" min-width="150">
          <template #default="{ row }">
            <template v-if="row.notify && row.notify.length">
              <el-tag v-for="c in row.notify" :key="c" size="small" style="margin: 0 4px 4px 0">{{ channelLabel(c) }}</el-tag>
            </template>
            <span v-else class="muted">不发送</span>
          </template>
        </el-table-column>
        <el-table-column label="应用范围" min-width="140">
          <template #default="{ row }">
            <template v-if="row.scope === 'specified'">
              <el-tag type="warning" size="small">指定主机</el-tag>
              <span class="scope-count">{{ (row.nodes || []).length }} 台</span>
            </template>
            <el-tag v-else type="success" size="small">全部主机</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="持续" width="80">
          <template #default="{ row }">{{ row.for === '0' ? '立即' : row.for }}</template>
        </el-table-column>
        <el-table-column label="启用" width="80" align="center">
          <template #default="{ row }">
            <el-switch
              v-model="row.enabled"
              @change="toggleRule(row)"
            />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="130">
          <template #default="{ row }">
            <el-button link size="small" @click="edit(row)">编辑</el-button>
            <el-button link type="danger" size="small" @click="del(row.id)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 高级：抑制与分组（P4） -->
    <div class="glass panel" style="margin-bottom: 16px">
      <div class="panel-title" style="margin-bottom: 12px">高级设置 · 抑制与分组</div>

      <div class="adv-section">
        <div class="adv-title">告警分组</div>
        <div class="adv-row">
          <el-switch v-model="grouping.enabled" active-text="已开启" inactive-text="已关闭" />
          <span class="muted">将相同标签的告警合并为一组，按等待/间隔汇总发送，减少通知风暴</span>
        </div>
        <div class="adv-row" v-if="grouping.enabled">
          <div class="adv-item">
            <span class="adv-label">分组标签</span>
            <el-select v-model="grouping.groupBy" multiple collapse-tags placeholder="分组标签">
              <el-option label="规则名" value="name" />
              <el-option label="规则ID" value="rule" />
              <el-option label="节点" value="node" />
              <el-option label="实例" value="instance" />
              <el-option label="级别" value="severity" />
              <el-option label="指标" value="metric" />
            </el-select>
          </div>
          <div class="adv-item">
            <span class="adv-label">首次等待</span>
            <el-input v-model="grouping.groupWait" placeholder="30s" style="width: 110px" />
          </div>
          <div class="adv-item">
            <span class="adv-label">汇总间隔</span>
            <el-input v-model="grouping.groupInterval" placeholder="5m" style="width: 110px" />
          </div>
          <el-button type="primary" size="small" @click="saveGrouping">保存</el-button>
        </div>
      </div>

      <el-divider />

      <div class="adv-section">
        <div class="panel-title-row">
          <span class="adv-title" style="margin: 0">抑制规则</span>
          <el-button size="small" type="primary" @click="newInhibit">新增规则</el-button>
        </div>
        <el-table :data="inhibits" stripe style="width: 100%; margin-top: 8px" empty-text="暂无抑制规则">
          <el-table-column label="源匹配（触发时）" min-width="200">
            <template #default="{ row }">{{ inhibitText(row.source) }}</template>
          </el-table-column>
          <el-table-column label="目标匹配（被抑制）" min-width="200">
            <template #default="{ row }">{{ inhibitText(row.target) }}</template>
          </el-table-column>
          <el-table-column label="Equal" min-width="150">
            <template #default="{ row }">
              <el-tag v-for="k in (row.equal || [])" :key="k" size="small" style="margin: 0 4px 4px 0">{{ k }}</el-tag>
              <span v-if="!row.equal || !row.equal.length" class="muted">—</span>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="130">
            <template #default="{ row, $index }">
              <el-button link size="small" @click="editInhibit(row, $index)">编辑</el-button>
              <el-button link type="danger" size="small" @click="delInhibit($index)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>
    </div>

    <!-- 告警事件 -->
    <div class="glass panel">
      <div class="panel-title-row">
        <span class="panel-title" style="margin-bottom: 0">告警事件</span>
        <div class="event-toolbar">
          <el-radio-group v-model="eventFilter" size="small">
            <el-radio-button value="firing">活跃</el-radio-button>
            <el-radio-button value="resolved">已恢复</el-radio-button>
            <el-radio-button value="">全部</el-radio-button>
          </el-radio-group>
          <el-button size="small" :disabled="!selected.length" @click="batchAck">批量确认 ({{ selected.length }})</el-button>
          <el-button size="small" :loading="testing" @click="testAlert">测试事件</el-button>
        </div>
      </div>
      <el-table
        :data="filteredAlerts"
        stripe
        style="width: 100%"
        empty-text="暂无告警事件"
        @selection-change="onSelect"
        @row-dblclick="openDetail"
      >
        <el-table-column type="selection" width="45" />
        <el-table-column prop="ruleName" label="规则" min-width="140" />
        <el-table-column prop="node" label="节点" min-width="130" />
        <el-table-column label="级别" width="80">
          <template #default="{ row }">
            <el-tag :type="sevType(row.severity)" size="small" effect="dark">{{ sevLabel(row.severity) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="120">
          <template #default="{ row }">
            <el-tag v-if="acks[ackKey(row)]" type="info" size="small" effect="plain">已确认</el-tag>
            <el-tag v-else :type="row.state === 'firing' ? 'danger' : 'success'" size="small" effect="dark">
              {{ row.state === 'firing' ? '告警中' : '已恢复' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="抑制" width="90">
          <template #default="{ row }">
            <el-tag v-if="row.suppressed" type="info" size="small" effect="plain">已抑制</el-tag>
            <span v-else class="muted">—</span>
          </template>
        </el-table-column>
        <el-table-column prop="message" label="详情" min-width="200" show-overflow-tooltip />
        <el-table-column label="时间" width="160">
          <template #default="{ row }">{{ fmt(row.startsAt || row.endsAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="80">
          <template #default="{ row }">
            <el-button link size="small" @click="openDetail(row)">详情</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <RuleModal v-if="editing" :rule="editing" :groups="groups" :channels="channelOptions" @close="editing = null" @saved="onSaved" />

    <!-- 抑制规则编辑 -->
    <el-dialog v-model="inhibitDialog" :title="inhibitEditIndex < 0 ? '新增抑制规则' : '编辑抑制规则'" width="620px">
      <div class="inh-block">
        <div class="inh-title">源匹配（当该告警处于告警中时）</div>
        <div class="inh-row"><span class="inh-label">规则ID 包含</span><el-input v-model="inhibitForm.sourceRule" placeholder="如 host-offline，留空不限" /></div>
        <div class="inh-row"><span class="inh-label">级别</span>
          <el-select v-model="inhibitForm.sourceSeverity" clearable placeholder="不限">
            <el-option label="紧急" value="critical" /><el-option label="警告" value="warning" /><el-option label="信息" value="info" />
          </el-select>
        </div>
        <div class="inh-row"><span class="inh-label">指标正则</span><el-input v-model="inhibitForm.sourceMetricRegex" placeholder="如 .* 或 cpu.*，留空不限" /></div>
      </div>
      <div class="inh-block">
        <div class="inh-title">目标匹配（将被抑制的告警）</div>
        <div class="inh-row"><span class="inh-label">级别</span>
          <el-select v-model="inhibitForm.targetSeverity" clearable placeholder="不限">
            <el-option label="紧急" value="critical" /><el-option label="警告" value="warning" /><el-option label="信息" value="info" />
          </el-select>
        </div>
        <div class="inh-row"><span class="inh-label">指标正则</span><el-input v-model="inhibitForm.targetMetricRegex" placeholder="如 .* 或 mem.*，留空不限" /></div>
      </div>
      <div class="inh-row"><span class="inh-label">Equal 标签</span>
        <el-select v-model="inhibitForm.equal" multiple collapse-tags placeholder="需相同的标签">
          <el-option label="节点" value="node" /><el-option label="实例" value="instance" /><el-option label="级别" value="severity" /><el-option label="规则ID" value="rule" />
        </el-select>
      </div>
      <template #footer>
        <el-button @click="inhibitDialog = false">取消</el-button>
        <el-button type="primary" @click="saveInhibit">保存</el-button>
      </template>
    </el-dialog>

    <!-- 告警事件详情 -->
    <el-drawer v-model="drawer" :title="detail?.ruleName || '告警详情'" size="480px" @closed="onDrawerClosed">
      <template v-if="detail">
        <div class="ev-field"><span>级别</span><el-tag :type="sevType(detail.severity)" effect="dark">{{ sevLabel(detail.severity) }}</el-tag></div>
        <div class="ev-field"><span>状态</span>{{ stateLabel(detail) }}</div>
        <div class="ev-field">
          <span>节点</span>
          <span>
            <el-link type="primary" @click="gotoNode(detail)">{{ detail.node }}</el-link>
            <span class="muted" v-if="detail.nodeIp"> ({{ detail.nodeIp }})</span>
            <span v-if="acks[ackKey(detail)]" class="ev-acked">· 已确认</span>
          </span>
        </div>
        <div class="ev-field"><span>触发条件</span><span class="mono">{{ detail.metric }} {{ detail.operator }} {{ detail.threshold }}</span></div>
        <div class="ev-field"><span>触发值</span><span class="mono">{{ detail.value }}</span></div>
        <div class="ev-field"><span>详情</span><span>{{ detail.message }}</span></div>
        <div class="ev-field"><span>开始</span><span>{{ fmt(detail.startsAt) }}</span></div>
        <div class="ev-field"><span>恢复</span><span>{{ detail.state === 'resolved' ? fmt(detail.endsAt) : '—' }}</span></div>
        <div class="ev-chart-title" v-if="detail.metric">触发指标近 1 小时趋势</div>
        <div class="ev-chart" ref="chartRef" v-if="detail.metric"></div>
        <div class="ev-actions">
          <el-button type="primary" size="small" :disabled="acks[ackKey(detail)]" @click="ackEvent(detail)">
            {{ acks[ackKey(detail)] ? '已确认' : '确认告警' }}
          </el-button>
          <el-button size="small" @click="drawer = false">关闭</el-button>
        </div>
      </template>
    </el-drawer>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import { Plus } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import * as echarts from 'echarts'
import http from '../api/http'
import RuleModal from './RuleModal.vue'

const router = useRouter()
const rules = ref([])
const alerts = ref([])
const groups = ref([])
const templates = ref([])
const acks = ref({})
const editing = ref(null)
const eventFilter = ref('firing')
const testing = ref(false)
const selected = ref([])
const drawer = ref(false)
const detail = ref(null)
const chartRef = ref(null)
let chartInstance = null
const maintenance = ref({ enabled: false, start: 0, end: 0, reason: '' })
// 全部渠道及其展示名；enabled 由通知配置决定，未启用的渠道在新建规则时置灰。
const CHANNEL_META = [
  { value: 'email', label: '邮件' },
  { value: 'webhook', label: 'Webhook' },
  { value: 'dingtalk', label: '钉钉' },
  { value: 'feishu', label: '飞书' },
  { value: 'wecom', label: '企业微信' },
]
const channelOptions = ref(CHANNEL_META.map((c) => ({ ...c, enabled: true })))
let timer = null

// P4：统计看板 / 抑制规则 / 分组配置
const stats = ref({ firing: 0, suppressed: 0, total: 0, bySeverity: { critical: 0, warning: 0, info: 0 }, topRules: [] })
const inhibits = ref([])
const grouping = ref({ enabled: false, groupBy: ['name'], groupWait: '30s', groupInterval: '5m' })
const inhibitDialog = ref(false)
const inhibitEditIndex = ref(-1)
const inhibitForm = ref(emptyInhibitForm())
function emptyInhibitForm() {
  return { sourceRule: '', sourceSeverity: '', sourceMetricRegex: '', targetSeverity: '', targetMetricRegex: '', equal: [] }
}

const activeCount = computed(() => alerts.value.filter((a) => a.state === 'firing').length)
const criticalCount = computed(() => alerts.value.filter((a) => a.state === 'firing' && a.severity === 'critical').length)
const warningCount = computed(() => alerts.value.filter((a) => a.state === 'firing' && a.severity === 'warning').length)

const filteredAlerts = computed(() => {
  if (!eventFilter.value) return alerts.value
  return alerts.value.filter((a) => a.state === eventFilter.value)
})

function ackKey(e) {
  return `${e.ruleName}|${e.node}|${e.instance || ''}`
}
function sevType(s) {
  return { critical: 'danger', warning: 'warning', info: 'info' }[s] || 'info'
}
function sevLabel(s) {
  return { critical: '紧急', warning: '警告', info: '信息' }[s] || s
}
function stateLabel(e) {
  if (acks.value[ackKey(e)]) return '已确认'
  return { firing: '告警中', resolved: '已恢复', pending: '待触发' }[e.state] || e.state
}
function channelLabel(v) {
  return (CHANNEL_META.find((c) => c.value === v) || {}).label || v
}
function fmt(ts) {
  if (!ts) return '-'
  return new Date(ts).toLocaleString('zh-CN', { hour12: false })
}

const maintenanceStart = computed({
  get: () => maintenance.value.start ? new Date(maintenance.value.start) : null,
  set: (v) => { maintenance.value.start = v ? (typeof v === 'number' ? v : v.getTime()) : 0 },
})
const maintenanceEnd = computed({
  get: () => maintenance.value.end ? new Date(maintenance.value.end) : null,
  set: (v) => { maintenance.value.end = v ? (typeof v === 'number' ? v : v.getTime()) : 0 },
})

async function loadMaintenance() {
  try {
    const mw = await http.get('/api/v1/maintenance')
    if (mw) maintenance.value = mw
  } catch (e) { /* ignore */ }
}

async function saveMaintenance() {
  try {
    await http.put('/api/v1/maintenance', {
      enabled: maintenance.value.enabled,
      start: maintenance.value.start,
      end: maintenance.value.end,
      reason: maintenance.value.reason,
    })
  } catch (e) {
    ElMessage.error('保存维护窗口失败')
  }
}

async function load() {
  try {
    rules.value = (await http.get('/api/v1/rules')).rules || []
  } catch (e) { /* ignore */ }
  try {
    alerts.value = (await http.get('/api/v1/alerts')).alerts || []
  } catch (e) { /* ignore */ }
  try {
    groups.value = (await http.get('/api/v1/groups')).groups || []
  } catch (e) { /* ignore */ }
  try {
    templates.value = (await http.get('/api/v1/rules/templates')).templates || []
  } catch (e) { /* ignore */ }
  try {
    acks.value = (await http.get('/api/v1/alerts/acks')).acks || {}
  } catch (e) { /* ignore */ }
  try {
    stats.value = await http.get('/api/v1/alerts/stats')
  } catch (e) { /* ignore */ }
  try {
    inhibits.value = (await http.get('/api/v1/inhibit')).rules || []
  } catch (e) { /* ignore */ }
  try {
    const g = await http.get('/api/v1/grouping')
    if (g && typeof g === 'object') grouping.value = g
  } catch (e) { /* ignore */ }
  try {
    const cfg = await http.get('/api/v1/notify')
    const enabled = {
      email: cfg.email && cfg.email.enabled,
      webhook: cfg.webhook && cfg.webhook.enabled,
      dingtalk: cfg.dingtalk && cfg.dingtalk.enabled,
      feishu: cfg.feishu && cfg.feishu.enabled,
      wecom: cfg.wecom && cfg.wecom.enabled,
    }
    channelOptions.value = CHANNEL_META.map((c) => ({ ...c, enabled: !!enabled[c.value] }))
  } catch (e) { /* ignore */ }
}

async function testAlert() {
  testing.value = true
  try {
    const r = await http.post('/api/v1/alerts/test')
    if (r.ok) {
      ElMessage.success('已发送测试告警事件，请查看事件列表或通知渠道')
    } else {
      ElMessageBox.alert(r.error || '测试失败', '触发失败', { type: 'error', confirmButtonText: '关闭' })
    }
    load()
  } catch (e) {
    ElMessageBox.alert(e.message || '请求失败', '触发失败', { type: 'error', confirmButtonText: '关闭' })
  } finally {
    testing.value = false
  }
}

function newRule() {
  // 设为空对象（truthy）以触发 RuleModal 渲染（v-if="editing"）
  editing.value = {}
}
function onTemplateCmd(cmd) {
  if (cmd === '__blank') {
    newRule()
    return
  }
  const t = templates.value.find((x) => x.name === cmd)
  if (t) editing.value = { ...t }
}
function edit(rule) {
  editing.value = { ...rule }
}
async function del(id) {
  try {
    await ElMessageBox.confirm('确认删除该规则？', '提示', { type: 'warning' })
    await http.del('/api/v1/rules/' + id)
    ElMessage.success('已删除')
    load()
  } catch (e) { /* 取消 */ }
}
function onSaved() {
  editing.value = null
  load()
}
async function toggleRule(rule) {
  try {
    await http.post('/api/v1/rules/' + rule.id + '/toggle')
    ElMessage.success(rule.enabled ? '已启用' : '已停用')
    load()
  } catch (e) {
    rule.enabled = !rule.enabled
    ElMessage.error('操作失败')
  }
}

function onSelect(rows) {
  selected.value = rows
}
function openDetail(row) {
  detail.value = row
  drawer.value = true
  nextTick(() => loadTrend(row))
}
function gotoNode(row) {
  router.push({ path: '/hosts', query: { node: row.node } })
}
async function ackEvent(row) {
  try {
    await http.post('/api/v1/alerts/ack', { rule: row.ruleName, host: row.node, instance: row.instance || '' })
    ElMessage.success('已确认')
    await load()
  } catch (e) {
    ElMessage.error('确认失败')
  }
}
async function batchAck() {
  if (!selected.value.length) return
  try {
    await Promise.all(selected.value.map((r) =>
      http.post('/api/v1/alerts/ack', { rule: r.ruleName, host: r.node, instance: r.instance || '' }),
    ))
    ElMessage.success(`已确认 ${selected.value.length} 条`)
    await load()
  } catch (e) {
    ElMessage.error('批量确认失败')
  }
}

async function loadTrend(row) {
  if (!row.metric || !chartRef.value) return
  const end = Date.now()
  const start = end - 3600 * 1000
  let url = `/api/v1/query/range?node=${encodeURIComponent(row.node)}&metric=${encodeURIComponent(row.metric)}&start=${start}&end=${end}&step=60000`
  if (row.instance) url += `&labels.instance=${encodeURIComponent(row.instance)}`
  try {
    const data = await http.get(url)
    const pts = (data.series || []).map((p) => [p.time, p.value])
    renderChart(pts, row)
  } catch (e) { /* ignore */ }
}
function renderChart(points, row) {
  if (chartInstance) { chartInstance.dispose(); chartInstance = null }
  chartInstance = echarts.init(chartRef.value)
  chartInstance.setOption({
    grid: { left: 48, right: 16, top: 24, bottom: 28 },
    tooltip: { trigger: 'axis' },
    xAxis: { type: 'time' },
    yAxis: { type: 'value', name: row.metric },
    series: [{
      type: 'line',
      data: points,
      showSymbol: false,
      smooth: true,
      areaStyle: { opacity: 0.15 },
      lineStyle: { color: '#dc382d' },
      itemStyle: { color: '#dc382d' },
      markLine: row.threshold
        ? { silent: true, symbol: 'none', data: [{ yAxis: row.threshold }], lineStyle: { color: '#e6a23c', type: 'dashed' }, label: { formatter: '阈值 ' + row.threshold } }
        : undefined,
    }],
  })
}
function onDrawerClosed() {
  if (chartInstance) { chartInstance.dispose(); chartInstance = null }
  detail.value = null
}

// ---- P4：分组配置 ----
async function saveGrouping() {
  try {
    await http.put('/api/v1/grouping', {
      enabled: grouping.value.enabled,
      groupBy: grouping.value.groupBy || ['name'],
      groupWait: grouping.value.groupWait || '30s',
      groupInterval: grouping.value.groupInterval || '5m',
    })
    ElMessage.success('分组配置已保存（热生效）')
  } catch (e) {
    ElMessage.error('保存分组配置失败')
  }
}

// ---- P4：抑制规则 ----
function inhibitText(ms) {
  if (!ms) return '—'
  const parts = []
  if (ms.match) for (const [k, v] of Object.entries(ms.match)) parts.push(`${k}=${v}`)
  if (ms.matchRegex) for (const [k, v] of Object.entries(ms.matchRegex)) parts.push(`${k}~${v}`)
  return parts.length ? parts.join('  &  ') : '—'
}

function buildInhibitRule() {
  const f = inhibitForm.value
  const src = { match: {}, matchRegex: {} }
  if (f.sourceSeverity) src.match.severity = f.sourceSeverity
  if (f.sourceRule) src.matchRegex.rule = f.sourceRule
  if (f.sourceMetricRegex) src.matchRegex.metric = f.sourceMetricRegex
  const tgt = { match: {}, matchRegex: {} }
  if (f.targetSeverity) tgt.match.severity = f.targetSeverity
  if (f.targetMetricRegex) tgt.matchRegex.metric = f.targetMetricRegex
  return { source: src, target: tgt, equal: f.equal || [] }
}

function formFromRule(r) {
  const f = emptyInhibitForm()
  if (r.source) {
    f.sourceSeverity = r.source.match?.severity || ''
    f.sourceRule = r.source.matchRegex?.rule || r.source.match?.rule || ''
    f.sourceMetricRegex = r.source.matchRegex?.metric || ''
  }
  if (r.target) {
    f.targetSeverity = r.target.match?.severity || ''
    f.targetMetricRegex = r.target.matchRegex?.metric || ''
  }
  f.equal = r.equal || []
  return f
}

function newInhibit() {
  inhibitEditIndex.value = -1
  inhibitForm.value = emptyInhibitForm()
  inhibitDialog.value = true
}
function editInhibit(row, idx) {
  inhibitEditIndex.value = idx
  inhibitForm.value = formFromRule(row)
  inhibitDialog.value = true
}
async function persistInhibits() {
  try {
    await http.put('/api/v1/inhibit', inhibits.value)
    ElMessage.success('抑制规则已保存（热生效）')
  } catch (e) {
    ElMessage.error('保存抑制规则失败')
  }
}
function saveInhibit() {
  const rule = buildInhibitRule()
  ;['source', 'target'].forEach((k) => {
    const ms = rule[k]
    if (Object.keys(ms.match).length === 0) delete ms.match
    if (Object.keys(ms.matchRegex).length === 0) delete ms.matchRegex
  })
  const list = [...inhibits.value]
  if (inhibitEditIndex.value >= 0) list[inhibitEditIndex.value] = rule
  else list.push(rule)
  inhibits.value = list
  inhibitDialog.value = false
  persistInhibits()
}
async function delInhibit(idx) {
  try {
    await ElMessageBox.confirm('确认删除该抑制规则？', '提示', { type: 'warning' })
    inhibits.value.splice(idx, 1)
    persistInhibits()
  } catch (e) { /* 取消 */ }
}

// 每 30s 自动刷新告警列表，确保实时性
onMounted(() => {
  load()
  loadMaintenance()
  timer = setInterval(load, 30000)
})

onUnmounted(() => {
  if (timer) clearInterval(timer)
  if (chartInstance) chartInstance.dispose()
})
</script>

<style scoped>
.panel-title-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.event-toolbar {
  display: flex;
  gap: 8px;
  align-items: center;
}
.muted {
  color: var(--muted, #909399);
}
.ev-acked {
  margin-left: 6px;
  color: var(--muted, #909399);
}
.maintenance-row {
  display: flex;
  gap: 16px;
  margin-top: 12px;
  align-items: flex-start;
  flex-wrap: wrap;
}
.maintenance-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 200px;
}
.maintenance-label {
  font-size: 12px;
  color: var(--text-dim);
}
.maintenance-hint {
  margin-top: 10px;
  font-size: 12px;
  color: var(--amber, #e6a23c);
  background: rgba(230, 162, 60, 0.08);
  padding: 6px 12px;
  border-radius: 4px;
}
.scope-count {
  margin-left: 6px;
  font-size: 12px;
  color: var(--muted, #909399);
}
.ev-field {
  display: flex;
  gap: 12px;
  padding: 8px 0;
  border-bottom: 1px solid var(--border, #2a2f3a);
  font-size: 13px;
}
.ev-field > span:first-child {
  width: 64px;
  color: var(--text-dim);
  flex-shrink: 0;
}
.ev-chart-title {
  margin: 16px 0 8px;
  font-size: 13px;
  color: var(--text-dim);
}
.ev-chart {
  width: 100%;
  height: 240px;
}
.ev-actions {
  margin-top: 16px;
  display: flex;
  gap: 8px;
}
.kpi-value.gray {
  color: var(--muted, #909399);
}
.stats-extra {
  display: flex;
  gap: 24px;
  margin-top: 14px;
  flex-wrap: wrap;
}
.stats-col {
  flex: 1;
  min-width: 240px;
}
.mini-title {
  font-size: 12px;
  color: var(--text-dim);
  margin-bottom: 8px;
}
.sev-bar-wrap {
  font-size: 13px;
  color: var(--text);
  display: flex;
  gap: 16px;
  align-items: center;
}
.sev-dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  margin-right: 4px;
}
.sev-dot.danger { background: #f56c6c; }
.sev-dot.warning { background: #e6a23c; }
.sev-dot.info { background: #909399; }
.top-rule {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 4px 0;
  font-size: 13px;
  border-bottom: 1px dashed var(--border, #2a2f3a);
}
.top-name {
  color: var(--text);
}
.top-badge {
  color: var(--muted, #909399);
  font-size: 12px;
}
.adv-section {
  padding: 6px 0;
}
.adv-title {
  font-size: 14px;
  font-weight: 600;
  margin-bottom: 10px;
  color: var(--text);
}
.adv-row {
  display: flex;
  align-items: center;
  gap: 14px;
  flex-wrap: wrap;
  margin-bottom: 12px;
}
.adv-item {
  display: flex;
  align-items: center;
  gap: 6px;
}
.adv-label {
  font-size: 13px;
  color: var(--text-dim);
  white-space: nowrap;
}
.inh-block {
  background: rgba(255, 255, 255, 0.02);
  border: 1px solid var(--border, #2a2f3a);
  border-radius: 6px;
  padding: 12px;
  margin-bottom: 12px;
}
.inh-title {
  font-size: 13px;
  color: var(--text-dim);
  margin-bottom: 10px;
}
.inh-row {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
}
.inh-label {
  width: 92px;
  font-size: 13px;
  color: var(--text-dim);
  flex-shrink: 0;
}
</style>
