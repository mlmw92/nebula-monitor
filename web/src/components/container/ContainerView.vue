<template>
  <div class="container-view">
    <PageHeader
      title="容器与工作负载"
      desc="只读查看 K8s 集群的工作负载、Pod 与事件。指令随 Agent 上报下发，结果约一个上报周期后返回"
    >
      <template #actions>
        <el-select
          v-model="clusterKey"
          placeholder="选择集群"
          size="small"
          class="cluster-select"
          :loading="loading"
          @change="onClusterChange"
        >
          <el-option
            v-for="c in clusters"
            :key="keyOf(c)"
            :value="keyOf(c)"
            :label="clusterLabel(c)"
          />
        </el-select>
        <el-button size="small" :icon="Refresh" :loading="loading" @click="loadClusters">刷新集群</el-button>
      </template>
    </PageHeader>

    <!-- 没配集群时先把"怎么配"说清楚，而不是给一个空表格 -->
    <EmptyState
      v-if="!loading && clusters.length === 0"
      :icon="Grid"
      title="尚未采集到 Kubernetes 集群"
      :hints="[
        '在目标机器的 agent.yaml 里配置 k8sInstances（apiserver 地址 + kubeconfig 或 token），凭据只留在该机器上',
        '确认该节点在线，且 Agent 已开启 K8s 采集（collectors.k8s: true）',
        '配置生效需要等一个上报周期',
      ]"
    />

    <template v-else>
      <el-tabs v-model="activeTab" type="card" class="container-tabs">
        <el-tab-pane v-for="t in TABS" :key="t.key" :label="t.title" :name="t.key" />
      </el-tabs>

      <div v-if="!currentCluster" class="cluster-hint">请选择要查看的集群</div>

      <template v-else>
        <div v-if="!currentCluster.up" class="offline-hint">
          该集群当前不在线（上报节点 {{ currentCluster.node }} 可能已离线），查询会被服务端拒绝
        </div>

        <SectionCard title="查询结果" dense>
          <template #actions>
            <div class="query-toolbar">
              <el-input
                v-model="namespace"
                placeholder="命名空间（留空为全部）"
                size="small"
                clearable
                class="ns-input"
                @keyup.enter="run(activeTab)"
              />
              <el-button
                size="small"
                type="primary"
                :loading="q.busy"
                :disabled="!canQuery"
                @click="run(activeTab)"
              >
                查询
              </el-button>
            </div>
          </template>

          <el-alert
            v-if="q.error"
            type="error"
            :closable="false"
            show-icon
            class="query-error"
            :title="q.error"
          />

          <!-- 异步任务：必须把"在等 Agent 上报"这件事说清楚，否则用户会以为按钮没反应 -->
          <div v-if="q.busy && !q.error" class="query-state">
            <span class="state-dot" :class="q.task ? 'delivering' : 'pending'"></span>
            <span>{{ stateText }}</span>
            <span class="state-task mono">{{ q.task ? q.task.id : '' }}</span>
            <el-button
              v-if="q.task && q.task.state === 'queued'"
              link
              size="small"
              @click="cancel(activeTab)"
            >
              撤回
            </el-button>
          </div>

          <template v-if="result">
            <div v-if="result.notice" class="result-notice">{{ result.notice }}</div>
            <el-table
              :data="resultRows"
              size="small"
              stripe
              :max-height="520"
              class="result-table"
              @row-click="onRowClick"
            >
              <el-table-column
                v-for="(col, i) in result.columns"
                :key="col + i"
                :label="col"
                :min-width="i === 0 ? 140 : 110"
                show-overflow-tooltip
              >
                <template #default="{ row }">{{ row.cells[i] }}</template>
              </el-table-column>
              <el-table-column v-if="canDescribe" label="操作" width="90" fixed="right">
                <template #default="{ row }">
                  <el-button link size="small" @click.stop="describeRow(row)">详情</el-button>
                </template>
              </el-table-column>
            </el-table>

            <div class="result-foot">
              <span>
                共 {{ result.total }} 个对象{{ result.truncated ? '，仅显示前 ' + result.rows.length + ' 条' : '' }}
              </span>
              <span v-if="result.truncated" class="foot-warn">结果已截断，请缩小命名空间范围</span>
            </div>
          </template>

          <EmptyState
            v-else-if="!q.busy && !q.error"
            :icon="activeIcon"
            title="还没有查询结果"
            :hints="[
              '已自动按当前集群提交查询，结果通常 15 秒内返回',
              '换集群或切 Tab 会自动重查，也可以点「查询」手动刷新',
            ]"
          />
        </SectionCard>
      </template>
    </template>

    <!-- 对象详情：只读投影，敏感字段不下发 -->
    <el-drawer v-model="detailVisible" :title="detailTitle" size="620px">
      <div v-if="detailBusy" class="drawer-loading">正在查询（等待 Agent 上报）…</div>
      <el-alert v-else-if="detailError" type="error" :closable="false" show-icon :title="detailError" />
      <template v-else-if="detail">
        <div v-if="detail.notice" class="result-notice">{{ detail.notice }}</div>
        <el-table :data="detailRows" size="small" stripe style="width: 100%">
          <el-table-column prop="field" label="字段" width="150" />
          <el-table-column prop="value" label="值" min-width="300" show-overflow-tooltip />
        </el-table>
      </template>
    </el-drawer>
  </div>
</template>

<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { Refresh, Grid, Monitor, Bell } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import http from '../../api/http'
import { createOpsTask, getOpsTask, cancelOpsTasks } from '../../api/ops'
import PageHeader from '../common/PageHeader.vue'
import SectionCard from '../common/SectionCard.vue'
import EmptyState from '../common/EmptyState.vue'

// 容器只读管理面。
//
// 交互形态由 ADR-0003 决定：Server 不直连 apiserver（凭据只在 Agent 本地），指令随 Agent
// 上报的响应下发、回执随下一轮上报送回。所以这里是"提交 → 轮询"的异步任务，不是点开即返回。
const TABS = [
  { key: 'workloads', kind: 'container.workloads', title: '工作负载', icon: Grid },
  { key: 'pods', kind: 'container.pods', title: 'Pod', icon: Monitor },
  { key: 'events', kind: 'container.events', title: '事件', icon: Bell },
]

// 详情要用到的定位信息。刻意在这里显式写出列下标，而不是让后端在载荷里塞一个"魔法列"：
// 载荷是通用表格，藏字段会让它变得不可预期。
const DETAIL_SPEC = {
  workloads: {
    nsCol: 1,
    nameCol: 2,
    resourceByKind: {
      Deployment: 'deployments',
      StatefulSet: 'statefulsets',
      DaemonSet: 'daemonsets',
      Job: 'jobs',
    },
  },
  pods: { nsCol: 0, nameCol: 1, resource: 'pods' },
}

const POLL_INTERVAL = 3000
const POLL_TIMEOUT = 60000

const clusters = ref([])
const loading = ref(false)
const clusterKey = ref('')
const namespace = ref('')
const activeTab = ref('workloads')

const query = reactive({
  workloads: { busy: false, task: null, error: '', result: null },
  pods: { busy: false, task: null, error: '', result: null },
  events: { busy: false, task: null, error: '', result: null },
})

const detailVisible = ref(false)
const detailBusy = ref(false)
const detailError = ref('')
const detail = ref(null)
const detailTitle = ref('对象详情')

const timers = new Set()

const activeMeta = computed(() => TABS.find((t) => t.key === activeTab.value) || TABS[0])
const activeIcon = computed(() => activeMeta.value.icon)
const q = computed(() => query[activeTab.value])
const currentCluster = computed(() => clusters.value.find((c) => keyOf(c) === clusterKey.value) || null)
const result = computed(() => q.value.result)
const canQuery = computed(() => !!currentCluster.value && currentCluster.value.up && !q.value.busy)
const canDescribe = computed(() => !!DETAIL_SPEC[activeTab.value])

// 表格行：把 [][]string 摊成对象，模板里好取值（保留原始 cells，详情要用）。
const resultRows = computed(() => (result.value?.rows || []).map((cells) => ({ cells })))
const detailRows = computed(() =>
  (detail.value?.rows || []).map((cells) => ({ field: cells[0], value: cells[1] }))
)

const stateText = computed(() => {
  const t = q.value.task
  if (!t) return '正在提交任务…'
  switch (t.state) {
    case 'queued':
      return '已排队，等待 Agent 下一轮上报时领取…'
    case 'delivered':
      return 'Agent 已领取，正在查询…'
    case 'running':
      return '正在查询…'
    default:
      return '处理中…'
  }
})

const keyOf = (c) => c.node + '|' + c.instance
const clusterLabel = (c) => `${c.name || c.instance}（${c.node}${c.up ? '' : ' · 离线'}）`
const isTerminal = (state) => ['succeeded', 'failed', 'expired', 'cancelled'].includes(state)

/* ===== 集群清单 ===== */
async function loadClusters() {
  loading.value = true
  try {
    const res = await http.get('/api/v1/container/k8s/clusters')
    clusters.value = res.clusters || []
    // 默认选第一个在线集群：直接给一个可用的起点，省掉一次"再选一次"。
    if (!clusters.value.some((c) => keyOf(c) === clusterKey.value)) {
      const first = clusters.value.find((c) => c.up) || clusters.value[0]
      clusterKey.value = first ? keyOf(first) : ''
    }
  } catch (e) {
    clusters.value = []
    ElMessage.error(e.message || '加载集群清单失败')
  } finally {
    loading.value = false
  }
}

function onClusterChange() {
  // 换集群后上一个结果已经没有意义：清掉，避免"看着 A 的表以为是 B 的"。
  for (const t of TABS) {
    query[t.key].result = null
    query[t.key].error = ''
    query[t.key].task = null
  }
  // 清完顺手把当前 Tab 查一次，免得换完集群又对着空白面板。
  autoRun(activeTab.value)
}

// autoRun 只在"这个 Tab 还没有结果"时自动发起查询。
//
// 为什么要自动查：全平台其它 Tab 都是自动加载，只有这一页要手点「查询」，
// 第一眼看到「还没有查询结果」会以为功能坏了（真实用户反馈）。
//
// 命名空间输入**不**触发自动查询：那是"改条件"而不是"换视图"，
// 边打字边下发任务既吵又费，那种场景让用户点「查询」更合适。
function autoRun(tab) {
  const item = query[tab]
  if (!item || item.busy || item.result) return
  if (!currentCluster.value || !currentCluster.value.up) return
  run(tab)
}

/* ===== 提交与轮询 ===== */
async function run(tab) {
  const target = TABS.find((t) => t.key === tab)
  const item = query[tab]
  if (!target || !currentCluster.value) return

  item.error = ''
  item.result = null
  item.task = null
  item.busy = true
  try {
    const params = { cluster: currentCluster.value.name || currentCluster.value.instance }
    const ns = namespace.value.trim()
    if (ns) params.namespace = ns
    const res = await createOpsTask({
      node: currentCluster.value.node,
      kind: target.kind,
      params,
      reason: '容器只读查询',
    })
    const task = res && res.task
    if (!task || !task.id) throw new Error('服务端未返回任务 ID')
    item.task = task
    // 任务创建本身就是一次真实下发：先把状态摆出来，再等回执。
    await waitTask(
      task.id,
      (t) => {
        item.task = t
      },
      (t, err) => {
        if (err) {
          item.error = err
        } else if (t.state !== 'succeeded') {
          item.error = taskFailureText(t)
        } else {
          item.result = parsePayload(t)
          if (!item.result) item.error = t.message || '任务成功但未返回结构化结果'
        }
        item.busy = false
      }
    )
  } catch (e) {
    // 服务端的说明就是给用户的下一步（升级 Agent / 改 guards.ops / 换命名空间），原样展示。
    item.error = e.message || '下发失败'
    item.busy = false
  }
}

// waitTask 轮询到终态；超时给明确结论，而不是让界面永远转圈。
function waitTask(id, onTick, done) {
  return new Promise((resolve) => {
    let waited = 0
    const tick = async () => {
      let task = null
      try {
        const res = await getOpsTask(id)
        task = res && res.task
      } catch (e) {
        done(null, e.message || '查询任务失败')
        resolve()
        return
      }
      if (task) {
        onTick(task)
        if (isTerminal(task.state)) {
          done(task)
          resolve()
          return
        }
      }
      waited += POLL_INTERVAL
      if (waited >= POLL_TIMEOUT) {
        done(null, '等待超过 60 秒仍未返回。确认目标节点在线，且已升级到支持容器查询的 Agent 版本')
        resolve()
        return
      }
      const h = setTimeout(() => {
        timers.delete(h)
        tick()
      }, POLL_INTERVAL)
      timers.add(h)
    }
    tick()
  })
}

function taskFailureText(t) {
  if (t.state === 'expired') return '任务已过期：Agent 未在有效期内领取，通常意味着目标节点离线或未声明支持该动作'
  if (t.state === 'cancelled') return '任务已被撤回'
  return t.message || '任务未成功'
}

function parsePayload(task) {
  if (!task || !task.json) return null
  try {
    return JSON.parse(task.json)
  } catch (e) {
    return null
  }
}

async function cancel(tab) {
  const task = query[tab].task
  if (!task) return
  try {
    await cancelOpsTasks({ ids: [task.id] })
    query[tab].error = ''
    query[tab].busy = false
    query[tab].task = null
    ElMessage.success('已撤回')
  } catch (e) {
    ElMessage.error(e.message || '撤回失败：任务可能已被 Agent 领取')
  }
}

/* ===== 对象详情 ===== */
async function onRowClick(row) {
  if (canDescribe.value) await describeRow(row)
}

async function describeRow(row) {
  const spec = DETAIL_SPEC[activeTab.value]
  if (!spec || !currentCluster.value) return
  const cells = row.cells || []
  const name = cells[spec.nameCol]
  const ns = cells[spec.nsCol]
  const resource = spec.resource || spec.resourceByKind[cells[0]]
  if (!name || !ns || !resource) return

  detailTitle.value = `${resource}/${name}`
  detail.value = null
  detailError.value = ''
  detailBusy.value = true
  detailVisible.value = true

  try {
    const res = await createOpsTask({
      node: currentCluster.value.node,
      kind: 'container.describe',
      params: {
        cluster: currentCluster.value.name || currentCluster.value.instance,
        namespace: ns,
        resource,
        name,
      },
      reason: '容器对象详情',
    })
    const task = res && res.task
    if (!task || !task.id) throw new Error('服务端未返回任务 ID')
    await waitTask(
      task.id,
      () => {},
      (t, err) => {
        detailBusy.value = false
        if (err) {
          detailError.value = err
        } else if (t.state !== 'succeeded') {
          detailError.value = taskFailureText(t)
        } else {
          detail.value = parsePayload(t)
          if (!detail.value) detailError.value = t.message || '未返回结构化结果'
        }
      }
    )
  } catch (e) {
    detailError.value = e.message || '下发失败'
    detailBusy.value = false
  }
}

onMounted(async () => {
  await loadClusters()
  // 进页面就把当前 Tab 查一次（同上：不要让人对着空白面板猜）
  autoRun(activeTab.value)
})

// 切 Tab 时若该 Tab 还没有结果，也自动查一次；已有结果不重查，避免重复下发任务。
watch(activeTab, (tab) => {
  nextTick(() => autoRun(tab))
})

onBeforeUnmount(() => {
  for (const h of timers) clearTimeout(h)
  timers.clear()
})
</script>

<style scoped>
.container-view {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.cluster-select {
  width: 240px;
}
.container-tabs {
  margin-bottom: 0;
}
.container-tabs :deep(.el-tabs__header) {
  margin: 0;
}
.container-tabs :deep(.el-tabs__content) {
  display: none;
}
.cluster-hint,
.offline-hint {
  padding: 10px 14px;
  border-radius: var(--r-md);
  border: 1px solid var(--bd);
  background: var(--fill-1);
  font-size: var(--fs-sm);
  color: var(--t2);
}
.offline-hint {
  border-color: var(--warn-bd);
  background: var(--warn-dim);
  color: var(--warn);
}
.query-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
}
.ns-input {
  width: 220px;
}
.query-error {
  margin-bottom: 12px;
}
.query-state {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 14px;
  margin-bottom: 12px;
  border-radius: var(--r-md);
  border: 1px solid var(--bd);
  background: var(--fill-1);
  font-size: var(--fs-sm);
  color: var(--t2);
}
.state-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}
.state-dot.pending {
  background: var(--warn);
}
.state-dot.delivering {
  background: var(--accent);
}
.state-task {
  color: var(--t3);
  font-size: var(--fs-xs);
}
.result-notice {
  margin-bottom: 10px;
  padding: 8px 12px;
  border-radius: var(--r-sm);
  background: var(--info-dim);
  border: 1px solid var(--info-bd);
  color: var(--info);
  font-size: var(--fs-sm);
}
.result-table :deep(.el-table__row) {
  cursor: pointer;
}
.result-foot {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-top: 10px;
  font-size: var(--fs-xs);
  color: var(--t3);
}
.foot-warn {
  color: var(--warn);
}
.drawer-loading {
  padding: 24px 0;
  text-align: center;
  color: var(--t3);
  font-size: var(--fs-sm);
}
</style>
