<template>
  <section class="view">
    <header class="view-head">
      <div class="head-row">
        <h2>节点操作</h2>
        <span class="muted">
          平台下发一条白名单动作 → 指定节点执行 → 回执与审计。指令随节点下一次上报下发（默认 15s 内），
          因此**节点离线时无法下发**；已下线/不在线的节点会被服务端直接拒绝。
        </span>
      </div>
      <div class="head-row">
        <el-button v-if="canExec" type="primary" @click="openDispatch">下发操作</el-button>
        <span v-else class="muted">当前账号只读：缺 ops:exec 权限（下发属高风险操作）</span>
        <el-button :loading="loading" @click="reload">刷新</el-button>
        <span v-if="autoRefresh" class="muted">有任务在执行，已开启自动刷新</span>
      </div>
    </header>

    <el-alert v-if="loadError" type="error" :closable="false" show-icon :title="loadError" class="alert-gap" />

    <div class="panel">
      <div class="filters">
        <el-select v-model="filter.node" placeholder="全部节点" clearable style="width: 180px" @change="reload">
          <el-option v-for="n in nodes" :key="n.hostname" :label="n.hostname" :value="n.hostname" />
        </el-select>
        <el-select v-model="filter.state" placeholder="全部状态" clearable style="width: 150px" @change="reload">
          <el-option v-for="s in STATE_OPTIONS" :key="s.value" :label="s.label" :value="s.value" />
        </el-select>
      </div>

      <el-table :data="tasks" v-loading="loading" empty-text="还没有下发过操作任务" style="width: 100%">
        <el-table-column label="下发时间" width="180">
          <template #default="{ row }">
            <div>{{ fmtTime(row.createdAt) }}</div>
            <div class="muted small">{{ relTime(row.createdAt) }}</div>
          </template>
        </el-table-column>
        <el-table-column label="节点" width="140">
          <template #default="{ row }"><span class="mono">{{ row.node }}</span></template>
        </el-table-column>
        <el-table-column label="动作" min-width="200">
          <template #default="{ row }">
            <span>{{ row.title || row.kind }}</span>
            <span v-if="row.readOnly === false" class="tag-write">写操作</span>
            <div class="muted small">{{ kindSummary(row) }}</div>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="120">
          <template #default="{ row }">
            <span class="dot" :class="stateClass(row.state)" />
            <span :class="'st-' + stateClass(row.state)">{{ stateLabel(row.state) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="触发人 / 原因" min-width="180">
          <template #default="{ row }">
            <div>{{ row.operator || '—' }}</div>
            <div class="muted small">{{ row.reason || '未填写原因' }}</div>
          </template>
        </el-table-column>
        <el-table-column label="耗时" width="90">
          <template #default="{ row }">
            <span v-if="row.durationMs">{{ row.durationMs }} ms</span>
            <span v-else class="muted">—</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="120" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openResult(row)">查看结果</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 下发：节点 → 动作 → 参数 → 原因 -->
    <el-dialog v-model="dispatchVisible" title="下发操作" width="640px">
      <el-alert
        type="warning"
        :closable="false"
        show-icon
        class="alert-gap"
        title="指令会在这台机器上执行。能否真正执行取决于该机器自己的 agent.yaml（guards.ops）——默认只放行只读动作。"
      />
      <el-form label-width="90px">
        <el-form-item label="目标节点" required>
          <el-select v-model="form.node" filterable placeholder="选择节点" style="width: 100%" @change="loadActions">
            <el-option v-for="n in nodes" :key="n.hostname" :label="`${n.hostname}（${n.status === 'online' ? '在线' : n.status}）`" :value="n.hostname" />
          </el-select>
        </el-form-item>
        <el-form-item label="动作" required>
          <el-select v-model="form.kind" placeholder="选择动作" style="width: 100%" @change="onActionChange">
            <el-option-group v-for="g in actionGroups" :key="g.key" :label="g.key">
              <el-option
                v-for="a in g.actions"
                :key="a.kind"
                :value="a.kind"
                :label="a.title"
                :disabled="!actionSupported(a)"
              >
                <div class="act-option">
                  <span class="act-title">{{ a.title }}</span>
                  <span class="mono muted small">{{ a.kind }}</span>
                  <span v-if="!a.readOnly" class="tag-write">写操作</span>
                  <span v-if="!actionSupported(a)" class="act-disabled">本机未放行</span>
                </div>
              </el-option>
            </el-option-group>
          </el-select>
          <div v-if="selectedAction" class="field-hint">{{ selectedAction.desc }}</div>
          <!-- 「为什么是灰的」必须说清楚：这条链路上"机器有权不同意"是核心设计，不该让用户猜 -->
          <div v-if="form.node && actionsLoaded && support.length === 0" class="field-hint warn">
            节点 {{ form.node }} 未声明任何可执行动作：Agent 版本可能过低，或本机 guards.ops 全关。
          </div>
        </el-form-item>
        <el-form-item v-for="p in paramSpecs" :key="p.name" :label="p.title || p.name" :required="p.required">
          <el-input v-model="form.params[p.name]" :placeholder="p.example || p.desc" />
          <div v-if="p.desc" class="field-hint">{{ p.desc }}</div>
        </el-form-item>
        <el-form-item label="原因">
          <el-input v-model="form.reason" type="textarea" :rows="2" placeholder="选填，但强烈建议填：回看历史时「为什么执行它」往往比「谁执行的」更重要" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dispatchVisible = false">取消</el-button>
        <el-button type="primary" :loading="submitting" :disabled="!form.node || !form.kind" @click="submitDispatch">下发</el-button>
      </template>
    </el-dialog>

    <!-- 执行结果：按分节展示 -->
    <el-dialog v-model="resultVisible" :title="resultTitle" width="760px">
      <div v-if="resultTask">
        <div class="d-meta">
          <span class="mono">{{ resultTask.node }}</span> · {{ resultTask.kind }} ·
          状态 {{ stateLabel(resultTask.state) }} ·
          <template v-if="resultTask.durationMs">耗时 {{ resultTask.durationMs }} ms · </template>
          下发 <span v-if="resultTask.readOnly !== false">（只读动作）</span>
        </div>
        <el-alert
          v-if="resultTask.message"
          :type="resultTask.state === 'succeeded' ? 'success' : resultTask.state === 'failed' ? 'error' : 'info'"
          :closable="false"
          show-icon
          class="alert-gap"
          :title="resultTask.message"
        />
        <template v-if="resultSections.length">
          <div v-for="sec in resultSections" :key="sec.key" class="sec">
            <span>{{ sec.key }}</span>
            <span class="muted">命令输出原文</span>
          </div>
          <pre class="out">{{ sec.value }}</pre>
        </template>
        <el-empty v-else description="暂无输出（任务可能还在等待节点领取或执行）" />
      </div>
    </el-dialog>
  </section>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { createOpsTask, listOpsActions, listOpsTasks } from '../api/ops'
import http from '../api/http'
import { useAuth } from '../composables/useAuth'

function fmtTime(ts) {
  if (!ts) return '—'
  return new Date(ts).toLocaleString('zh-CN', { hour12: false })
}

// 相对时间：任务列表关心的是"多久前下发的"（排队多久、是不是刚点的）
function relTime(ts) {
  if (!ts) return '—'
  const diff = Date.now() - ts
  if (diff < 60_000) return '刚刚'
  if (diff < 3600_000) return `${Math.floor(diff / 60_000)} 分钟前`
  if (diff < 86400_000) return `${Math.floor(diff / 3600_000)} 小时前`
  return `${Math.floor(diff / 86400_000)} 天前`
}

const auth = useAuth()
// 前端隐藏只是体验：服务端 ops:exec 是真正的边界（且属高风险权限）
const canExec = computed(() => auth.can('ops:exec'))

const STATE_OPTIONS = [
  { value: 'queued', label: '排队中' },
  { value: 'delivered', label: '已下发' },
  { value: 'running', label: '执行中' },
  { value: 'succeeded', label: '成功' },
  { value: 'failed', label: '失败' },
  { value: 'expired', label: '已超时' },
]
const STATE_LABELS = Object.fromEntries(STATE_OPTIONS.map((s) => [s.value, s.label]))
const stateLabel = (s) => STATE_LABELS[s] || s
// 状态 → 配色类（排队/下发/执行中是不定性，成功/失败/超时是终态）
const STATE_CLASSES = { queued: 'queued', delivered: 'queued', running: 'queued', succeeded: 'online', failed: 'missing', expired: 'archived' }
const stateClass = (s) => STATE_CLASSES[s] || 'archived'
const TERMINAL = ['succeeded', 'failed', 'expired']

const tasks = ref([])
const nodes = ref([])
const loading = ref(false)
const loadError = ref('')
const filter = ref({ node: '', state: '' })

const dispatchVisible = ref(false)
const submitting = ref(false)
const actions = ref([])
const permissions = ref({})
const support = ref([])
const actionsLoaded = ref(false)
const form = reactive({ node: '', kind: '', params: {}, reason: '' })

const resultVisible = ref(false)
const resultTask = ref(null)

let timer = null
// 只有在存在未完成任务时才轮询：终态任务不会再变，常驻轮询只是白烧请求。
const autoRefresh = computed(() => tasks.value.some((t) => !TERMINAL.includes(t.state)))

onMounted(async () => {
  await loadNodes()
  await reload()
  timer = setInterval(() => {
    if (autoRefresh.value) reload()
  }, 5000)
})
onBeforeUnmount(() => {
  if (timer) clearInterval(timer)
})

async function loadNodes() {
  try {
    nodes.value = (await http.get('/api/v1/nodes')).nodes || []
  } catch (e) {
    nodes.value = []
  }
}

async function reload() {
  loading.value = true
  loadError.value = ''
  try {
    const res = await listOpsTasks({ node: filter.value.node, state: filter.value.state, limit: 100 })
    tasks.value = res.tasks || []
  } catch (e) {
    loadError.value = e.message || '加载操作任务失败'
    tasks.value = []
  } finally {
    loading.value = false
  }
}

// 动作目录按分组聚合（服务端已按「分组顺序 + 组内只读优先」排好）
const actionGroups = computed(() => {
  const out = []
  for (const a of actions.value) {
    let g = out.find((x) => x.key === a.group)
    if (!g) {
      g = { key: a.group, actions: [] }
      out.push(g)
    }
    g.actions.push(a)
  }
  return out
})
const selectedAction = computed(() => actions.value.find((a) => a.kind === form.kind) || null)
const paramSpecs = computed(() => (selectedAction.value ? selectedAction.value.params || [] : []))

// actionSupported 判断该动作在**当前选中的节点**上是否可用（本机护栏是否放行）
function actionSupported(a) {
  if (!actionsLoaded.value) return true // 目录未加载完时不做灰化，避免闪现全灰
  return support.value.includes(a.kind)
}

function kindSummary(row) {
  const params = row.params || {}
  const keys = Object.keys(params)
  if (!keys.length) return row.kind
  return keys.map((k) => `${k}=${params[k]}`).join(' ')
}

async function openDispatch() {
  form.node = filter.value.node || ''
  form.kind = ''
  form.params = {}
  form.reason = ''
  actionsLoaded.value = false
  support.value = []
  dispatchVisible.value = true
  await loadActions()
}

async function loadActions() {
  actionsLoaded.value = false
  try {
    const res = await listOpsActions(form.node || '')
    actions.value = res.actions || []
    permissions.value = res.permissions || {}
    support.value = res.nodeSupport || []
    actionsLoaded.value = true
    // 切节点后原动作可能不再被放行 → 清掉，避免提交时才发现
    if (form.kind && !actionSupported(selectedAction.value)) {
      form.kind = ''
      form.params = {}
    }
  } catch (e) {
    actions.value = []
    actionsLoaded.value = true
    ElMessage.error(e.message || '加载动作目录失败')
  }
}

function onActionChange() {
  form.params = {}
  for (const p of paramSpecs.value) form.params[p.name] = ''
}

async function submitDispatch() {
  const a = selectedAction.value
  if (!a) return
  const params = {}
  for (const [k, v] of Object.entries(form.params)) {
    if (v !== undefined && v !== null && v !== '') params[k] = v
  }
  const detail = a.readOnly
    ? `将在节点 ${form.node} 上执行「${a.title}」（只读）`
    : `将在节点 ${form.node} 上执行「${a.title}」——**这会改变该机器的运行状态**`
  try {
    await ElMessageBox.confirm(detail.replace(/\*\*/g, ''), '确认下发操作', { type: a.readOnly ? 'info' : 'warning', confirmButtonText: '下发', cancelButtonText: '取消' })
  } catch (e) {
    return
  }
  submitting.value = true
  try {
    await createOpsTask({ node: form.node, kind: a.kind, params, reason: form.reason })
    ElMessage.success(`已下发，等待节点下一次上报领取（任务 ${a.title}）`)
    dispatchVisible.value = false
    await reload()
  } catch (e) {
    // 服务端的说明就是给用户看的下一步（去升级 Agent / 去改目标机器的 guards.ops），原样展示
    ElMessage.error(e.message || '下发失败')
    await loadActions()
  } finally {
    submitting.value = false
  }
}

function openResult(row) {
  resultTask.value = row
  resultVisible.value = true
}

const resultTitle = computed(() => (resultTask.value ? `执行结果 · ${resultTask.value.title || resultTask.value.kind}` : '执行结果'))
const resultSections = computed(() => {
  const data = (resultTask.value && resultTask.value.data) || {}
  return Object.keys(data).map((key) => ({ key, value: data[key] }))
})
</script>

<style scoped>
.small {
  font-size: 12px;
}
.filters {
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
  margin-bottom: 12px;
}
.tag-write {
  display: inline-block;
  margin-left: 6px;
  padding: 0 6px;
  border: 1px solid rgba(255, 180, 84, 0.45);
  border-radius: 3px;
  background: rgba(255, 180, 84, 0.1);
  color: #ffb054;
  font-size: 11px;
  line-height: 16px;
}
.dot {
  display: inline-block;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  margin-right: 5px;
  vertical-align: 1px;
}
.dot.online {
  background: var(--accent);
}
.dot.missing {
  background: var(--danger);
}
.dot.queued {
  background: var(--warn);
}
.dot.archived {
  background: var(--text-dim);
}
.st-online {
  color: var(--accent);
}
.st-missing {
  color: var(--danger);
}
.st-queued {
  color: var(--warn);
}
.st-archived {
  color: var(--text-dim);
}
.act-option {
  display: flex;
  align-items: center;
  gap: 8px;
}
.act-title {
  font-weight: 500;
}
.act-disabled {
  margin-left: auto;
  color: var(--text-dim);
  font-size: 12px;
}
.sec {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  margin: 14px 0 6px;
  font-size: 13px;
}
.out {
  margin: 0;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: rgba(255, 255, 255, 0.03);
  font-family: var(--mono);
  font-size: 12.5px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 320px;
  overflow: auto;
}
.field-hint {
  font-size: 12.5px;
  color: var(--text-dim);
  line-height: 1.5;
  margin-top: 4px;
}
.field-hint.warn {
  color: var(--warn);
}
</style>
