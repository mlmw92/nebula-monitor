<template>
  <div class="templates-view">
    <div class="page-header">
      <div class="header-left">
        <h2 class="page-title">采集项模板</h2>
        <p class="page-desc">
          「拉取 HTTP 端点 → 映射成指标」的中间件，只写模板不改代码即可接入；
          模板由 Server 统一存管并按节点分组下发给 Agent（改完一个采集周期内生效，无需重启）
        </p>
      </div>
      <el-button v-if="canWrite" type="primary" @click="openCreate">新建模板</el-button>
    </div>

    <el-alert
      type="info"
      :closable="false"
      show-icon
      class="mech-tip"
      title="下发机制"
      description="模板随 Agent 上报响应下发：Agent 声明支持并回执已生效版本号，Server 仅在版本不一致时携带。只下发给 groups 命中的节点；下发内容会替换该节点 agent.yaml 里的本机模板（首次接管时 Agent 日志会明确提示）。"
    />

    <div class="chart-section glass">
      <div class="section-title">模板列表</div>
      <el-table :data="rows" style="width: 100%" v-loading="loading">
        <el-table-column prop="id" label="ID" width="130">
          <template #default="{ row }">
            <span class="mono">{{ row.id }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="title" label="展示名" min-width="140" />
        <el-table-column label="类型" width="170">
          <template #default="{ row }"><el-tag size="small">{{ row.kind }}</el-tag></template>
        </el-table-column>
        <el-table-column label="生效分组" min-width="160">
          <template #default="{ row }">
            <el-tag v-for="g in row.groups || []" :key="g" size="small" class="ch-tag">{{ g }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="目标数" width="90" align="center">
          <template #default="{ row }">{{ (row.targets || []).length }}</template>
        </el-table-column>
        <el-table-column label="采集情况" min-width="220">
          <template #default="{ row }">
            <template v-if="row.stat && row.stat.total > 0">
              <span class="metric-good">{{ row.stat.up }} 在线</span>
              <span v-if="row.stat.down > 0" class="metric-bad"> / {{ row.stat.down }} 失败</span>
            </template>
            <el-tag v-else size="small" type="warning" effect="plain">
              已配置但无数据
            </el-tag>
            <!-- 护栏类取数方式要在各机器自己的 agent.yaml 里放行；未放行的节点收不到该模板。
                 不把「有多少节点没放行」摆出来，用户只能看到「配了却没数据」 -->
            <el-tooltip v-if="row.ineffectiveNodes" placement="top" content="这些节点没有在 agent.yaml 的 templateGuards 里放行该取数方式，因此收不到此模板（各机器需自行放行）">
              <el-tag size="small" type="danger" effect="plain" class="ch-tag">
                {{ row.ineffectiveNodes }} 个节点未放行
              </el-tag>
            </el-tooltip>
          </template>
        </el-table-column>
        <el-table-column v-if="canWrite" label="操作" width="150" fixed="right">
          <template #default="{ row }">
            <el-button link @click="openEdit(row)">编辑</el-button>
            <el-button link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
      <el-empty
        v-if="!loading && rows.length === 0"
        description="还没有模板。需要接入 exporter / 自研接口型中间件时，新建一个模板即可（无需改代码）。"
      />
    </div>

    <!-- 新建 / 编辑 -->
    <el-dialog v-model="showDialog" :title="editing ? `编辑模板 ${form.id}` : '新建采集项模板'" width="680px">
      <el-form :model="form" label-width="96px">
        <!-- 从预设创建：常见中间件（RabbitMQ / Elasticsearch / Etcd / ClickHouse / ZooKeeper）
             的取数与映射规则已逐一核对过真实 exporter 的输出形态，避免「不知道 keep 该怎么写」 -->
        <el-form-item v-if="!editing" label="从预设">
          <el-select
            v-model="presetId"
            clearable
            placeholder="选择常见中间件，自动填好取数与映射规则（仍需选择生效分组）"
            style="width: 100%"
            @change="applyPreset"
          >
            <el-option v-for="p in presets" :key="p.id" :label="p.title" :value="p.id">
              <span>{{ p.title }}</span>
              <span class="opt-desc">{{ p.desc }}</span>
            </el-option>
          </el-select>
          <div v-if="presetNote" class="hint-inline">{{ presetNote }}</div>
        </el-form-item>
        <el-form-item label="ID">
          <el-input v-model="form.id" :disabled="editing" placeholder="小写字母开头，如 rabbitmq；它同时是指标名前缀" />
          <div class="hint-inline">
            不得与既有指标族前缀冲突（redis_ / mysql_ / k8s_ 等）或与其它模板互为前缀；保存时校验
          </div>
        </el-form-item>
        <el-form-item label="展示名">
          <el-input v-model="form.title" placeholder="如 RabbitMQ（用于 Tab 与卡片展示）" />
        </el-form-item>
        <el-form-item label="类型">
          <el-select v-model="form.kind" style="width: 320px">
            <el-option label="prometheus-exporter（Prometheus 文本）" value="prometheus-exporter" />
            <el-option label="http-json（JSON 路径取值）" value="http-json" />
            <el-option label="http-text（正则抓取）" value="http-text" />
            <el-option label="jdbc（数据库只读查询）" value="jdbc" />
            <el-option label="exec（本机执行命令）" value="exec" />
            <el-option label="file（读取本机文件）" value="file" />
          </el-select>
          <div class="hint-inline">{{ kindHint }}</div>
        </el-form-item>
        <el-form-item label="生效分组">
          <el-select
            v-model="form.groups"
            multiple
            filterable
            allow-create
            default-first-option
            placeholder="选择或输入节点分组（必填）"
            style="width: 100%"
          >
            <el-option v-for="g in knownGroups" :key="g" :label="g" :value="g" />
          </el-select>
          <div class="hint-inline">
            只下发给这些分组的节点。留空会在所有节点上产出 template_target_up=0，故必填
          </div>
        </el-form-item>

        <el-form-item label="采集目标">
          <!-- 本机/数据库取数类（jdbc/exec/file）的目标字段较多（command/args/path/database/auth…），
               与规则区同样的取舍：能结构化的用表单，表达力强的用 JSON + 服务端校验 -->
          <template v-if="isLocalKind">
            <el-input v-model="targetsText" type="textarea" :rows="6" spellcheck="false" :placeholder="targetsPlaceholder" />
            <div v-if="authNote" class="hint-inline">{{ authNote }}</div>
            <div class="hint-inline">
              凭据（auth）不会回显：留空即保留原凭据；要换密码就整段重写。
              改了 instance / addr / command / path 会被视为换了一个目标，原凭据不再沿用。
            </div>
          </template>
          <div v-else class="targets">
            <div v-for="(t, i) in form.targets" :key="i" class="target-row">
              <el-input v-model="t.instance" placeholder="实例标识（留空取地址 host:port）" style="width: 220px" />
              <el-input v-model="t.addr" placeholder="http://host:port/path" style="flex: 1" />
              <el-button link type="danger" @click="form.targets.splice(i, 1)">移除</el-button>
            </div>
            <el-button link @click="form.targets.push({ instance: '', addr: '' })">+ 添加目标</el-button>
          </div>
        </el-form-item>

        <el-form-item label="映射规则">
          <el-input
            v-model="rulesText"
            type="textarea"
            :rows="8"
            spellcheck="false"
            placeholder='JSON 格式，例如 prometheus-exporter：
{
  "keep": "^rabbitmq_",
  "drop": "_bucket$|_sum$|_count$",
  "rename": [{ "match": "^rabbitmq_queue_messages$", "to": "rabbitmq_queue_depth" }],
  "labels": { "cluster": "prod" },
  "unlabel": ["job"],
  "aggregate": [{ "match": "^rabbitmq_queue_messages$", "op": "sum" }],
  "promoteLabel": [{ "match": "^rabbitmq_queue_messages$", "label": "queue" }]
}
aggregate：丢掉维度标签后按 sum/max/min/avg 合并同名序列（不声明聚合却出现重复序列时，只保留第一条并告警）
promoteLabel：把标签取值提升为指标名的一部分（Nacos 的 nacos_monitor{name="..."} 就靠它拆成独立指标名）
http-json / http-text 需给出 metrics（path 或 pattern，可带 label/unit 供展示）'
          />
          <div class="hint-inline">规则较丰富，这里用 JSON 直接写；保存前点「校验」可拿到与保存一致的精确原因</div>
        </el-form-item>

        <el-form-item v-if="validationErrors.length > 0" label=" ">
          <el-alert type="error" :closable="false" title="校验未通过">
            <ul class="err-list">
              <li v-for="(msg, i) in validationErrors" :key="i">{{ msg }}</li>
            </ul>
          </el-alert>
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button @click="showDialog = false">取消</el-button>
        <el-button @click="validate" :loading="validating">校验</el-button>
        <el-button type="primary" @click="save" :loading="saving">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import http from '../../api/http'
import { useAuth } from '../../composables/useAuth'

const auth = useAuth()
const canWrite = computed(() => auth.can('middleware:write'))

const loading = ref(false)
const saving = ref(false)
const validating = ref(false)
const rows = ref([])
const knownGroups = ref([])
const showDialog = ref(false)
const editing = ref(false)
const validationErrors = ref([])
const rulesText = ref('')
const presets = ref([])
const presetId = ref('')
const presetNote = ref('')

// kindHint 按类型给出「该配哪些字段」与前置条件。
// 三类本机/数据库取数（jdbc/exec/file）会以 root 触碰被监控机或携带库凭据，
// 因此**必须在目标机器的 agent.yaml 里通过 templateGuards 放行**——
// 这一点不写出来，用户就会陷入「模板建好了却一直没有数据」的排查。
const KIND_HINTS = {
  'prometheus-exporter': '拉取 Prometheus 文本端点；指标名直接来自响应，用 keep/drop/rename 收窄。',
  'http-json': '拉取 JSON 接口；每条指标给 path（如 a.b[0].c）。',
  'http-text': '拉取纯文本页面；每条指标给 pattern（取第 1 个捕获组）。',
  jdbc: '连库执行只读查询：需 driver（mysql/postgres）+ 目标 addr(host:port)/database 与库账号；每条指标给 query（只允许 SELECT/SHOW/EXPLAIN 单条语句）。需在目标机器的 templateGuards.jdbc 中放行。',
  exec: '在本机执行命令：需目标的 command（绝对路径）与可选 args，每条指标给 pattern。argv 直传、不经 shell；需在目标机器的 templateGuards.exec.allow 中精确列出该命令。',
  file: '读取本机文件末尾：需目标的 path（绝对路径），每条指标给 pattern。需在目标机器的 templateGuards.file.allow 中精确列出该路径（软链按实际指向校验）。',
}
const kindHint = computed(() => KIND_HINTS[form.value.kind] || '')

// 本机/数据库取数类：目标字段较多，表单换成 JSON 编辑（见模板里对应的 v-if）
const isLocalKind = computed(() => ['jdbc', 'exec', 'file'].includes(form.value.kind))
const targetsText = ref('')
const authNote = ref('')

const TARGETS_PLACEHOLDER = {
  jdbc:
    '[\n  { "instance": "biz-db-01", "addr": "10.0.0.5:3306", "database": "appdb",\n' +
    '    "auth": { "basic": { "user": "monitor", "password": "enc:xxxx" } } }\n]',
  exec:
    '[\n  { "instance": "cache-01", "command": "/usr/local/bin/redis-cli",\n' +
    '    "args": ["-h", "127.0.0.1", "INFO"], "timeoutSec": 5 }\n]',
  file: '[\n  { "instance": "app-01", "path": "/var/lib/myapp/metrics.txt" }\n]',
}
const targetsPlaceholder = computed(() => TARGETS_PLACEHOLDER[form.value.kind] || '[]')

// stripAuthFlag 去掉读视图里的 hasAuth 提示位：它是展示用的，不该被回传（服务端也不认这个字段）
function stripAuthFlag(list) {
  return (list || []).map((t) => {
    const copy = { ...t }
    delete copy.hasAuth
    return copy
  })
}

// collectTargets 返回提交用的目标列表：本机取数类从 JSON 文本解析，其余取表单值。
// 解析失败返回 null（调用方提示并中止），绝不把半截数据发出去。
function collectTargets() {
  if (!isLocalKind.value) {
    return form.value.targets
      .filter((t) => (t.addr || '').trim() !== '')
      .map((t) => ({ instance: t.instance.trim(), addr: t.addr.trim() }))
  }
  try {
    const parsed = JSON.parse(targetsText.value.trim() || '[]')
    if (!Array.isArray(parsed)) {
      ElMessage.error('采集目标必须是 JSON 数组')
      return null
    }
    return parsed
  } catch (e) {
    ElMessage.error('采集目标不是合法 JSON：' + e.message)
    return null
  }
}

// applyPreset 用预设填好表单：不覆盖生效分组（分组取决于用户环境，预设刻意留空）。
function applyPreset(id) {
  const p = presets.value.find((x) => x.id === id)
  if (!p) {
    presetNote.value = ''
    return
  }
  const c = p.config || {}
  form.value.id = c.id || ''
  form.value.title = c.title || ''
  form.value.kind = c.kind || 'prometheus-exporter'
  form.value.targets = (c.targets || []).map((t) => ({ instance: t.instance || '', addr: t.addr || '' }))
  if (form.value.targets.length === 0) form.value.targets.push({ instance: '', addr: '' })
  // 预设目前都是网络取数类；若将来加了本机取数类的预设，这里要同步 JSON 文本
  targetsText.value = isLocalKind.value ? JSON.stringify(c.targets || [], null, 2) : ''
  form.value.rules = c.rules || {}
  rulesText.value = JSON.stringify(c.rules || {}, null, 2)
  presetNote.value = p.note || ''
  validationErrors.value = []
}

const emptyForm = () => ({
  id: '',
  title: '',
  kind: 'prometheus-exporter',
  groups: [],
  targets: [{ instance: '', addr: '' }],
  rules: {},
})
const form = ref(emptyForm())

async function load() {
  loading.value = true
  try {
    // 模板列表与「各类型的采集情况」都取一次：后者用于把「已配置但无数据」直接摆在列表里
    const [tplData, ovData] = await Promise.all([
      http.get('/api/v1/middleware/templates'),
      http.get('/api/v1/middleware/overview'),
    ])
    const statByType = {}
    for (const t of ovData.types || []) {
      statByType[t.type] = { total: t.total, up: t.up, down: t.down }
    }
    rows.value = (tplData.templates || []).map((t) => ({ ...t, stat: statByType[t.id] }))
  } catch (e) {
    console.error('加载模板失败', e)
  } finally {
    loading.value = false
  }
}

async function loadGroups() {
  try {
    const data = await http.get('/api/v1/groups')
    knownGroups.value = (data.groups || []).map((g) => (typeof g === 'string' ? g : g.name)).filter(Boolean)
  } catch (e) {
    console.error('加载节点分组失败', e)
  }
}

async function loadPresets() {
  try {
    const data = await http.get('/api/v1/middleware/templates/presets')
    presets.value = data.presets || []
  } catch (e) {
    // 预设是便利功能：取不到时仍可手工填写，不影响建模板
    console.error('加载模板预设失败', e)
  }
}

function openCreate() {
  editing.value = false
  form.value = emptyForm()
  rulesText.value = ''
  targetsText.value = ''
  authNote.value = ''
  presetId.value = ''
  presetNote.value = ''
  validationErrors.value = []
  showDialog.value = true
}

function openEdit(row) {
  editing.value = true
  const targets = stripAuthFlag(row.targets)
  form.value = {
    id: row.id,
    title: row.title || '',
    kind: row.kind,
    groups: [...(row.groups || [])],
    targets,
    rules: row.rules || {},
  }
  rulesText.value = JSON.stringify(row.rules || {}, null, 2)
  // 凭据不回显：明说一句，否则用户会以为凭据丢了、甚至重新手填一遍
  const withAuth = (row.targets || []).filter((t) => t.hasAuth).length
  authNote.value = withAuth > 0 ? `已有 ${withAuth} 个目标配置了凭据（服务端不回显；留空即保留原凭据）` : ''
  targetsText.value = isLocalKind.value ? JSON.stringify(targets, null, 2) : ''
  validationErrors.value = []
  showDialog.value = true
}

// buildPayload 组装请求体；规则 JSON 非法时返回 null 并提示
function buildPayload() {
  let rules = {}
  if (rulesText.value.trim() !== '') {
    try {
      rules = JSON.parse(rulesText.value)
    } catch (e) {
      ElMessage.error('映射规则不是合法 JSON：' + e.message)
      return null
    }
  }
  const targets = collectTargets()
  if (!targets) return null
  return {
    id: form.value.id.trim(),
    title: form.value.title.trim(),
    kind: form.value.kind,
    groups: form.value.groups,
    targets,
    rules,
  }
}

async function validate() {
  const payload = buildPayload()
  if (!payload) return
  validating.value = true
  validationErrors.value = []
  try {
    const res = await http.post('/api/v1/middleware/templates/validate', payload)
    if (res.ok) {
      ElMessage.success('校验通过')
    } else {
      validationErrors.value = res.errors || ['未通过校验']
    }
  } catch (e) {
    validationErrors.value = [e.message]
  } finally {
    validating.value = false
  }
}

async function save() {
  const payload = buildPayload()
  if (!payload) return
  saving.value = true
  validationErrors.value = []
  try {
    if (editing.value) {
      await http.put(`/api/v1/middleware/templates/${encodeURIComponent(payload.id)}`, payload)
    } else {
      await http.post('/api/v1/middleware/templates', payload)
    }
    showDialog.value = false
    ElMessage.success('已保存，将在下一个采集周期内下发给命中分组的 Agent')
    await load()
  } catch (e) {
    // 后端把校验原因放在 error 字段（http.js 已取出），直接展示，避免「保存失败再猜」
    validationErrors.value = [e.message]
  } finally {
    saving.value = false
  }
}

async function remove(row) {
  if (!confirm(`确认删除模板 "${row.id}"？命中分组的 Agent 将在下个采集周期停止该模板采集。`)) return
  try {
    await http.del(`/api/v1/middleware/templates/${encodeURIComponent(row.id)}`)
    ElMessage.success('已删除')
    await load()
  } catch (e) {
    ElMessage.error(e.message)
  }
}

onMounted(async () => {
  await Promise.all([load(), loadGroups(), loadPresets()])
})
</script>

<style scoped>
.templates-view { padding: 4px 0 16px; }
.page-header { display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 16px; }
.page-title {
  font-size: 22px;
  font-weight: 700;
  letter-spacing: -0.01em;
  background: linear-gradient(135deg, var(--text) 0%, var(--text-dim) 100%);
  -webkit-background-clip: text;
  -webkit-text-fill-color: transparent;
  background-clip: text;
}
.page-desc { font-size: 13px; color: var(--text-dim); margin-top: 4px; max-width: 900px; }
.mech-tip { margin-bottom: 12px; }
.chart-section { padding: 16px; border-radius: var(--radius); }
.section-title { font-size: 14px; font-weight: 600; margin-bottom: 12px; }
.mono { font-family: var(--mono, ui-monospace, monospace); }
.ch-tag { margin-right: 4px; }
.metric-good { color: var(--success, #4caf50); }
.metric-bad { color: var(--danger); }
.hint-inline { font-size: 12px; color: var(--text-muted); margin-top: 4px; line-height: 1.5; }
.targets { width: 100%; }
.target-row { display: flex; gap: 8px; align-items: center; margin-bottom: 8px; }
.err-list { margin: 0; padding-left: 18px; font-size: 13px; line-height: 1.7; }
.opt-desc { float: right; color: var(--text-muted); font-size: 12px; margin-left: 12px; }
</style>
