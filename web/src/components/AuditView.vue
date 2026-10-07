<template>
  <div class="audit-view">
    <PageHeader
      title="操作审计"
      desc="记录登录和管理操作，支持按分类、操作者、接口路径与时间范围筛选"
    >
      <template #actions>
        <el-button size="small" :loading="loading" @click="loadEvents">刷新</el-button>
        <el-button size="small" type="primary" :loading="exporting" @click="exportCSV">导出 CSV</el-button>
        <el-button size="small" plain @click="printList">打印</el-button>
      </template>
    </PageHeader>

    <div class="card filter-bar">
      <div class="filters">
        <el-select v-model="filters.category" clearable placeholder="全部分类" size="small" style="width: 150px" @change="applyFilters">
          <el-option value="authentication" label="认证" />
          <el-option value="management" label="管理操作" />
          <el-option value="security" label="安全中心" />
        </el-select>
        <el-input v-model="filters.user" clearable placeholder="操作者" size="small" @keyup.enter="applyFilters" />
        <el-input v-model="filters.path" clearable placeholder="接口路径" size="small" @keyup.enter="applyFilters" />
        <!-- 时间范围：入库后审计按时间保留（默认 180 天），"能查到多久以前"要靠它才够得着。
             不选 = 不限时间，行为与改造前一致。 -->
        <el-date-picker
          v-model="filters.range"
          type="datetimerange"
          size="small"
          start-placeholder="开始时间"
          end-placeholder="结束时间"
          :shortcuts="rangeShortcuts"
          style="width: 360px"
          @change="applyFilters"
        />
        <el-select v-model="filters.limit" size="small" style="width: 120px" @change="applyFilters">
          <el-option :value="50" label="每页 50 条" />
          <el-option :value="100" label="每页 100 条" />
          <el-option :value="200" label="每页 200 条" />
        </el-select>
        <!-- 关联 id 筛选：从资产变更历史跳进来时用的就是它（/audit?requestId=xxx）。
             做成可关闭的标签而不是隐形条件——生效中的筛选必须看得见，
             否则"审计怎么少了"会变成下一个问题。 -->
        <el-tag
          v-if="filters.requestId"
          closable
          size="small"
          type="warning"
          title="只显示这一次操作（同一次请求）产生的审计记录"
          @close="clearRequestId"
        >
          本次操作 {{ filters.requestId }}
        </el-tag>
        <el-button size="small" type="primary" @click="applyFilters">查询</el-button>
      </div>
    </div>

    <el-alert
      v-if="loadError"
      type="error"
      :closable="false"
      show-icon
      class="audit-error"
    >
      <template #title>审计记录加载失败：{{ loadError }}
        <el-button link type="primary" @click="loadEvents">重试</el-button>
      </template>
    </el-alert>

    <div class="card" v-loading="loading">
      <el-table :data="events" style="width: 100%">
        <template #empty>
          <EmptyState
            title="暂无审计记录"
            :hints="[
              '审计仅在启用 RBAC 后记录，确认已在系统设置中开启',
              requestIdHint,
              '调整时间范围或清除筛选条件后重新查询',
            ]"
          />
        </template>
        <el-table-column label="时间" width="180">
          <template #default="{ row }">{{ formatTime(row.time) }}</template>
        </el-table-column>
        <el-table-column label="分类" width="110">
          <template #default="{ row }">
            <el-tag size="small" effect="plain">{{ categoryLabel(row.category) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="user" label="操作者" width="140">
          <template #default="{ row }">{{ row.user || '未登录/未知' }}</template>
        </el-table-column>
        <el-table-column label="操作" min-width="300">
          <template #default="{ row }">
            <span class="method" :class="'method-' + row.method.toLowerCase()">{{ row.method }}</span>
            <span class="path">{{ row.path }}</span>
            <div v-if="row.action" class="action">{{ row.action }}</div>
          </template>
        </el-table-column>
        <el-table-column label="变更摘要" min-width="300">
          <template #default="{ row }">
            <div v-if="changeSummary(row.detail).changed.length || changeSummary(row.detail).added.length || changeSummary(row.detail).removed.length" class="change-summary">
              <span v-if="changeSummary(row.detail).changed.length">修改：{{ changeSummary(row.detail).changed.join('、') }}</span>
              <span v-if="changeSummary(row.detail).added.length">新增：{{ changeSummary(row.detail).added.join('、') }}</span>
              <span v-if="changeSummary(row.detail).removed.length">删除：{{ changeSummary(row.detail).removed.join('、') }}</span>
            </div>
            <span v-else class="muted">{{ row.detail || '-' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="来源" min-width="200">
          <template #default="{ row }">
            <div class="source-cell">
              <span class="source-ip">{{ row.remoteIP }}</span>
              <span v-if="row.sourceLocation" class="source-loc">{{ row.sourceLocation }}</span>
              <span v-else class="source-loc source-loc--unknown">归属地未知</span>
            </div>
          </template>
        </el-table-column>
        <el-table-column prop="status" label="状态码" width="90" />
        <el-table-column label="结果" width="90">
          <template #default="{ row }">
            <el-tag :type="row.succeeded ? 'success' : 'danger'" size="small">
              {{ row.succeeded ? '成功' : '失败' }}
            </el-tag>
          </template>
        </el-table-column>
        <!-- 反向入口：审计说"调了哪个接口"，这里回答"那次调用实际动了哪些资产的哪些字段"。
             没有关联 id 的行（旧数据、采集上报）不给入口——不为一个查不到的操作造按钮。 -->
        <el-table-column label="资产改动" width="110">
          <template #default="{ row }">
            <el-button
              v-if="row.requestId && can('assets:read')"
              link
              type="primary"
              size="small"
              @click="openChanges(row)"
            >
              本次改动
            </el-button>
            <span v-else class="muted">—</span>
          </template>
        </el-table-column>
      </el-table>
      <!-- 分页：审计入库后不再被 2000 条截断，"往前翻"才有意义。
           总数来自服务端同条件计数（与列表共用一处 WHERE），不会出现"总数说 12、列表只有 3 条"。 -->
      <el-pagination
        v-if="total > 0"
        layout="total, prev, pager, next, jumper"
        :total="total"
        :page-size="filters.limit"
        :current-page="page"
        style="margin-top: 12px; justify-content: flex-end"
        @current-change="changePage"
      />
    </div>

    <!-- 「本次改动」抽屉：把这一次操作在资产台账里改动的字段列清楚（可跨多个资产）。
         与资产页的变更历史互为反面：那条回答"这台资产经历了什么"，这条回答"那次操作动了谁"。
         条数被服务端截断时显式说明——静默截断会让人以为"这次就动了这些"。 -->
    <el-drawer v-model="changesDrawer" :title="changesTitle" size="760px" direction="rtl">
      <div class="changes-head">
        <div class="mono">{{ currentEvent ? currentEvent.method + ' ' + currentEvent.path : '' }}</div>
        <div class="muted">
          操作者 {{ (currentEvent && currentEvent.user) || '未登录/未知' }} ·
          {{ currentEvent ? formatTime(currentEvent.time) : '' }} ·
          关联 id <span class="mono">{{ changesRequestId }}</span>
        </div>
      </div>

      <el-alert
        v-if="changesError"
        type="error"
        :closable="false"
        show-icon
        :title="changesError"
        class="audit-error"
      />
      <el-alert
        v-if="changesTruncated"
        type="warning"
        :closable="false"
        show-icon
        :title="`改动条数超过上限，这里只显示前 ${changes.length} 条`"
        description="这一次操作改动的字段比能列出的更多，不要据此判断它的全部影响"
        class="audit-error"
      />

      <el-table v-loading="changesLoading" :data="changes" style="width: 100%">
        <template #empty>
          <EmptyState
            title="这次操作没有改动资产"
            :hints="[
              '审计记录下来了，但没有任何资产字段发生变化（如只做了更新但值没变）',
              '也可能改动落在你的资源范围之外——范围外的资产不会出现在这里',
            ]"
          />
        </template>
        <el-table-column label="资产" min-width="200">
          <template #default="{ row }">
            <div class="asset-cell">
              <span>{{ row.name || row.naturalKey }}</span>
              <span class="muted">{{ row.typeKey }} · {{ row.naturalKey }}</span>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="字段" width="140">
          <template #default="{ row }"><span class="mono">{{ row.field }}</span></template>
        </el-table-column>
        <el-table-column label="变更" min-width="220">
          <template #default="{ row }">
            <span class="struck">{{ row.old || '（空）' }}</span> → <b>{{ row.new || '（已清除）' }}</b>
          </template>
        </el-table-column>
        <el-table-column label="来源" width="80">
          <template #default="{ row }">{{ row.source === 'manual' ? '人工' : '采集' }}</template>
        </el-table-column>
        <el-table-column label="时间" width="170">
          <template #default="{ row }">{{ formatTime(row.at) }}</template>
        </el-table-column>
      </el-table>
    </el-drawer>
  </div>
</template>

<script setup>
import { reactive, ref, computed, onMounted, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import http, { getToken, setToken } from '../api/http'
import PageHeader from './common/PageHeader.vue'
import EmptyState from './common/EmptyState.vue'
import { getAssetChangesByRequest } from '../api/asset'
import { useAuth } from '../composables/useAuth'
import { printPage } from '../utils/print'

const route = useRoute()
const router = useRouter()
// 仅用于体验级隐藏（服务端才是安全边界）：没有 assets:read 时不给「本次改动」入口。
const { can } = useAuth()

const loading = ref(false)
const exporting = ref(false)
const loadError = ref('')
const events = ref([])
let latestRequest = 0
// total 是**同条件总数**（服务端算，与列表共用一处 WHERE）；page 从 1 开始。
const total = ref(0)
const page = ref(1)
// requestId 是"这一次操作"的关联 id：从资产变更历史跳进来时带上（见 applyDeepLink）。
const filters = reactive({ category: '', user: '', path: '', limit: 100, range: [], requestId: '' })

// 「本次改动」抽屉的状态。
const changesDrawer = ref(false)
const changesLoading = ref(false)
const changesError = ref('')
const changes = ref([])
const changesTruncated = ref(false)
const changesRequestId = ref('')
const currentEvent = ref(null)
let latestChanges = 0

const changesTitle = computed(() =>
  `本次操作改动的资产字段${changes.value.length ? `（${changes.value.length} 条）` : ''}`
)

// 空态里那条与关联 id 有关的提示：关联 id 是本次升级之后才写的，
// 旧记录天然为空——不说明的话，"按 id 查不到"会被当成功能坏了。
const requestIdHint = computed(() =>
  filters.requestId
    ? '关联 id 只在本次升级之后的操作上才有，更早的记录该字段为空'
    : '按「本次操作」的关联 id 直查：从资产变更历史的「查看对应审计」可跳到这里'
)

// 时间范围快捷项：审计排查最常用的就是「今天 / 近 7 天 / 近 30 天」。
const rangeShortcuts = [
  {
    text: '今天',
    value: () => {
      const start = new Date()
      start.setHours(0, 0, 0, 0)
      return [start, new Date()]
    }
  },
  {
    text: '近 7 天',
    value: () => {
      const start = new Date()
      start.setDate(start.getDate() - 7)
      return [start, new Date()]
    }
  },
  {
    text: '近 30 天',
    value: () => {
      const start = new Date()
      start.setDate(start.getDate() - 30)
      return [start, new Date()]
    }
  }
]

const categoryMap = {
  authentication: '认证',
  management: '管理操作',
  security: '安全中心'
}
function categoryLabel(value) {
  return categoryMap[value] || value || '其他'
}

function changeSummary(detail) {
  const result = { changed: [], added: [], removed: [] }
  if (!detail) return result
  for (const key of Object.keys(result)) {
    const match = detail.match(new RegExp(`${key}=([^ ]*)`))
    if (match && match[1]) result[key] = match[1].split(',').filter(Boolean)
  }
  return result
}
function formatTime(value) {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString('zh-CN', { hour12: false })
}

// includeOffset=false 用于导出：导出的是「符合条件的前 N 条」，不该带上当前页码。
function queryString({ includeOffset = true, format = '' } = {}) {
  const params = new URLSearchParams({
    limit: String(filters.limit),
    user: filters.user,
    path: filters.path,
    category: filters.category,
  })
  if (includeOffset && page.value > 1) params.set('offset', String((page.value - 1) * filters.limit))
  // 关联 id 是精确匹配（服务端不走子串）：带着它查就是"这一次操作"，不该被别的条件稀释
  if (filters.requestId) params.set('requestId', filters.requestId)
  const [from, to] = filters.range || []
  if (from) params.set('from', String(new Date(from).getTime()))
  if (to) params.set('to', String(new Date(to).getTime()))
  if (format) params.set('format', format)
  return params.toString()
}

// 审计打印件要能说明"按什么条件导出的"，否则作为凭证材料无效
function printList() {
  const [from, to] = filters.range || []
  const cond = [
    `分类=${categoryLabel(filters.category)}`,
    `操作者=${filters.user || '全部'}`,
    `接口路径=${filters.path || '全部'}`,
    `关联 id=${filters.requestId || '不限'}`,
    `时间范围=${from ? `${formatTime(from)} ~ ${formatTime(to || new Date())}` : '不限'}`,
    `第 ${page.value} 页 / 每页 ${filters.limit} 条（符合条件共 ${total.value} 条）`,
  ]
  printPage({ title: '操作审计', meta: [`本页 ${events.value.length} 条`, cond.join(' · ')] })
}

async function loadEvents() {
  const request = ++latestRequest
  loading.value = true
  try {
    const data = await http.get(`/api/v1/audit/events?${queryString()}`)
    if (request !== latestRequest) return
    events.value = data.events || []
    total.value = Number(data.total) || 0
    loadError.value = ''
  } catch (error) {
    if (request !== latestRequest) return
    events.value = []
    total.value = 0
    loadError.value = error.message || '网络异常，请稍后重试'
  } finally {
    if (request === latestRequest) loading.value = false
  }
}

// 改筛选条件必须回到第 1 页：否则「第 3 页 + 新条件」会查出空列表，看起来像"没有记录"。
function applyFilters() {
  page.value = 1
  loadEvents()
}

function changePage(next) {
  page.value = next
  loadEvents()
}

async function exportCSV() {
  exporting.value = true
  try {
    // 导出走独立路由（需 audit:export 权限点），查看与导出在服务端权限点不同
    const token = getToken()
    const response = await fetch(`/api/v1/audit/export?${queryString({ includeOffset: false })}`, {
      credentials: 'include',
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    })
    if (response.status === 401) {
      setToken('')
      window.dispatchEvent(new CustomEvent('auth-expired'))
      throw new Error('未登录或登录已过期')
    }
    if (!response.ok) throw new Error('导出失败')
    const blob = await response.blob()
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = 'audit-events.csv'
    link.click()
    URL.revokeObjectURL(url)
    ElMessage.success('已导出审计记录 CSV')
  } catch (error) {
    ElMessage.error('导出失败：' + (error.message || '请稍后重试'))
  } finally {
    exporting.value = false
  }
}

// 深链：/audit?requestId=xxx（从资产变更历史的「查看对应审计」跳来）。
// 只认这一个参数，其余筛选保持默认——"跳过来却什么都没看到"比多点一次筛选糟得多。
function applyDeepLink() {
  const rid = route.query.requestId
  if (typeof rid === 'string' && rid.trim()) filters.requestId = rid.trim()
}

// clearRequestId 关掉"只看这一次操作"这个条件，并把它从地址栏一起摘掉：
// 只清状态不清 URL 的话，刷新一次条件又回来了，用户会以为关不掉。
function clearRequestId() {
  filters.requestId = ''
  if (route.query.requestId) {
    const query = { ...route.query }
    delete query.requestId
    router.replace({ path: route.path, query })
  }
  applyFilters()
}

// openChanges 打开「本次改动」：一次操作 = 一次请求 = 一个关联 id，
// 服务端按它把这次操作在台账里改动的字段（可能跨多个资产）取回来。
// 取不到关联 id 就不发请求（空值在服务端是 400，且语义上是"采集侧的全部变更"）。
async function openChanges(row) {
  changesRequestId.value = row.requestId || ''
  currentEvent.value = row
  changes.value = []
  changesTruncated.value = false
  changesError.value = ''
  changesDrawer.value = true
  if (!changesRequestId.value) return
  const request = ++latestChanges
  changesLoading.value = true
  try {
    const data = await getAssetChangesByRequest(changesRequestId.value)
    if (request !== latestChanges) return
    changes.value = data.changes || []
    changesTruncated.value = !!data.truncated
  } catch (error) {
    if (request !== latestChanges) return
    changesError.value = '读取本次改动失败：' + (error.message || '请稍后重试')
  } finally {
    if (request === latestChanges) changesLoading.value = false
  }
}

onMounted(() => {
  applyDeepLink()
  loadEvents()
})
onUnmounted(() => { latestRequest++; latestChanges++ })
</script>

<style scoped>
.audit-view { display: flex; flex-direction: column; gap: 16px; }
.audit-error :deep(.el-button--primary) { font-size: 13px; }
/* 页头与筛选分离：筛选条独立成一张薄卡，不再和标题挤在同一个容器里 */
.filter-bar { padding: 12px 16px; }
.toolbar { display: flex; gap: 8px; }
.filters { display: flex; flex-wrap: wrap; gap: 8px; }
.filters .el-input { width: 180px; }
.method { display: inline-block; min-width: 48px; margin-right: 8px; color: var(--accent, #4a9df0); font-size: 13px; font-weight: 700; }
.method-post, .method-put, .method-patch, .method-delete { color: #ffb454; }
.path { color: var(--text); font-family: ui-monospace, SFMono-Regular, Consolas, monospace; font-size: 13px; }
.change-summary { display: flex; flex-direction: column; gap: 3px; color: var(--text-muted); font-size: 13px; }
.change-summary span:first-child { color: #e6a23c; }
.change-summary span:nth-child(2) { color: #67c23a; }
.change-summary span:nth-child(3) { color: #f56c6c; }
.muted { color: var(--text-muted); font-size: 13px; }
.mono { font-family: ui-monospace, SFMono-Regular, Consolas, monospace; }
/* 删除/旧值用删除线：与资产页的变更 diff 同一读法（那里也是"旧值划线 + 新值加粗"） */
.struck { color: var(--text-muted); text-decoration: line-through; }
/* 「本次改动」抽屉：头部先把"这是哪一次操作"说清楚，再列改动 */
.changes-head {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-bottom: 12px;
  padding-bottom: 10px;
  border-bottom: 1px solid var(--border, rgba(255, 255, 255, 0.08));
  font-size: 13px;
}
.asset-cell { display: flex; flex-direction: column; gap: 2px; line-height: 1.3; font-size: 13px; }
.source-cell { display: flex; flex-direction: column; gap: 2px; line-height: 1.3; }
.source-ip { font-family: var(--font-mono, monospace); font-size: 13px; }
.source-loc { font-size: 13px; color: var(--text-muted); }
.source-loc--unknown { font-style: italic; opacity: 0.7; }
@media (max-width: 720px) { .toolbar { width: 100%; } }
</style>
