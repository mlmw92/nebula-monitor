<template>
  <div class="dash">
    <div class="dash-head">
      <div class="tabs">
        <span
          v-for="d in dashboards"
          :key="d.id"
          :class="['tab', { active: d.id === activeId }]"
          @click="activeId = d.id"
        >{{ d.name }}</span>
        <el-button size="small" @click="newDash">+ 新建看板</el-button>
      </div>
      <div v-if="active" class="head-actions">
        <el-button size="small" type="primary" @click="enterEdit" v-if="!editing">编辑</el-button>
        <template v-else>
          <el-button size="small" @click="addPanel">+ 添加面板</el-button>
          <el-button size="small" type="success" @click="save">保存</el-button>
          <el-button size="small" @click="cancelEdit">取消</el-button>
        </template>
        <el-popconfirm title="确认删除该看板？" @confirm="delDash">
          <template #reference><el-button size="small" type="danger">删除看板</el-button></template>
        </el-popconfirm>
      </div>
    </div>

    <div v-if="!active" class="empty">暂无看板，点击「新建看板」创建</div>
    <div v-else class="grid">
      <PanelChart
        v-for="(p, i) in activePanels"
        :key="i"
        :panel="p"
        @edit="editPanel(i)"
        @remove="removePanel(i)"
      />
    </div>

    <!-- 面板编辑弹窗 -->
    <el-dialog v-model="panelDlg" title="面板配置" width="460px">
      <el-form label-width="90px">
        <el-form-item label="面板标题"><el-input v-model="editForm.title" /></el-form-item>
        <el-form-item label="指标">
          <el-select v-model="editForm.metric" filterable placeholder="选择指标" style="width:100%">
            <el-option v-for="m in flatMetrics" :key="m.name" :label="`${m.title} (${m.name})`" :value="m.name" />
          </el-select>
        </el-form-item>
        <el-form-item label="图表类型">
          <el-select v-model="editForm.chartType" style="width:100%">
            <el-option label="折线" value="line" />
            <el-option label="面积" value="area" />
            <el-option label="柱状" value="bar" />
            <el-option label="仪表" value="gauge" />
          </el-select>
        </el-form-item>
        <el-form-item label="限定主机">
          <el-input v-model="editForm.node" placeholder="留空=全部" />
        </el-form-item>
        <el-form-item label="时间范围">
          <el-select v-model="editForm.range" style="width:100%">
            <el-option label="近1小时" value="1h" />
            <el-option label="近6小时" value="6h" />
            <el-option label="近24小时" value="24h" />
            <el-option label="近7天" value="7d" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="panelDlg = false">取消</el-button>
        <el-button type="primary" @click="savePanel">确定</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import PanelChart from './PanelChart.vue'
import http from '../../api/http'
import { useDashboards } from '../../composables/useDashboards'

const { state, load, create, update, remove } = useDashboards()
const dashboards = computed(() => state.dashboards)
const activeId = ref('')
const editing = ref(false)
const draft = ref(null)
const panelDlg = ref(false)
const editIndex = ref(-1)
const editForm = ref({ title: '', metric: '', chartType: 'line', node: '', range: '1h' })
const flatMetrics = ref([])

const active = computed(() => dashboards.value.find((d) => d.id === activeId.value) || null)
const activePanels = computed(() => {
  const d = active.value
  if (!d) return []
  return editing.value ? (draft.value ? draft.value.panels : []) : d.panels
})

async function refresh() {
  await load(true)
  if (!activeId.value && dashboards.value.length) activeId.value = dashboards.value[0].id
}

function newDash() {
  const name = prompt('看板名称')
  if (!name) return
  create(name, []).then(refresh).catch((e) => ElMessage.error(e.message || e))
}

function delDash() {
  if (!active.value) return
  remove(active.value.id).then(() => {
    activeId.value = ''
    refresh()
  }).catch((e) => ElMessage.error(e.message || e))
}

function enterEdit() {
  editing.value = true
  draft.value = JSON.parse(JSON.stringify(active.value))
}

function cancelEdit() {
  editing.value = false
  draft.value = null
}

function addPanel() {
  editIndex.value = -1
  editForm.value = { title: '', metric: '', chartType: 'line', node: '', range: '1h' }
  panelDlg.value = true
}

function editPanel(i) {
  editIndex.value = i
  const p = draft.value.panels[i]
  editForm.value = { ...p }
  panelDlg.value = true
}

function savePanel() {
  if (!editForm.value.metric) { ElMessage.warning('请选择指标'); return }
  if (editIndex.value >= 0) draft.value.panels[editIndex.value] = { ...editForm.value }
  else draft.value.panels.push({ ...editForm.value })
  panelDlg.value = false
}

function removePanel(i) {
  draft.value.panels.splice(i, 1)
}

function save() {
  update(active.value.id, draft.value.name, draft.value.panels)
    .then(() => { editing.value = false; draft.value = null; refresh() })
    .catch((e) => ElMessage.error(e.message || e))
}

onMounted(async () => {
  await refresh()
  const d = await http.metricCatalog()
  const cat = d.catalog || {}
  const arr = []
  for (const k in cat) arr.push(...cat[k])
  flatMetrics.value = arr
})
</script>

<style scoped>
.dash { padding: 12px 16px; height: calc(100vh - 132px); overflow: auto; }
.dash-head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }
.tabs { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.tab { padding: 4px 12px; border-radius: 6px; cursor: pointer; color: #94a3b8; border: 1px solid transparent; }
.tab.active { color: #e5edf7; border-color: rgba(34,211,238,0.4); background: rgba(34,211,238,0.08); }
.head-actions { display: flex; gap: 8px; }
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(360px, 1fr)); gap: 14px; }
.empty { color: #64748b; text-align: center; margin-top: 60px; }
</style>
