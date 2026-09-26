<template>
  <div class="template-tab">
    <div class="tip-bar glass">
      <span>
        该类型由采集项模板 <b>{{ type }}</b> 派生（模板可在「中间件监控 → 采集项模板」维护）。
        模板改动随 Agent 上报下发，通常一个采集周期内生效。
      </span>
    </div>

    <div class="chart-section glass">
      <div class="section-title">{{ label }} 实例</div>
      <el-table :data="instances" style="width: 100%" v-loading="loading">
        <el-table-column prop="instance" label="实例" min-width="180" show-overflow-tooltip />
        <el-table-column prop="node" label="节点" width="160" show-overflow-tooltip />
        <el-table-column prop="group" label="分组" width="120" />
        <el-table-column label="采集状态" width="120">
          <template #default="{ row }">
            <span class="status-led" :class="row.up ? 'on' : 'off'"></span>
            <span class="status-text" :class="row.up ? 'on' : 'off'">{{ row.up ? '正常' : '采集失败' }}</span>
          </template>
        </el-table-column>
        <el-table-column v-for="col in metricColumns" :key="col.key" :label="col.label" min-width="130">
          <template #default="{ row }">
            <span v-if="valueOf(row, col.key) === null" class="muted">-</span>
            <span v-else>{{ valueOf(row, col.key) }}{{ col.unit ? ' ' + col.unit : '' }}</span>
          </template>
        </el-table-column>
      </el-table>

      <!-- 「已配置但无数据」必须说清排查方向：否则用户只知道没数据，不知道从哪查 -->
      <el-empty v-if="!loading && instances.length === 0" :description="emptyText" />
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import http from '../../api/http'

const props = defineProps({
  type: { type: String, required: true },
  label: { type: String, default: '' },
})

const loading = ref(false)
const instances = ref([])

const emptyText = computed(
  () => `模板 ${props.type} 已配置，但暂无实例数据：请确认应用该模板的节点分组（groups）是否与 Agent 实际分组一致、`
    + '目标地址是否可达，以及 Agent 是否已收到下发（Agent 日志有「已应用 Server 下发的采集项模板」）。'
)

// 摘要指标的列：取第一个实例的指标集合（所有实例的指标集合一致，由模板的 rules.metrics 决定）
const metricColumns = computed(() => {
  const first = instances.value.find((i) => (i.metrics || []).length > 0)
  return (first?.metrics || []).map((m) => ({ key: m.key, label: m.label || m.key, unit: m.unit || '' }))
})

function valueOf(row, key) {
  const hit = (row.metrics || []).find((m) => m.key === key)
  return hit ? hit.value : null
}

async function load() {
  loading.value = true
  try {
    const data = await http.get(`/api/v1/middleware/${encodeURIComponent(props.type)}/instances`)
    instances.value = data.instances || []
  } catch (e) {
    console.error('加载模板实例失败', e)
    instances.value = []
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.template-tab {
  padding: 0 0 16px;
}
.tip-bar {
  padding: 10px 14px;
  margin-bottom: 12px;
  font-size: 13px;
  color: var(--text-dim);
  border-radius: var(--radius);
}
.chart-section {
  padding: 16px;
  border-radius: var(--radius);
}
.section-title {
  font-size: 14px;
  font-weight: 600;
  margin-bottom: 12px;
  color: var(--text);
}
.status-led {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  margin-right: 6px;
}
.status-led.on { background: var(--success, #4caf50); }
.status-led.off { background: var(--danger); }
.status-text.on { color: var(--success, #4caf50); }
.status-text.off { color: var(--danger); }
.muted { color: var(--text-muted); }
</style>
