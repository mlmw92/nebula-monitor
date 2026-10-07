<!--
  关系视图（全库）：一页两段。

  **上段是聚合**：按「来源类型 × 关系种类 × 目标类型 × 边来源」数条数。全库规模下把每条边都画出来
  是不可读的（力导向图会散成一团），所以"库里整体有哪些关系"这件事由聚合回答，行数与台账规模无关。

  **下段是图**：以"一组边"为中心（不是以某个资产为中心），带筛选与上限——它回答"某个子集长什么样"。
  点总览的某一行会把下面的图切到该筛选，两段因此是一条动线而不是两块互不相干的内容。
-->
<template>
  <div class="asset-topology-view">
    <PageHeader
      title="关系视图"
      desc="先看整体形态（聚合），再按筛选看具体连法；关系由采集自动建立，也可人工维护"
    >
      <template #actions>
        <el-button size="small" :loading="loading" @click="reload">刷新</el-button>
      </template>
    </PageHeader>

    <div class="card">
      <div class="sec">
        <span>关系总览</span>
        <span class="muted">按「来源类型 × 关系种类 × 目标类型 × 边来源」聚合；点一行把下面的图切到该筛选</span>
      </div>
      <div class="summary-line">
        <span>关系总数 <b>{{ overview.totalLinks }}</b></span>
        <span :class="{ warn: overview.assetsWithoutLinks > 0 }">
          无边资产 <b>{{ overview.assetsWithoutLinks }}</b>
          <span class="muted">（纳管了但没有任何关系）</span>
        </span>
      </div>

      <el-alert v-if="loadError" type="error" :closable="false" show-icon :title="loadError" />

      <el-table
        v-if="overview.groups.length"
        v-loading="loading"
        :data="overview.groups"
        class="ov-table"
        @row-click="applyGroup"
      >
        <el-table-column label="来源类型" min-width="150">
          <template #default="{ row }">{{ typeTitle(row.fromType) }}</template>
        </el-table-column>
        <el-table-column label="关系" width="150">
          <template #default="{ row }"><span class="tag">{{ kindLabel(row.kind) }}</span></template>
        </el-table-column>
        <el-table-column label="目标类型" min-width="150">
          <template #default="{ row }">{{ typeTitle(row.toType) }}</template>
        </el-table-column>
        <el-table-column label="边来源" width="100">
          <template #default="{ row }">{{ row.source === 'manual' ? '人工维护' : '采集发现' }}</template>
        </el-table-column>
        <el-table-column prop="count" label="条数" width="80" />
        <el-table-column label="样本" min-width="280">
          <template #default="{ row }">
            <!-- 显示名字（好读），精确自然键放在 title 里（同名实例不少，要核对时得有准值） -->
            <span
              class="muted mono"
              :title="row.sample.fromKey + ' → ' + row.sample.toKey"
            >
              {{ row.sample.fromName || row.sample.fromKey }}
              → {{ row.sample.toName || row.sample.toKey }}
            </span>
          </template>
        </el-table-column>
      </el-table>
      <EmptyState
        v-else
        :icon="Share"
        title="还没有任何关系"
        :hints="[
          '主机与中间件实例上报后自动建立「运行于」关系',
          '中间件主从（依赖）、容器归属也会自动建立',
          '其余关系可在资产台账详情里人工维护',
        ]"
      />
    </div>

    <div class="card">
      <div class="sec">
        <span>关系图</span>
        <span class="muted">
          共 {{ graph.total }} 条关系<span v-if="graph.total">，已画出 {{ graph.edges.length }} 条</span>
        </span>
        <el-button v-if="hasFilter" size="small" style="margin-left: auto" @click="clearFilters">
          清除筛选
        </el-button>
      </div>

      <div class="filters">
        <el-select
          v-model="filters.kind"
          multiple
          clearable
          collapse-tags
          placeholder="全部关系种类"
          size="small"
          style="width: 240px"
          @change="loadGraph"
        >
          <el-option v-for="(label, key) in KIND_LABELS" :key="key" :value="key" :label="label" />
        </el-select>
        <el-select v-model="filters.source" clearable placeholder="全部边来源" size="small" style="width: 150px" @change="loadGraph">
          <el-option value="discovery" label="采集发现" />
          <el-option value="manual" label="人工维护" />
        </el-select>
        <el-select
          v-model="filters.type"
          multiple
          clearable
          collapse-tags
          placeholder="全部资产类型"
          size="small"
          style="width: 240px"
          @change="loadGraph"
        >
          <el-option v-for="t in typeOptions" :key="t" :value="t" :label="typeTitle(t)" />
        </el-select>
        <el-select
          v-model="filters.node"
          multiple
          clearable
          collapse-tags
          placeholder="全部归属节点"
          size="small"
          style="width: 240px"
          @change="loadGraph"
        >
          <el-option v-for="n in nodes" :key="n" :value="n" :label="n" />
        </el-select>
      </div>

      <el-alert
        v-if="graph.truncated"
        type="warning"
        :closable="false"
        show-icon
        title="命中关系超过上限，图只画出了前一部分"
        description="缩小筛选范围（例如只看某一种关系或某一台机器）可以看到全部。静默省略会让人以为关系就这么多。"
      />

      <TopologyGraph
        v-if="graph.edges.length"
        :data="graph"
        :re-center="false"
        :height="520"
        @node-click="openAsset"
      />
      <EmptyState
        v-else
        :icon="Share"
        title="当前筛选下没有关系"
        :hints="[
          '清除筛选可以看到全部关系',
          '若总览也是空的，说明还没有任何关系被建立',
        ]"
      />
    </div>
  </div>
</template>

<script setup>
import { reactive, ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import http from '../../api/http'
import { getLinkStats, getTopologyGraph } from '../../api/asset'
import PageHeader from '../common/PageHeader.vue'
import EmptyState from '../common/EmptyState.vue'
import { Share } from '@element-plus/icons-vue'
import TopologyGraph from './TopologyGraph.vue'

const router = useRouter()

// 与资产详情页共用一套措辞（两处必须一致，否则同一张图在两个入口读起来像两种关系）
const KIND_LABELS = {
  runs_on: '运行于',
  member_of: '归属',
  depends_on: '依赖',
  exposes: '暴露',
}
const kindLabel = (k) => KIND_LABELS[k] || k

// 类型名称：内置类型的标题由服务端给（列表接口带 typeTitle），这里只兜底少数几个常用值。
const TYPE_TITLES = {
  host: '主机',
  'middleware-instance': '中间件实例',
  pod: '容器（Pod）',
  workload: '工作负载',
  service: 'K8s 服务',
}
const typeTitle = (k) => TYPE_TITLES[k] || k

const loading = ref(false)
const loadError = ref('')
const overview = reactive({ groups: [], totalLinks: 0, assetsWithoutLinks: 0 })
const graph = reactive({ nodes: [], edges: [], total: 0, truncated: false })
const nodes = ref([]) // 归属节点下拉的候选（来自节点列表）
const filters = reactive({ kind: [], source: '', type: [], node: [] })

const hasFilter = computed(() =>
  filters.kind.length > 0 || filters.source !== '' || filters.type.length > 0 || filters.node.length > 0)

// 类型下拉的候选取自**总览里出现过的类型**：不引新接口，而且候选项按定义就是相关的
// （列一堆当前没有关系的类型只会让人以为筛选坏了）。
const typeOptions = computed(() => {
  const set = new Set()
  for (const g of overview.groups) {
    set.add(g.fromType)
    set.add(g.toType)
  }
  return [...set].sort()
})

async function loadOverview() {
  try {
    const data = await getLinkStats()
    overview.groups = data.groups || []
    overview.totalLinks = Number(data.totalLinks) || 0
    overview.assetsWithoutLinks = Number(data.assetsWithoutLinks) || 0
    loadError.value = ''
  } catch (e) {
    overview.groups = []
    overview.totalLinks = 0
    overview.assetsWithoutLinks = 0
    loadError.value = '关系总览加载失败：' + (e.message || '请稍后重试')
  }
}

async function loadGraph() {
  try {
    const data = await getTopologyGraph({
      kind: filters.kind,
      source: filters.source,
      type: filters.type,
      node: filters.node,
    })
    graph.nodes = data.nodes || []
    graph.edges = data.edges || []
    graph.total = Number(data.total) || 0
    graph.truncated = !!data.truncated
  } catch (e) {
    graph.nodes = []
    graph.edges = []
    graph.total = 0
    graph.truncated = false
    loadError.value = '关系图加载失败：' + (e.message || '请稍后重试')
  }
}

async function reload() {
  loading.value = true
  try {
    await Promise.all([loadOverview(), loadGraph()])
  } finally {
    loading.value = false
  }
}

// 点总览的一行 → 把图切到"这一类关系"。三个维度都带上：来源类型也带上，
// 否则点"主机 → 容器"那行会顺带把"中间件实例 → 容器"也带进来（那不是用户点的东西）。
function applyGroup(row) {
  filters.kind = [row.kind]
  filters.source = row.source || ''
  filters.type = row.fromType ? [row.fromType] : []
  filters.node = []
  loadGraph()
}

function clearFilters() {
  filters.kind = []
  filters.source = ''
  filters.type = []
  filters.node = []
  loadGraph()
}

// 数据模式下点节点 → 打开该资产详情（台账页支持 ?id= 深链，抽屉里还有它自己的邻域图）。
function openAsset(node) {
  if (!node || !node.id) return
  router.push({ path: '/assets', query: { id: String(node.id) } })
}

async function loadNodes() {
  try {
    const data = await http.get('/api/v1/nodes')
    nodes.value = (data.nodes || []).map((n) => n.hostname).filter(Boolean).sort()
  } catch (e) {
    nodes.value = [] // 节点列表拿不到只是少一个筛选维度，不该让整页报错
  }
}

onMounted(() => {
  loadNodes()
  reload()
})
</script>

<style scoped>
.asset-topology-view { display: flex; flex-direction: column; gap: 16px; }
.sec { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; }
.sec > span:first-child { font-weight: 600; }
.summary-line { display: flex; gap: 24px; margin-bottom: 10px; font-size: 13px; }
.summary-line b { font-size: 15px; }
.summary-line .warn b { color: #e6a23c; }
.ov-table :deep(.el-table__row) { cursor: pointer; }
.tag { font-size: 12px; padding: 1px 6px; border-radius: 3px; background: rgba(74, 157, 240, 0.14); }
.filters { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 10px; }
.muted { color: var(--text-muted, #8a94a6); font-size: 13px; }
.mono { font-family: ui-monospace, SFMono-Regular, Consolas, monospace; }
</style>
