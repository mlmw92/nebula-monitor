<template>
  <div class="report-view">
    <div class="page-header">
      <div class="header-left">
        <h2 class="page-title">巡检报告</h2>
        <p class="page-desc">日报/周报/月报，含主机资源趋势图、中间件监控指标与健康巡检发现</p>
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
      <el-alert
        class="tip"
        type="info"
        :closable="false"
        show-icon
        title="报告内容"
        description="报告包含资源趋势柱状图/折线图、各主机健康评分、中间件连接数/响应时间/内存使用率/命中率明细，以及按严重程度排序的巡检发现（问题描述 / 影响范围 / 修复建议）。"
      />
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
        <el-table-column label="操作" width="200">
          <template #default="{ row }">
            <el-button link type="primary" @click="preview(row.id)">预览</el-button>
            <el-button link @click="openNewTab(row.id)">新标签页</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <div class="chart-section glass report-frame" v-if="previewUrl">
      <div class="section-title frame-head">
        <span>报告预览</span>
        <div class="frame-actions">
          <el-button size="small" @click="openNewTabById(currentId)">新标签页打开</el-button>
          <el-button size="small" type="primary" @click="download(currentId)">下载 HTML</el-button>
        </div>
      </div>
      <iframe :src="previewUrl" class="report-iframe" title="巡检报告预览"></iframe>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import http from '../api/http'

const reportType = ref('daily')
const generating = ref(false)
const history = ref([])
const previewUrl = ref('')
const currentId = ref('')

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
    if (data && data.id) {
      preview(data.id)
      await loadHistory()
    }
  } catch (e) { console.error(e) } finally { generating.value = false }
}

function preview(id) {
  currentId.value = id
  previewUrl.value = `/api/v1/report/download?id=${encodeURIComponent(id)}`
  // 滚动到预览区域
  setTimeout(() => {
    const el = document.querySelector('.report-frame')
    if (el) el.scrollIntoView({ behavior: 'smooth', block: 'start' })
  }, 60)
}

function openNewTab(id) { window.open(`/api/v1/report/download?id=${encodeURIComponent(id)}`, '_blank') }
function openNewTabById(id) { if (id) openNewTab(id) }
function download(id) {
  const a = document.createElement('a')
  a.href = `/api/v1/report/download?id=${encodeURIComponent(id)}`
  a.download = `${id}.html`
  a.target = '_blank'
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
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
.tip { margin-top: 14px; }
.frame-head { display: flex; align-items: center; justify-content: space-between; }
.frame-actions { display: flex; gap: 8px; }
.report-iframe {
  width: 100%;
  height: 860px;
  border: 1px solid var(--border, #ebeef5);
  border-radius: 10px;
  background: #fff;
}
</style>
