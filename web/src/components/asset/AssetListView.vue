<template>
  <section class="view">
    <header class="view-head">
      <div class="head-row">
        <h2>资产台账</h2>
        <span class="muted">
          主机与中间件实例由 Agent 每轮上报自动发现；人工维护的值不会覆盖采集值，两者差异在详情中可见
        </span>
      </div>
    </header>

    <div class="panel">
      <div class="toolbar">
        <el-select v-model="filter.type" size="small" placeholder="全部类型" clearable style="width: 160px">
          <el-option label="主机" value="host" />
          <el-option label="中间件实例" value="middleware-instance" />
        </el-select>
        <el-input
          v-model="filter.node"
          size="small"
          clearable
          placeholder="归属节点，如 web-01"
          style="width: 180px"
          @keyup.enter="reload"
        />
        <el-input
          v-model="filter.keyword"
          size="small"
          clearable
          placeholder="名称或自然键关键字"
          style="width: 200px"
          @keyup.enter="reload"
        />
        <el-button type="primary" size="small" :loading="loading" @click="reload">查询</el-button>
        <span v-if="canWrite" style="margin-left: auto">
          <el-button size="small" @click="openCreate">新建资产</el-button>
        </span>
      </div>

      <el-alert v-if="loadError" type="error" :closable="false" show-icon :title="loadError" class="alert-gap" />

      <el-table
        :data="items"
        v-loading="loading"
        size="small"
        empty-text="没有匹配的资产（资产会在 Agent 首次上报后自动出现）"
        style="width: 100%"
      >
        <el-table-column label="名称" min-width="160">
          <template #default="{ row }">
            <a class="link" @click="openDetail(row)">{{ row.name || row.naturalKey }}</a>
          </template>
        </el-table-column>
        <el-table-column label="类型" width="120">
          <template #default="{ row }">
            <span class="tag">{{ typeLabel(row.typeKey) }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="naturalKey" label="自然键" min-width="200" show-overflow-tooltip />
        <el-table-column label="归属节点" width="140">
          <template #default="{ row }">{{ row.node || '—' }}</template>
        </el-table-column>
        <el-table-column label="属性数" width="90">
          <template #default="{ row }">{{ Object.keys(row.values || {}).length }}</template>
        </el-table-column>
        <el-table-column label="最近更新" width="180">
          <template #default="{ row }">{{ fmtTime(row.updatedAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="140" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" size="small" @click="openDetail(row)">详情</el-button>
            <el-button v-if="canWrite" link type="primary" size="small" @click="openEdit(row)">维护</el-button>
          </template>
        </el-table-column>
      </el-table>

      <div class="foot">
        <span class="muted">共 {{ items.length }} 条（受资源范围限制，仅显示你有权查看的资产）</span>
        <el-button v-if="items.length >= pageSize" size="small" :loading="loading" @click="loadMore">
          加载更多
        </el-button>
      </div>
    </div>

    <!-- 详情：属性双来源对比 + 变更历史 -->
    <el-drawer v-model="detailVisible" size="640px" :title="detail ? detail.name || detail.naturalKey : '资产详情'">
      <div v-if="detail" class="detail">
        <el-descriptions :column="1" size="small" border>
          <el-descriptions-item label="类型">{{ typeLabel(detail.typeKey) }}</el-descriptions-item>
          <el-descriptions-item label="自然键">{{ detail.naturalKey }}</el-descriptions-item>
          <el-descriptions-item label="归属节点">{{ detail.node || '—' }}</el-descriptions-item>
          <el-descriptions-item label="建档时间">{{ fmtTime(detail.createdAt) }}</el-descriptions-item>
          <el-descriptions-item label="最近更新">{{ fmtTime(detail.updatedAt) }}</el-descriptions-item>
        </el-descriptions>

        <h4 class="sec">属性（人工值优先，采集值原样保留）</h4>
        <el-table :data="attrRows" size="small" empty-text="暂无属性" style="width: 100%">
          <el-table-column prop="key" label="属性" width="130" />
          <el-table-column prop="effective" label="生效值" min-width="120" show-overflow-tooltip />
          <el-table-column label="来源明细" min-width="200">
            <template #default="{ row }">
              <div v-if="row.manual !== undefined" class="src">
                <span class="tag">人工</span> {{ row.manual }}
                <span v-if="row.manualBy" class="muted">（{{ row.manualBy }}）</span>
              </div>
              <div v-if="row.discovery !== undefined" class="src">
                <span class="tag online">采集</span> {{ row.discovery }}
              </div>
            </template>
          </el-table-column>
        </el-table>

        <h4 class="sec">变更历史（字段级）</h4>
        <el-table :data="history" size="small" empty-text="暂无变更" style="width: 100%">
          <el-table-column label="时间" width="160">
            <template #default="{ row }">{{ fmtTime(row.at) }}</template>
          </el-table-column>
          <el-table-column prop="field" label="字段" width="110" />
          <el-table-column label="变化" min-width="180">
            <template #default="{ row }">
              <template v-if="row.kind === 'initial'"><span class="muted">建档</span></template>
              <template v-else>
                <span class="muted">{{ row.old || '（空）' }}</span> → <b>{{ row.new || '（空）' }}</b>
              </template>
            </template>
          </el-table-column>
          <el-table-column label="来源" width="90">
            <template #default="{ row }">{{ row.source === 'manual' ? '人工' : '采集' }}</template>
          </el-table-column>
          <el-table-column prop="actor" label="操作人" width="110" />
        </el-table>

        <div v-if="canWrite" class="drawer-actions">
          <el-button type="primary" size="small" @click="openEdit(detail)">维护人工值</el-button>
        </div>
      </div>
    </el-drawer>

    <!-- 新建：手工建档（人工来源） -->
    <el-dialog v-model="createVisible" title="新建资产" width="600px">
      <el-form label-width="90px" size="small">
        <el-form-item label="资产类型">
          <el-select v-model="form.typeKey" style="width: 100%">
            <el-option label="主机" value="host" />
            <el-option label="中间件实例" value="middleware-instance" />
          </el-select>
        </el-form-item>
        <el-form-item label="自然键">
          <el-input v-model="form.naturalKey" placeholder="主机填 hostname；实例填 <类型>:<地址>，如 redis:127.0.0.1:6379" />
        </el-form-item>
        <el-form-item label="名称">
          <el-input v-model="form.name" placeholder="可留空" />
        </el-form-item>
        <el-form-item label="归属节点">
          <el-input v-model="form.node" placeholder="必须是你有权访问的节点；决定资源范围可见性" />
        </el-form-item>
        <el-form-item label="属性">
          <div class="attr-editor">
            <div v-for="(row, i) in editAttrs" :key="i" class="attr-row">
              <el-input v-model="row.key" size="small" placeholder="属性名" style="width: 42%" />
              <el-input v-model="row.value" size="small" placeholder="值" style="width: 42%" />
              <el-button link type="danger" size="small" @click="editAttrs.splice(i, 1)">删除</el-button>
            </div>
            <el-button link type="primary" size="small" @click="editAttrs.push({ key: '', value: '' })">
              + 添加属性
            </el-button>
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button size="small" @click="createVisible = false">取消</el-button>
        <el-button type="primary" size="small" :loading="saving" @click="submitCreate">提交</el-button>
      </template>
    </el-dialog>

    <!-- 维护：只更新人工值（采集值不受影响） -->
    <el-dialog v-model="editVisible" title="维护人工值" width="600px">
      <el-alert
        type="info"
        :closable="false"
        show-icon
        title="这里写入的是人工值：Agent 采集的值会原样保留，两者差异在详情里可对比。归属节点不可在此修改。"
        class="alert-gap"
      />
      <el-form label-width="90px" size="small">
        <el-form-item label="名称">
          <el-input v-model="form.name" placeholder="留空表示不修改" />
        </el-form-item>
        <el-form-item label="属性">
          <div class="attr-editor">
            <div v-for="(row, i) in editAttrs" :key="i" class="attr-row">
              <el-input v-model="row.key" size="small" placeholder="属性名" style="width: 42%" />
              <el-input v-model="row.value" size="small" placeholder="值" style="width: 42%" />
              <el-button link type="danger" size="small" @click="editAttrs.splice(i, 1)">删除</el-button>
            </div>
            <el-button link type="primary" size="small" @click="editAttrs.push({ key: '', value: '' })">
              + 添加属性
            </el-button>
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button size="small" @click="editVisible = false">取消</el-button>
        <el-button type="primary" size="small" :loading="saving" @click="submitEdit">提交</el-button>
      </template>
    </el-dialog>
  </section>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { listAssets, getAsset, getAssetHistory, createAsset, updateAsset } from '../../api/asset'
import { useAuth } from '../../composables/useAuth'

const auth = useAuth()
// 前端隐藏仅为体验：服务端 assets:write 是真正的边界（且属高风险权限，提交前二次确认）。
const canWrite = computed(() => auth.can('assets:write'))

const pageSize = 50
const items = ref([])
const offset = ref(0)
const loading = ref(false)
const saving = ref(false)
const loadError = ref('')

const filter = ref({ type: '', node: '', keyword: '' })

const detailVisible = ref(false)
const detail = ref(null)
const history = ref([])

const createVisible = ref(false)
const editVisible = ref(false)
const editId = ref('')
const editAttrs = ref([])
const form = ref({ typeKey: 'host', naturalKey: '', name: '', node: '' })

const TYPE_LABELS = { host: '主机', 'middleware-instance': '中间件实例' }
const typeLabel = (key) => TYPE_LABELS[key] || key

function fmtTime(ts) {
  if (!ts) return '—'
  return new Date(ts).toLocaleString('zh-CN', { hour12: false })
}

// 属性按 key 归并：同一 key 的采集值与人工值并排展示，便于直接看出差异。
const attrRows = computed(() => {
  const byKey = new Map()
  for (const attr of (detail.value && detail.value.attrs) || []) {
    const row = byKey.get(attr.key) || { key: attr.key }
    if (attr.source === 'manual') {
      row.manual = attr.value
      row.manualBy = attr.updatedBy || ''
    } else {
      row.discovery = attr.value
    }
    byKey.set(attr.key, row)
  }
  for (const row of byKey.values()) {
    // 生效值：人工优先（与服务端 Asset.Value 同一规则）
    row.effective = row.manual !== undefined ? row.manual : row.discovery
  }
  return [...byKey.values()].sort((a, b) => a.key.localeCompare(b.key))
})

async function load(reset = true) {
  loading.value = true
  loadError.value = ''
  try {
    if (reset) offset.value = 0
    const params = { ...filter.value, limit: pageSize, offset: offset.value }
    const res = await listAssets(params)
    const list = (res && res.assets) || []
    items.value = reset ? list : items.value.concat(list)
  } catch (e) {
    loadError.value = e.message || '加载资产失败'
  } finally {
    loading.value = false
  }
}

function reload() {
  load(true)
}

function loadMore() {
  offset.value += pageSize
  load(false)
}

async function openDetail(row) {
  detail.value = row
  history.value = []
  detailVisible.value = true
  try {
    detail.value = await getAsset(row.id)
    const res = await getAssetHistory(row.id)
    history.value = (res && res.records) || []
  } catch (e) {
    ElMessage.error(e.message || '加载资产详情失败')
  }
}

function openCreate() {
  form.value = { typeKey: 'host', naturalKey: '', name: '', node: '' }
  editAttrs.value = [{ key: '', value: '' }]
  createVisible.value = true
}

function openEdit(row) {
  editId.value = row.id
  form.value = { typeKey: row.typeKey, naturalKey: row.naturalKey, name: '', node: row.node || '' }
  // 预填当前人工值：采集值不预填，避免误以为「提交就会覆盖采集值」
  editAttrs.value = (row.attrs || [])
    .filter((a) => a.source === 'manual')
    .map((a) => ({ key: a.key, value: a.value }))
  if (!editAttrs.value.length) editAttrs.value = [{ key: '', value: '' }]
  editVisible.value = true
}

function attrsPayload(rows) {
  const out = {}
  for (const row of rows) {
    const key = (row.key || '').trim()
    if (key) out[key] = row.value
  }
  return out
}

async function confirmWrite(action) {
  // 资产维护属高风险权限（服务端 HighRiskPermissions）：写之前让操作者再看一眼。
  try {
    await ElMessageBox.confirm(action, '确认修改资产', { type: 'warning', confirmButtonText: '确认', cancelButtonText: '取消' })
    return true
  } catch (e) {
    return false
  }
}

async function submitCreate() {
  const payload = {
    typeKey: form.value.typeKey,
    naturalKey: form.value.naturalKey.trim(),
    name: form.value.name.trim(),
    node: form.value.node.trim(),
    attrs: attrsPayload(editAttrs.value),
  }
  if (!payload.typeKey || !payload.naturalKey) {
    ElMessage.warning('资产类型与自然键为必填项')
    return
  }
  if (!(await confirmWrite(`将新建资产 ${payload.naturalKey}（归属节点 ${payload.node || '未指定'}）`))) return
  saving.value = true
  try {
    await createAsset(payload)
    ElMessage.success('资产已创建')
    createVisible.value = false
    reload()
  } catch (e) {
    ElMessage.error(e.message || '创建失败')
  } finally {
    saving.value = false
  }
}

async function submitEdit() {
  const payload = { name: form.value.name.trim(), attrs: attrsPayload(editAttrs.value) }
  if (!(await confirmWrite('将写入这些人工值；采集值不会被覆盖'))) return
  saving.value = true
  try {
    const updated = await updateAsset(editId.value, payload)
    ElMessage.success('已保存人工值')
    editVisible.value = false
    if (detail.value && String(detail.value.id) === String(editId.value)) {
      detail.value = updated
      const res = await getAssetHistory(editId.value)
      history.value = (res && res.records) || []
    }
    reload()
  } catch (e) {
    ElMessage.error(e.message || '保存失败')
  } finally {
    saving.value = false
  }
}

onMounted(reload)
</script>

<style scoped>
.view-head .head-row {
  display: flex;
  align-items: baseline;
  gap: 12px;
  flex-wrap: wrap;
}
.toolbar {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-bottom: 12px;
}
.foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: 12px;
}
.link {
  color: var(--accent);
  cursor: pointer;
}
.muted {
  color: var(--text-dim);
  font-size: 12px;
}
.sec {
  margin: 18px 0 8px;
  font-size: 13px;
  color: var(--text-dim);
}
.src {
  font-size: 12px;
  line-height: 18px;
}
.attr-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}
.attr-editor {
  width: 100%;
}
.drawer-actions {
  margin-top: 16px;
}
</style>
