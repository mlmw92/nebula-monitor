<template>
  <el-card class="settings-card" shadow="never">
    <template #header>
      <div class="card-head">
        <div>
          <span class="title">告警事件管道</span>
          <span class="sub">告警派发前改写标签、补充字段并按渠道渲染通知内容；保存后即时生效</span>
        </div>
        <div class="head-actions">
          <el-button :disabled="saving || loading" @click="load">重新加载</el-button>
          <el-button type="primary" :loading="saving" :disabled="loading" @click="save">保存</el-button>
        </div>
      </div>
    </template>

    <el-alert type="info" :closable="false" show-icon class="tip">
      执行顺序：先按「标签重写」「标签增补」变换事件标签，再按渠道匹配「消息模板」渲染通知正文。
      内置标签为 name / rule / node / instance / severity / metric；未命中模板或模板渲染失败时，
      一律回退系统内置描述，不会丢失告警。
    </el-alert>

    <!-- 1. 标签重写 -->
    <div class="section">
      <div class="section-head">
        <span class="section-title">标签重写</span>
        <el-button size="small" @click="addRelabel">添加规则</el-button>
      </div>
      <div class="field-hint">
        「条件」留空表示无条件生效；填 <code>k=v,k2=v2</code> 表示仅当这些标签相等时才应用。
      </div>
      <el-table :data="form.relabels" size="small" border empty-text="暂无规则">
        <el-table-column label="操作" width="120">
          <template #default="{ row }">
            <el-select v-model="row.op" size="small">
              <el-option v-for="o in OPS" :key="o.value" :label="o.label" :value="o.value" />
            </el-select>
          </template>
        </el-table-column>
        <el-table-column label="源标签" width="150">
          <template #default="{ row }">
            <el-input v-model="row.source" size="small" placeholder="如 node" />
          </template>
        </el-table-column>
        <el-table-column label="目标标签" width="150">
          <template #default="{ row }">
            <el-input v-model="row.target" size="small" placeholder="如 host" />
          </template>
        </el-table-column>
        <el-table-column label="正则 / 替换为 / 值">
          <template #default="{ row }">
            <div class="triple">
              <el-input
                v-model="row.pattern"
                size="small"
                placeholder="正则（仅替换）"
                :disabled="row.op !== 'replace'"
              />
              <el-input
                v-model="row.replace"
                size="small"
                placeholder="替换为，如 CN-$1"
                :disabled="row.op !== 'replace'"
              />
              <el-input
                v-model="row.value"
                size="small"
                placeholder="置值（仅置值）"
                :disabled="row.op !== 'set'"
              />
            </div>
          </template>
        </el-table-column>
        <el-table-column label="条件" width="190">
          <template #default="{ row }">
            <el-input
              :model-value="whenText(row.when)"
              size="small"
              placeholder="env=prod"
              @update:model-value="(v) => setWhenText(row, v)"
            />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="64" align="center">
          <template #default="{ $index }">
            <el-button type="danger" link :icon="Delete" @click="form.relabels.splice($index, 1)" />
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 2. 标签增补 -->
    <div class="section">
      <div class="section-head">
        <span class="section-title">标签增补</span>
        <el-button size="small" @click="addEnrich">添加规则</el-button>
      </div>
      <div class="field-hint">按条件向事件注入固定标签，常用于补充 team / owner / env 等归属信息。</div>
      <el-table :data="form.enrich" size="small" border empty-text="暂无规则">
        <el-table-column label="目标标签" width="180">
          <template #default="{ row }">
            <el-input v-model="row.target" size="small" placeholder="如 team" />
          </template>
        </el-table-column>
        <el-table-column label="注入值" width="220">
          <template #default="{ row }">
            <el-input v-model="row.value" size="small" placeholder="如 sre" />
          </template>
        </el-table-column>
        <el-table-column label="条件">
          <template #default="{ row }">
            <el-input
              :model-value="whenText(row.when)"
              size="small"
              placeholder="env=prod（留空表示无条件）"
              @update:model-value="(v) => setWhenText(row, v)"
            />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="64" align="center">
          <template #default="{ $index }">
            <el-button type="danger" link :icon="Delete" @click="form.enrich.splice($index, 1)" />
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 3. 消息模板 -->
    <div class="section">
      <div class="section-head">
        <span class="section-title">消息模板</span>
        <el-button size="small" @click="addTemplate">添加模板</el-button>
      </div>
      <div class="field-hint">
        模板为 Go text/template 语法，可访问事件字段（<code v-pre>{{.RuleName}}</code>、
        <code v-pre>{{.Node}}</code>、<code v-pre>{{.Value}}</code>）与标签
        （<code v-pre>{{index .Labels "team"}}</code>）。渠道专属模板优先于「全部渠道」。
      </div>
      <el-table :data="form.templates" size="small" border empty-text="暂无模板（使用系统内置描述）">
        <el-table-column label="名称" width="150">
          <template #default="{ row }">
            <el-input v-model="row.name" size="small" placeholder="如 钉钉精简版" />
          </template>
        </el-table-column>
        <el-table-column label="渠道" width="140">
          <template #default="{ row }">
            <el-select v-model="row.channel" size="small">
              <el-option v-for="c in CHANNELS" :key="c.value" :label="c.label" :value="c.value" />
            </el-select>
          </template>
        </el-table-column>
        <el-table-column label="级别" width="200">
          <template #default="{ row }">
            <el-select v-model="row.severity" size="small" multiple collapse-tags placeholder="全部级别">
              <el-option v-for="s in SEVERITIES" :key="s.value" :label="s.label" :value="s.value" />
            </el-select>
          </template>
        </el-table-column>
        <el-table-column label="规则 ID（逗号分隔，支持前缀）" width="200">
          <template #default="{ row }">
            <el-input
              :model-value="(row.ruleIds || []).join(',')"
              size="small"
              placeholder="留空=全部规则"
              @update:model-value="(v) => (row.ruleIds = splitList(v))"
            />
          </template>
        </el-table-column>
        <el-table-column label="模板内容">
          <template #default="{ row }">
            <el-input
              v-model="row.template"
              type="textarea"
              :rows="3"
              class="mono"
              :placeholder="templatePlaceholder"
            />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="64" align="center">
          <template #default="{ $index }">
            <el-button type="danger" link :icon="Delete" @click="form.templates.splice($index, 1)" />
          </template>
        </el-table-column>
      </el-table>
    </div>

    <el-divider />

    <!-- 4. 效果预览 -->
    <div class="section">
      <div class="section-head">
        <span class="section-title">效果预览</span>
        <div class="head-actions">
          <el-select v-model="previewChannel" size="small" style="width: 130px">
            <el-option v-for="c in CHANNELS" :key="c.value" :label="c.label || '全部渠道'" :value="c.value" />
          </el-select>
          <el-button size="small" :loading="previewing" @click="runPreview">按当前配置试算</el-button>
        </div>
      </div>
      <div class="field-hint">
        试算使用「当前表单里尚未保存的配置」，不会影响线上；样例事件可自行编辑，留空则用服务端内置样例。
      </div>
      <el-row :gutter="20">
        <el-col :xs="24" :md="11">
          <el-form-item label="样例事件（JSON）">
            <el-input v-model="sampleEventText" type="textarea" :rows="14" class="mono" />
          </el-form-item>
        </el-col>
        <el-col :xs="24" :md="13">
          <el-form-item label="变换后的标签">
            <pre class="preview-box">{{ preview.labels }}</pre>
          </el-form-item>
          <el-form-item label="渲染后的通知正文">
            <pre class="preview-box">{{ preview.message }}</pre>
          </el-form-item>
          <el-form-item label="原始描述（回退值）">
            <pre class="preview-box muted">{{ preview.originalMessage }}</pre>
          </el-form-item>
        </el-col>
      </el-row>
    </div>

    <!-- 5. 高级：JSON 直编 -->
    <el-collapse class="adv">
      <el-collapse-item name="adv">
        <template #title>
          <span class="section-title">高级：配置直编</span>
          <span class="field-hint inline">服务端以 YAML 持久化；此处编辑等价 JSON，便于批量修改与复用</span>
        </template>
        <el-input v-model="rawJson" type="textarea" :rows="14" class="mono" />
        <div class="btn-line">
          <el-button size="small" @click="applyRawJson">应用到表单</el-button>
          <el-button size="small" @click="rawJson = JSON.stringify(form, null, 2)">从表单生成</el-button>
        </div>
      </el-collapse-item>
    </el-collapse>
  </el-card>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { Delete } from '@element-plus/icons-vue'
import http from '../../api/http'

const OPS = [
  { label: '重命名', value: 'rename' },
  { label: '删除', value: 'delete' },
  { label: '正则替换', value: 'replace' },
  { label: '置值', value: 'set' },
]
const CHANNELS = [
  { label: '', value: '' },
  { label: '邮件', value: 'email' },
  { label: 'Webhook', value: 'webhook' },
  { label: '钉钉', value: 'dingtalk' },
  { label: '飞书', value: 'feishu' },
  { label: '企业微信', value: 'wecom' },
]
const SEVERITIES = [
  { label: 'Info', value: 'info' },
  { label: 'Warning', value: 'warning' },
  { label: 'Critical', value: 'critical' },
]

// 内置样例事件（与服务端 preview 的默认样例保持一致，便于离线理解字段含义）
const SAMPLE_EVENT = {
  id: 'preview-1',
  ruleId: 'rule-cpu-usage',
  ruleName: 'CPU 使用率过高',
  node: 'web-01',
  nodeIp: '10.0.0.11',
  metric: 'cpu_usage',
  value: 92.4,
  operator: '>',
  threshold: 90,
  severity: 'critical',
  state: 'firing',
  message: '主机 web-01 CPU 使用率 92.40% 超过阈值 90.00%',
  labels: { env: 'prod' },
}

// 模板输入框占位示例（字面量放在脚本里，避免与 Vue 插值定界符冲突）
const templatePlaceholder = '【{{.Severity}}】{{.RuleName}} | {{.Node}}'

const loading = ref(false)
const saving = ref(false)
const previewing = ref(false)
const form = ref({ relabels: [], enrich: [], templates: [] })
const rawJson = ref('')
const previewChannel = ref('dingtalk')
const sampleEventText = ref(JSON.stringify(SAMPLE_EVENT, null, 2))
const preview = ref({ labels: '—', message: '—', originalMessage: '—' })

// ---------- when 条件（表格内以 k=v,k2=v2 简写编辑）----------
function whenText(when) {
  const m = (when && when.match) || {}
  return Object.entries(m)
    .map(([k, v]) => `${k}=${v}`)
    .join(',')
}

function setWhenText(row, text) {
  const match = {}
  String(text || '')
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
    .forEach((pair) => {
      const i = pair.indexOf('=')
      if (i > 0) match[pair.slice(0, i).trim()] = pair.slice(i + 1).trim()
    })
  const matchRegex = (row.when && row.when.matchRegex) || {}
  if (Object.keys(match).length || Object.keys(matchRegex).length) {
    row.when = { match, matchRegex }
  } else {
    row.when = null
  }
}

function splitList(text) {
  return String(text || '')
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
}

// ---------- 行操作 ----------
function addRelabel() {
  form.value.relabels.push({ op: 'rename', source: '', target: '', pattern: '', replace: '', value: '', when: null })
}
function addEnrich() {
  form.value.enrich.push({ target: '', value: '', when: null })
}
function addTemplate() {
  form.value.templates.push({ name: '', channel: '', severity: [], ruleIds: [], template: '' })
}

// ---------- 配置归一化（提交前清理无关字段，避免服务端校验失败）----------
function normalizeWhen(when) {
  if (!when) return null
  const out = {}
  const match = {}
  Object.entries(when.match || {}).forEach(([k, v]) => {
    if (k && k.trim() && String(v).trim()) match[k.trim()] = String(v).trim()
  })
  if (Object.keys(match).length) out.match = match
  const matchRegex = {}
  Object.entries(when.matchRegex || {}).forEach(([k, v]) => {
    if (k && k.trim() && String(v).trim()) matchRegex[k.trim()] = String(v).trim()
  })
  if (Object.keys(matchRegex).length) out.matchRegex = matchRegex
  return Object.keys(out).length ? out : null
}

function normalize() {
  const cfg = { relabels: [], enrich: [], templates: [] }

  for (const r of form.value.relabels) {
    const op = (r.op || '').trim()
    if (!op) continue
    const item = { op }
    if (r.source && r.source.trim()) item.source = r.source.trim()
    if (r.target && r.target.trim()) item.target = r.target.trim()
    if (op === 'replace') {
      item.pattern = r.pattern || ''
      item.replace = r.replace || ''
    }
    if (op === 'set') item.value = r.value || ''
    const when = normalizeWhen(r.when)
    if (when) item.when = when
    cfg.relabels.push(item)
  }

  for (const e of form.value.enrich) {
    if (!e.target || !e.target.trim()) continue
    const item = { target: e.target.trim(), value: e.value || '' }
    const when = normalizeWhen(e.when)
    if (when) item.when = when
    cfg.enrich.push(item)
  }

  for (const t of form.value.templates) {
    const item = {
      name: (t.name || '').trim() || '未命名模板',
      channel: t.channel || '',
      severity: (t.severity || []).filter(Boolean),
      ruleIds: (t.ruleIds || []).filter(Boolean),
      template: t.template || '',
    }
    const when = normalizeWhen(t.when)
    if (when) item.when = when
    cfg.templates.push(item)
  }

  return cfg
}

// ---------- 加载 / 保存 ----------
function fillForm(cfg) {
  form.value = {
    relabels: (cfg.relabels || []).map((r) => ({
      op: r.op || 'rename',
      source: r.source || '',
      target: r.target || '',
      pattern: r.pattern || '',
      replace: r.replace || '',
      value: r.value || '',
      when: r.when || null,
    })),
    enrich: (cfg.enrich || []).map((e) => ({
      target: e.target || '',
      value: e.value || '',
      when: e.when || null,
    })),
    templates: (cfg.templates || []).map((t) => ({
      name: t.name || '',
      channel: t.channel || '',
      severity: t.severity || [],
      ruleIds: t.ruleIds || [],
      template: t.template || '',
    })),
  }
  rawJson.value = JSON.stringify(form.value, null, 2)
}

async function load() {
  loading.value = true
  try {
    const cfg = await http.get('/api/v1/alert-pipeline')
    fillForm(cfg || {})
  } catch (e) {
    ElMessage.error('加载告警管道配置失败：' + e.message)
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  try {
    const cfg = normalize()
    await http.put('/api/v1/alert-pipeline', cfg)
    ElMessage.success('已保存并即时生效')
    // 用服务端归一化后的结果回填，避免前端与服务端理解不一致
    fillForm(cfg)
  } catch (e) {
    ElMessage.error('保存失败：' + e.message)
  } finally {
    saving.value = false
  }
}

// ---------- 预览 ----------
async function runPreview() {
  previewing.value = true
  try {
    const payload = { config: normalize(), channel: previewChannel.value }
    if (sampleEventText.value.trim()) {
      payload.event = JSON.parse(sampleEventText.value)
    }
    const res = await http.post('/api/v1/alert-pipeline/preview', payload)
    preview.value = {
      labels: JSON.stringify(res.labels || {}, null, 2),
      message: res.message || '（空）',
      originalMessage: res.originalMessage || '（空）',
    }
  } catch (e) {
    ElMessage.error('试算失败：' + e.message)
  } finally {
    previewing.value = false
  }
}

// ---------- 高级：JSON 直编 ----------
function applyRawJson() {
  try {
    const parsed = JSON.parse(rawJson.value || '{}')
    fillForm(parsed)
    ElMessage.success('已应用到表单，确认无误后请点击保存')
  } catch (e) {
    ElMessage.error('JSON 解析失败：' + e.message)
  }
}

onMounted(load)
</script>

<style scoped>
.settings-card {
  border: 1px solid var(--border);
}
.card-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}
.card-head .title {
  font-size: 16px;
  font-weight: 600;
  color: var(--text);
}
.card-head .sub {
  display: block;
  margin-top: 4px;
  font-size: 13px;
  color: var(--text-dim);
}
.head-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
}
.tip {
  margin-bottom: 18px;
}
.section {
  margin-bottom: 22px;
}
.section-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 8px;
}
.section-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--text);
}
.field-hint {
  font-size: 12px;
  color: var(--text-muted);
  line-height: 1.6;
  margin-bottom: 8px;
}
.field-hint.inline {
  margin: 0 0 0 10px;
  display: inline;
}
.field-hint code {
  font-family: var(--mono);
  background: rgba(127, 127, 127, 0.12);
  padding: 0 4px;
  border-radius: 3px;
}
.triple {
  display: flex;
  gap: 6px;
}
.preview-box {
  margin: 0;
  padding: 10px 12px;
  min-height: 44px;
  max-height: 220px;
  overflow: auto;
  font-family: var(--mono);
  font-size: 12.5px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-all;
  background: rgba(127, 127, 127, 0.08);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text);
}
.preview-box.muted {
  color: var(--text-dim);
}
.mono :deep(textarea) {
  font-family: var(--mono);
  font-size: 12.5px;
}
.adv {
  margin-top: 4px;
}
</style>
