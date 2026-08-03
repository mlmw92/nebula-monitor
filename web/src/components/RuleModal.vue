<template>
  <el-dialog :model-value="true" title="告警规则" width="680px" @close="$emit('close')">
    <el-form :model="form" label-width="90px" label-position="left" class="rule-form">

      <!-- ── 基本信息 ── -->
      <div class="section">
        <div class="section-title">基本信息</div>
        <el-row :gutter="16">
          <el-col :span="16">
            <el-form-item label="规则名称" required>
              <el-input v-model="form.name" placeholder="例如：主机离线告警" />
            </el-form-item>
          </el-col>
          <el-col :span="8">
            <el-form-item label="启用">
              <el-switch v-model="form.enabled" active-text="开" inactive-text="关" />
            </el-form-item>
          </el-col>
        </el-row>
        <el-row :gutter="16">
          <el-col :span="12">
            <el-form-item label="规则类型" required>
              <el-select v-model="form.type" style="width: 100%">
                <el-option value="" label="阈值规则" />
                <el-option value="node_offline" label="主机离线" />
                <el-option value="service_down" label="服务离线" />
                <el-option value="role_change" label="主从切换" />
                <el-option value="cluster_fault" label="集群损坏" />
              </el-select>
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="告警级别" required>
              <el-select v-model="form.severity" style="width: 100%" placeholder="请选择">
                <el-option value="critical" label="紧急 (Critical)" />
                <el-option value="warning" label="警告 (Warning)" />
                <el-option value="info" label="信息 (Info)" />
              </el-select>
            </el-form-item>
          </el-col>
        </el-row>
        <div class="type-hint">{{ typeHint }}</div>
      </div>

      <!-- ── 触发条件（按类型动态） ── -->
      <div class="section">
        <div class="section-title">触发条件</div>

        <!-- 阈值规则 -->
        <template v-if="form.type === ''">
          <el-form-item label="监控指标" required>
            <el-select
              v-model="form.metric"
              filterable clearable
              placeholder="搜索并选择指标"
              :filter-method="filterMetrics"
              style="width: 100%"
            >
              <el-option-group v-for="g in filteredGroups" :key="g.category" :label="g.category">
                <el-option v-for="m in g.metrics" :key="m.name" :value="m.name" :label="`${m.label} (${m.name})`">
                  <div class="metric-option">
                    <span class="metric-label">{{ m.label }}</span>
                    <span class="metric-name">{{ m.name }}</span>
                    <span v-if="m.unit" class="metric-unit">{{ m.unit }}</span>
                  </div>
                </el-option>
              </el-option-group>
            </el-select>
            <div v-if="selectedMetric" class="field-hint">{{ selectedMetric.description }}<template v-if="selectedMetric.unit"> · 单位：{{ selectedMetric.unit }}</template></div>
          </el-form-item>
          <el-row :gutter="12" align="middle">
            <el-col :span="7">
              <el-form-item label="运算符">
                <el-select v-model="form.operator" style="width: 100%">
                  <el-option value=">" label="大于 &gt;" />
                  <el-option value=">=" label="大于等于 ≥" />
                  <el-option value="<" label="小于 &lt;" />
                  <el-option value="<=" label="小于等于 ≤" />
                  <el-option value="==" label="等于 ==" />
                  <el-option value="!=" label="不等于 ≠" />
                </el-select>
              </el-form-item>
            </el-col>
            <el-col :span="8">
              <el-form-item label="阈值" required>
                <el-input-number v-model="form.threshold" :step="0.1" :min="0" :precision="2" controls-position="right" placeholder="阈值" style="width: 100%" />
              </el-form-item>
            </el-col>
            <el-col :span="9">
              <el-form-item label="持续时间">
                <el-select v-model="form.for" style="width: 100%">
                  <el-option value="0" label="立即触发" />
                  <el-option value="1m" label="1 分钟" />
                  <el-option value="5m" label="5 分钟" />
                  <el-option value="10m" label="10 分钟" />
                  <el-option value="15m" label="15 分钟" />
                  <el-option value="30m" label="30 分钟" />
                </el-select>
              </el-form-item>
            </el-col>
          </el-row>
        </template>

        <!-- 主机离线 -->
        <template v-else-if="form.type === 'node_offline'">
          <el-row :gutter="12">
            <el-col :span="10">
              <el-form-item label="离线持续" required>
                <el-select v-model="form.for" style="width: 100%">
                  <el-option value="1m" label="1 分钟" />
                  <el-option value="3m" label="3 分钟" />
                  <el-option value="5m" label="5 分钟" />
                  <el-option value="10m" label="10 分钟" />
                  <el-option value="15m" label="15 分钟" />
                </el-select>
              </el-form-item>
            </el-col>
          </el-row>
          <div class="field-hint">主机心跳超时（由服务端离线判定）持续上述时长后告警；恢复在线即自动解除。</div>
        </template>

        <!-- 服务离线 / 主从切换 / 集群损坏 -->
        <template v-else>
          <el-row :gutter="12">
            <el-col :span="form.type === 'role_change' || form.type === 'cluster_fault' ? 12 : 24">
              <el-form-item label="中间件" required>
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
              <el-form-item label="拓扑模式">
                <el-select v-model="form.topology" style="width: 100%">
                  <el-option value="cluster" label="集群 (Cluster)" />
                  <el-option value="replication" label="主从复制 (Replication)" />
                </el-select>
              </el-form-item>
            </el-col>
          </el-row>

          <!-- 服务离线的阈值行 -->
          <el-row :gutter="12" v-if="form.type === 'service_down'" align="middle">
            <el-col :span="7">
              <el-form-item label="判定方式">
                <el-select v-model="form.operator" style="width: 100%">
                  <el-option value="<=" label="≤ 小于等于" />
                  <el-option value="<" label="< 小于" />
                  <el-option value="==" label="== 等于" />
                </el-select>
              </el-form-item>
            </el-col>
            <el-col :span="8">
              <el-form-item label="阈值">
                <el-input-number v-model="form.threshold" :step="0.1" :min="0" :precision="2" controls-position="right" style="width: 100%" />
              </el-form-item>
            </el-col>
            <el-col :span="9">
              <el-form-item label="持续">
                <el-select v-model="form.for" style="width: 100%">
                  <el-option value="0" label="立即" />
                  <el-option value="1m" label="1 分钟" />
                  <el-option value="3m" label="3 分钟" />
                  <el-option value="5m" label="5 分钟" />
                </el-select>
              </el-form-item>
            </el-col>
          </el-row>

          <!-- 主从切换/集群损坏的持续 -->
          <el-row :gutter="12" v-if="form.type !== 'service_down'">
            <el-col :span="10">
              <el-form-item label="持续">
                <el-select v-model="form.for" :disabled="form.type === 'role_change'" style="width: 100%">
                  <el-option v-if="form.type === 'role_change'" value="0" label="事件触发（每次切换即告警）" />
                  <template v-else>
                    <el-option value="0" label="立即" />
                    <el-option value="1m" label="1 分钟" />
                    <el-option value="2m" label="2 分钟" />
                    <el-option value="5m" label="5 分钟" />
                  </template>
                </el-select>
              </el-form-item>
            </el-col>
          </el-row>

          <div class="field-hint">
            <template v-if="form.type === 'service_down'">依据 {{ serviceMetricHint }} 指标判定服务可用性（值 0 = 离线），默认 ≤ 0.5 即可触发。</template>
            <template v-else-if="form.type === 'role_change'">监测 {{ serviceMetricHint }} 的 role 标签变化（PRIMARY↔SECONDARY / master↔slave），检测到角色切换即触发；下一周期无变化自动恢复。</template>
            <template v-else>按集群/分组聚合各实例角色，无主（缺少 PRIMARY）或多主（脑裂）即告警。</template>
          </div>
        </template>
      </div>

      <!-- ── 应用范围 ── -->
      <div class="section">
        <div class="section-title">应用范围</div>
        <el-form-item label="目标主机">
          <el-radio-group v-model="form.scope">
            <el-radio value="all">全部主机</el-radio>
            <el-radio value="specified">指定主机</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item v-if="form.scope === 'specified'" label="选择主机" style="margin-bottom: 0">
          <el-select v-model="form.nodes" multiple filterable collapse-tags collapse-tags-tooltip placeholder="请选择主机" style="width: 100%">
            <el-option v-for="n in nodeList" :key="n.hostname" :value="n.hostname" :label="n.displayName ? `${n.displayName} (${n.hostname})` : n.hostname" />
          </el-select>
          <div class="field-hint">已选 {{ form.nodes.length }} 台主机</div>
        </el-form-item>
        <el-form-item label="节点分组">
          <el-select v-model="form.group" clearable placeholder="不限分组" style="width: 240px">
            <el-option v-for="g in groups" :key="g.name" :value="g.name" :label="g.name" />
          </el-select>
        </el-form-item>
      </div>

      <!-- ── 通知渠道 ── -->
      <div class="section">
        <div class="section-title">通知渠道</div>
        <el-form-item label="渠道" style="margin-bottom: 0">
          <el-checkbox-group v-model="form.notify">
            <el-checkbox v-for="c in channels" :key="c.value" :value="c.value" :disabled="!c.enabled">
              {{ c.enabled ? c.label : `${c.label}（未启用）` }}
            </el-checkbox>
          </el-checkbox-group>
          <div class="field-hint">留空则不发送通知；未启用的渠道需先在「通知配置」中开启。</div>
        </el-form-item>
      </div>

      <!-- ── 高级选项（可折叠） ── -->
      <el-collapse v-model="advancedOpen" class="advanced-collapse">
        <el-collapse-item name="silence">
          <template #title>
            <span class="collapse-title">临时静默</span>
            <el-tag v-if="form.silenced" size="small" type="warning" effect="dark" style="margin-left: 8px">已开启</el-tag>
          </template>
          <div class="collapse-body">
            <el-switch v-model="form.silenced" active-text="已静默" inactive-text="未静默" />
            <span class="inline-hint">开启后该规则停止评估触发</span>
            <div v-if="form.silenced" style="margin-top: 12px">
              <el-date-picker v-model="silenceUntilDate" type="datetime" placeholder="截止时间（留空 = 手动关闭前持续静默）" style="width: 100%" />
            </div>
          </div>
        </el-collapse-item>

        <el-collapse-item name="quiet">
          <template #title>
            <span class="collapse-title">周期静默时段</span>
            <el-tag v-if="form.quietPeriods.length" size="small" type="info" effect="plain" style="margin-left: 8px">{{ form.quietPeriods.length }} 条</el-tag>
          </template>
          <div class="collapse-body">
            <div v-for="(q, idx) in form.quietPeriods" :key="idx" class="quiet-row">
              <el-select v-model="q.days" multiple collapse-tags placeholder="每天" style="width: 220px">
                <el-option :value="0" label="周日" /><el-option :value="1" label="周一" />
                <el-option :value="2" label="周二" /><el-option :value="3" label="周三" />
                <el-option :value="4" label="周四" /><el-option :value="5" label="周五" />
                <el-option :value="6" label="周六" />
              </el-select>
              <el-time-picker v-model="q.start" format="HH:mm" value-format="HH:mm" placeholder="开始" style="width: 110px" />
              <span class="quiet-sep">至</span>
              <el-time-picker v-model="q.end" format="HH:mm" value-format="HH:mm" placeholder="结束" style="width: 110px" />
              <el-button link type="danger" @click="removeQuiet(idx)">删除</el-button>
            </div>
            <el-button size="small" @click="addQuiet">+ 添加时段</el-button>
            <div class="field-hint" style="margin-top: 8px">按星期 + 时间区间定期静默，支持跨天（如 22:00–06:00）。与临时静默、全局维护窗口构成三层静默机制。</div>
          </div>
        </el-collapse-item>

        <el-collapse-item name="escalation">
          <template #title>
            <span class="collapse-title">升级策略</span>
            <el-tag v-if="form.escalation.enabled" size="small" type="danger" effect="dark" style="margin-left: 8px">已启用</el-tag>
          </template>
          <div class="collapse-body">
            <el-switch v-model="form.escalation.enabled" active-text="已启用" inactive-text="未启用" />
            <span class="inline-hint">告警持续未恢复超时后升级级别/渠道并重复提醒</span>
            <template v-if="form.escalation.enabled">
              <el-row :gutter="16" style="margin-top: 14px" align="middle">
                <el-col :span="8">
                  <div class="escalation-field">
                    <label>升级时间</label>
                    <el-input-number v-model="form.escalation.afterMinutes" :min="0" :step="5" controls-position="right" style="width: 120px" />
                    <span class="inline-hint">分钟</span>
                  </div>
                </el-col>
                <el-col :span="8">
                  <div class="escalation-field">
                    <label>升级级别</label>
                    <el-select v-model="form.escalation.toSeverity" clearable placeholder="沿用原级别" style="width: 140px">
                      <el-option value="critical" label="紧急" />
                      <el-option value="warning" label="警告" />
                      <el-option value="info" label="信息" />
                    </el-select>
                  </div>
                </el-col>
                <el-col :span="8">
                  <div class="escalation-field">
                    <label>重复提醒</label>
                    <el-input-number v-model="form.escalation.repeatMinutes" :min="0" :step="5" controls-position="right" style="width: 100px" />
                    <span class="inline-hint">分钟/次</span>
                  </div>
                </el-col>
              </el-row>
              <div style="margin-top: 12px">
                <label class="escalation-label">升级渠道</label>
                <el-checkbox-group v-model="form.escalation.channels">
                  <el-checkbox v-for="c in channels" :key="'esc-' + c.value" :value="c.value" :disabled="!c.enabled">
                    {{ c.enabled ? c.label : `${c.label}（未启用）` }}
                  </el-checkbox>
                </el-checkbox-group>
                <div class="field-hint">留空则沿用规则的通知渠道</div>
              </div>
            </template>
          </div>
        </el-collapse-item>
      </el-collapse>
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
const advancedOpen = ref([])

const TYPE_HINTS = {
  '': '基于指标阈值触发的传统规则，适用于 CPU / 内存 / 磁盘等数值指标。',
  node_offline: '当主机心跳超时被服务端判定为离线时立即触发。',
  service_down: '当中间件探测不可达（*_instance_up = 0）时触发。',
  role_change: '监测数据库主从角色变化（PRIMARY ↔ SECONDARY），发生切换即告警。',
  cluster_fault: '按集群聚合各实例角色，出现无主或多主（脑裂）时告警。',
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
  id: '', name: '', type: '', metric: '', operator: '>', threshold: null, for: '5m',
  severity: '', group: '', scope: 'all', nodes: [], enabled: true, notify: [],
  silenced: false, silenceUntil: 0,
  service: 'mysql', topology: 'cluster',
  quietPeriods: [],
  escalation: { enabled: false, afterMinutes: 15, toSeverity: 'critical', repeatMinutes: 30, channels: [] },
})

const silenceUntilDate = computed({
  get: () => form.silenceUntil ? new Date(form.silenceUntil) : null,
  set: (v) => { form.silenceUntil = v ? v.getTime() : 0 },
})

watch(() => props.rule, (r) => {
  form.id = r?.id ?? ''
  form.name = r?.name ?? ''
  form.type = r?.type ?? ''
  form.metric = r?.metric ?? ''
  form.operator = r?.operator ?? '>'
  form.threshold = r?.threshold ?? null
  form.for = r?.for || '5m'
  form.severity = r?.severity ?? ''
  form.group = r?.group ?? ''
  form.scope = r?.scope ?? 'all'
  form.nodes = r?.nodes ? [...r.nodes] : []
  form.enabled = r?.enabled ?? true
  form.notify = r?.notify ? [...r.notify] : []
  form.silenced = r?.silenced || false
  form.silenceUntil = r?.silenceUntil || 0
  form.service = r?.service ?? 'mysql'
  form.topology = r?.topology ?? 'cluster'
  form.quietPeriods = r?.quietPeriods ? r.quietPeriods.map((q) => ({ ...q, days: [...(q.days || [])] })) : []
  const e = r?.escalation ?? null
  form.escalation = {
    enabled: !!e?.enabled,
    afterMinutes: e?.afterMinutes ?? 15,
    toSeverity: e?.toSeverity ?? 'critical',
    repeatMinutes: e?.repeatMinutes ?? 30,
    channels: e?.channels ? [...e.channels] : [],
  }
}, { immediate: true })

onMounted(loadNodes)

async function loadNodes() {
  try { nodeList.value = (await http.get('/api/v1/nodes')).nodes || [] }
  catch { nodeList.value = [] }
}

function addQuiet() { form.quietPeriods.push({ days: [], start: '02:00', end: '06:00' }) }
function removeQuiet(idx) { form.quietPeriods.splice(idx, 1) }

const filteredGroups = ref(metricGroups)
function filterMetrics(query) {
  const q = (query || '').trim().toLowerCase()
  if (!q) { filteredGroups.value = metricGroups; return }
  filteredGroups.value = metricGroups
    .map((g) => ({ category: g.category, metrics: g.metrics.filter((m) => m.name.toLowerCase().includes(q) || m.label.toLowerCase().includes(q) || (m.description || '').toLowerCase().includes(q) || (m.unit || '').toLowerCase().includes(q)) }))
    .filter((g) => g.metrics.length > 0)
}
const selectedMetric = computed(() => (form.metric ? metricMap[form.metric] : null))

async function submit() {
  if (!form.name || !form.severity) { ElMessage.warning('请填写完整：规则名称、告警级别均为必填项'); return }
  if (form.type === '' && (!form.metric || form.threshold == null)) { ElMessage.warning('阈值规则需选择指标并填写阈值'); return }
  if (form.type === 'node_offline' && !form.for) { ElMessage.warning('请选择离线持续时长'); return }
  if (['service_down','role_change','cluster_fault'].includes(form.type) && !form.service) { ElMessage.warning('请选择中间件类型'); return }
  if (form.scope === 'specified' && (!form.nodes || form.nodes.length === 0)) { ElMessage.warning('指定主机时请至少选择一台'); return }
  const body = {
    name: form.name, type: form.type || '', metric: form.metric, operator: form.operator,
    threshold: form.threshold == null ? 0 : Number(form.threshold), for: form.for || '5m',
    severity: form.severity, group: form.group, scope: form.scope || 'all',
    nodes: form.scope === 'specified' ? form.nodes : [], enabled: form.enabled,
    notify: form.notify || [], silenced: form.silenced, silenceUntil: form.silenceUntil,
    service: form.service, topology: form.topology,
    quietPeriods: form.quietPeriods.filter((q) => q.start && q.end),
    escalation: form.escalation.enabled ? { enabled: true, afterMinutes: Number(form.escalation.afterMinutes) || 0, toSeverity: form.escalation.toSeverity || '', repeatMinutes: Number(form.escalation.repeatMinutes) || 0, channels: form.escalation.channels || [] } : null,
  }
  try {
    if (form.id) await http.put('/api/v1/rules/' + form.id, body)
    else await http.post('/api/v1/rules', body)
    ElMessage.success('规则已保存')
    emit('saved')
  } catch { ElMessage.error('保存失败') }
}
</script>

<style scoped>
.rule-form { max-height: 70vh; overflow-y: auto; padding-right: 4px; }

/* 分区 */
.section {
  background: var(--el-fill-color-lighter);
  border-radius: 8px;
  padding: 16px 18px;
  margin-bottom: 14px;
}
.section-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  margin-bottom: 12px;
  padding-left: 8px;
  border-left: 3px solid var(--el-color-primary);
  line-height: 1;
}
.type-hint {
  font-size: 12px;
  color: var(--text-dim);
  margin-top: -4px;
  padding-left: 4px;
}
.field-hint {
  font-size: 12px;
  color: var(--text-dim);
  line-height: 1.5;
  margin-top: 4px;
}

/* 指标下拉 */
.metric-option { display: flex; align-items: center; gap: 8px; }
.metric-label { font-weight: 500; }
.metric-name { color: var(--text-dim); font-size: 12px; }
.metric-unit { margin-left: auto; padding: 0 6px; font-size: 12px; color: var(--el-color-primary); background: var(--el-color-primary-light-9); border-radius: 4px; }

/* 静默时段行 */
.quiet-row { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
.quiet-sep { color: var(--text-dim); font-size: 13px; }

/* 折叠面板 */
.advanced-collapse { border: none; --el-collapse-border-color: transparent; }
.advanced-collapse :deep(.el-collapse-item__header) {
  height: 40px;
  background: var(--el-fill-color-lighter);
  border-radius: 8px;
  padding: 0 14px;
  margin-bottom: 8px;
  border: 1px solid var(--el-border-color-lighter);
  font-size: 13px;
}
.advanced-collapse :deep(.el-collapse-item__wrap) {
  background: var(--el-fill-color-lighter);
  border-radius: 0 0 8px 8px;
  border: 1px solid var(--el-border-color-lighter);
  border-top: none;
  margin-bottom: 8px;
}
.advanced-collapse :deep(.el-collapse-item__content) { padding: 14px 16px; }
.collapse-title { font-weight: 600; font-size: 13px; }
.collapse-body { font-size: 13px; color: var(--el-text-color-regular); }
.inline-hint { font-size: 12px; color: var(--text-dim); margin-left: 8px; }

/* 升级策略内联字段 */
.escalation-field { display: flex; align-items: center; gap: 6px; }
.escalation-field > label { font-size: 12px; color: var(--el-text-color-secondary); white-space: nowrap; }
.escalation-label { font-size: 12px; color: var(--el-text-color-secondary); display: block; margin-bottom: 6px; }

/* 滚动条美化 */
.rule-form::-webkit-scrollbar { width: 5px; }
.rule-form::-webkit-scrollbar-thumb { background: var(--el-border-color); border-radius: 4px; }
</style>
