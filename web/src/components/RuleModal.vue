<template>
  <el-dialog :model-value="true" title="告警规则" width="580px" @close="$emit('close')">
    <el-form :model="form" label-width="auto" label-position="left">
      <el-form-item label="规则名称" :rules="[{ required: true, message: '请输入规则名称', trigger: 'blur' }]">
        <el-input v-model="form.name" placeholder="例如：CPU 高使用率告警" />
      </el-form-item>
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
      <el-form-item label="通知渠道">
        <el-select
          v-model="form.notify"
          multiple
          clearable
          placeholder="不选则发送给全部已启用渠道"
          style="width: 100%"
        >
          <el-option
            v-for="c in channels"
            :key="c.value"
            :value="c.value"
            :label="c.enabled ? c.label : c.label + '（未启用）'"
            :disabled="!c.enabled"
          />
        </el-select>
        <div class="form-hint">
          仅推送告警到所选渠道；留空表示使用全部已启用渠道。未启用的渠道请先在「通知配置」中开启。
        </div>
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="$emit('close')">取消</el-button>
      <el-button type="primary" @click="submit">保存</el-button>
    </template>
  </el-dialog>
</template>

<script setup>
import { reactive, ref, computed, watch } from 'vue'
import { ElMessage } from 'element-plus'
import http from '../api/http'
import { metricGroups, metricMap } from '../metrics/dictionary'

const props = defineProps({ rule: Object, groups: Array, channels: { type: Array, default: () => [] } })
const emit = defineEmits(['close', 'saved'])

const form = reactive({
  id: '',
  name: '',
  metric: '',
  operator: '>',
  threshold: null,
  for: '5m',
  severity: '',
  group: '',
  enabled: true,
  notify: [],
})

watch(
  () => props.rule,
  (r) => {
    form.id = r ? r.id : ''
    form.name = r ? r.name : ''
    form.metric = r ? r.metric : ''
    form.operator = r ? r.operator : '>'
    form.threshold = r ? r.threshold : null
    form.for = r ? r.for || '5m' : '5m'
    form.severity = r ? r.severity : ''
    form.group = r ? r.group : ''
    form.enabled = r ? r.enabled : true
    form.notify = r && r.notify ? [...r.notify] : []
  },
  { immediate: true }
)

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
  if (!form.name || !form.metric || form.threshold == null || !form.severity) {
    ElMessage.warning('请填写完整：规则名称、指标、阈值、告警级别均为必填项')
    return
  }
  const body = {
    name: form.name,
    metric: form.metric,
    operator: form.operator,
    threshold: Number(form.threshold),
    for: form.for,
    severity: form.severity,
    group: form.group,
    enabled: form.enabled,
    notify: form.notify || [],
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
  color: var(--muted, #909399);
  font-size: 12px;
}
.metric-unit {
  margin-left: auto;
  padding: 0 6px;
  font-size: 12px;
  color: var(--el-color-primary, #409eff);
  background: var(--el-color-primary-light-9, #ecf5ff);
  border-radius: 4px;
}
.form-hint {
  margin-top: 4px;
  font-size: 12px;
  color: var(--muted, #909399);
  line-height: 1.5;
}
</style>
