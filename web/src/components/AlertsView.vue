<template>
  <div>
    <!-- 告警统计 -->
    <div class="alert-stats">
      <div class="glass panel kpi">
        <div class="kpi-label">活跃告警</div>
        <div class="kpi-value red">{{ activeCount }}</div>
      </div>
      <div class="glass panel kpi">
        <div class="kpi-label">紧急</div>
        <div class="kpi-value red">{{ criticalCount }}</div>
      </div>
      <div class="glass panel kpi">
        <div class="kpi-label">警告</div>
        <div class="kpi-value amber">{{ warningCount }}</div>
      </div>
      <div class="glass panel kpi">
        <div class="kpi-label">规则总数</div>
        <div class="kpi-value cyan">{{ rules.length }}</div>
      </div>
    </div>

    <!-- 告警规则 -->
    <div class="glass panel" style="margin-bottom: 16px">
      <div class="panel-title-row">
        <span class="panel-title" style="margin-bottom: 0">告警规则</span>
        <el-button type="primary" size="small" :icon="Plus" @click="newRule">新建规则</el-button>
      </div>
      <el-table :data="rules" stripe style="width: 100%" empty-text="暂无规则">
        <el-table-column prop="name" label="名称" min-width="140" />
        <el-table-column label="触发条件" min-width="180">
          <template #default="{ row }">
            <span class="mono">{{ row.metric }} {{ row.operator }} {{ row.threshold }}</span>
          </template>
        </el-table-column>
        <el-table-column label="级别" width="90">
          <template #default="{ row }">
            <el-tag :type="sevType(row.severity)" size="small" effect="dark">{{ sevLabel(row.severity) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="通知渠道" min-width="150">
          <template #default="{ row }">
            <template v-if="row.notify && row.notify.length">
              <el-tag v-for="c in row.notify" :key="c" size="small" style="margin: 0 4px 4px 0">{{ channelLabel(c) }}</el-tag>
            </template>
            <span v-else class="muted">不发送</span>
          </template>
        </el-table-column>
        <el-table-column label="持续" width="80">
          <template #default="{ row }">{{ row.for === '0' ? '立即' : row.for }}</template>
        </el-table-column>
        <el-table-column label="启用" width="80">
          <template #default="{ row }">
            <el-tag :type="row.enabled ? 'success' : 'info'" size="small">{{ row.enabled ? '启用' : '停用' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="130">
          <template #default="{ row }">
            <el-button link size="small" @click="edit(row)">编辑</el-button>
            <el-button link type="danger" size="small" @click="del(row.id)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 告警事件 -->
    <div class="glass panel">
      <div class="panel-title-row">
        <span class="panel-title" style="margin-bottom: 0">告警事件</span>
        <el-radio-group v-model="eventFilter" size="small">
          <el-radio-button value="active">活跃</el-radio-button>
          <el-radio-button value="resolved">已恢复</el-radio-button>
          <el-radio-button value="">全部</el-radio-button>
        </el-radio-group>
      </div>
      <el-table :data="filteredAlerts" stripe style="width: 100%" empty-text="暂无告警事件">
        <el-table-column prop="ruleName" label="规则" min-width="140" />
        <el-table-column prop="node" label="节点" min-width="130" />
        <el-table-column label="级别" width="80">
          <template #default="{ row }">
            <el-tag :type="sevType(row.severity)" size="small" effect="dark">{{ sevLabel(row.severity) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag :type="row.state === 'firing' ? 'danger' : 'success'" size="small" effect="dark">
              {{ row.state === 'firing' ? '告警中' : '已恢复' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="message" label="详情" min-width="200" show-overflow-tooltip />
        <el-table-column label="时间" width="160">
          <template #default="{ row }">{{ fmt(row.startsAt) }}</template>
        </el-table-column>
      </el-table>
    </div>

    <RuleModal v-if="editing" :rule="editing" :groups="groups" :channels="channelOptions" @close="editing = null" @saved="onSaved" />
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { Plus } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import http from '../api/http'
import RuleModal from './RuleModal.vue'

const rules = ref([])
const alerts = ref([])
const groups = ref([])
const editing = ref(null)
const eventFilter = ref('active')
// 全部渠道及其展示名；enabled 由通知配置决定，未启用的渠道在新建规则时置灰。
const CHANNEL_META = [
  { value: 'email', label: '邮件' },
  { value: 'webhook', label: 'Webhook' },
  { value: 'dingtalk', label: '钉钉' },
  { value: 'feishu', label: '飞书' },
  { value: 'wecom', label: '企业微信' },
]
const channelOptions = ref(CHANNEL_META.map((c) => ({ ...c, enabled: true })))
let timer = null

const activeCount = computed(() => alerts.value.filter((a) => a.state === 'firing').length)
const criticalCount = computed(() => alerts.value.filter((a) => a.state === 'firing' && a.severity === 'critical').length)
const warningCount = computed(() => alerts.value.filter((a) => a.state === 'firing' && a.severity === 'warning').length)

const filteredAlerts = computed(() => {
  if (!eventFilter.value) return alerts.value
  return alerts.value.filter((a) => a.state === eventFilter.value)
})

function sevType(s) {
  return { critical: 'danger', warning: 'warning', info: 'info' }[s] || 'info'
}
function sevLabel(s) {
  return { critical: '紧急', warning: '警告', info: '信息' }[s] || s
}
function channelLabel(v) {
  return (CHANNEL_META.find((c) => c.value === v) || {}).label || v
}
function fmt(ts) {
  if (!ts) return '-'
  return new Date(ts).toLocaleString('zh-CN', { hour12: false })
}

async function load() {
  try {
    rules.value = (await http.get('/api/v1/rules')).rules || []
  } catch (e) {
    /* ignore */
  }
  try {
    alerts.value = (await http.get('/api/v1/alerts')).alerts || []
  } catch (e) {
    /* ignore */
  }
  try {
    groups.value = (await http.get('/api/v1/groups')).groups || []
  } catch (e) {
    /* ignore */
  }
  try {
    const cfg = await http.get('/api/v1/notify')
    const enabled = {
      email: cfg.email && cfg.email.enabled,
      webhook: cfg.webhook && cfg.webhook.enabled,
      dingtalk: cfg.dingtalk && cfg.dingtalk.enabled,
      feishu: cfg.feishu && cfg.feishu.enabled,
      wecom: cfg.wecom && cfg.wecom.enabled,
    }
    channelOptions.value = CHANNEL_META.map((c) => ({ ...c, enabled: !!enabled[c.value] }))
  } catch (e) {
    /* ignore */
  }
}

function newRule() {
  // 设为空对象（truthy）以触发 RuleModal 渲染（v-if="editing"）
  editing.value = {}
}
function edit(rule) {
  editing.value = { ...rule }
}
async function del(id) {
  try {
    await ElMessageBox.confirm('确认删除该规则？', '提示', { type: 'warning' })
    await http.del('/api/v1/rules/' + id)
    ElMessage.success('已删除')
    load()
  } catch (e) {
    /* 取消 */
  }
}
function onSaved() {
  editing.value = null
  load()
}

// 每 30s 自动刷新告警列表，确保实时性
onMounted(() => {
  load()
  timer = setInterval(load, 30000)
})

onUnmounted(() => {
  if (timer) clearInterval(timer)
})
</script>

<style scoped>
.panel-title-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 14px;
}
.muted {
  color: var(--muted, #909399);
}
</style>
