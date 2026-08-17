<template>
  <div class="audit-view">
    <div class="glass panel">
      <div class="panel-title-row">
        <div>
          <div class="panel-title">操作审计</div>
          <div class="panel-subtitle">记录登录和管理操作，支持按分类、操作者与接口路径筛选</div>
        </div>
        <div class="toolbar">
          <el-button size="small" :loading="loading" @click="loadEvents">刷新</el-button>
          <el-button size="small" type="primary" :loading="exporting" @click="exportCSV">导出 CSV</el-button>
        </div>
      </div>
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

    <div class="glass panel" v-loading="loading">
      <el-table :data="events" empty-text="暂无审计记录" style="width: 100%">
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
import http from '../api/http'

const loading = ref(false)
const exporting = ref(false)
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

async function loadEvents() {
  loading.value = true
  try {
    const data = await http.get(`/api/v1/audit/events?${queryString()}`)
    events.value = data.events || []
  } catch (error) {
    events.value = []
  } finally {
    loading.value = false
  }
}

async function exportCSV() {
  exporting.value = true
  try {
    const response = await fetch(`/api/v1/audit/events?${queryString('csv')}`, { credentials: 'include' })
    if (!response.ok) throw new Error('导出失败')
    const blob = await response.blob()
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = 'audit-events.csv'
    link.click()
    URL.revokeObjectURL(url)
  } finally {
    exporting.value = false
  }
}

onMounted(loadEvents)
</script>

<style scoped>
.audit-view { display: flex; flex-direction: column; gap: 16px; }
.panel { padding: 16px; }
.panel-title-row { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.panel-title { color: var(--text); font-size: 16px; font-weight: 600; }
.panel-subtitle { color: var(--text-muted); font-size: 12px; margin-top: 5px; }
.toolbar { display: flex; gap: 8px; }
.filters { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 16px; }
.filters .el-input { width: 180px; }
.method { display: inline-block; min-width: 48px; margin-right: 8px; color: var(--accent, #4a9df0); font-size: 11px; font-weight: 700; }
.method-post, .method-put, .method-patch, .method-delete { color: #ffb454; }
.path { color: var(--text); font-family: ui-monospace, SFMono-Regular, Consolas, monospace; font-size: 12px; }
.change-summary { display: flex; flex-direction: column; gap: 3px; color: var(--text-muted); font-size: 11px; }
.change-summary span:first-child { color: #e6a23c; }
.change-summary span:nth-child(2) { color: #67c23a; }
.change-summary span:nth-child(3) { color: #f56c6c; }
.muted { color: var(--text-muted); font-size: 11px; }
.source-cell { display: flex; flex-direction: column; gap: 2px; line-height: 1.3; }
.source-ip { font-family: var(--font-mono, monospace); font-size: 12px; }
.source-loc { font-size: 11px; color: var(--text-muted); }
.source-loc--unknown { font-style: italic; opacity: 0.7; }
@media (max-width: 720px) { .panel-title-row { align-items: flex-start; flex-direction: column; } .toolbar { width: 100%; } }
</style>
