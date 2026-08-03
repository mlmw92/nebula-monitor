<template>
  <el-dialog :model-value="true" title="告警规则" width="620px" @close="$emit('close')">
    <el-form :model="form" label-width="auto" label-position="left">
      <el-form-item label="规则名称" :rules="[{ required: true, message: '请输入规则名称', trigger: 'blur' }]">
        <el-input v-model="form.name" placeholder="例如：主机离线告警" />
      </el-form-item>

      <el-form-item label="规则类型" :rules="[{ required: true, message: '请选择规则类型', trigger: 'change' }]">
        <el-select v-model="form.type" style="width: 100%">
          <el-option value="" label="阈值规则（指标阈值触发）" />
          <el-option value="node_offline" label="主机离线" />
          <el-option value="service_down" label="中间件/服务离线" />
          <el-option value="role_change" label="数据库主从切换" />
          <el-option value="cluster_fault" label="集群状态损坏" />
        </el-select>
        <div class="form-hint">{{ typeHint }}</div>
      </el-form-item>

      <!-- 阈值规则：指标/运算符/阈值 -->
      <template v-if="form.type === ''">
        <el-form-item label="指标" prop="metric" :rules="[{ required: true, message: '请选择指标', trigger: 'change' }]">
          <el-select
            v-model="form.metric"
            filterable
            clearable
            placeholder="搜索并选择指标（支持中文 / 英文）"
            :filter-method="filterMetrics"
            style="width: 100%"
          >
            <el-option-group v-for="g in filteredGroups" :key="g.category" :label="g.category">
              <el-option
                v-for="m in g.metrics"
                :key="m.name"
                :value="m.name"
                :label="`${m.label} (${m.name})`"
              >
                <div class="metric-option">
                  <span class="metric-label">{{ m.label }}</span>
                  <span class="metric-name">{{ m.name }}</span>
                  <span v-if="m.unit" class="metric-unit">{{ m.unit }}</span>
                </div>
              </el-option>
            </el-option-group>
          </el-select>
          <div v-if="selectedMetric" class="form-hint">
            {{ selectedMetric.description }}
            <template v-if="selectedMetric.unit"> · 单位：{{ selectedMetric.unit }}</template>
          </div>
          <div v-else class="form-hint">从下拉列表中选择监控指标，避免手动输入出错</div>
        </el-form-item>
        <el-row :gutter="12">
          <el-col :span="8">
            <el-form-item label="运算符">
              <el-select v-model="form.operator">
                <el-option value=">" label=">" />
                <el-option value=">=" label=">=" />
                <el-option value="<" label="<" />
                <el-option value="<=" label="<=" />
                <el-option value="==" label="==" />
                <el-option value="!=" label="!=" />
              </el-select>
            </el-form-item>
          </el-col>
          <el-col :span="8">
            <el-form-item label="阈值" :rules="[{ required: true, message: '请输入阈值', trigger: 'blur' }]">
              <el-input-number v-model="form.threshold" :step="0.1" :min="0" :precision="2" controls-position="right" placeholder="阈值" style="width: 100%" />
            </el-form-item>
          </el-col>
          <el-col :span="8">
            <el-form-item label="持续">
              <el-select v-model="form.for">
                <el-option value="0" label="立即" />
                <el-option value="1m" label="1 分钟" />
                <el-option value="5m" label="5 分钟" />
                <el-option value="10m" label="10 分钟" />
              </el-select>
            </el-form-item>
          </el-col>
        </el-row>
      </template>

      <!-- 主机离线：仅持续时长 -->
      <template v-else-if="form.type === 'node_offline'">
        <el-row :gutter="12">
          <el-col :span="12">
            <el-form-item label="离线持续">
              <el-select v-model="form.for">
                <el-option value="1m" label="1 分钟" />
                <el-option value="5m" label="5 分钟" />
                <el-option value="10m" label="10 分钟" />
                <el-option value="15m" label="15 分钟" />
              </el-select>
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="触发阈值" v-if="false"></el-form-item>
          </el-col>
        </el-row>
        <div class="form-hint">主机心跳超阈值（由服务端离线判定）持续上述时长后告警；主机恢复在线即自动恢复。无需依赖指标上报。</div>
      </template>

      <!-- 服务离线 / 主从切换 / 集群损坏：选中间件类型 -->
      <template v-else>
        <el-row :gutter="12">
          <el-col :span="12">
            <el-form-item label="中间件类型" :rules="[{ required: true, message: '请选择中间件', trigger: 'change' }]">
              <el-select v-model="form.service" style="width: 100%">
                <el-option value="mysql" label="MySQL" />
                <el-option value="postgres" label="PostgreSQL" />
                <el-option value="redis" label="Redis" />
                <el-option value="nginx" label="Nginx" />
                <el-option value="kafka" label="Kafka" />
                <el-option value="rocketmq" label="RocketMQ" />
                <el-option value="docker" label="Docker" />
                <el-option value="k8s" label="Kubernetes" />
              </el-select>
            </el-form-item>
          </el-col>
          <el-col :span="12" v-if="form.type === 'role_change' || form.type === 'cluster_fault'">
            <el-form-item label="拓扑">
              <el-select v-model="form.topology" style="width: 100%">
                <el-option value="cluster" label="集群（cluster）" />
                <el-option value="replication" label="主从复制（replication）" />
              </el-select>
            </el-form-item>
          </el-col>
        </el-row>

        <!-- 服务离线：保留自定义阈值 -->
        <el-row :gutter="12" v-if="form.type === 'service_down'">
          <el-col :span="8">
            <el-form-item label="判定">
              <el-select v-model="form.operator">
                <el-option value="<=" label="<=（≤）" />
                <el-option value="<" label="<" />
                <el-option value="==" label="==" />
              </el-select>
            </el-form-item>
          </el-col>
          <el-col :span="8">
            <el-form-item label="离线阈值">
              <el-input-number v-model="form.threshold" :step="0.1" :min="0" :precision="2" controls-position="right" style="width: 100%" />
            </el-form-item>
          </el-col>
          <el-col :span="8">
            <el-form-item label="持续">
              <el-select v-model="form.for">
                <el-option value="0" label="立即" />
                <el-option value="1m" label="1 分钟" />
                <el-option value="3m" label="3 分钟" />
                <el-option value="5m" label="5 分钟" />
              </el-select>
            </el-form-item>
          </el-col>
        </el-row>
        <div v-if="form.type === 'service_down'" class="form-hint">
          依据 {{ serviceMetricHint }} 指标（值为 0 即离线）。可直接用默认阈值 ≤ 0.5 触发。
        </div>
        <div v-if="form.type === 'role_change'" class="form-hint">
          监测 {{ serviceMetricHint }} 的 role 标签（PRIMARY/SECONDARY、master/slave）变化，检测到角色切换即触发；下一评估周期无变化自动恢复，重复切换会重复触发。
        </div>
        <div v-if="form.type === 'cluster_fault'" class="form-hint">
          按集群/分组聚合各实例角色，无主（缺少 PRIMARY/主库）或多主（脑裂）即告警；持续上述时长后触发。
        </div>
        <el-row :gutter="12" v-if="form.type === 'role_change' || form.type === 'cluster_fault'">
          <el-col :span="12">
            <el-form-item label="持续">
              <el-select v-model="form.for" v-if="form.type === 'cluster_fault'">
                <el-option value="0" label="立即" />
                <el-option value="1m" label="1 分钟" />
                <el-option value="2m" label="2 分钟" />
                <el-option value="5m" label="5 分钟" />
              </el-select>
              <el-select v-else v-model="form.for" disabled>
                <el-option value="0" label="事件触发（每次切换）" />
              </el-select>
            </el-form-item>
          </el-col>
        </el-row>
      </template>

      <el-row :gutter="12">
        <el-col :span="8">
          <el-form-item label="级别" :rules="[{ required: true, message: '请选择告警级别', trigger: 'change' }]">
            <el-select v-model="form.severity" clearable placeholder="请选择级别">
              <el-option value="critical" label="紧急" />
              <el-option value="warning" label="警告" />
              <el-option value="info" label="信息" />
            </el-select>
          </el-form-item>
        </el-col>
        <el-col :span="8">
          <el-form-item label="分组">
            <el-select v-model="form.group" clearable placeholder="全部">
              <el-option v-for="g in groups" :key="g.name" :value="g.name" :label="g.name" />
            </el-select>
          </el-form-item>
        </el-col>
        <el-col :span="8">
          <el-form-item label="启用">
            <el-switch v-model="form.enabled" />
          </el-form-item>
        </el-col>
      </el-row>
      <el-form-item label="应用范围">
        <el-radio-group v-model="form.scope">
          <el-radio value="all">全部主机</el-radio>
          <el-radio value="specified">指定主机</el-radio>
        </el-radio-group>
      </el-form-item>
      <el-form-item v-if="form.scope === 'specified'" label="选择主机">
        <el-select
          v-model="form.nodes"
          multiple
          filterable
          collapse-tags
          collapse-tags-tooltip
          placeholder="请选择要应用规则的主机（可多选）"
          style="width: 100%"
        >
          <el-option
            v-for="n in nodeList"
            :key="n.hostname"
            :value="n.hostname"
            :label="n.displayName ? n.displayName + ' (' + n.hostname + ')' : n.hostname"
          />
        </el-select>
        <div class="form-hint">
          已选 {{ form.nodes.length }} 台主机；未选时规则对全部主机不生效，请至少选择一台。
        </div>
      </el-form-item>
      <el-form-item label="通知渠道">
        <el-checkbox-group v-model="form.notify">
          <el-checkbox
            v-for="c in channels"
            :key="c.value"
            :value="c.value"
            :disabled="!c.enabled"
          >
            {{ c.enabled ? c.label : c.label + '（未启用）' }}
          </el-checkbox>
        </el-checkbox-group>
        <div class="form-hint">
          勾选要接收告警的渠道；留空表示不发送通知。未启用的渠道请先在「通知配置」中开启。
        </div>
      </el-form-item>

      <!-- 单次静默 -->
      <el-form-item label="临时静默">
        <el-switch v-model="form.silenced" />
        <span class="form-hint" style="margin-left: 12px;">开启后该规则停止评估触发，不影响其他规则</span>
      </el-form-item>
      <el-form-item v-if="form.silenced" label="静默截止">
        <el-date-picker
          v-model="silenceUntilDate"
          type="datetime"
          placeholder="选择静默截止时间（留空表示不限时）"
          style="width: 100%"
        />
        <div class="form-hint">到截止时间后自动解除静默；不选则持续静默直到手动关闭</div>
      </el-form-item>

      <!-- 周期静默时段 -->
      <el-form-item label="静默时段">
        <div style="width: 100%">
          <div v-for="(q, idx) in form.quietPeriods" :key="idx" class="quiet-row">
            <el-select v-model="q.days" multiple collapse-tags placeholder="每天" style="width: 240px">
              <el-option :value="0" label="周日" />
              <el-option :value="1" label="周一" />
              <el-option :value="2" label="周二" />
              <el-option :value="3" label="周三" />
              <el-option :value="4" label="周四" />
              <el-option :value="5" label="周五" />
              <el-option :value="6" label="周六" />
            </el-select>
            <el-time-picker v-model="q.start" format="HH:mm" value-format="HH:mm" placeholder="开始" style="width: 110px" />
            <span class="muted">至</span>
            <el-time-picker v-model="q.end" format="HH:mm" value-format="HH:mm" placeholder="结束" style="width: 110px" />
            <el-button link type="danger" @click="removeQuiet(idx)">删除</el-button>
          </div>
          <el-button size="small" @click="addQuiet">+ 添加静默时段</el-button>
          <div class="form-hint">周期静默时段（按星期+时间区间，支持跨天如 22:00-06:00）内跳过该规则评估触发；与上方临时静默、全局维护窗口构成三层静默。</div>
        </div>
      </el-form-item>

      <!-- 告警升级策略 -->
      <el-form-item label="升级策略">
        <el-switch v-model="form.escalation.enabled" />
        <span class="form-hint" style="margin-left: 12px;">告警持续未恢复超时后升级级别/渠道并重复提醒</span>
      </el-form-item>
      <template v-if="form.escalation.enabled">
        <el-row :gutter="12">
          <el-col :span="8">
            <el-form-item label="升级时间">
              <el-input-number v-model="form.escalation.afterMinutes" :min="0" :step="5" controls-position="right" style="width: 100%" />
              <span class="form-hint">分钟后升级</span>
            </el-form-item>
          </el-col>
          <el-col :span="8">
            <el-form-item label="升级级别">
              <el-select v-model="form.escalation.toSeverity" clearable placeholder="沿用原级别">
                <el-option value="critical" label="紧急" />
                <el-option value="warning" label="警告" />
                <el-option value="info" label="信息" />
              </el-select>
            </el-form-item>
          </el-col>
          <el-col :span="8">
            <el-form-item label="重复提醒">
              <el-input-number v-model="form.escalation.repeatMinutes" :min="0" :step="5" controls-position="right" style="width: 100%" />
              <span class="form-hint">分钟/次（0=不重复）</span>
            </el-form-item>
          </el-col>
        </el-row>
        <el-form-item label="升级渠道">
          <el-checkbox-group v-model="form.escalation.channels">
            <el-checkbox
              v-for="c in channels"
              :key="c.value"
              :value="c.value"
              :disabled="!c.enabled"
            >
              {{ c.enabled ? c.label : c.label + '（未启用）' }}
            </el-checkbox>
          </el-checkbox-group>
          <div class="form-hint">升级后使用的通知渠道；留空表示沿用规则的通知渠道。</div>
        </el-form-item>
      </template>
    </el-form>
    <template #footer>
      <el-button @click="$emit('close')">取消</el-button>
      <el-button type="primary" @click="submit">保存</el-button>
    </template>
  </el-dialog>
</template>

<script setup>
import { reactive, ref, computed, watch, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import http from '../api/http'
import { metricGroups, metricMap } from '../metrics/dictionary'

const props = defineProps({ rule: Object, groups: Array, channels: { type: Array, default: () => [] } })
const emit = defineEmits(['close', 'saved'])

const nodeList = ref([])

const TYPE_HINTS = {
  '': '基于指标阈值触发（传统规则），如 CPU/内存/磁盘使用率。',
  node_offline: '主机心跳超时（服务端判定离线）即触发，无需指标上报。',
  service_down: '中间件/服务探测不可达（*_instance_up=0）即触发。',
  role_change: '监测数据库主从角色变化，发生切换即告警。',
  cluster_fault: '监测集群无主/多主（脑裂），状态损坏即告警。',
}
const typeHint = computed(() => TYPE_HINTS[form.type] || '')
const serviceMetricHint = computed(() => {
  const map = {
    mysql: 'mysql_instance_up', postgres: 'postgres_instance_up', redis: 'redis_instance_up',
    nginx: 'nginx_instance_up', kafka: 'kafka_instance_up', rocketmq: 'rocketmq_instance_up',
    docker: 'docker_container_up', k8s: 'k8s_cluster_up',
  }
  return map[form.service] || form.service + '_instance_up'
})

const form = reactive({
  id: '',
  name: '',
  type: '',
  metric: '',
  operator: '>',
  threshold: null,
  for: '5m',
  severity: '',
  group: '',
  scope: 'all',
  nodes: [],
  enabled: true,
  notify: [],
  silenced: false,
  silenceUntil: 0,
  service: 'mysql',
  topology: 'cluster',
  quietPeriods: [],
  escalation: { enabled: false, afterMinutes: 15, toSeverity: 'critical', repeatMinutes: 30, channels: [] },
})

const silenceUntilDate = computed({
  get: () => form.silenceUntil ? new Date(form.silenceUntil) : null,
  set: (v) => { form.silenceUntil = v ? v.getTime() : 0 },
})

watch(
  () => props.rule,
  (r) => {
    form.id = r ? r.id : ''
    form.name = r ? r.name : ''
    form.type = r && r.type ? r.type : ''
    form.metric = r ? r.metric : ''
    form.operator = r ? r.operator : '>'
    form.threshold = r ? r.threshold : null
    form.for = r ? r.for || '5m' : '5m'
    form.severity = r ? r.severity : ''
    form.group = r ? r.group : ''
    form.scope = r && r.scope ? r.scope : 'all'
    form.nodes = r && r.nodes ? [...r.nodes] : []
    form.enabled = r ? r.enabled : true
    form.notify = r && r.notify ? [...r.notify] : []
    form.silenced = r ? r.silenced || false : false
    form.silenceUntil = r ? r.silenceUntil || 0 : 0
    form.service = r && r.service ? r.service : 'mysql'
    form.topology = r && r.topology ? r.topology : 'cluster'
    form.quietPeriods = r && r.quietPeriods ? r.quietPeriods.map((q) => ({ ...q, days: [...(q.days || [])] })) : []
    const e = r && r.escalation ? r.escalation : null
    form.escalation = {
      enabled: e ? !!e.enabled : false,
      afterMinutes: e && e.afterMinutes ? e.afterMinutes : 15,
      toSeverity: e && e.toSeverity ? e.toSeverity : 'critical',
      repeatMinutes: e && e.repeatMinutes ? e.repeatMinutes : 30,
      channels: e && e.channels ? [...e.channels] : [],
    }
  },
  { immediate: true }
)

onMounted(loadNodes)

async function loadNodes() {
  try {
    const data = await http.get('/api/v1/nodes')
    nodeList.value = data.nodes || []
  } catch {
    nodeList.value = []
  }
}

function addQuiet() {
  form.quietPeriods.push({ days: [], start: '02:00', end: '06:00' })
}
function removeQuiet(idx) {
  form.quietPeriods.splice(idx, 1)
}

// 指标下拉：默认展示全部分组，输入时按中文名/英文名/描述/单位过滤
const filteredGroups = ref(metricGroups)

function filterMetrics(query) {
  const q = (query || '').trim().toLowerCase()
  if (!q) {
    filteredGroups.value = metricGroups
    return
  }
  filteredGroups.value = metricGroups
    .map((g) => ({
      category: g.category,
      metrics: g.metrics.filter(
        (m) =>
          m.name.toLowerCase().includes(q) ||
          m.label.toLowerCase().includes(q) ||
          (m.description || '').toLowerCase().includes(q) ||
          (m.unit || '').toLowerCase().includes(q)
      ),
    }))
    .filter((g) => g.metrics.length > 0)
}

const selectedMetric = computed(() => (form.metric ? metricMap[form.metric] : null))

async function submit() {
  if (!form.name || !form.severity) {
    ElMessage.warning('请填写完整：规则名称、告警级别均为必填项')
    return
  }
  if (form.type === '' && (!form.metric || form.threshold == null)) {
    ElMessage.warning('阈值规则需选择指标并填写阈值')
    return
  }
  if (form.type === 'node_offline' && !form.for) {
    ElMessage.warning('请选择离线持续时长')
    return
  }
  if ((form.type === 'service_down' || form.type === 'role_change' || form.type === 'cluster_fault') && !form.service) {
    ElMessage.warning('请选择中间件类型')
    return
  }
  if (form.scope === 'specified' && (!form.nodes || form.nodes.length === 0)) {
    ElMessage.warning('应用范围为「指定主机」时，请至少选择一台主机')
    return
  }
  const body = {
    name: form.name,
    type: form.type || '',
    metric: form.metric,
    operator: form.operator,
    threshold: form.threshold == null ? 0 : Number(form.threshold),
    for: form.for || '5m',
    severity: form.severity,
    group: form.group,
    scope: form.scope || 'all',
    nodes: form.scope === 'specified' ? form.nodes : [],
    enabled: form.enabled,
    notify: form.notify || [],
    silenced: form.silenced,
    silenceUntil: form.silenceUntil,
    service: form.service,
    topology: form.topology,
    quietPeriods: form.quietPeriods.filter((q) => q.start && q.end),
    escalation: form.escalation.enabled
      ? {
          enabled: true,
          afterMinutes: Number(form.escalation.afterMinutes) || 0,
          toSeverity: form.escalation.toSeverity || '',
          repeatMinutes: Number(form.escalation.repeatMinutes) || 0,
          channels: form.escalation.channels || [],
        }
      : null,
  }
  try {
    if (form.id) await http.put('/api/v1/rules/' + form.id, body)
    else await http.post('/api/v1/rules', body)
    ElMessage.success('规则已保存')
    emit('saved')
  } catch (e) {
    ElMessage.error('保存失败')
  }
}
</script>

<style scoped>
.metric-option {
  display: flex;
  align-items: center;
  gap: 8px;
}
.metric-label {
  font-weight: 500;
}
.metric-name {
  color: var(--text-dim);
  font-size: 12px;
}
.metric-unit {
  margin-left: auto;
  padding: 0 6px;
  font-size: 12px;
  color: var(--el-color-primary);
  background: var(--el-color-primary-light-9);
  border-radius: 4px;
}
.form-hint {
  margin-top: 4px;
  font-size: 12px;
  color: var(--text-dim);
  line-height: 1.5;
}
.quiet-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
}
.muted {
  color: var(--text-dim);
}
</style>
