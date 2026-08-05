<template>
  <div class="upgrade-view">
    <!-- 当前版本 -->
    <div class="glass panel">
      <div class="panel-title">当前版本</div>
      <div class="ver-grid">
        <div class="ver-item">
          <span class="ver-label">版本</span>
          <span class="ver-val">{{ currentVersion.server || '...' }}</span>
        </div>
        <div class="ver-item">
          <span class="ver-label">构建时间</span>
          <span class="ver-val">{{ fmtBuildTime(currentVersion.buildTime) }}</span>
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
      <div class="panel-title">待升级</div>
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
      <el-table :data="pagedHistory" v-loading="loadingHistory" empty-text="暂无升级记录">
        <el-table-column label="时间" width="180">
          <template #default="{ row }">{{ fmtTime(row.at) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="100">
          <template #default="{ row }">
            <el-tag :type="row.action === 'rollback' ? 'warning' : 'primary'" size="small" effect="plain">{{ actionLabel(row.action) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="version" label="版本" width="120" />
        <el-table-column label="结果" width="100">
          <template #default="{ row }">
            <el-tag :type="row.result === 'success' ? 'success' : 'danger'" size="small" effect="dark">{{ resultLabel(row.result) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="detail" label="详情" />
        <el-table-column label="回退" width="100" fixed="right">
          <template #default="{ row }">
            <el-button
              size="small"
              type="warning"
              plain
              :disabled="!canRollbackTo(row.version) || rollingBack"
              @click="rollbackTo(row.version)"
            >切换</el-button>
          </template>
        </el-table-column>
      </el-table>
      <el-pagination
        v-if="history.length"
        class="hist-pager"
        layout="total, sizes, prev, pager, next"
        :total="history.length"
        :page-size="historyPageSize"
        :current-page="historyPage"
        :page-sizes="[10, 20, 50]"
        background
        @current-change="(p) => (historyPage = p)"
        @size-change="(s) => { historyPageSize = s; historyPage = 1 }"
      />
      <div class="rollback-row" v-if="history.length">
        <el-button type="warning" plain :disabled="applying" @click="doRollback">
          回滚到上一次备份
        </el-button>
      </div>
    </div>

    <!-- IP 地理库（独立入口：只替换归属地数据，不影响 Server/Web/Agent，不重启服务） -->
    <div class="glass panel">
      <div class="panel-title-row">
        <span class="panel-title">IP 地理库</span>
        <el-button size="small" link @click="loadGeoip">刷新</el-button>
      </div>
      <div class="pending-grid" v-loading="geoipLoading">
        <div class="pending-row">
          <span class="r-label">当前库</span>
          <span class="r-val">
            <el-tag :type="geoip.source === 'custom' ? 'success' : 'info'" size="small" effect="plain">
              {{ geoip.source === 'custom' ? '已更新的库' : '程序内置库' }}
            </el-tag>
            <el-tag v-if="geoip.loaded === false" type="danger" size="small" effect="dark" style="margin-left: 6px">未加载</el-tag>
          </span>
        </div>
        <div class="pending-row">
          <span class="r-label">大小</span>
          <span class="r-val">{{ fmtSize(geoip.size) }}</span>
        </div>
        <div class="pending-row">
          <span class="r-label">更新时间</span>
          <span class="r-val">{{ geoip.updatedAt ? fmtTime(geoip.updatedAt) : '-' }}</span>
        </div>
        <div class="pending-row">
          <span class="r-label">存放路径</span>
          <span class="r-val mono small">{{ geoip.path || '未配置' }}</span>
        </div>
        <div class="pending-row">
          <span class="r-label">SHA256</span>
          <span class="r-val mono small">{{ geoip.sha256 || '-' }}</span>
        </div>
      </div>

      <el-alert
        v-if="geoip.error"
        :title="geoip.error"
        type="error"
        show-icon
        :closable="false"
        style="margin: 12px 0"
      />
      <el-alert
        v-else-if="geoip.editable === false"
        title="未配置 IP 地理库存放路径（server.yaml 的 geoipFile），当前仅能使用程序内置库"
        type="warning"
        show-icon
        :closable="false"
        style="margin: 12px 0"
      />

      <el-upload
        drag
        :show-file-list="false"
        :http-request="onGeoipUpload"
        :before-upload="beforeGeoipUpload"
        :disabled="geoipUploading || geoip.editable === false"
        accept=".xdb"
        class="upload-area"
        style="margin-top: 12px"
      >
        <el-icon class="el-icon--upload"><upload-filled /></el-icon>
        <div class="el-upload__text">拖拽 ip2region 库文件到此，或<em>点击上传</em></div>
        <template #tip>
          <div class="el-upload__tip">
            支持 ip2region v4（IPv4）的 .xdb 文件；上传后立即生效，无需重启服务或重新升级系统
          </div>
          <div class="el-upload__tip" style="margin-top: 4px; color: #909399">
            升级包下载：ip2region 官方仓库
            <el-link type="primary" href="https://github.com/lionsoul2014/ip2region" target="_blank" :underline="false">github.com/lionsoul2014/ip2region</el-link>
            的 data 目录下获取最新的 ip2region.xdb（需为 v4 格式），下载后将文件后缀/命名保留为 .xdb 即可上传
          </div>
        </template>
      </el-upload>
      <div v-if="geoipUploading" class="upload-progress">
        <el-progress
          :percentage="geoipProgress"
          :stroke-width="14"
          :status="geoipProgress >= 100 ? 'success' : undefined"
        />
        <div class="upload-progress-text">
          {{ geoipProgress >= 100 ? '服务端校验中…' : '上传中 ' + geoipProgress + '%' }}
        </div>
      </div>

      <div class="geo-test-row">
        <el-input
          v-model="geoipTestIP"
          placeholder="输入 IP 验证归属地，如 114.114.114.114"
          clearable
          style="max-width: 320px"
          @keyup.enter="doGeoipTest"
        />
        <el-button :loading="geoipTesting" @click="doGeoipTest">查询</el-button>
        <span v-if="geoipTestResult" class="geo-test-result">
          {{ geoipTestResult }}
        </span>
      </div>

      <el-table
        v-if="geoip.samples && geoip.samples.length"
        :data="geoip.samples"
        size="small"
        style="margin-top: 12px"
      >
        <el-table-column prop="ip" label="样本 IP" width="160" />
        <el-table-column prop="country" label="国家" width="120" />
        <el-table-column prop="province" label="省份" width="120" />
        <el-table-column prop="city" label="城市" width="120" />
        <el-table-column prop="region" label="原始记录" />
      </el-table>

      <div class="rollback-row" v-if="geoip.source === 'custom'">
        <el-button type="warning" plain :disabled="geoipUploading" @click="doGeoipReset">
          恢复程序内置库
        </el-button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { UploadFilled } from '@element-plus/icons-vue'
import http from '../api/http'
import { WEB_VERSION } from '../version'

const currentVersion = ref({ server: WEB_VERSION }) // 初始用构建内嵌版本，加载后覆盖为 Server 实际运行版本
const pending = ref(null)
const applying = ref(false)
const applyError = ref('')
const history = ref([])
const loadingHistory = ref(false)
const historyPage = ref(1)
const historyPageSize = ref(10)
const pagedHistory = computed(() => {
  const start = (historyPage.value - 1) * historyPageSize.value
  return history.value.slice(start, start + historyPageSize.value)
})
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

// 已归档版本列表（供升级历史表格判断哪些版本可切换）。
const archiveVersions = ref([])
const archiveLoading = ref(false)
const rollingBack = ref(false)

async function loadArchive() {
  archiveLoading.value = true
  try {
    const r = await http.get('/api/v1/system/upgrade/archive')
    archiveVersions.value = r.versions || []
  } catch (e) { /* ignore */ }
  archiveLoading.value = false
}

// 该版本是否可回退：必须是已归档版本、且不是当前运行版本。
function canRollbackTo(version) {
  if (!version || version === currentVersion.value.server) return false
  return archiveVersions.value.some(a => a.version === version)
}

async function rollbackTo(version) {
  try {
    await ElMessageBox.confirm(
      `确认切换到版本 v${version}？将按该版本重新替换 Server / Web / Agent 并重启 monitor-server，期间服务短暂不可用。该操作同样会生成备份，可继续向后切换或回退。`,
      '切换到指定版本',
      { type: 'warning', confirmButtonText: '切换', cancelButtonText: '取消' }
    )
  } catch { return }
  rollingBack.value = true
  try {
    await http.post('/api/v1/system/upgrade/rollback-to', { version, operator: 'web' })
    ElMessage.success(`已切换到 v${version}，server 即将重启。请稍候刷新页面。`)
    setTimeout(() => window.location.reload(), 8000)
  } catch (e) {
    ElMessage.error('切换失败：' + e.message)
    await loadArchive()
  } finally {
    rollingBack.value = false
  }
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
    await loadArchive()
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
function fmtBuildTime(s) {
  if (!s || s === 'unknown') return '-'
  try {
    return new Date(s).toLocaleString()
  } catch {
    return s
  }
}
function actionLabel(a) {
  if (a === 'apply') return '升级'
  if (a === 'rollback') return '回滚'
  return a || '-'
}
function resultLabel(r) {
  if (r === 'success') return '成功'
  if (r === 'failed') return '失败'
  return r || '-'
}
function fmtSize(b) {
  if (!b) return '-'
  const u = ['B', 'KB', 'MB', 'GB']
  let i = 0
  while (b >= 1024 && i < u.length - 1) { b /= 1024; i++ }
  return b.toFixed(1) + ' ' + u[i]
}

// ===== IP 地理库（独立于系统升级：只替换归属地数据文件并热加载） =====
const geoip = ref({})
const geoipLoading = ref(false)
const geoipUploading = ref(false)
const geoipProgress = ref(0)
const geoipTestIP = ref('')
const geoipTesting = ref(false)
const geoipTestResult = ref('')

async function loadGeoip() {
  geoipLoading.value = true
  try {
    geoip.value = await http.get('/api/v1/system/geoip')
  } catch (e) { /* ignore */ }
  geoipLoading.value = false
}

function beforeGeoipUpload(file) {
  if (!file.name.toLowerCase().endsWith('.xdb')) {
    ElMessage.error('仅支持 ip2region 的 .xdb 文件')
    return false
  }
  if (file.size > 64 * 1024 * 1024) {
    ElMessage.error('文件超过 64MB')
    return false
  }
  return true
}

async function onGeoipUpload(option) {
  const fd = new FormData()
  fd.append('file', option.file)
  geoipUploading.value = true
  geoipProgress.value = 0
  try {
    const r = await http.upload('/api/v1/system/geoip/upload', fd, (pct) => {
      geoipProgress.value = pct
    })
    geoipProgress.value = 100
    geoip.value = r
    ElMessage.success('IP 地理库已更新并生效')
    option.onSuccess && option.onSuccess(r)
  } catch (e) {
    ElMessage.error('更新失败：' + e.message)
    option.onError && option.onError(e)
  } finally {
    geoipUploading.value = false
  }
}

async function doGeoipReset() {
  try {
    await ElMessageBox.confirm('将删除已上传的地理库并恢复为程序内置库，是否继续？', '恢复内置库', {
      type: 'warning',
    })
  } catch { return }
  try {
    geoip.value = await http.post('/api/v1/system/geoip/reset', {})
    ElMessage.success('已恢复为程序内置库')
  } catch (e) {
    ElMessage.error('恢复失败：' + e.message)
  }
}

async function doGeoipTest() {
  const ip = (geoipTestIP.value || '').trim()
  if (!ip) {
    ElMessage.warning('请输入 IP')
    return
  }
  geoipTesting.value = true
  geoipTestResult.value = ''
  try {
    const r = await http.get('/api/v1/system/geoip/test?ip=' + encodeURIComponent(ip))
    geoipTestResult.value = [r.country, r.province, r.city].filter(Boolean).join(' / ') || r.region || '无结果'
  } catch (e) {
    ElMessage.error(e.message)
  } finally {
    geoipTesting.value = false
  }
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
  await loadArchive()
  await loadGeoip()
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
.hist-pager {
  display: flex;
  justify-content: flex-end;
  margin-top: 12px;
}
.rollback-row {
  display: flex;
  justify-content: flex-end;
  margin-top: 12px;
}
.geo-test-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 12px;
  flex-wrap: wrap;
}
.geo-test-result {
  font-size: 13px;
  color: var(--accent);
  font-family: var(--mono);
}
</style>