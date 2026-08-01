<template>
  <div class="dialtest-view">
    <div class="page-header">
      <div class="header-left">
        <h2 class="page-title">服务拨测</h2>
        <p class="page-desc">HTTP/HTTPS/TCP/ICMP 拨测监控，SSL 证书到期检测</p>
      </div>
      <el-button type="primary" @click="showDialog = true">新建拨测任务</el-button>
    </div>

    <div class="chart-section glass">
      <div class="section-title">拨测任务列表</div>
      <el-table :data="tasks" style="width: 100%" v-loading="loading">
        <el-table-column prop="name" label="任务名称" min-width="120" />
        <el-table-column prop="type" label="类型" width="80">
          <template #default="{ row }"><el-tag size="small">{{ row.type }}</el-tag></template>
        </el-table-column>
        <el-table-column prop="target" label="目标" min-width="200" show-overflow-tooltip />
        <el-table-column prop="interval" label="间隔(s)" width="90" />
        <el-table-column prop="timeout" label="超时(s)" width="90" />
        <el-table-column label="启用" width="80">
          <template #default="{ row }"><el-tag :type="row.enabled ? 'success' : 'info'" size="small">{{ row.enabled ? '是' : '否' }}</el-tag></template>
        </el-table-column>
        <el-table-column label="告警级别" width="100">
          <template #default="{ row }">
            <el-tag :type="sevType(row.severity)" size="small" effect="dark">{{ sevLabel(row.severity) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="通知" width="120">
          <template #default="{ row }">
            <span v-if="!row.notify || row.notify.length === 0" class="muted">全部渠道</span>
            <el-tag v-for="c in (row.notify || [])" :key="c" size="small" class="ch-tag">{{ chLabel(c) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="150">
          <template #default="{ row }">
            <el-button link @click="editTask(row)">编辑</el-button>
            <el-button link type="danger" @click="deleteTask(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <div class="chart-section glass" v-if="results.length > 0">
      <div class="section-title">最近拨测结果</div>
      <el-table :data="results" style="width: 100%">
        <el-table-column prop="name" label="任务名" min-width="120" />
        <el-table-column prop="type" label="类型" width="80" />
        <el-table-column prop="target" label="目标" min-width="200" show-overflow-tooltip />
        <el-table-column label="状态" width="80">
          <template #default="{ row }"><span :class="['dot', row.up ? 'up' : 'down']"></span>{{ row.up ? '正常' : '异常' }}</template>
        </el-table-column>
        <el-table-column label="原因" min-width="220" show-overflow-tooltip>
          <template #default="{ row }">
            <span v-if="!row.up" class="dial-err">{{ row.error || '未知错误' }}</span>
            <span v-else class="muted">-</span>
          </template>
        </el-table-column>
        <el-table-column prop="latency" label="延迟(ms)" width="100" sortable />
        <el-table-column label="证书到期(天)" width="120">
          <template #default="{ row }">
            <span v-if="row.certExpiry" :class="certClass(row.certExpiry)">{{ row.certExpiry.toFixed(0) }} 天</span>
            <span v-else>-</span>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 新建/编辑对话框 -->
    <el-dialog v-model="showDialog" :title="editing ? '编辑拨测任务' : '新建拨测任务'" width="500px">
      <el-form :model="form" label-width="80px">
        <el-form-item label="名称"><el-input v-model="form.name" /></el-form-item>
        <el-form-item label="类型">
          <el-select v-model="form.type">
            <el-option label="HTTP" value="http" />
            <el-option label="HTTPS" value="https" />
            <el-option label="TCP" value="tcp" />
            <el-option label="ICMP" value="icmp" />
          </el-select>
        </el-form-item>
        <el-form-item label="目标"><el-input v-model="form.target" placeholder="host:port 或 host 或 域名" /></el-form-item>
        <el-form-item label="间隔(s)"><el-input-number v-model="form.interval" :min="10" :max="3600" /></el-form-item>
        <el-form-item label="超时(s)"><el-input-number v-model="form.timeout" :min="1" :max="60" /></el-form-item>
        <el-form-item label="启用"><el-switch v-model="form.enabled" /></el-form-item>
        <el-form-item label="告警级别">
          <el-select v-model="form.severity" placeholder="选择严重级别">
            <el-option label="紧急" value="critical" />
            <el-option label="警告" value="warning" />
            <el-option label="信息" value="info" />
          </el-select>
        </el-form-item>
        <el-form-item label="通知渠道">
          <el-select v-model="form.notify" multiple collapse-tags placeholder="留空=全部已启用渠道">
            <el-option label="邮件" value="email" />
            <el-option label="Webhook" value="webhook" />
            <el-option label="钉钉" value="dingtalk" />
            <el-option label="飞书" value="feishu" />
            <el-option label="企业微信" value="wecom" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showDialog = false">取消</el-button>
        <el-button type="primary" @click="saveTask">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import http from '../api/http'

const loading = ref(false)
const tasks = ref([])
const results = ref([])
const showDialog = ref(false)
const editing = ref(false)
const form = ref({ name: '', type: 'http', target: '', interval: 60, timeout: 10, enabled: true, severity: 'warning', notify: [] })

async function load() {
  loading.value = true
  try {
    const [taskData, resData] = await Promise.all([
      http.get('/api/v1/dialtest/tasks'),
      http.get('/api/v1/dialtest/latest'),
    ])
    tasks.value = taskData.tasks || []
    results.value = resData.results || []
  } catch (e) { console.error(e) } finally { loading.value = false }
}

function editTask(row) {
  editing.value = true
  form.value = { ...row }
  showDialog.value = true
}

async function saveTask() {
  try {
    if (editing.value) {
      await http.put(`/api/v1/dialtest/tasks/${form.value.id}`, form.value)
    } else {
      await http.post('/api/v1/dialtest/tasks', form.value)
    }
    showDialog.value = false
    editing.value = false
    form.value = { name: '', type: 'http', target: '', interval: 60, timeout: 10, enabled: true, severity: 'warning', notify: [] }
    await load()
  } catch (e) { console.error(e) }
}

async function deleteTask(row) {
  if (!confirm(`确认删除任务 "${row.name}"？`)) return
  try {
    await http.del(`/api/v1/dialtest/tasks/${row.id}`)
    await load()
  } catch (e) { console.error(e) }
}

function certClass(days) { if (days <= 7) return 'metric-bad'; if (days <= 30) return 'metric-warn'; return 'metric-good' }

function sevLabel(s) { return s === 'critical' ? '紧急' : s === 'info' ? '信息' : '警告' }
function sevType(s) { return s === 'critical' ? 'danger' : s === 'info' ? 'info' : 'warning' }
function chLabel(c) {
  return { email: '邮件', webhook: 'Webhook', dingtalk: '钉钉', feishu: '飞书', wecom: '企业微信' }[c] || c
}

onMounted(load)
</script>

<style scoped>
.dialtest-view { padding: 4px 0 16px; }
.page-header { display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 16px; }
.page-title { font-size: 22px; font-weight: 700; margin: 0; background: linear-gradient(135deg, #e5edf7 0%, #9fb3c8 100%); -webkit-background-clip: text; -webkit-text-fill-color: transparent; }
.page-desc { font-size: 13px; color: var(--text-dim); margin-top: 4px; }
.chart-section { padding: 16px; margin-bottom: 16px; }
.section-title { font-size: 14px; font-weight: 600; margin-bottom: 12px; }
.dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 4px; }
.dot.up { background: #3fb950; }
.dot.down { background: #dc382d; }
.dial-err { color: #dc382d; font-size: 12px; word-break: break-all; }
.muted { color: var(--text-dim); }
.metric-good { color: #3fb950; }
.metric-warn { color: #f0883e; }
.metric-bad { color: #dc382d; }
.ch-tag { margin-right: 4px; }
</style>
