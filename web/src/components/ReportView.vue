<template>
  <div class="report-view">
    <PageHeader
      title="巡检报告"
      desc="日报 / 周报 / 月报，含主机资源趋势图、中间件监控指标与健康巡检发现"
    />

    <SectionCard title="生成报告">
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
    </SectionCard>

    <!-- 周期化生成：默认关闭，打开后按间隔自动生成一份（全景表 11-4）。
         与上方「生成报告」的分工：那个是"现在生成一份"，这里是"以后自动生成"。 -->
    <SectionCard title="周期化生成">
      <div class="generate-row">
        <el-switch v-model="schedule.enabled" @change="saveSchedule" />
        <span class="schedule-label">每</span>
        <el-input-number
          v-model="schedule.intervalHours"
          :min="1"
          :max="720"
          size="small"
          controls-position="right"
          @change="saveSchedule"
        />
        <span class="schedule-label">小时自动生成</span>
        <el-select v-model="schedule.type" style="width: 140px" size="small" @change="saveSchedule">
          <el-option label="日报" value="daily" />
          <el-option label="周报" value="weekly" />
          <el-option label="月报" value="monthly" />
        </el-select>
        <el-button size="small" :loading="scheduleRunning" @click="runScheduleNow">立即生成一次</el-button>
      </div>
      <div class="schedule-hint">
        <template v-if="scheduleError">{{ scheduleError }}</template>
        <template v-else-if="schedule.lastRunAt">
          上次运行 {{ formatTime(schedule.lastRunAt) }}
          <span v-if="schedule.lastReportId">· 报告 {{ schedule.lastReportId }}</span>
          <span v-if="schedule.lastError" class="schedule-fail">· 失败：{{ schedule.lastError }}</span>
          <span v-if="schedule.nextAt && schedule.enabled">· 下次约 {{ formatTime(schedule.nextAt) }}</span>
        </template>
        <template v-else>尚未运行过。关闭时不影响上方的手动生成。</template>
      </div>
      <el-alert
        v-if="schedule.lastError"
        class="tip"
        type="warning"
        :closable="false"
        show-icon
        title="上次自动生成失败"
        :description="schedule.lastError"
      />
    </SectionCard>

    <SectionCard title="历史报告">
      <el-alert
        v-if="historyError"
        type="error"
        :closable="false"
        show-icon
        class="history-error"
        title="历史报告加载失败"
        :description="historyError"
      />
      <el-table v-if="history.length > 0" :data="history" style="width: 100%">
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
      <EmptyState
        v-else-if="!historyError"
        title="暂无历史报告"
        :hints="['在上方选择日报 / 周报 / 月报，点「生成报告」创建第一份']"
        action-text="生成报告"
        @action="generate"
      />
    </SectionCard>

    <SectionCard v-if="previewUrl" title="报告预览" class="report-frame">
      <template #actions>
        <el-button size="small" @click="openNewTabById(currentId)">新标签页打开</el-button>
        <el-button size="small" @click="printReport(currentId)">打印</el-button>
        <el-button size="small" type="primary" @click="download(currentId)">下载 HTML</el-button>
      </template>
      <iframe :src="previewUrl" class="report-iframe" title="巡检报告预览"></iframe>
    </SectionCard>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import http from '../api/http'
import PageHeader from './common/PageHeader.vue'
import SectionCard from './common/SectionCard.vue'
import EmptyState from './common/EmptyState.vue'

const reportType = ref('daily')
const generating = ref(false)
const history = ref([])
const historyError = ref('')
const previewUrl = ref('')
const currentId = ref('')

async function loadHistory() {
  try {
    const data = await http.get('/api/v1/report/history')
    history.value = data.reports || []
    historyError.value = ''
  } catch (e) {
    console.error(e)
    historyError.value = e.message || '网络异常，请稍后重试'
  }
}

async function generate() {
  generating.value = true
  try {
    const data = await http.post('/api/v1/report/generate', { type: reportType.value })
    if (data && data.id) {
      preview(data.id)
      await loadHistory()
      ElMessage.success('报告已生成')
    }
  } catch (e) {
    console.error(e)
    ElMessage.error('生成报告失败：' + (e.message || '请稍后重试'))
  } finally { generating.value = false }
}

/* ================= 周期化生成（全景表 11-4） ================= */

// 调度配置与运行状态。lastRunAt/lastReportId/lastError 由服务端维护（界面上只读）。
const schedule = ref({ enabled: false, type: 'weekly', intervalHours: 24, lastRunAt: 0, lastReportId: '', lastError: '', nextAt: 0 })
const scheduleError = ref('')
const scheduleRunning = ref(false)

function applySchedule(data) {
  const cfg = (data && data.config) || {}
  const last = (data && data.last) || {}
  schedule.value = {
    enabled: !!cfg.enabled,
    type: cfg.type || 'weekly',
    intervalHours: cfg.intervalHours || 24,
    lastRunAt: last.at || cfg.lastRunAt || 0,
    lastReportId: last.reportId || cfg.lastReportId || '',
    lastError: last.error || cfg.lastError || '',
    nextAt: (data && data.nextAt) || 0,
  }
}

async function loadSchedule() {
  try {
    applySchedule(await http.get('/api/v1/report/schedule'))
    scheduleError.value = ''
  } catch (e) {
    // 该能力未启用（503）时如实说明，而不是让开关看起来"关着"——
    // 那会让人以为打开就好了，实际上后端根本没有调度器。
    scheduleError.value = e.message || '报告调度不可用'
  }
}

async function saveSchedule() {
  try {
    applySchedule(
      await http.put('/api/v1/report/schedule', {
        enabled: schedule.value.enabled,
        type: schedule.value.type,
        intervalHours: schedule.value.intervalHours,
      })
    )
    ElMessage.success('已保存（热生效）')
  } catch (e) {
    ElMessage.error(e.message || '保存失败')
    // 回读服务端真实状态：否则界面会停留在"我改成了这样"，而后端并没有接受
    await loadSchedule()
  }
}

async function runScheduleNow() {
  scheduleRunning.value = true
  try {
    const run = await http.post('/api/v1/report/schedule/run', {})
    await Promise.all([loadSchedule(), loadHistory()])
    ElMessage.success('已生成一份报告')
    if (run && run.reportId) preview(run.reportId)
  } catch (e) {
    ElMessage.error(e.message || '生成失败')
    await loadSchedule()
  } finally {
    scheduleRunning.value = false
  }
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

// 报告本身是独立 HTML（自带 @media print），必须在新窗口里打，
// 直接 window.print() 会把外层深色控制台一起印出来。
function printReport(id) {
  if (!id) return
  const w = window.open(`/api/v1/report/download?id=${encodeURIComponent(id)}`, '_blank')
  if (!w) {
    ElMessage.warning('浏览器拦截了弹窗，请允许弹出窗口后重试，或先点「新标签页打开」再 Ctrl+P')
    return
  }
  w.addEventListener('load', () => { w.focus(); w.print() }, { once: true })
}
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

onMounted(() => {
  loadHistory()
  loadSchedule()
})
</script>

<style scoped>
.report-view { padding: 4px 0 16px; }
.schedule-label { color: var(--text-dim); font-size: 13px; }
.schedule-hint { margin-top: 8px; color: var(--text-dim); font-size: 12.5px; }
.schedule-fail { color: #f56c6c; }
.page-header { margin-bottom: 16px; }
.page-title { font-size: 22px; font-weight: 700; margin: 0; background: linear-gradient(135deg, var(--text) 0%, var(--text-dim) 100%); -webkit-background-clip: text; -webkit-text-fill-color: transparent; }
.page-desc { font-size: 13px; color: var(--text-dim); margin-top: 4px; }
.chart-section { padding: 16px; margin-bottom: 16px; }
.section-title { font-size: 14px; font-weight: 600; margin-bottom: 12px; }
.generate-row { display: flex; gap: 12px; align-items: center; }
.tip { margin-top: 14px; }
.history-error { margin-bottom: 12px; }
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
