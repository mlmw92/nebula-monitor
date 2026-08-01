<template>
  <div class="report-view">
    <div class="page-header">
      <div class="header-left">
        <h2 class="page-title">巡检报告</h2>
        <p class="page-desc">日报/周报/月报，含主机资源趋势与告警汇总</p>
      </div>
    </div>

    <div class="chart-section glass">
      <div class="section-title">生成报告</div>
      <div class="generate-row">
        <el-select v-model="reportType" style="width: 200px">
          <el-option label="日报" value="daily" />
          <el-option label="周报" value="weekly" />
          <el-option label="月报" value="monthly" />
        </el-select>
        <el-button type="primary" @click="generate" :loading="generating">生成报告</el-button>
      </div>
    </div>

    <div class="chart-section glass" v-if="history.length > 0">
      <div class="section-title">历史报告</div>
      <el-table :data="history" style="width: 100%">
        <el-table-column label="类型" width="80">
          <template #default="{ row }">
            <el-tag v-if="row.type" size="small">{{ typeLabel(row.type) }}</el-tag>
            <span v-else style="color: var(--text-muted)">—</span>
          </template>
        </el-table-column>
        <el-table-column label="统计周期" min-width="150">
          <template #default="{ row }">{{ row.period || '—' }}</template>
        </el-table-column>
        <el-table-column label="生成时间" width="180">
          <template #default="{ row }">{{ formatTime(row.generatedAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="120">
          <template #default="{ row }">
            <el-button link @click="preview(row.id)">预览</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import http from '../api/http'

const reportType = ref('daily')
const generating = ref(false)
const history = ref([])

async function loadHistory() {
  try {
    const data = await http.get('/api/v1/report/history')
    history.value = data.reports || []
  } catch (e) { console.error(e) }
}

async function generate() {
  generating.value = true
  try {
    const data = await http.post('/api/v1/report/generate', { type: reportType.value })
    preview(data.id)
    await loadHistory()
  } catch (e) { console.error(e) } finally { generating.value = false }
}

function preview(id) {
  window.open(`/api/v1/report/download?id=${id}`, '_blank')
}

function typeLabel(t) { return { daily: '日报', weekly: '周报', monthly: '月报' }[t] || t }
function formatTime(ms) { return new Date(ms).toLocaleString('zh-CN') }

onMounted(loadHistory)
</script>

<style scoped>
.report-view { padding: 4px 0 16px; }
.page-header { margin-bottom: 16px; }
.page-title { font-size: 22px; font-weight: 700; margin: 0; background: linear-gradient(135deg, var(--text) 0%, var(--text-dim) 100%); -webkit-background-clip: text; -webkit-text-fill-color: transparent; }
.page-desc { font-size: 13px; color: var(--text-dim); margin-top: 4px; }
.chart-section { padding: 16px; margin-bottom: 16px; }
.section-title { font-size: 14px; font-weight: 600; margin-bottom: 12px; }
.generate-row { display: flex; gap: 12px; align-items: center; }
</style>
