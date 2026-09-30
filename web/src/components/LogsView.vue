<template>
  <section class="view">
    <header class="view-head">
      <div class="head-row">
        <h2>集中日志</h2>
        <span class="muted">检索各节点上报的日志行（Agent 侧只上传 <code>logSources.patterns</code> 命中的行）</span>
      </div>
    </header>

    <div class="panel">
      <div class="toolbar">
        <el-date-picker
          v-model="range"
          type="datetimerange"
          size="small"
          range-separator="至"
          start-placeholder="开始时间"
          end-placeholder="结束时间"
          :shortcuts="shortcuts"
          style="width: 360px"
        />
        <el-select v-model="mode" size="small" style="width: 96px">
          <el-option label="关键词" value="kw" />
          <el-option label="正则" value="re" />
        </el-select>
        <el-input
          v-model="keyword"
          size="small"
          clearable
          style="width: 240px"
          :placeholder="mode === 're' ? '正则，如 disk|oom' : '子串，如 error'"
          @keyup.enter="search"
        />
        <el-select
          v-model="nodes"
          multiple
          collapse-tags
          collapse-tags-tooltip
          clearable
          placeholder="全部节点"
          size="small"
          style="width: 200px"
        >
          <el-option v-for="n in nodeNames" :key="n" :label="n" :value="n" />
        </el-select>
        <el-select
          v-model="sources"
          multiple
          collapse-tags
          collapse-tags-tooltip
          clearable
          placeholder="全部来源"
          size="small"
          style="width: 180px"
        >
          <el-option v-for="s in sourceNames" :key="s" :label="s" :value="s" />
        </el-select>
        <el-button type="primary" size="small" :loading="loading" @click="search">查询</el-button>
        <!-- 日志来源配在 Agent 侧，页面上看不到任何字段；"没数据"时最缺的就是可复制的模板 -->
        <el-button size="small" @click="exampleVisible = true">配置示例</el-button>
      </div>

      <el-alert
        v-if="loadError"
        type="error"
        :closable="false"
        show-icon
        :title="loadError"
        class="alert-gap"
      />
      <!-- 截断必须显式提示：静默截断会让人以为「日志就这么多」，从而得出错误结论 -->
      <el-alert
        v-if="truncated"
        type="warning"
        :closable="false"
        show-icon
        title="结果被截断：当前条件下还有更多日志"
        description="可以点下方「加载更多」继续翻，或缩小时间范围 / 指定节点或关键词，让结果更精确。"
        class="alert-gap"
      />
      <el-alert
        v-else-if="scannedHint"
        type="info"
        :closable="false"
        show-icon
        :title="scannedHint"
        class="alert-gap"
      />

      <el-table :data="lines" empty-text="没有命中的日志" style="width: 100%">
        <el-table-column label="时间" width="180">
          <template #default="{ row }">{{ fmtTime(row.ts) }}</template>
        </el-table-column>
        <el-table-column prop="node" label="节点" width="150" />
        <el-table-column prop="source" label="来源" width="120" />
        <el-table-column label="命中模式" width="120">
          <template #default="{ row }">
            <span v-if="row.pattern" class="tag">{{ row.pattern }}</span>
            <span v-else class="muted">—</span>
          </template>
        </el-table-column>
        <el-table-column label="日志" min-width="420">
          <template #default="{ row }">
            <!-- 默认单行省略，点击展开（多行堆栈合并后的日志需要看全） -->
            <div
              class="logline"
              :class="{ open: isOpen(row) }"
              :title="isOpen(row) ? '点击收起' : '点击展开'"
              @click="toggle(row)"
            >
              {{ row.text }}
            </div>
          </template>
        </el-table-column>
      </el-table>

      <div class="foot">
        <el-button v-if="hasMore" size="small" :loading="loadingMore" @click="loadMore">
          加载更多
        </el-button>
        <span v-else-if="lines.length" class="muted">已到末尾（本轮共 {{ lines.length }} 条）</span>
        <span v-if="!lines.length && !loading" class="muted">
          {{ emptyHint }}
          <el-button link type="primary" @click="exampleVisible = true">查看配置示例</el-button>
        </span>
      </div>
    </div>

    <!-- 配置示例：logSources 配在 Agent 侧，界面上看不到字段，是"查不到数据"时最需要的东西。
         复制走 execCommand 兜底：离线部署多为 http 源，navigator.clipboard 在非安全上下文不可用。 -->
    <el-dialog v-model="exampleVisible" title="Agent 侧 logSources 配置示例" width="760px">
      <el-alert
        type="info"
        :closable="false"
        show-icon
        title="在「被监控节点」的 /etc/monitor-agent/agent.yaml 里添加，改完重启 Agent；不配置则一条日志都不采集。"
        class="alert-gap"
      />
      <div class="ex-row">
        <el-select v-model="exampleKey" size="small" style="width: 320px">
          <el-option v-for="ex in LOG_EXAMPLES" :key="ex.key" :label="ex.label" :value="ex.key" />
        </el-select>
        <el-button size="small" type="primary" plain @click="copyExample">复制这段配置</el-button>
        <span class="muted">{{ currentLogExample.hint }}</span>
      </div>
      <pre class="ex-box">{{ currentLogExample.yaml }}</pre>
      <ul class="ex-notes">
        <li><code>id</code>：小写字母开头，只含小写字母 / 数字 / 下划线（它同时是存储分片名与指标前缀）</li>
        <li><code>patterns[].name</code>：字母或下划线开头（会拼进指标名 <code>&lt;id&gt;_log_&lt;name&gt;_total</code>）</li>
        <li><code>paths</code>：必须是绝对路径，<b>不支持通配符</b>，也不能含 <code>..</code></li>
        <li>不给 <code>patterns</code> 就必须显式写 <code>all: true</code>，否则 Agent 拒绝启动（刻意的隐私默认值）</li>
        <li>首次见到文件<strong>从末尾开始读，不回溯历史</strong>；想看历史日志请到机器上看</li>
        <li>单轮超上限会「跳过该文件剩余部分并计数」（<code>&lt;id&gt;_log_dropped_total</code>），不会延后补读</li>
      </ul>
      <template #footer>
        <span class="muted">写错时 Agent 启动会直接拒绝并指出是哪一处</span>
        <el-button @click="exampleVisible = false">关闭</el-button>
      </template>
    </el-dialog>
  </section>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import http from '../api/http'

const route = useRoute()

// 时间范围默认最近 1 小时：日志量远大于指标，默认范围必须收窄（与服务端默认一致）。
function defaultRange() {
  const now = Date.now()
  return [new Date(now - 3600 * 1000), new Date(now)]
}

const shortcuts = [
  { text: '最近 15 分钟', value: () => [new Date(Date.now() - 900 * 1000), new Date()] },
  { text: '最近 1 小时', value: () => [new Date(Date.now() - 3600 * 1000), new Date()] },
  { text: '最近 24 小时', value: () => [new Date(Date.now() - 24 * 3600 * 1000), new Date()] },
  { text: '最近 7 天', value: () => [new Date(Date.now() - 7 * 24 * 3600 * 1000), new Date()] },
]

const range = ref(defaultRange())
const mode = ref('kw')
const keyword = ref('')
const nodes = ref([])
const sources = ref([])
const nodeNames = ref([])
const sourceNames = ref([])

const lines = ref([])
const cursor = ref('')
const truncated = ref(false)
const scanned = ref({ bytes: 0, lines: 0, files: 0 })
const loading = ref(false)
const loadingMore = ref(false)
const loadError = ref('')
const expanded = ref(new Set())

// ---------- 配置示例 ----------
// 为什么放在页面里：logSources 配在 Agent 侧，界面上一个字段都看不到，
// 而"查不到数据"的头号原因就是"没配"（默认一条都不采集）。给可复制的模板最省事。
//
// 用 String.raw：正则里的 \b \d \. 在普通模板字符串里会被 JS 当转义吃掉，
// 生成出的 YAML 会静默变味（这类错误只有到机器上才会暴露）。
const LOG_EXAMPLES = [
  {
    key: 'nginx_access',
    label: 'Nginx 访问日志（只看 4xx/5xx）',
    hint: '状态码前是「引号+空格」，据此避免匹配到 URL 里的数字。',
    yaml: String.raw`
logSources:
  - id: nginx_access
    paths: ["/var/log/nginx/access.log"]
    patterns:
      - { name: http_5xx, regex: '" 5[0-9][0-9] ' }
      - { name: http_4xx, regex: '" 4[0-9][0-9] ' }
`.trim(),
  },
  {
    key: 'nginx_error',
    label: 'Nginx 错误日志',
    hint: '只收 error 及以上级别。',
    yaml: String.raw`
logSources:
  - id: nginx_error
    paths: ["/var/log/nginx/error.log"]
    patterns:
      - { name: err, regex: '(?i)\[(error|crit|alert|emerg)\]' }
`.trim(),
  },
  {
    key: 'applog',
    label: 'Java / Spring 应用日志（堆栈跨行合并）',
    hint: '行首不匹配「日期开头」的行会并入上一条——这是让堆栈成为一条记录的关键。',
    yaml: String.raw`
logSources:
  - id: applog
    paths: ["/opt/app/logs/app.log"]
    patterns:
      - { name: err, regex: '(?i)\b(ERROR|Exception|Caused by)\b' }
      - { name: warn, regex: '(?i)\bWARN\b' }
    multiline:
      startPattern: '^\d{4}-\d{2}-\d{2}'
      maxLines: 100
`.trim(),
  },
  {
    key: 'authlog',
    label: '系统认证 / sudo 审计',
    hint: 'CentOS/RHEL 用 /var/log/secure，Debian/Ubuntu 用 /var/log/auth.log——只写实际存在的那个。',
    yaml: String.raw`
logSources:
  - id: authlog
    paths: ["/var/log/secure"]
    patterns:
      - { name: failed, regex: '(?i)(Failed password|authentication failure|Invalid user)' }
      - { name: sudo, regex: '(?i)sudo:.*COMMAND=' }
`.trim(),
  },
  {
    key: 'mysql_slow',
    label: 'MySQL 慢查询日志',
    hint: '慢查询一条记录跨多行，按 "# Time:" 行首合并。',
    yaml: String.raw`
logSources:
  - id: mysql_slow
    paths: ["/var/log/mysql/slow.log"]
    patterns:
      - { name: slow, regex: '^# Query_time' }
    multiline:
      startPattern: '^# Time:'
      maxLines: 200
`.trim(),
  },
]

const exampleVisible = ref(false)
const exampleKey = ref(LOG_EXAMPLES[0].key)
// 模板里无条件引用它，因此必须有默认值（不能是 null）
const currentLogExample = computed(() => LOG_EXAMPLES.find((e) => e.key === exampleKey.value) || LOG_EXAMPLES[0])

// 离线部署多为 http 源：非安全上下文下 navigator.clipboard 不存在，必须留 execCommand 兜底，
// 否则「复制」按钮在真实环境里点了没反应——而这类问题只在部署后才发现。
function fallbackCopy(text) {
  const ta = document.createElement('textarea')
  ta.value = text
  ta.style.position = 'fixed'
  ta.style.top = '-1000px'
  document.body.appendChild(ta)
  ta.select()
  let ok = false
  try {
    ok = document.execCommand('copy')
  } catch (e) {
    ok = false
  }
  document.body.removeChild(ta)
  if (ok) ElMessage.success('已复制到剪贴板')
  else ElMessage.warning('复制失败，请手动选中文本复制')
}

function copyExample() {
  const text = currentLogExample.value.yaml
  if (navigator.clipboard && window.isSecureContext) {
    navigator.clipboard.writeText(text)
      .then(() => ElMessage.success('已复制到剪贴板'))
      .catch(() => fallbackCopy(text))
    return
  }
  fallbackCopy(text)
}

const hasMore = computed(() => truncated.value && cursor.value !== '')

// 扫描诊断：只在「没结果但确实扫过」时提示——解释「是确实没有，还是扫描预算先用完了」。
const scannedHint = computed(() => {
  if (lines.value.length || loading.value) return ''
  const s = scanned.value
  if (!s.lines) return ''
  return `扫描了 ${s.files} 个文件 / ${s.lines} 行，没有命中。可放宽时间范围或关键词再试。`
})

const emptyHint = computed(() => {
  if (!nodeNames.value.length) return '暂无节点上报数据。'
  // 最常见的原因：Agent 没配 logSources（那就根本不会有日志上行）
  if (!sourceNames.value.length) {
    return '没有任何节点配置日志来源（logSources）。请在目标机的 agent.yaml 里添加并重启 Agent —— 未配置时不会上传任何日志。'
  }
  return '没有命中的日志。可放宽时间范围、去掉关键词，或检查 Agent 侧 patterns 是否覆盖了你关心的行。'
})

function fmtTime(ts) {
  if (!ts) return '-'
  const d = new Date(ts)
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

function rowKey(row) {
  return `${row.node}|${row.source}|${row.ts}|${row.text.slice(0, 32)}`
}
function isOpen(row) {
  return expanded.value.has(rowKey(row))
}
function toggle(row) {
  const next = new Set(expanded.value)
  const k = rowKey(row)
  if (next.has(k)) next.delete(k)
  else next.add(k)
  expanded.value = next
}

function buildQuery(withCursor) {
  const p = new URLSearchParams()
  if (range.value && range.value[0] && range.value[1]) {
    p.set('from', String(range.value[0].getTime()))
    p.set('to', String(range.value[1].getTime()))
  }
  const kw = keyword.value.trim()
  if (kw) p.set(mode.value === 're' ? 'regex' : 'q', kw)
  if (nodes.value.length) p.set('nodes', nodes.value.join(','))
  if (sources.value.length) p.set('sources', sources.value.join(','))
  p.set('limit', '200')
  if (withCursor && cursor.value) p.set('cursor', cursor.value)
  return p.toString()
}

async function search() {
  loading.value = true
  loadError.value = ''
  expanded.value = new Set()
  try {
    const data = await http.get('/api/v1/logs?' + buildQuery(false))
    lines.value = data.lines || []
    cursor.value = data.cursor || ''
    truncated.value = !!data.truncated
    scanned.value = { bytes: data.scannedBytes || 0, lines: data.scannedLines || 0, files: data.files || 0 }
  } catch (e) {
    loadError.value = e.message || '查询失败'
    lines.value = []
    cursor.value = ''
    truncated.value = false
  } finally {
    loading.value = false
  }
}

async function loadMore() {
  if (!hasMore.value) return
  loadingMore.value = true
  loadError.value = ''
  try {
    const data = await http.get('/api/v1/logs?' + buildQuery(true))
    lines.value = lines.value.concat(data.lines || [])
    cursor.value = data.cursor || ''
    truncated.value = !!data.truncated
  } catch (e) {
    loadError.value = e.message || '加载更多失败'
  } finally {
    loadingMore.value = false
  }
}

// 节点与来源下拉的数据来自节点清单：来源就是各节点声明的 logSources 并集，
// 因此列表本身就是「哪些来源真的可能上报」——也为空状态提示提供了判断依据。
async function loadMeta() {
  try {
    const data = await http.get('/api/v1/nodes')
    const list = data.nodes || []
    nodeNames.value = list.map((n) => n.hostname).filter(Boolean)
    const set = new Set()
    for (const n of list) {
      for (const s of n.logSources || []) set.add(s)
    }
    sourceNames.value = Array.from(set).sort()
  } catch (e) {
    // 元信息拿不到不影响查询（可手动不选节点/来源）
    console.error('加载节点与日志来源失败', e)
  }
}

// 深链：/logs?node=<节点>&from=<ms>&to=<ms>&q=<关键词>（如从告警详情「查看日志」跳来）。
// 只覆盖显式传入的项，其余保持默认，避免「跳过来却看不到东西」。
function applyDeepLink() {
  const q = route.query
  if (q.node) nodes.value = String(q.node).split(',').map((s) => s.trim()).filter(Boolean)
  const from = Number(q.from)
  const to = Number(q.to)
  if (from > 0 && to > from) range.value = [new Date(from), new Date(to)]
  if (q.regex) {
    keyword.value = String(q.regex)
    mode.value = 're'
  } else if (q.q) {
    keyword.value = String(q.q)
    mode.value = 'kw'
  }
}

onMounted(async () => {
  applyDeepLink()
  await loadMeta()
  await search()
})
</script>

<style scoped>
.view { padding: 16px; }
.view-head { margin-bottom: 12px; }
.head-row { display: flex; align-items: baseline; gap: 12px; flex-wrap: wrap; }
.head-row h2 { margin: 0; font-size: 18px; }
.muted { color: var(--text-muted); font-size: 12px; }
.panel { background: var(--panel-bg, transparent); border: 1px solid var(--border-color, #e5e7eb); border-radius: 8px; padding: 12px; }
.toolbar { display: flex; gap: 8px; flex-wrap: wrap; margin-bottom: 10px; }
.alert-gap { margin-bottom: 10px; }
.tag { display: inline-block; padding: 1px 6px; border-radius: 4px; background: var(--tag-bg, rgba(64, 158, 255, 0.12)); font-size: 12px; }
.logline { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; cursor: pointer; }
.logline.open { white-space: pre-wrap; word-break: break-all; }
.foot { margin-top: 10px; display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
/* 配置示例弹窗 */
.ex-row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; margin: 10px 0; }
.ex-row .muted { flex: 1; min-width: 200px; }
.ex-box {
  margin: 0;
  padding: 12px;
  max-height: 300px;
  overflow: auto;
  font-family: var(--mono);
  font-size: 12.5px;
  line-height: 1.6;
  white-space: pre;
  background: rgba(127, 127, 127, 0.08);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text);
}
.ex-notes { margin: 12px 0 0; padding-left: 18px; font-size: 12.5px; color: var(--text-dim); line-height: 1.9; }
.ex-notes code { font-family: var(--mono); background: rgba(127, 127, 127, 0.12); padding: 0 4px; border-radius: 3px; }
</style>
