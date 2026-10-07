<template>
  <section class="view">
    <PageHeader
      title="配置巡检"
      desc="把每个资产的配置快照与上一次快照比（新增 / 变更 / 缺失），再与「标杆资产」的期望值比合规偏差；只给结论，不自动修复"
    >
      <template #actions>
        <el-button size="small" plain :icon="Printer" @click="printList">打印</el-button>
      </template>
    </PageHeader>

    <!-- 执行区：范围复用台账筛选（服务端仍按资源范围裁剪，前端只是体验） -->
    <div class="card panel">
      <div class="filter-bar">
        <div class="field">
          <span class="field-label">类型</span>
          <el-select v-model="filter.type" placeholder="全部类型" clearable size="small" style="width: 150px">
            <el-option v-for="t in assetTypes" :key="t.key" :label="t.title" :value="t.key" />
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

    <!-- 周期化巡检：默认关闭，打开后按间隔自动跑一次。
         与上方「执行巡检」的分工：那个是"现在跑一次"，这里是"以后每隔一段时间自动跑一次"。
         界面刻意**不提供范围开关**：范围由服务端按配置保存者的身份折算，并随他权限收窄——
         范围是授权的一部分，不是页面上的一个选项（否则受限用户能靠勾一个更宽的范围越权）。 -->
    <SectionCard title="周期化巡检">
      <div class="filter-bar">
        <el-switch v-model="schedule.enabled" :disabled="!canRun" @change="saveSchedule" />
        <span class="muted">每</span>
        <el-input-number
          v-model="schedule.intervalHours"
          :min="1"
          :max="720"
          size="small"
          controls-position="right"
          :disabled="!canRun"
          @change="saveSchedule"
        />
        <span class="muted">小时自动巡检一次</span>
        <el-select
          v-model="schedule.type"
          placeholder="全部类型"
          clearable
          size="small"
          style="width: 140px"
          :disabled="!canRun"
          @change="saveSchedule"
        >
          <el-option v-for="t in assetTypes" :key="t.key" :label="t.title" :value="t.key" />
        </el-select>
        <el-input
          v-model="schedule.node"
          clearable
          size="small"
          placeholder="归属节点"
          style="width: 130px"
          :disabled="!canRun"
          @change="saveSchedule"
        />
        <el-input
          v-model="schedule.keyword"
          clearable
          size="small"
          placeholder="关键词"
          style="width: 170px"
          :disabled="!canRun"
          @change="saveSchedule"
        />
        <el-button size="small" :loading="scheduleRunning" :disabled="!canRun" @click="runScheduleNow">
          立即执行一次
        </el-button>
        <span v-if="!canRun" class="muted">缺 inspect:run 权限，只能查看</span>
      </div>
      <div class="muted schedule-hint">
        <template v-if="scheduleLoadError">{{ scheduleLoadError }}</template>
        <template v-else-if="schedule.lastRunAt">
          上次{{ schedule.lastManual ? '手动' : '定时' }}执行 {{ fmtTime(schedule.lastRunAt) }}
          <span v-if="schedule.lastRunId">· 记录 #{{ schedule.lastRunId }}</span>
          <span v-if="schedule.nextAt && schedule.enabled">· 下次约 {{ fmtTime(schedule.nextAt) }}</span>
        </template>
        <template v-else>尚未执行过。关闭时不影响上方的「执行巡检」。</template>
      </div>
      <!-- 上一轮没跑（占用中 / 范围为空 / 范围来源账号已不存在）或跑失败：必须显式说出来，
           否则"巡检怎么不跑了"只能靠翻文件时间猜。 -->
      <el-alert
        v-if="schedule.lastError"
        class="tip"
        type="warning"
        :closable="false"
        show-icon
        title="上一次巡检没有执行或执行失败"
        :description="schedule.lastError"
      />
    </SectionCard>

    <!-- 巡检记录 -->
    <SectionCard title="巡检记录" dense>
      <el-table
        :data="runs"
        v-loading="loading"
        highlight-current-row
        style="width: 100%"
        @row-click="selectRun"
      >
        <template #empty>
          <EmptyState
            title="当前范围内还没有可见的巡检记录"
            :hints="[
              '先选定上方的类型 / 归属节点范围，再点「执行巡检」',
              '首次巡检只会建立基线，不产出差异；第二次起才会比对',
            ]"
          />
        </template>
      >
        <el-table-column label="时间" width="180">
          <template #default="{ row }">{{ fmtTime(row.startedAt) }}</template>
        </el-table-column>
        <el-table-column label="范围" width="200">
          <template #default="{ row }">            <span v-if="row.partialScope" class="muted">当前可见范围（局部结果）</span>
            <span v-else class="mono">{{ row.scope }}</span></template>
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
          <template #default="{ row }">
            <!-- 定时触发的留痕是 schedule（不是某个登录用户）：它没有请求上下文，
                 界面要让人一眼分清"这条是机器跑的"还是"某人点的" -->
            <el-tag v-if="row.actor === 'schedule'" size="small" type="info">定时</el-tag>
            <span v-else>{{ row.actor || '—' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="备注">
          <template #default="{ row }">
            <!-- 截断必须显式提示：静默少检一部分会让人以为"其余资产都合规" -->
            <span v-if="row.truncated && row.partialScope" class="warn-text">原巡检达到资产上限；此处仅显示当前可见的 {{ row.assets }} 个资产，不能据此判断其他资产</span>
            <span v-else-if="row.truncated" class="warn-text">资产数超过单次巡检上限，本次只覆盖了前 {{ row.assets }} 个</span>
            <span v-else class="muted">—</span>
          </template>
        </el-table-column>
      </el-table>
      <p v-if="runs.some(row => row.partialScope)" class="muted note">覆盖资产、首次建基线和差异项均按当前可见资产重算；这不是原巡检的全量结论。</p>
      <p v-if="runs.length" class="muted note">点击一行查看该次巡检的差异项。</p>
    </SectionCard>

    <!-- 差异项 -->
    <SectionCard v-if="selectedRun" dense>
      <template #actions>
        <div class="panel-title" style="margin: 0">
          差异项（{{ findings.length }}）
          <span v-if="selectedRun.partialScope" class="muted">· 仅显示当前可见资产的局部差异</span>
          <span class="muted">· 记录 #{{ selectedRun.id }} · {{ fmtTime(selectedRun.startedAt) }}</span>
        </div>
      </template>
      <el-alert
        v-if="selectedRun.baselined > 0"
        type="info"
        :closable="false"
        show-icon
        class="alert-gap"
        :title="`本次有 ${selectedRun.baselined} 个资产是首次见到，只建立了基线`"
        description="数据不足不等于不合规：首次巡检没有可比对的上一份快照，所以它们不会产出差异。下一次巡检起就会比对。"
      />
      <el-table :data="findings" v-loading="findingLoading" style="width: 100%">
        <template #empty>
          <EmptyState
            :title="selectedRun.partialScope ? '当前可见资产没有差异' : '本次没有差异'"
            :hints="selectedRun.partialScope ? ['这里只代表当前可见资产，不能推断其他资产的巡检结果'] : ['所选范围内所有资产的配置与上一次快照一致，也没有偏离标杆']"
          />
        </template>
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
    </SectionCard>

    <!-- 期望值（标杆） -->
    <SectionCard title="期望值（标杆资产）" dense>
      <p class="muted note">
        合规偏差（deviation）的期望值来自「标杆」：在资产台账详情里把一台标准机设为该类型的期望值。
        每个资产类型只保留一个标杆；未设置标杆时只做快照前后比对。
      </p>
      <el-table :data="baselines" style="width: 100%">
        <template #empty>
          <EmptyState
            title="当前可见范围内没有标杆资产"
            :hints="['可在资产台账详情里将可见资产设为该类型的标杆', '范围外标杆不会用于当前权限范围内的巡检']"
          />
        </template>
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
    </SectionCard>
  </section>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Printer } from '@element-plus/icons-vue'
import PageHeader from '../common/PageHeader.vue'
import SectionCard from '../common/SectionCard.vue'
import EmptyState from '../common/EmptyState.vue'
import {
  runInspect as runInspectApi,
  listInspectRuns,
  listInspectFindings,
  listInspectBaselines,
  clearAssetBaseline,
  getInspectSchedule,
  saveInspectSchedule,
  runInspectScheduleNow,
} from '../../api/asset'
import { useAuth } from '../../composables/useAuth'
import { useAssetTypes } from '../../composables/useAssetTypes'
import { printPage } from '../../utils/print'

const auth = useAuth()
// 类型候选来自配置项模型接口（两处下拉共用），接口不可用时退回内置四类
const { types: assetTypes } = useAssetTypes()
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

// 周期化巡检（配置与运行状态同文件；默认关闭）。
// 这里只放可编辑的四项 + 运行状态；**范围**不在其中——它由服务端按身份折算。
const schedule = ref({
  enabled: false,
  intervalHours: 24,
  type: '',
  node: '',
  keyword: '',
  lastRunAt: 0,
  lastRunId: 0,
  lastManual: false,
  lastError: '',
  // 下次预计执行时间放在同一个对象里：模板读的是 schedule.nextAt，
  // 存到另一个 ref 里会让那一行**永远不渲染**（且不报错）
  nextAt: 0,
})
const scheduleLoadError = ref('')
const scheduleRunning = ref(false)

const KIND_LABELS = { added: '新增', changed: '变更', missing: '缺失', deviation: '合规偏差' }
const kindLabel = (k) => KIND_LABELS[k] || k
const LEVEL_LABELS = { info: '提示', warning: '警告', critical: '严重' }
const levelLabel = (l) => LEVEL_LABELS[l] || l

function fmtTime(ts) {
  if (!ts) return '—'
  return new Date(ts).toLocaleString('zh-CN', { hour12: false })
}

// 巡检结论经常要作为整改工单的附件；选中了某次巡检就只打那次的差异项
function printList() {
  const meta = []
  if (selectedRun.value) {
    meta.push(`巡检记录 #${selectedRun.value.id} · ${fmtTime(selectedRun.value.startedAt)}`)
    if (selectedRun.value.partialScope) meta.push('仅按当前可见资产统计的局部结果')
    meta.push(`差异项 ${findings.value.length} 条`)
  } else {
    if (runs.value.some(row => row.partialScope)) meta.push('巡检记录按当前可见资产统计（局部结果）')
    meta.push(`巡检记录 ${runs.value.length} 条`)
    if (filter.value.type) meta.push(`类型=${filter.value.type}`)
    if (filter.value.keyword) meta.push(`关键词=${filter.value.keyword}`)
  }
  printPage({ title: '配置巡检', meta })
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

// applySchedule 把服务端返回的配置 + 运行状态铺到界面上。
// 只取界面上真正用得到的字段：范围（scope）**只读**，不往表单里放——放进去就会被误当成可编辑项。
function applySchedule(data) {
  const cfg = (data && data.config) || {}
  schedule.value = {
    enabled: !!cfg.enabled,
    intervalHours: cfg.intervalHours || 24,
    type: cfg.type || '',
    node: cfg.node || '',
    keyword: cfg.keyword || '',
    lastRunAt: cfg.lastRunAt || 0,
    lastRunId: cfg.lastRunId || 0,
    lastManual: !!cfg.lastManual,
    lastError: cfg.lastError || '',
    nextAt: (data && data.nextAt) || 0,
  }
}

async function loadSchedule() {
  try {
    applySchedule(await getInspectSchedule())
    scheduleLoadError.value = ''
  } catch (e) {
    // 取不到就说清楚，而不是把开关显示成"关着"——那会让人以为是自己的配置
    scheduleLoadError.value = '周期化巡检不可用：' + (e.message || '请稍后重试')
  }
}

async function saveSchedule() {
  if (!canRun.value) return
  try {
    applySchedule(
      await saveInspectSchedule({
        enabled: schedule.value.enabled,
        intervalHours: schedule.value.intervalHours,
        type: schedule.value.type,
        node: schedule.value.node,
        keyword: schedule.value.keyword,
      }),
    )
    ElMessage.success(schedule.value.enabled ? '已开启周期化巡检' : '已关闭周期化巡检')
  } catch (e) {
    ElMessage.error(e.message || '保存周期化巡检配置失败')
    // 保存失败时不能让界面停在一个"看起来生效了"的开关位置：拉回服务端的真实状态
    await loadSchedule()
  }
}

async function runScheduleNow() {
  if (!canRun.value) return
  scheduleRunning.value = true
  try {
    const run = await runInspectScheduleNow()
    ElMessage.success(`巡检完成：覆盖 ${run.assets} 个资产，差异 ${run.findings} 条`)
    await loadRuns()
    if (run.runId) await selectRun({ id: run.runId })
  } catch (e) {
    ElMessage.error(e.message || '执行巡检失败')
  } finally {
    scheduleRunning.value = false
    // 无论成败都刷新：失败/未执行的原因在 lastError 里，界面上要能看到
    await loadSchedule()
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
  loadSchedule()
})
</script>

<style scoped>
/* 内边距由 MainLayout 的 .content 统一提供，页面不再自己套一层 */
.view { padding: 0; display: flex; flex-direction: column; gap: 12px; }
.muted { color: var(--text-dim); font-size: 13px; }
.panel { padding: 12px 16px; }
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
  background: var(--fill-1);
}
.field { display: inline-flex; align-items: center; gap: 6px; }
.field-label { font-size: 13px; color: var(--text-dim); white-space: nowrap; }
.mono { font-family: var(--mono); }
.note { margin: 10px 0 0; line-height: 20px; }
/* 周期化巡检：控件行沿用执行区那套 .filter-bar；下面这行小字与失败告警与报告页同一读法 */
.schedule-hint { margin-top: 10px; }
.tip { margin-top: 10px; }
.alert-gap { margin-bottom: 12px; }
.has-finding { color: var(--warn); font-weight: 600; }
.warn-text { color: var(--warn); font-size: 13px; }
.name { color: var(--accent); font-weight: 500; }
.sub { color: var(--text-dim); font-size: 12px; margin-top: 2px; }
/* 级别标签：与台账页同一套语义配色 */
.tag { display: inline-flex; align-items: center; padding: 2px 10px; border-radius: 20px; font-size: 13px; font-weight: 500; }
.tag.info { background: var(--fill-2); color: var(--text-dim); }
.tag.warning { background: var(--warn-dim); color: var(--warn); }
.tag.critical { background: var(--danger-dim); color: var(--danger); }
</style>
