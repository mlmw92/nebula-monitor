<template>
  <section class="view">
    <header class="view-head">
      <div class="head-row">
        <h2>配置巡检</h2>
        <span class="muted">
          把每个资产的配置快照与上一次快照比（新增 / 变更 / 缺失），再与「标杆资产」的期望值比合规偏差；
          只给结论，不自动修复
        </span>
      </div>
    </header>

    <!-- 执行区：范围复用台账筛选（服务端仍按资源范围裁剪，前端只是体验） -->
    <div class="panel">
      <div class="filter-bar">
        <div class="field">
          <span class="field-label">类型</span>
          <el-select v-model="filter.type" placeholder="全部类型" clearable size="small" style="width: 150px">
            <el-option label="主机" value="host" />
            <el-option label="中间件实例" value="middleware-instance" />
          </el-select>
        </div>
        <div class="field">
          <span class="field-label">归属节点</span>
          <el-input v-model="filter.node" clearable size="small" placeholder="如 web-01" style="width: 150px" />
        </div>
        <el-input v-model="filter.keyword" clearable size="small" placeholder="关键词（名称 / 自然键 / 属性值）" style="width: 240px" />
        <el-button type="primary" size="small" :loading="running" :disabled="!canRun" @click="runInspect">
          执行巡检
        </el-button>
        <span v-if="!canRun" class="muted">当前账号缺 inspect:run 权限，只能查看历史结果</span>
        <span v-else class="muted">运行态字段（up / status 等）默认不参与比对，避免探活翻转把差异清单淹掉</span>
      </div>
    </div>

    <!-- 巡检记录 -->
    <div class="panel">
      <div class="panel-title">巡检记录</div>
      <el-table
        :data="runs"
        v-loading="loading"
        highlight-current-row
        empty-text="还没有巡检记录"
        style="width: 100%"
        @row-click="selectRun"
      >
        <el-table-column label="时间" width="180">
          <template #default="{ row }">{{ fmtTime(row.startedAt) }}</template>
        </el-table-column>
        <el-table-column label="范围" width="200">
          <template #default="{ row }"><span class="mono">{{ row.scope }}</span></template>
        </el-table-column>
        <el-table-column label="覆盖资产" width="100" prop="assets" />
        <el-table-column label="首次建基线" width="120">
          <template #default="{ row }">
            <span v-if="row.baselined" :title="'这些资产还没有历史快照，本次只建立基线，因此不产出差异'">{{ row.baselined }}</span>
            <span v-else class="muted">—</span>
          </template>
        </el-table-column>
        <el-table-column label="差异项" width="100">
          <template #default="{ row }">
            <span :class="{ 'has-finding': row.findings > 0 }">{{ row.findings }}</span>
          </template>
        </el-table-column>
        <el-table-column label="操作人" width="140">
          <template #default="{ row }">{{ row.actor || '—' }}</template>
        </el-table-column>
        <el-table-column label="备注">
          <template #default="{ row }">
            <!-- 截断必须显式提示：静默少检一部分会让人以为"其余资产都合规" -->
            <span v-if="row.truncated" class="warn-text">资产数超过单次巡检上限，本次只覆盖了前 {{ row.assets }} 个</span>
            <span v-else class="muted">—</span>
          </template>
        </el-table-column>
      </el-table>
      <p v-if="runs.length" class="muted note">点击一行查看该次巡检的差异项。</p>
    </div>

    <!-- 差异项 -->
    <div v-if="selectedRun" class="panel">
      <div class="panel-title">
        差异项（{{ findings.length }}）
        <span class="muted">· 记录 #{{ selectedRun.id }} · {{ fmtTime(selectedRun.startedAt) }}</span>
      </div>
      <el-alert
        v-if="selectedRun.baselined > 0"
        type="info"
        :closable="false"
        show-icon
        class="alert-gap"
        :title="`本次有 ${selectedRun.baselined} 个资产是首次见到，只建立了基线`"
        description="数据不足不等于不合规：首次巡检没有可比对的上一份快照，所以它们不会产出差异。下一次巡检起就会比对。"
      />
      <el-table :data="findings" v-loading="findingLoading" empty-text="本次没有差异" style="width: 100%">
        <el-table-column label="级别" width="90">
          <template #default="{ row }">
            <span class="tag" :class="row.level">{{ levelLabel(row.level) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="差异" width="100">
          <template #default="{ row }">{{ kindLabel(row.kind) }}</template>
        </el-table-column>
        <el-table-column label="资产" min-width="200">
          <template #default="{ row }">
            <span class="name">{{ row.assetName || row.assetKey }}</span>
            <div class="sub">{{ row.assetKey }}<template v-if="row.node"> · {{ row.node }}</template>
              <template v-if="row.assetType === 'host'"> · 主机</template>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="字段" width="160">
          <template #default="{ row }"><span class="mono">{{ row.field }}</span></template>
        </el-table-column>
        <el-table-column label="期望值" min-width="160">
          <template #default="{ row }">
            <span v-if="row.expected">{{ row.expected }}</span>
            <span v-else class="muted">—</span>
          </template>
        </el-table-column>
        <el-table-column label="实际值" min-width="160">
          <template #default="{ row }">
            <span v-if="row.actual">{{ row.actual }}</span>
            <span v-else class="muted">（字段已缺失）</span>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 期望值（标杆） -->
    <div class="panel">
      <div class="panel-title">期望值（标杆资产）</div>
      <p class="muted note">
        合规偏差（deviation）的期望值来自「标杆」：在资产台账详情里把一台标准机设为该类型的期望值。
        每个资产类型只保留一个标杆；未设置标杆时只做快照前后比对。
      </p>
      <el-table :data="baselines" empty-text="还没有设置任何期望值" style="width: 100%">
        <el-table-column label="资产类型" width="160">
          <template #default="{ row }">{{ row.typeKey === 'host' ? '主机' : '中间件实例' }}</template>
        </el-table-column>
        <el-table-column label="标杆资产" min-width="240">
          <template #default="{ row }"><span class="mono">{{ row.assetKey }}</span></template>
        </el-table-column>
        <el-table-column label="设置人" width="150">
          <template #default="{ row }">{{ row.setBy || '—' }}</template>
        </el-table-column>
        <el-table-column label="设置时间" width="180">
          <template #default="{ row }">{{ fmtTime(row.setAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="110">
          <template #default="{ row }">
            <el-button v-if="canWrite" link type="danger" @click="clearBaseline(row)">清除</el-button>
            <span v-else class="muted">只读</span>
          </template>
        </el-table-column>
      </el-table>
    </div>
  </section>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  runInspect as runInspectApi,
  listInspectRuns,
  listInspectFindings,
  listInspectBaselines,
  clearAssetBaseline,
} from '../../api/asset'
import { useAuth } from '../../composables/useAuth'

const auth = useAuth()
// 前端隐藏仅为体验：服务端 inspect:run / assets:write 才是边界
const canRun = computed(() => auth.can('inspect:run'))
const canWrite = computed(() => auth.can('assets:write'))

const filter = ref({ type: '', node: '', keyword: '' })
const runs = ref([])
const findings = ref([])
const baselines = ref([])
const selectedRun = ref(null)
const loading = ref(false)
const findingLoading = ref(false)
const running = ref(false)

const KIND_LABELS = { added: '新增', changed: '变更', missing: '缺失', deviation: '合规偏差' }
const kindLabel = (k) => KIND_LABELS[k] || k
const LEVEL_LABELS = { info: '提示', warning: '警告', critical: '严重' }
const levelLabel = (l) => LEVEL_LABELS[l] || l

function fmtTime(ts) {
  if (!ts) return '—'
  return new Date(ts).toLocaleString('zh-CN', { hour12: false })
}

async function loadRuns() {
  loading.value = true
  try {
    const data = await listInspectRuns(50)
    runs.value = (data && data.runs) || []
  } catch (e) {
    ElMessage.error(e.message || '加载巡检记录失败')
    runs.value = []
  } finally {
    loading.value = false
  }
}

async function loadBaselines() {
  try {
    const data = await listInspectBaselines()
    baselines.value = (data && data.baselines) || []
  } catch (e) {
    baselines.value = []
  }
}

async function selectRun(row) {
  selectedRun.value = row
  findings.value = []
  findingLoading.value = true
  try {
    const data = await listInspectFindings(row.id)
    findings.value = (data && data.findings) || []
  } catch (e) {
    ElMessage.error(e.message || '加载差异项失败')
  } finally {
    findingLoading.value = false
  }
}

async function runInspect() {
  running.value = true
  try {
    const run = await runInspectApi({ ...filter.value })
    ElMessage.success(`巡检完成：覆盖 ${run.assets} 个资产，差异 ${run.findings} 条`)
    await loadRuns()
    await selectRun(run)
    await loadBaselines()
  } catch (e) {
    ElMessage.error(e.message || '执行巡检失败')
  } finally {
    running.value = false
  }
}

async function clearBaseline(row) {
  try {
    await ElMessageBox.confirm(
      `将清除「${row.typeKey === 'host' ? '主机' : '中间件实例'}」类型的期望值（标杆 ${row.assetKey}），此后不再做合规偏差比对。`,
      '确认清除期望值',
      { type: 'warning', confirmButtonText: '清除', cancelButtonText: '取消' },
    )
  } catch (e) {
    return
  }
  try {
    // 清除按资产走（标杆就是某个资产），因此这里需要它的资产 ID
    await clearAssetBaseline(row.assetId)
    ElMessage.success('已清除期望值')
    await loadBaselines()
  } catch (e) {
    ElMessage.error(e.message || '清除失败')
  }
}

onMounted(() => {
  loadRuns()
  loadBaselines()
})
</script>

<style scoped>
.view { padding: 16px; }
.view-head { margin-bottom: 12px; }
.head-row { display: flex; align-items: baseline; gap: 12px; flex-wrap: wrap; }
.head-row h2 { margin: 0; font-size: 18px; }
.muted { color: var(--text-dim); font-size: 13px; }
.panel + .panel { margin-top: 12px; }
.panel-title { font-size: 16px; font-weight: 600; margin-bottom: 12px; }
.panel-title .muted { font-weight: 400; font-size: 13px; }
.filter-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  padding: 12px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: rgba(255, 255, 255, 0.02);
}
.field { display: inline-flex; align-items: center; gap: 6px; }
.field-label { font-size: 13px; color: var(--text-dim); white-space: nowrap; }
.mono { font-family: var(--mono); }
.note { margin: 10px 0 0; line-height: 20px; }
.alert-gap { margin-bottom: 12px; }
.has-finding { color: var(--warn); font-weight: 600; }
.warn-text { color: var(--warn); font-size: 13px; }
.name { color: var(--accent); font-weight: 500; }
.sub { color: var(--text-dim); font-size: 12px; margin-top: 2px; }
/* 级别标签：与台账页同一套语义配色 */
.tag { display: inline-flex; align-items: center; padding: 2px 10px; border-radius: 20px; font-size: 13px; font-weight: 500; }
.tag.info { background: rgba(255, 255, 255, 0.06); color: var(--text-dim); }
.tag.warning { background: var(--warn-dim); color: var(--warn); }
.tag.critical { background: var(--danger-dim); color: var(--danger); }
</style>
