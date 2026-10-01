<template>
  <section class="view">
    <header class="view-head">
      <div class="head-row">
        <h2>节点操作</h2>
        <span class="muted">
          平台下发一条白名单动作 → 目标节点执行 → 回执与审计。可一次选多个节点（按分组整选）。
          指令随节点下一次上报下发（默认 15s 内），因此<b class="warn-text">离线节点无法下发</b>；
          能否真正执行还取决于每台机器自己的 agent.yaml（guards.ops）。
        </span>
      </div>
      <div class="head-row">
        <el-button v-if="canExec" type="primary" @click="openDispatch">下发操作</el-button>
        <span v-else class="muted">当前账号只读：缺 ops:exec 权限（下发属高风险操作）</span>
        <el-button :loading="loading" @click="reload">刷新</el-button>
        <span v-if="autoRefresh" class="muted">有任务未结束，已开启自动刷新</span>
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
        <!-- 批次筛选：批量下发后最常见的诉求是"这一批现在怎么样了" -->
        <el-tag v-if="filter.batchId" closable type="info" @close="clearBatchFilter">批次：{{ filter.batchId }}</el-tag>
      </div>

      <el-table :data="tasks" v-loading="loading" empty-text="还没有下发过操作任务" style="width: 100%">
        <el-table-column label="下发时间" width="180">
          <template #default="{ row }">
            <div>{{ fmtTime(row.createdAt) }}</div>
            <div class="muted small">{{ relTime(row.createdAt) }}</div>
          </template>
        </el-table-column>
        <el-table-column label="节点" width="150">
          <template #default="{ row }"><span class="mono">{{ row.node }}</span></template>
        </el-table-column>
        <el-table-column label="动作" min-width="190">
          <template #default="{ row }">
            <span>{{ row.title || row.kind }}</span>
            <span v-if="row.readOnly === false" class="tag-write">写操作</span>
            <div class="muted small">{{ kindSummary(row) }}</div>
          </template>
        </el-table-column>
        <el-table-column label="批次" width="110">
          <template #default="{ row }">
            <el-button v-if="row.batchId" link type="primary" @click="filterByBatch(row.batchId)">{{ row.batchId }}</el-button>
            <span v-else class="muted">单条</span>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="120">
          <template #default="{ row }">
            <span class="dot" :class="stateClass(row.state)" />
            <span :class="'st-' + stateClass(row.state)">{{ stateLabel(row.state) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="触发人 / 原因" min-width="160">
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
        <el-table-column label="操作" width="190" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openResult(row)">查看结果</el-button>
            <!-- 排队中：可以撤回（还没发出去）；已下发的撤不回来，因此不显示取消 -->
            <el-button v-if="canExec && row.state === 'queued'" link type="warning" @click="cancelOne(row)">取消</el-button>
            <el-button v-if="canExec && isTerminal(row.state)" link type="danger" @click="removeOne(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 下发：节点（可多选）→ 动作 → 参数 → 原因 -->
    <el-dialog v-model="dispatchVisible" title="下发操作" width="720px">
      <el-alert
        type="warning"
        :closable="false"
        show-icon
        class="alert-gap"
        title="指令会在选中的机器上执行。能否真正执行取决于每台机器自己的 agent.yaml（guards.ops）——默认只放行只读动作。"
      />
      <el-form label-width="90px">
        <el-form-item label="目标节点" required>
          <div class="node-picker">
            <el-select
              v-model="form.nodes"
              multiple filterable collapse-tags collapse-tags-tooltip
              placeholder="选择节点（可多选）"
              style="width: 100%"
              @change="onNodesChange"
            >
              <el-option
                v-for="n in nodes"
                :key="n.hostname"
                :label="`${n.hostname}（${n.status === 'online' ? '在线' : n.status}）`"
                :value="n.hostname"
                :disabled="n.status !== 'online'"
              />
            </el-select>
            <!-- 按分组整选：批量操作的真实起点通常就是"把 web 这组全选上" -->
            <el-dropdown v-if="groups.length" trigger="click" @command="selectGroup">
              <el-button>按分组选<el-icon :size="12"><ArrowDown /></el-icon></el-button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item v-for="g in groups" :key="g.name" :command="g.name">
                    {{ g.name }}（{{ groupNodeCount(g.name) }} 台在线）
                  </el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
            <el-button v-if="form.nodes.length" @click="form.nodes = []; onNodesChange()">清空</el-button>
          </div>
          <div class="field-hint">
            已选 {{ form.nodes.length }} 个节点（离线节点不可选：指令只能随上报响应送回）
          </div>
        </el-form-item>
        <el-form-item label="动作" required>
          <el-select v-model="form.kind" placeholder="选择动作" style="width: 100%" @change="onActionChange">
            <el-option-group v-for="g in actionGroups" :key="g.key" :label="g.key">
              <el-option
                v-for="a in g.actions"
                :key="a.kind"
                :value="a.kind"
                :label="a.title"
                :disabled="!actionUsable(a)"
              >
                <div class="act-option">
                  <span class="act-title">{{ a.title }}</span>
                  <span class="mono muted small">{{ a.kind }}</span>
                  <span v-if="!a.readOnly" class="tag-write">写操作</span>
                  <span class="act-count">{{ actionCoverage(a) }}</span>
                </div>
              </el-option>
            </el-option-group>
          </el-select>
          <div v-if="selectedAction" class="field-hint">{{ selectedAction.desc }}</div>
          <div v-if="form.nodes.length && capsLoaded && !anyUsable" class="field-hint warn">
            所选节点没有任何可执行动作：多半是 Agent 版本过低，或那些机器都没放行 guards.ops。
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
        <el-button type="primary" :loading="submitting" :disabled="!form.nodes.length || !form.kind" @click="submitDispatch">
          下发到 {{ form.nodes.length }} 个节点
        </el-button>
      </template>
    </el-dialog>

    <!-- 下发结果：逐节点给结论（部分成功是常态） -->
    <el-dialog v-model="batchVisible" title="批量下发结果" width="700px">
      <div v-if="batchResult">
        <el-alert
          :type="batchResult.failed === 0 ? 'success' : batchResult.created > 0 ? 'warning' : 'error'"
          :closable="false"
          show-icon
          class="alert-gap"
          :title="`批次 ${batchResult.batchId}：目标 ${batchResult.total} 台，已下发 ${batchResult.created} 台，未下发 ${batchResult.failed} 台`"
          :description="batchResult.created > 0 ? '每一台的结果见下方；未下发的那些给出了具体原因。' : '没有任何一台可以下发，原因见下方。'"
        />
        <div class="sec"><span>已下发（{{ batchResult.created }}）</span><span class="muted">等待节点领取，可在此列表里取消</span></div>
        <div v-for="it in batchResult.okItems" :key="it.node" class="result-row">
          <span class="mono">{{ it.node }}</span>
          <el-tag size="small" type="success">{{ it.taskId }}</el-tag>
        </div>
        <template v-if="batchResult.failedItems.length">
          <div class="sec"><span>未下发（{{ batchResult.failedItems.length }}）</span><span class="muted">逐台原因</span></div>
          <div v-for="it in batchResult.failedItems" :key="it.node" class="result-row">
            <span class="mono">{{ it.node }}</span>
            <span class="warn-text">{{ it.error }}</span>
          </div>
        </template>
      </div>
      <template #footer>
        <el-button @click="batchVisible = false">关闭</el-button>
        <el-button v-if="batchResult && batchResult.created > 0" type="warning" @click="cancelBatch">撤回本批未下发的任务</el-button>
        <el-button type="primary" @click="viewBatch">查看本批任务</el-button>
      </template>
    </el-dialog>

    <!-- 执行结果：按分节展示（正文限高见文件末尾的非 scoped 样式） -->
    <el-dialog v-model="resultVisible" :title="resultTitle" width="760px" top="5vh" class="ops-result-dialog">
      <div v-if="resultTask">
        <div class="d-meta">
          <span class="mono">{{ resultTask.node }}</span> · {{ resultTask.kind }} ·
          状态 {{ stateLabel(resultTask.state) }} ·
          <template v-if="resultTask.durationMs">耗时 {{ resultTask.durationMs }} ms · </template>
          <span v-if="resultTask.batchId">批次 {{ resultTask.batchId }}</span>
        </div>
        <el-alert
          v-if="resultTask.message"
          :type="resultTask.state === 'succeeded' ? 'success' : resultTask.state === 'failed' ? 'error' : 'info'"
          :closable="false"
          show-icon
          class="alert-gap"
          :title="resultTask.message"
        />
        <!-- 每个分节 = 标题行 + 该节输出原文。两者必须在**同一个 v-for 里**：
             此前 `<pre>` 落在 v-for 之外，`sec` 成了未定义变量，弹窗一渲染就抛
             TypeError（"Cannot read properties of undefined"），表现是"点了没反应"，
             控制台之外看不到任何线索。 -->
        <template v-if="resultSections.length">
          <div v-for="sec in resultSections" :key="sec.key" class="sec-block">
            <div class="sec">
              <span>{{ sec.key }}</span>
              <span class="muted">命令输出原文</span>
            </div>
            <pre class="out">{{ sec.value }}</pre>
          </div>
        </template>
        <el-empty v-else description="暂无输出（任务可能还在等待节点领取或执行）" />
      </div>
    </el-dialog>
  </section>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { cancelOpsTasks, createOpsBatch, deleteOpsTask, listOpsActions, listOpsTasks } from '../api/ops'
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
  { value: 'cancelled', label: '已取消' },
]
const STATE_LABELS = Object.fromEntries(STATE_OPTIONS.map((s) => [s.value, s.label]))
const stateLabel = (s) => STATE_LABELS[s] || s
// 状态 → 配色类（排队/下发/执行中是不定性，成功/失败/超时/取消是终态）
const STATE_CLASSES = {
  queued: 'queued', delivered: 'queued', running: 'queued',
  succeeded: 'online', failed: 'missing', expired: 'archived', cancelled: 'archived',
}
const stateClass = (s) => STATE_CLASSES[s] || 'archived'
const TERMINAL = ['succeeded', 'failed', 'expired', 'cancelled']
const isTerminal = (s) => TERMINAL.includes(s)

const tasks = ref([])
const nodes = ref([])
const groups = ref([])
const loading = ref(false)
const loadError = ref('')
const filter = ref({ node: '', state: '', batchId: '' })

const dispatchVisible = ref(false)
const submitting = ref(false)
const actions = ref([])
const support = ref({}) // node → 该节点放行的动作（批量：一次查全部选中节点）
const capsLoaded = ref(false)
const form = reactive({ nodes: [], kind: '', params: {}, reason: '' })

const batchVisible = ref(false)
const batchResult = ref(null)

const resultVisible = ref(false)
const resultTask = ref(null)

let timer = null
// 只有在存在未结束任务时才轮询：终态任务不会再变，常驻轮询只是白烧请求。
const autoRefresh = computed(() => tasks.value.some((t) => !isTerminal(t.state)))

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
    const res = await http.get('/api/v1/nodes')
    nodes.value = res.nodes || []
  } catch (e) {
    nodes.value = []
  }
  try {
    groups.value = (await http.get('/api/v1/groups')).groups || []
  } catch (e) {
    groups.value = []
  }
}

async function reload() {
  loading.value = true
  loadError.value = ''
  try {
    const res = await listOpsTasks({
      node: filter.value.node, state: filter.value.state, batchId: filter.value.batchId, limit: 100,
    })
    tasks.value = res.tasks || []
  } catch (e) {
    loadError.value = e.message || '加载操作任务失败'
    tasks.value = []
  } finally {
    loading.value = false
  }
}

function filterByBatch(batchId) {
  filter.value.batchId = batchId
  reload()
}
function clearBatchFilter() {
  filter.value.batchId = ''
  reload()
}
function groupNodeCount(group) {
  return nodes.value.filter((n) => n.group === group && n.status === 'online').length
}
function selectGroup(group) {
  const picked = nodes.value.filter((n) => n.group === group && n.status === 'online').map((n) => n.hostname)
  const merged = new Set([...form.nodes, ...picked])
  form.nodes = [...merged]
  onNodesChange()
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

// actionCoverage 说明"这个动作有多少比例的目标节点放行"——
// 批量下发时最需要的信息，否则用户只能在结果里一台台看失败原因。
function actionCoverage(a) {
  if (!form.nodes.length || !capsLoaded.value) return ''
  const n = form.nodes.filter((node) => (support.value[node] || []).includes(a.kind)).length
  return `${n}/${form.nodes.length} 台放行`
}
function actionUsable(a) {
  if (!capsLoaded.value || !form.nodes.length) return true // 未加载完不灰化，避免闪现全灰
  return form.nodes.some((node) => (support.value[node] || []).includes(a.kind))
}
const anyUsable = computed(() => actions.value.some((a) => actionUsable(a)))

function kindSummary(row) {
  const params = row.params || {}
  const keys = Object.keys(params)
  if (!keys.length) return row.kind
  return keys.map((k) => `${k}=${params[k]}`).join(' ')
}

async function openDispatch() {
  form.nodes = filter.value.node ? [filter.value.node] : []
  form.kind = ''
  form.params = {}
  form.reason = ''
  capsLoaded.value = false
  support.value = {}
  dispatchVisible.value = true
  await Promise.all([loadActions(), loadCatalog()])
}

// 动作目录只需拉一次（与节点无关）
async function loadCatalog() {
  if (actions.value.length) return
  try {
    const res = await listOpsActions('')
    actions.value = res.actions || []
  } catch (e) {
    ElMessage.error(e.message || '加载动作目录失败')
  }
}

// 选中节点变化 → 一次查回所有目标节点的放行清单（N 个节点一次请求）
async function loadActions() {
  capsLoaded.value = false
  if (!form.nodes.length) {
    support.value = {}
    capsLoaded.value = true
    return
  }
  try {
    const res = await listOpsActions('', form.nodes)
    support.value = res.nodeCaps || {}
  } catch (e) {
    support.value = {}
    ElMessage.error(e.message || '查询节点放行情况失败')
  } finally {
    capsLoaded.value = true
  }
}
function onNodesChange() {
  loadActions()
  // 换节点后原动作可能一台都不放行 → 清掉，避免提交后一片失败
  if (form.kind && !actionUsable(selectedAction.value)) {
    form.kind = ''
    form.params = {}
  }
}

function onActionChange() {
  form.params = {}
  for (const p of paramSpecs.value) form.params[p.name] = ''
}

async function submitDispatch() {
  const a = selectedAction.value
  if (!a || !form.nodes.length) return
  const params = {}
  for (const [k, v] of Object.entries(form.params)) {
    if (v !== undefined && v !== null && v !== '') params[k] = v
  }
  const covered = form.nodes.filter((node) => (support.value[node] || []).includes(a.kind)).length
  const lines = [
    `将对 ${form.nodes.length} 个节点下发「${a.title}」${a.readOnly ? '（只读）' : '——这会改变这些机器的运行状态'}`,
  ]
  if (covered < form.nodes.length) {
    lines.push(`其中只有 ${covered} 台放行了该动作，其余会被跳过（逐台给出原因）`)
  }
  try {
    await ElMessageBox.confirm(lines.join('；'), '确认下发操作', {
      type: a.readOnly ? 'info' : 'warning', confirmButtonText: '下发', cancelButtonText: '取消',
    })
  } catch (e) {
    return
  }
  submitting.value = true
  try {
    const res = await createOpsBatch({ nodes: form.nodes, kind: a.kind, params, reason: form.reason })
    const b = res.batch || {}
    batchResult.value = {
      ...b,
      okItems: (b.items || []).filter((i) => i.ok),
      failedItems: (b.items || []).filter((i) => !i.ok),
    }
    dispatchVisible.value = false
    batchVisible.value = true
    await reload()
  } catch (e) {
    // 服务端的说明就是给用户看的下一步（去升级 Agent / 去改目标机器的 guards.ops），原样展示
    ElMessage.error(e.message || '下发失败')
    await loadActions()
  } finally {
    submitting.value = false
  }
}

async function viewBatch() {
  batchVisible.value = false
  if (batchResult.value && batchResult.value.batchId) filterByBatch(batchResult.value.batchId)
}

// 撤回本批**还没被领取**的任务：整批取消会逐条给结论，已下发的那些撤不回来。
async function cancelBatch() {
  if (!batchResult.value || !batchResult.value.batchId) return
  try {
    await ElMessageBox.confirm(
      '将撤回本批中「仍在排队」的任务；已被节点领取的无法撤回（会在结果里列出）。',
      '撤回本批任务', { type: 'warning', confirmButtonText: '撤回', cancelButtonText: '取消' },
    )
  } catch (e) {
    return
  }
  try {
    const res = await cancelOpsTasks({ batchId: batchResult.value.batchId })
    const r = res.result || {}
    if (r.failed > 0) {
      ElMessage.warning(`已撤回 ${r.cancelled} 条；${r.failed} 条无法撤回（多半已被节点领取）`)
    } else {
      ElMessage.success(`已撤回 ${r.cancelled} 条`)
    }
    await reload()
  } catch (e) {
    ElMessage.error(e.message || '撤回失败')
  }
}

async function cancelOne(row) {
  try {
    await ElMessageBox.confirm(`撤回对 ${row.node} 的「${row.title || row.kind}」（仍在排队，尚未下发）`,
      '撤回任务', { type: 'warning', confirmButtonText: '撤回', cancelButtonText: '取消' })
  } catch (e) {
    return
  }
  try {
    const res = await cancelOpsTasks({ ids: [row.id] })
    const r = res.result || {}
    if (r.cancelled === 1) ElMessage.success('已撤回')
    else ElMessage.error((r.items && r.items[0] && r.items[0].error) || '撤回失败')
    await reload()
  } catch (e) {
    ElMessage.error(e.message || '撤回失败')
  }
}

async function removeOne(row) {
  try {
    await ElMessageBox.confirm(`删除这条操作记录（${row.node} · ${row.title || row.kind}）？审计里仍保留"谁删了什么"。`,
      '删除记录', { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' })
  } catch (e) {
    return
  }
  try {
    await deleteOpsTask(row.id)
    ElMessage.success('已删除')
    await reload()
  } catch (e) {
    ElMessage.error(e.message || '删除失败')
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
  align-items: center;
  margin-bottom: 12px;
}
.node-picker {
  display: flex;
  gap: 8px;
  align-items: center;
  width: 100%;
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
.act-count {
  margin-left: auto;
  color: var(--text-dim);
  font-size: 12px;
}
.result-row {
  display: flex;
  gap: 10px;
  align-items: baseline;
  padding: 3px 0;
  font-size: 13px;
}
.warn-text {
  color: var(--warn);
}
/* 分节 = 标题行（.sec）+ 输出块（.out）。.sec-block 只是成组的包裹元素，
   不留样式：标题行自己已有上下外边距（批量结果弹窗里也用同一个 .sec 当分节头），
   这里再加一层间距会让两处观感不一致。 */
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

<!-- 弹窗被 Element Plus 传送到 body，scoped 选择器匹配不到，因此这里**不加 scoped**，
   用弹窗自带的类名做命名空间。 -->
<style>
/* 执行结果弹窗限高：诊断包有 7 个分节，每节输出（uname / df -h / ps / ss）可能几十行，
   不限高时弹窗会比屏幕还高——标题与「状态 · 耗时 · 批次」那行被顶出视野后，
   用户就说不清自己正在看哪条任务的结果了。正文自己滚动，标题与摘要始终可见。 */
.ops-result-dialog .el-dialog__body {
  max-height: 62vh;
  overflow-y: auto;
}
</style>
