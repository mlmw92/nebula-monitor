<template>
  <div class="retention-view">
    <div class="toolbar">
      <span class="field-hint">
        可清理的本地数据只有两类：<b>告警处置记录</b>与<b>巡检报告</b>；审计事件与安全事件由内置上限约束，因此不会无界增长。
        指标数据的保留由时序库自身控制，这里只做只读呈现。
      </span>
      <div class="toolbar-actions">
        <el-button size="small" :loading="loading" @click="load">刷新</el-button>
      </div>
    </div>

    <el-alert v-if="error" :title="error" type="warning" :closable="false" show-icon class="err" />

    <!-- 策略 -->
    <div class="section">
      <div class="section-title">保留策略</div>
      <div class="form-row">
        <div class="form-item">
          <span class="label">自动清理</span>
          <el-switch v-model="form.enabled" size="small" active-text="启用" inactive-text="关闭" />
        </div>
        <div class="form-item">
          <span class="label">告警处置记录保留</span>
          <el-input-number v-model="form.acksDays" :min="0" :max="3650" size="small" controls-position="right" />
          <span class="field-hint inline">天（0 = 不清理）</span>
        </div>
        <div class="form-item">
          <span class="label">巡检报告保留</span>
          <el-input-number v-model="form.reportsDays" :min="0" :max="3650" size="small" controls-position="right" />
          <span class="field-hint inline">天（0 = 不清理）</span>
        </div>
        <div class="form-item">
          <span class="label">集中日志保留</span>
          <el-input-number v-model="form.logsDays" :min="0" :max="3650" size="small" controls-position="right" />
          <span class="field-hint inline">天（0 = 不清理；按日期分片整天删除，当天不删）</span>
        </div>
        <div class="form-item">
          <span class="label">清理周期</span>
          <el-input-number v-model="form.intervalHours" :min="1" :max="720" size="small" controls-position="right" />
          <span class="field-hint inline">小时</span>
        </div>
        <el-button type="primary" size="small" :loading="saving" @click="save">保存</el-button>
        <el-button size="small" :loading="cleaning" @click="cleanup">立即清理</el-button>
      </div>
      <div class="field-hint">
        「自动清理」只控制周期性清理；「立即清理」始终按上面的保留天数执行。待处置（未认领）的告警记录不会被清理。
      </div>
    </div>

    <!-- 上次清理结果 -->
    <el-alert v-if="lastResult" type="success" :closable="false" show-icon class="err">
      <template #title>
        上次清理：{{ fmt(lastResult.at) }} · 告警记录 {{ lastResult.acksRemoved }} 条 · 报告文件
        {{ lastResult.reportFilesRemoved }} 个（历史 {{ lastResult.reportHistoryRemoved }} 条）· 日志
        {{ lastResult.logFilesRemoved ?? 0 }} 个文件（分片 {{ lastResult.logDirsRemoved ?? 0 }}）· 释放
        {{ fileSize(lastResult.freedBytes) }}
        <span v-if="lastResult.skipped" class="field-hint inline">（{{ lastResult.skipped }}）</span>
      </template>
    </el-alert>

    <!-- 现状 -->
    <div class="section">
      <div class="section-title">当前占用</div>
      <el-descriptions :column="2" border size="small">
        <el-descriptions-item label="告警处置记录">
          {{ status.acks?.total ?? '—' }} 条（已处置 {{ status.acks?.handled ?? '—' }}）
          <span class="field-hint inline">最早 {{ fmt(status.acks?.oldest) }}</span>
        </el-descriptions-item>
        <el-descriptions-item label="巡检报告">
          {{ status.reports?.files ?? '—' }} 个文件 · {{ fileSize(status.reports?.bytes) }}
        </el-descriptions-item>
        <el-descriptions-item label="集中日志">
          {{ status.logs?.files ?? '—' }} 个文件 · {{ fileSize(status.logs?.bytes) }}
          <span class="field-hint inline">来源 {{ status.logs?.sources ?? 0 }} · 日期分片 {{ status.logs?.days ?? 0 }}</span>
        </el-descriptions-item>
        <el-descriptions-item label="审计事件">
          {{ status.audit?.count ?? '—' }} / {{ status.audit?.cap ?? '—' }} 条
          <span class="field-hint inline">超出上限自动淘汰最旧记录</span>
        </el-descriptions-item>
        <el-descriptions-item label="安全事件">
          {{ status.security?.count ?? '—' }} / {{ status.security?.cap ?? '—' }} 条
          <span class="field-hint inline">超出上限自动淘汰最旧记录</span>
        </el-descriptions-item>
      </el-descriptions>
    </div>

    <!-- 时序库保留（只读） -->
    <div class="section">
      <div class="section-title">指标数据保留（时序库侧）</div>
      <el-descriptions :column="1" border size="small">
        <el-descriptions-item label="时序库地址">{{ status.tsdb?.addr || '未配置' }}</el-descriptions-item>
        <el-descriptions-item label="保留参数">
          <el-tag v-if="status.tsdb?.setting" type="success" size="small" effect="plain">{{ status.tsdb.setting }}</el-tag>
          <span v-else class="field-hint">{{ status.tsdb?.error || '未读取到' }}</span>
        </el-descriptions-item>
      </el-descriptions>
      <div class="field-hint">
        指标保留期由时序库自身的启动参数决定（VictoriaMetrics 为 <code v-pre>-retentionPeriod</code>），
        Server 无法在运行期修改，需在时序库侧调整后重启。此处仅呈现当前值。
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import http from '../../api/http'

const loading = ref(false)
const saving = ref(false)
const cleaning = ref(false)
const error = ref('')
const status = ref({})
const lastResult = ref(null)
const form = ref({ enabled: true, acksDays: 90, reportsDays: 180, logsDays: 7, intervalHours: 24 })

function fmt(ms) {
  if (!ms || ms <= 0) return '—'
  return new Date(ms).toLocaleString('zh-CN', { hour12: false })
}

function fileSize(bytes) {
  if (bytes === undefined || bytes === null) return '—'
  const v = Number(bytes)
  if (v < 1024) return v + ' B'
  if (v < 1024 * 1024) return (v / 1024).toFixed(1) + ' KB'
  if (v < 1024 * 1024 * 1024) return (v / 1024 / 1024).toFixed(1) + ' MB'
  return (v / 1024 / 1024 / 1024).toFixed(2) + ' GB'
}

function apply(statusBody) {
  status.value = statusBody || {}
  const cfg = statusBody?.config
  if (cfg) {
    form.value = {
      enabled: !!cfg.enabled,
      acksDays: cfg.acksDays ?? 0,
      reportsDays: cfg.reportsDays ?? 0,
      logsDays: cfg.logsDays ?? 0,
      intervalHours: cfg.intervalHours ?? 24,
    }
  }
  if (statusBody?.lastCleanup) lastResult.value = statusBody.lastCleanup
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    apply(await http.getRetention())
  } catch (e) {
    error.value = '读取保留策略失败：' + (e.message || '请稍后重试')
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  try {
    apply(await http.saveRetention(form.value))
    ElMessage.success('保留策略已保存（热生效）')
  } catch (e) {
    ElMessage.error(e.message || '保存失败')
  } finally {
    saving.value = false
  }
}

async function cleanup() {
  cleaning.value = true
  try {
    const res = await http.cleanupRetention()
    lastResult.value = res
    if (res.skipped) {
      ElMessage.warning('未执行清理：' + res.skipped)
    } else {
      ElMessage.success(`已清理：告警记录 ${res.acksRemoved} 条、报告 ${res.reportFilesRemoved} 个（释放 ${fileSize(res.freedBytes)}）`)
    }
    await load()
  } catch (e) {
    ElMessage.error(e.message || '清理失败')
  } finally {
    cleaning.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.retention-view {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
}
.toolbar-actions {
  display: flex;
  align-items: center;
  gap: 10px;
}
.section {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.section-title {
  font-size: 13px;
  font-weight: 600;
}
.form-row {
  display: flex;
  align-items: center;
  gap: 18px;
  flex-wrap: wrap;
}
.form-item {
  display: flex;
  align-items: center;
  gap: 6px;
}
.label {
  font-size: 13px;
}
.field-hint {
  font-size: 12px;
  color: var(--text-dim);
}
.field-hint.inline {
  margin-left: 4px;
}
.err {
  margin-bottom: 2px;
}
code {
  font-family: var(--mono);
  font-size: 12px;
}
</style>
