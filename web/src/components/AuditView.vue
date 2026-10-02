<template>
  <div class="audit-view">
    <PageHeader
      title="操作审计"
      desc="记录登录和管理操作，支持按分类、操作者与接口路径筛选"
    >
      <template #actions>
        <el-button size="small" :loading="loading" @click="loadEvents">刷新</el-button>
        <el-button size="small" type="primary" :loading="exporting" @click="exportCSV">导出 CSV</el-button>
        <el-button size="small" plain @click="printList">打印</el-button>
      </template>
    </PageHeader>

    <div class="card filter-bar">
      <div class="filters">
        <el-select v-model="filters.category" clearable placeholder="全部分类" size="small" style="width: 150px" @change="loadEvents">
          <el-option value="authentication" label="认证" />
          <el-option value="management" label="管理操作" />
          <el-option value="security" label="安全中心" />
        </el-select>
        <el-input v-model="filters.user" clearable placeholder="操作者" size="small" @keyup.enter="loadEvents" />
        <el-input v-model="filters.path" clearable placeholder="接口路径" size="small" @keyup.enter="loadEvents" />
        <el-select v-model="filters.limit" size="small" style="width: 120px" @change="loadEvents">
          <el-option :value="50" label="最近 50 条" />
          <el-option :value="100" label="最近 100 条" />
          <el-option :value="200" label="最近 200 条" />
        </el-select>
        <el-button size="small" type="primary" @click="loadEvents">查询</el-button>
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
      </el-table>
    </div>
  </div>
</template>

<script setup>
import { reactive, ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import http from '../api/http'
import PageHeader from './common/PageHeader.vue'
import EmptyState from './common/EmptyState.vue'
import { printPage } from '../utils/print'

const loading = ref(false)
const exporting = ref(false)
const loadError = ref('')
const events = ref([])
const filters = reactive({ category: '', user: '', path: '', limit: 100 })

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

function queryString(format = '') {
  const params = new URLSearchParams({
    limit: String(filters.limit),
    user: filters.user,
    path: filters.path,
    category: filters.category,
  })
  if (format) params.set('format', format)
  return params.toString()
}

// 审计打印件要能说明"按什么条件导出的"，否则作为凭证材料无效
function printList() {
  const cond = [
    `分类=${categoryLabel(filters.category)}`,
    `操作者=${filters.user || '全部'}`,
    `接口路径=${filters.path || '全部'}`,
    `条数=${filters.limit}`,
  ]
  printPage({ title: '操作审计', meta: [`共 ${events.value.length} 条`, cond.join(' · ')] })
}

async function loadEvents() {
  loading.value = true
  try {
    const data = await http.get(`/api/v1/audit/events?${queryString()}`)
    events.value = data.events || []
    loadError.value = ''
  } catch (error) {
    events.value = []
    loadError.value = error.message || '网络异常，请稍后重试'
  } finally {
    loading.value = false
  }
}

async function exportCSV() {
  exporting.value = true
  try {
    // 导出走独立路由（需 audit:export 权限点），查看与导出在服务端权限点不同
    const response = await fetch(`/api/v1/audit/export?${queryString()}`, { credentials: 'include' })
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

onMounted(loadEvents)
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
.source-cell { display: flex; flex-direction: column; gap: 2px; line-height: 1.3; }
.source-ip { font-family: var(--font-mono, monospace); font-size: 13px; }
.source-loc { font-size: 13px; color: var(--text-muted); }
.source-loc--unknown { font-style: italic; opacity: 0.7; }
@media (max-width: 720px) { .toolbar { width: 100%; } }
</style>
