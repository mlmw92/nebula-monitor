<template>
  <div class="upgrade-view">
    <!-- 当前版本 -->
    <div class="glass panel">
      <div class="panel-title">当前版本</div>
      <div class="ver-grid">
        <div class="ver-item">
          <span class="ver-label">Web</span>
          <span class="ver-val">{{ webVersion }}</span>
        </div>
        <div class="ver-item">
          <span class="ver-label">Server</span>
          <span class="ver-val">{{ currentVersion.server || '...' }}</span>
        </div>
        <div class="ver-item">
          <span class="ver-label">构建时间</span>
          <span class="ver-val">{{ currentVersion.buildTime || '-' }}</span>
        </div>
      </div>
    </div>

    <!-- 上传 -->
    <div class="glass panel">
      <div class="panel-title">上传升级包</div>
      <el-upload
        drag
        :show-file-list="false"
        :http-request="onUpload"
        :before-upload="beforeUpload"
        :disabled="uploading"
        accept=".tar.gz,.tgz"
        class="upload-area"
      >
        <el-icon class="el-icon--upload"><upload-filled /></el-icon>
        <div class="el-upload__text">
          拖拽 upgrade 包到此，或<em>点击上传</em>
        </div>
        <template #tip>
          <div class="el-upload__tip">
            支持 nebula-monitor-v*-upgrade.tar.gz；最大 500MB
          </div>
        </template>
      </el-upload>
      <div v-if="uploading" class="upload-progress">
        <el-progress
          :percentage="uploadProgress"
          :stroke-width="14"
          :status="uploadProgress >= 100 ? 'success' : undefined"
        />
        <div class="upload-progress-text">
          {{ uploadProgress >= 100 ? '服务端解析中…' : '上传中 ' + uploadProgress + '%' }}
        </div>
      </div>
    </div>

    <!-- 待应用 -->
    <div class="glass panel" v-if="pending">
      <div class="panel-title">待应用升级</div>
      <div class="pending-grid">
        <div class="pending-row">
          <span class="r-label">新版本</span>
          <span class="r-val highlight">v{{ pending.version }}</span>
        </div>
        <div class="pending-row">
          <span class="r-label">上传时间</span>
          <span class="r-val">{{ fmtTime(pending.uploadedAt) }}</span>
        </div>
        <div class="pending-row">
          <span class="r-label">Server</span>
          <span class="r-val">
            <el-tag v-if="pending.serverArch" type="success" size="small">{{ pending.serverArch }} · {{ fmtSize(pending.serverSize) }}</el-tag>
            <el-tag v-else type="info" size="small">未包含本架构</el-tag>
          </span>
        </div>
        <div class="pending-row">
          <span class="r-label">Web</span>
          <span class="r-val">
            <el-tag type="success" size="small">{{ fmtSize(pending.webSize) }}</el-tag>
          </span>
        </div>
        <div class="pending-row">
          <span class="r-label">Agent</span>
          <span class="r-val">
            <el-tag v-for="a in pending.agentArches" :key="a" type="success" size="small" style="margin-right: 4px">{{ a }}</el-tag>
            <el-tag v-if="!pending.agentArches || !pending.agentArches.length" type="info" size="small">未包含</el-tag>
          </span>
        </div>
        <div class="pending-row" v-if="pending.components && pending.components.length">
          <span class="r-label">SHA256</span>
          <span class="r-val mono small">{{ pending.components[0].checksum || '-' }}</span>
        </div>
      </div>
      <div class="action-row">
        <el-button @click="cancelPending" :disabled="applying">取消</el-button>
        <el-button
          type="primary"
          :loading="applying"
          :disabled="applying || cooldown > 0"
          @click="doApply"
        >{{ cooldown > 0 ? `请等待 ${cooldown}s` : '立即升级' }}</el-button>
      </div>
      <el-alert
        v-if="applyError"
        :title="applyError"
        type="error"
        show-icon
        :closable="false"
        style="margin-top: 12px"
      />
    </div>

    <!-- 历史 -->
    <div class="glass panel">
      <div class="panel-title-row">
        <span class="panel-title">升级历史</span>
        <el-button size="small" link @click="loadHistory">刷新</el-button>
      </div>
      <el-table :data="history" v-loading="loadingHistory" empty-text="暂无升级记录">
        <el-table-column label="时间" width="180">
          <template #default="{ row }">{{ fmtTime(row.at) }}</template>
        </el-table-column>
        <el-table-column prop="action" label="操作" width="100" />
        <el-table-column prop="version" label="版本" width="120" />
        <el-table-column prop="result" label="结果" width="100">
          <template #default="{ row }">
            <el-tag :type="row.result === 'success' ? 'success' : 'danger'" size="small">
              {{ row.result }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="agentCDNUpdated" label="Agent CDN" width="120">
          <template #default="{ row }">
            <el-tag v-if="row.agentCDNUpdated" type="success" size="small">已更新</el-tag>
            <span v-else>-</span>
          </template>
        </el-table-column>
        <el-table-column prop="detail" label="详情" />
      </el-table>
      <div class="rollback-row" v-if="history.length">
        <el-button type="warning" plain :disabled="applying" @click="doRollback">
          回滚到上一次备份
        </el-button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { UploadFilled } from '@element-plus/icons-vue'
import http from '../api/http'
import { WEB_VERSION } from '../version'

const webVersion = WEB_VERSION
const currentVersion = ref({})
const pending = ref(null)
const applying = ref(false)
const applyError = ref('')
const history = ref([])
const loadingHistory = ref(false)
const uploading = ref(false)
const uploadProgress = ref(0)
const cooldown = ref(0)
let cooldownTimer = null

async function loadCurrentVersion() {
  try { currentVersion.value = await http.get('/api/v1/version') } catch (e) { /* ignore */ }
}
async function loadPending() {
  try {
    const r = await http.get('/api/v1/system/upgrade/current')
    pending.value = r.current || null
  } catch (e) { /* ignore */ }
}
async function loadHistory() {
  loadingHistory.value = true
  try {
    const r = await http.get('/api/v1/system/upgrade/history')
    history.value = r.history || []
  } catch (e) { /* ignore */ }
  loadingHistory.value = false
}

function beforeUpload(file) {
  if (file.size > 500 * 1024 * 1024) {
    ElMessage.error('文件超过 500MB')
    return false
  }
  return true
}

async function onUpload(option) {
  const fd = new FormData()
  fd.append('file', option.file)
  uploading.value = true
  uploadProgress.value = 0
  try {
    const task = await http.upload('/api/v1/system/upgrade/upload', fd, (pct) => {
      uploadProgress.value = pct
    })
    uploadProgress.value = 100
    pending.value = task
    ElMessage.success('升级包解析成功：v' + task.version)
    option.onSuccess && option.onSuccess(task)
  } catch (e) {
    ElMessage.error('上传失败：' + e.message)
    option.onError && option.onError(e)
  } finally {
    uploading.value = false
  }
}

function cancelPending() {
  // 后端暂未提供取消已上传包的接口，重新上传会覆盖；
  // 这里仅清空本地预览
  pending.value = null
  applyError.value = ''
}

async function doApply() {
  try {
    await ElMessageBox.confirm(
      '升级 Server 会重启 monitor-server，期间 Web 端将短暂不可用。新 Agent 二进制将复制到自带 CDN，但不会自动推送到主机（请到「主机列表」手动点击「升级」）。是否继续？',
      '确认升级',
      { type: 'warning', confirmButtonText: '立即升级', cancelButtonText: '取消' }
    )
  } catch { return }
  applying.value = true
  applyError.value = ''
  try {
    await http.post('/api/v1/system/upgrade/apply?operator=web', {})
    ElMessage.success('升级已提交，server 即将重启（约 5-15 秒）。请稍候刷新页面。')
    startCooldown(15)
    setTimeout(() => window.location.reload(), 8000)
  } catch (e) {
    applyError.value = e.message
    ElMessage.error('升级失败：' + e.message)
    await loadPending()
    await loadHistory()
  } finally {
    applying.value = false
  }
}

async function doRollback() {
  try {
    await ElMessageBox.confirm(
      '确认回滚到最近一次备份？将替换当前 server 与 web 并重启，期间服务短暂不可用。',
      '回滚',
      { type: 'warning' }
    )
  } catch { return }
  applying.value = true
  try {
    await http.post('/api/v1/system/upgrade/rollback?operator=web', {})
    ElMessage.success('回滚已提交，server 即将重启。请稍候刷新。')
    setTimeout(() => window.location.reload(), 8000)
  } catch (e) {
    ElMessage.error('回滚失败：' + e.message)
  } finally {
    applying.value = false
  }
}

function fmtTime(s) {
  if (!s) return '-'
  return new Date(s).toLocaleString()
}
function fmtSize(b) {
  if (!b) return '-'
  const u = ['B', 'KB', 'MB', 'GB']
  let i = 0
  while (b >= 1024 && i < u.length - 1) { b /= 1024; i++ }
  return b.toFixed(1) + ' ' + u[i]
}

function startCooldown(sec) {
  clearInterval(cooldownTimer)
  cooldown.value = sec
  cooldownTimer = setInterval(() => {
    cooldown.value -= 1
    if (cooldown.value <= 0) {
      clearInterval(cooldownTimer)
      cooldownTimer = null
    }
  }, 1000)
}

onMounted(async () => {
  await loadCurrentVersion()
  await loadPending()
  await loadHistory()
})

onUnmounted(() => {
  if (cooldownTimer) clearInterval(cooldownTimer)
})
</script>

<style scoped>
.upgrade-view {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.panel-title {
  font-size: 14px;
  font-weight: 600;
  margin-bottom: 12px;
}
.panel-title-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}
.ver-grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 16px;
}
.ver-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.ver-label {
  font-size: 11px;
  color: var(--text-muted);
}
.ver-val {
  font-family: var(--mono);
  font-size: 14px;
}
.upload-area :deep(.el-upload-dragger) {
  background: rgba(255, 255, 255, 0.02);
  border: 1px dashed rgba(0, 200, 150, 0.3);
}
.upload-progress {
  margin-top: 12px;
}
.upload-progress-text {
  font-size: 12px;
  color: var(--text-muted);
  margin-top: 6px;
  text-align: center;
}
.pending-grid {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.pending-row {
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 13px;
}
.r-label {
  color: var(--text-muted);
  width: 80px;
  flex-shrink: 0;
}
.r-val.highlight {
  font-family: var(--mono);
  font-size: 16px;
  color: var(--accent);
  font-weight: 600;
}
.r-val.mono {
  font-family: var(--mono);
}
.r-val.small {
  font-size: 11px;
}
.action-row {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 16px;
}
.rollback-row {
  display: flex;
  justify-content: flex-end;
  margin-top: 12px;
}
</style>