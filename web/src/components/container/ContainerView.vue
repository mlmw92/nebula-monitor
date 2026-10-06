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
                title="按当前集群与命名空间重新下发一次查询（这是这一页唯一会重新下发的地方）"
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
              <el-table-column v-if="canDescribe || canLogs || canLedger" label="操作" :width="canLogs ? 160 : 120" fixed="right">
                <template #default="{ row }">
                  <el-button v-if="canDescribe" link size="small" @click.stop="describeRow(row)">详情</el-button>
                  <el-button v-if="canLogs" link size="small" @click.stop="openLogs(row)">日志</el-button>
                  <el-button v-if="canLedger" link size="small" :loading="ledgerBusy" @click.stop="openLedger(row)">台账</el-button>
                </template>
              </el-table-column>
            </el-table>

            <div class="result-foot">
              <span>
                共 {{ result.total }} 个对象{{ result.truncated ? '，仅显示前 ' + result.rows.length + ' 条' : '' }}
              </span>
              <!-- 数据来自异步下发，界面必须回答"这是什么时候的数据"：
                   否则用户唯一的安全假设就是"它可能过期了，再点一次吧"，然后一直白等。 -->
              <span v-if="q.busy" class="foot-time">正在刷新…</span>
              <span
                v-else-if="q.at"
                class="foot-time"
                :title="'上次刷新：' + new Date(q.at).toLocaleString('zh-CN')"
              >上次刷新 {{ refreshedText(q.at) }}</span>
              <span v-if="result.truncated" class="foot-warn">结果已截断，请缩小命名空间范围</span>
              <!-- 结果会被保留、命名空间却随时可改：条件变了必须说出来，
                   否则就是"输入框写着 default、表里是全部命名空间的对象"而界面一言不发。 -->
              <span v-if="q.at && q.ns !== namespace.trim()" class="foot-warn">
                这份结果是「{{ q.ns || '全部命名空间' }}」的，改条件后请点「查询」
              </span>
            </div>
          </template>

          <EmptyState
            v-else-if="!q.busy && !q.error"
            :icon="activeIcon"
            title="还没有查询结果"
            :hints="[
              '已按当前集群提交一次查询，结果通常 15 秒内返回',
              '结果会保留：切集群、切 Tab 都不会重新下发，需要最新数据时点「查询」',
            ]"
          />
        </SectionCard>
      </template>
    </template>

    <!-- 对象详情：只读投影，敏感字段不下发 -->
    <el-drawer v-model="detailVisible" :title="detailTitle" size="620px">
      <!-- 与主表同一套规矩：点开行不重新下发，要最新的自己点「刷新」 -->
      <div class="detail-bar">
        <span class="muted">
          <template v-if="detailBusy">正在刷新…</template>
          <template v-else-if="detailState.at">上次刷新 {{ refreshedText(detailState.at) }}</template>
          <template v-else>尚未查询</template>
        </span>
        <el-button size="small" :loading="detailBusy" @click="refreshDetail">刷新</el-button>
      </div>
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
    <!-- Pod 日志：与详情同一套异步规矩——拉一次就缓存，要看最新的点「重新拉取」 -->
    <el-drawer v-model="logsVisible" :title="logsTitle" size="720px">
      <div class="detail-bar">
        <span class="muted">
          <template v-if="logsBusy">正在拉取（等待 Agent 上报）…</template>
          <template v-else-if="logsState.at">上次刷新 {{ refreshedText(logsState.at) }}</template>
          <template v-else>尚未拉取</template>
        </span>
        <div class="logs-toolbar">
          <el-input
            v-model="logsContainer"
            placeholder="容器（留空为第一个）"
            size="small"
            clearable
            class="logs-container"
          />
          <el-button size="small" :loading="logsBusy" @click="fetchLogs(true)">重新拉取</el-button>
        </div>
      </div>
      <div v-if="logsBusy && !logs" class="drawer-loading">正在拉取（等待 Agent 上报）…</div>
      <el-alert v-else-if="logsError" type="error" :closable="false" show-icon :title="logsError" />
      <template v-else-if="logs">
        <div v-if="logs.notice" class="result-notice">{{ logs.notice }}</div>
        <!-- 日志必须按原样呈现（保留空白与换行）：重排过的堆栈/表格等于换了一份内容 -->
        <pre v-if="logText" class="log-pre">{{ logText }}</pre>
        <EmptyState
          v-else
          title="这段时间窗内没有日志"
          :hints="['容器可能还没启动，或该时间段确实没有输出', '可换一个容器名，或直接上机器查看完整日志']"
        />
      </template>
    </el-drawer>
  </div>
</template>

<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Refresh, Grid, Monitor, Bell } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import http from '../../api/http'
import { createOpsTask, getOpsTask, cancelOpsTasks, listOpsTasks } from '../../api/ops'
import { lookupAsset } from '../../api/asset'
import PageHeader from '../common/PageHeader.vue'
import SectionCard from '../common/SectionCard.vue'
import EmptyState from '../common/EmptyState.vue'
import { containerDetailState, containerQueryState } from './queryCache'

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

const route = useRoute()
const router = useRouter()
const clusters = ref([])
const loading = ref(false)
const clusterKey = ref('')
const namespace = ref('')
const activeTab = ref('workloads')

const detailVisible = ref(false)
const detailTitle = ref('对象详情')

// 查询状态（含结果与**结果落地时间**）存在组件之外，理由见 queryCache.js：
// 结果来自异步下行任务（下发 → 等 Agent 上报 → 回执，通常 15 秒以上），
// 只放在组件状态里的话，每次路由切回来都会丢掉它、于是"点进这一页"就等于重新下发一轮。
const q = computed(() => containerQueryState(clusterKey.value, activeTab.value))
const currentCluster = computed(() => clusters.value.find((c) => keyOf(c) === clusterKey.value) || null)
const result = computed(() => q.value.result)
const canQuery = computed(() => !!currentCluster.value && currentCluster.value.up && !q.value.busy)
const canDescribe = computed(() => !!DETAIL_SPEC[activeTab.value])
// 日志只对 Pod 有意义：工作负载/事件列表里的对象不是容器实例。
const canLogs = computed(() => activeTab.value === 'pods')
// 台账入口：只有 Pod 与工作负载会落台账资产（事件不是对象；Job 刻意不建资产）。
const canLedger = computed(() => activeTab.value === 'pods' || activeTab.value === 'workloads')

// 工作负载列表的「类型」列 → 台账资产的自然键片段。
// Job 不在其中：台账不给 Job 建资产（它的 Pod 的 member_of 因此为空，那是如实的"没有归属"）。
const WORKLOAD_LEDGER_KINDS = { Deployment: 'deployment', StatefulSet: 'statefulset', DaemonSet: 'daemonset' }
const ledgerBusy = ref(false)

// 详情抽屉也走同一套缓存：重复点同一行同样是一次 15 秒的下发，没理由重来一遍。
const detailKey = ref({ cluster: '', kind: '', namespace: '', name: '' })
// 记住打开的是哪一行：抽屉里的「刷新」要能重新下发同一个对象。
const detailRow = ref(null)
const detailState = computed(() => containerDetailState(
  detailKey.value.cluster, detailKey.value.kind, detailKey.value.namespace, detailKey.value.name
))
const detailBusy = computed(() => detailState.value.busy)
const detailError = computed(() => detailState.value.error)
const detail = computed(() => detailState.value.result)

// Pod 日志抽屉：与详情共用同一套「异步下发 + 缓存 + 上次刷新」状态机。
// 缓存键里带上容器名：换容器是换内容，不能把上一个容器的日志显示在新容器名下。
const logsVisible = ref(false)
const logsTitle = ref('Pod 日志')
const logsKey = ref({ cluster: '', kind: '', namespace: '', name: '' })
const logsTarget = ref(null)
const logsContainer = ref('')
const logsState = computed(() => containerDetailState(
  logsKey.value.cluster, logsKey.value.kind, logsKey.value.namespace, logsKey.value.name
))
const logsBusy = computed(() => logsState.value.busy)
const logsError = computed(() => logsState.value.error)
const logs = computed(() => logsState.value.result)
const logText = computed(() => (logs.value?.rows || []).map((cells) => cells[0]).join('\n'))

const timers = new Set()

// now 只为把"上次刷新"的相对时间刷新成真话：只显示绝对时刻的话，
// "10:31:02" 需要在脑子里减一遍才知道多久以前；只显示"3 分钟前"则不会自己走。
const now = ref(Date.now())
let nowTimer = null

const activeMeta = computed(() => TABS.find((t) => t.key === activeTab.value) || TABS[0])
const activeIcon = computed(() => activeMeta.value.icon)

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
  // 换集群**不清缓存**：缓存按「集群 + 动作」分键，切回来要能直接看到那个集群上次的结果与时间
  // ——那正是"记住上次刷新的状态"。仍然不会张冠李戴：显示的结果取自
  // (当前集群, 当前 Tab) 这一个键，结构上就不可能把 A 的表显示在 B 下面。
  autoRun(activeTab.value)
}

// autoRun 只在"这个（集群 + Tab）还没有结果"时自动发起**一次**查询。
//
// 为什么要自动查一次：全平台其它 Tab 都是自动加载，只有这一页要手点「查询」，
// 第一眼看到「还没有查询结果」会以为功能坏了（真实用户反馈）。
//
// 为什么只查一次：结果是异步下发来的（通常 15 秒以上），重复下发纯属让用户白等。
// 已经有结果时一律等用户点「查询」——数据新鲜度由界面上那句"上次刷新"负责说明。
//
// 命名空间输入**不**触发自动查询：那是"改条件"而不是"换视图"，
// 边打字边下发任务既吵又费，那种场景让用户点「查询」更合适。
async function autoRun(tab) {
  const item = containerQueryState(clusterKey.value, tab)
  if (!item || item.busy || item.result) return
  if (!currentCluster.value || !currentCluster.value.up) return
  // 先尝试认领服务端留下的上次结果（秒出），没有再下发。
  if (await adoptLastResult(tab)) return
  run(tab)
}

// adoptLastResult 从服务端任务列表里"认领"该（集群 + 动作 + 命名空间）最近一次成功的回执。
//
// 为什么需要它：结果本身是**持久化**的（服务端 `ops_tasks.json` 保留最近 500 条任务，
// 回执的结构化载荷就在每条任务的 `json` 字段里），而前端缓存只在内存里 ——
// 浏览器刷新（F5）之后本地什么都没有，若直接自动下发，用户又要白等一个上报周期。
// 既然那份结果还在服务端，就先把它拿来用：普通 HTTP、**不惊动 Agent**、秒出。
//
// 时间用任务**完成**的时刻（doneAt）而不是现在：这才是这份数据真实的新鲜度，
// 界面照实显示"上次刷新 3 小时前"，而不是把一份旧快照说成刚查的。
//
// 失败一律返回 false 让调用方走正常下发：任务列表要 `ops:read`，
// 只有 `container:read` 的账号拿不到它——那是权限差异，不该让这一页报错或空白。
async function adoptLastResult(tab) {
  const target = TABS.find((t) => t.key === tab)
  const item = containerQueryState(clusterKey.value, tab)
  const cluster = currentCluster.value
  if (!target || !item || !cluster || item.result || item.busy) return false
  const ns = namespace.value.trim()
  const clusterName = cluster.name || cluster.instance

  let tasks = []
  try {
    const res = await listOpsTasks({ node: cluster.node, kind: target.kind, state: 'succeeded', limit: 50 })
    tasks = (res && res.tasks) || []
  } catch (e) {
    return false
  }

  // 命名空间必须完全一致才认领：条件是"全部命名空间"时，不能拿一条 default 的结果来顶。
  const hit = tasks.find((t) => {
    const p = t.params || {}
    return (p.cluster || '') === clusterName && (p.namespace || '') === ns
  })
  if (!hit) return false
  const payload = parsePayload(hit)
  if (!payload) return false

  item.result = payload
  item.ns = ns
  item.error = ''
  item.at = hit.doneAt || hit.createdAt || 0
  return true
}

// refreshedText 把"上次刷新时间"说成人话：绝对时刻 + 相对时长。
//
// 两个都要：只有绝对时刻要在脑子里减一遍；只有相对时长则不会自己走（且刷新后再看还是旧文案）。
function refreshedText(at) {
  if (!at) return ''
  const clock = new Date(at).toLocaleTimeString('zh-CN', { hour12: false })
  const diff = Math.max(0, now.value - at)
  if (diff < 60_000) return `${clock} · 刚刚`
  if (diff < 3600_000) return `${clock} · ${Math.floor(diff / 60_000)} 分钟前`
  if (diff < 86400_000) return `${clock} · ${Math.floor(diff / 3600_000)} 小时前`
  return `${clock} · ${Math.floor(diff / 86400_000)} 天前`
}

/* ===== 提交与轮询 ===== */
async function run(tab) {
  const target = TABS.find((t) => t.key === tab)
  const item = containerQueryState(clusterKey.value, tab)
  if (!target || !currentCluster.value) return

  item.error = ''
  // **不清 result**：刷新期间旧结果继续留在表里（配合"上次刷新"那句），
  // 只在成功回来后才替换。否则点一次刷新就是白屏等 15 秒，
  // 而"看到一份略旧的数据 + 明确的刷新时间"比白屏好判断得多。
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
          if (!item.result) {
            item.error = t.message || '任务成功但未返回结构化结果'
          } else {
            // 只有结果真的落地才推进"上次刷新"：失败时保留旧时间，
            // 界面才不会把一次失败说成"刚刷新过"。
            item.at = Date.now()
            // 一并记下这份结果是按什么条件查的（见 queryCache 里 ns 的说明）
            item.ns = ns
          }
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
  // 与 run 用同一处取法：`q` 是当前 Tab 的计算属性，这里按传入的 tab 取状态，
  // 免得"看着是 A 的按钮、撤的是 B 的任务"。
  const item = containerQueryState(clusterKey.value, tab)
  const task = item.task
  if (!task) return
  try {
    await cancelOpsTasks({ ids: [task.id] })
    item.error = ''
    item.busy = false
    item.task = null
    ElMessage.success('已撤回')
  } catch (e) {
    ElMessage.error(e.message || '撤回失败：任务可能已被 Agent 领取')
  }
}

/* ===== 对象详情 ===== */
async function onRowClick(row) {
  if (canDescribe.value) await describeRow(row)
}

// describeRow 打开某个对象的详情。force=true 表示用户明确要求刷新（抽屉里的「刷新」）。
//
// 默认**命中缓存不再下发**：点一下行就是一次 15 秒的下发，而用户点开往往只是"看看"，
// 来回点几行就要等好几轮。要看最新的点抽屉里的「刷新」——与主表的规矩一致。
async function describeRow(row, force = false) {
  const spec = DETAIL_SPEC[activeTab.value]
  if (!spec || !currentCluster.value) return
  const cells = row.cells || []
  const name = cells[spec.nameCol]
  const ns = cells[spec.nsCol]
  const resource = spec.resource || spec.resourceByKind[cells[0]]
  if (!name || !ns || !resource) return

  detailTitle.value = `${resource}/${name}`
  detailKey.value = { cluster: clusterKey.value, kind: resource, namespace: ns, name }
  detailRow.value = row
  detailVisible.value = true

  const st = containerDetailState(clusterKey.value, resource, ns, name)
  if (st.result && !force) return // 有上次的结果：直接显示，连同它的刷新时间

  st.error = ''
  st.task = null
  st.busy = true
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
    st.task = task
    await waitTask(
      task.id,
      () => {},
      (t, err) => {
        st.busy = false
        if (err) {
          st.error = err
        } else if (t.state !== 'succeeded') {
          st.error = taskFailureText(t)
        } else {
          st.result = parsePayload(t)
          if (!st.result) {
            st.error = t.message || '未返回结构化结果'
          } else {
            st.at = Date.now()
          }
        }
      }
    )
  } catch (e) {
    st.error = e.message || '下发失败'
    st.busy = false
  }
}

// refreshDetail 是抽屉里的「刷新」：显式重新下发同一个对象的详情。
function refreshDetail() {
  if (detailRow.value) describeRow(detailRow.value, true)
}

/* ===== Pod 日志 ===== */

// openLogs 打开某个 Pod 的日志抽屉。默认**命中缓存不再下发**（与详情同一规矩）：
// 点一下就是一次 15 秒的下发，用户点开往往只是"看看"。
function openLogs(row) {
  const spec = DETAIL_SPEC.pods
  const cells = row.cells || []
  openLogsFor(cells[spec.nsCol], cells[spec.nameCol])
}

// openLogsFor 是 openLogs 的本体：反向联动（从台账点进来）只有身份、没有表格行，
// 因此身份必须能独立传入——否则那条路径只能伪造一行假数据。
function openLogsFor(namespace, pod) {
  if (!namespace || !pod) return
  logsTarget.value = { namespace, pod }
  logsContainer.value = ''
  logsTitle.value = '日志 · ' + pod
  logsVisible.value = true
  fetchLogs(false)
}

/* ===== 跨页联动 ===== */

// openLedger 跳到台账里这条 Pod / 工作负载的资产详情。
//
// 自然键由服务端按身份拼（前端不拼）：集群取 **apiserver 地址**而不是别名——
// 台账的 Pod/工作负载资产就是按地址建的（见 collector.k8s.go 的 clusterOf）。
async function openLedger(row) {
  const spec = DETAIL_SPEC[activeTab.value]
  const cluster = currentCluster.value
  if (!spec || !cluster) return
  const cells = row.cells || []
  const params = {
    type: activeTab.value === 'pods' ? 'pod' : 'workload',
    cluster: cluster.instance,
    namespace: cells[spec.nsCol],
    name: cells[spec.nameCol],
  }
  if (activeTab.value === 'workloads') {
    const kind = WORKLOAD_LEDGER_KINDS[cells[0]]
    if (!kind) {
      ElMessage.info('该对象类型不进台账：只有 Deployment / StatefulSet / DaemonSet 会建资产')
      return
    }
    params.kind = kind
  }
  ledgerBusy.value = true
  try {
    const res = await lookupAsset(params)
    // 404 在这里是**正常结果**：清单上报有一个采集周期的延迟，刚建的 Pod 可能还没进台账
    if (res.status === 404) {
      ElMessage.info('该对象尚未进入台账（清单上报约一个采集周期，稍后再看）')
      return
    }
    if (!res.ok) {
      ElMessage.error((res.body && res.body.error) || '查询台账失败')
      return
    }
    const item = res.body && res.body.asset
    if (!item) {
      ElMessage.error('台账返回内容不完整')
      return
    }
    if (item.ignored) {
      ElMessage.warning('该资产已被从台账隐藏，可在「资产台账 → 含已忽略」中恢复')
    }
    router.push({ path: '/assets', query: { id: item.id } })
  } catch (e) {
    ElMessage.error(e.message || '查询台账失败')
  } finally {
    ledgerBusy.value = false
  }
}

// applyDeepLink 处理从台账反向点进来的地址（?cluster=&namespace=&pod=）：
// 选中集群 → 切到 Pod Tab → 打开该 Pod 的日志抽屉。
//
// 只认领一次（消费后把 query 里的定位参数清掉）：否则用户在这个页面上手动切集群，
// 又被 query 拽回去，看起来就像"选了没用"。
async function applyDeepLink() {
  const q = route.query
  const pod = String(q.pod || '').trim()
  const tab = String(q.tab || '').trim()
  const clusterRef = String(q.cluster || '').trim()
  const ns = String(q.namespace || '').trim()
  if (!pod && !tab && !clusterRef) return
  router.replace({ path: '/container', query: {} })

  const hit = clusters.value.find((c) => c.instance === clusterRef || c.name === clusterRef)
  if (!hit) {
    ElMessage.warning('未找到该对象所属的集群：可能该节点未上报，或集群凭据已变更')
    return
  }
  clusterKey.value = keyOf(hit)
  if (ns) namespace.value = ns
  if (tab && TABS.some((t) => t.key === tab)) activeTab.value = tab
  if (pod) activeTab.value = 'pods'
  await nextTick()
  // 列表：只在没有缓存结果时才下发（与 autoRun 同一规矩）；日志抽屉另走一次按需拉取
  autoRun(activeTab.value)
  if (pod) openLogsFor(ns, pod)
}

// fetchLogs 拉取当前 Pod 的最近日志；force=true 表示用户明确要求重新拉取。
//
// 行数与时间窗由服务端动作目录的规格兜住（默认 200 行、最多 500 行 / 24 小时）：
// 界面不提供"全量下载"，那是另一条通道的事，不是这一页能顺手打开的。
async function fetchLogs(force = false) {
  const target = logsTarget.value
  if (!target || !currentCluster.value) return
  const container = logsContainer.value.trim()
  const cacheName = target.pod + (container ? '/' + container : '')
  logsKey.value = {
    cluster: clusterKey.value, kind: 'container.logs', namespace: target.namespace, name: cacheName,
  }
  const st = containerDetailState(clusterKey.value, 'container.logs', target.namespace, cacheName)
  if (st.result && !force) return

  st.error = ''
  st.task = null
  st.busy = true
  try {
    const params = {
      cluster: currentCluster.value.name || currentCluster.value.instance,
      namespace: target.namespace,
      name: target.pod,
    }
    if (container) params.container = container
    const res = await createOpsTask({
      node: currentCluster.value.node,
      kind: 'container.logs',
      params,
      reason: 'Pod 日志',
    })
    const task = res && res.task
    if (!task || !task.id) throw new Error('服务端未返回任务 ID')
    st.task = task
    await waitTask(
      task.id,
      () => {},
      (t, err) => {
        st.busy = false
        if (err) {
          st.error = err
        } else if (t.state !== 'succeeded') {
          st.error = taskFailureText(t)
        } else {
          st.result = parsePayload(t)
          if (!st.result) {
            st.error = t.message || '未返回结构化结果'
          } else {
            st.at = Date.now()
          }
        }
      }
    )
  } catch (e) {
    st.error = e.message || '下发失败'
    st.busy = false
  }
}

onMounted(async () => {
  // "上次刷新多久之前"要自己走：否则停了十分钟再看，它还会说"刚刚"。
  nowTimer = setInterval(() => {
    now.value = Date.now()
  }, 30_000)
  await loadClusters()
  // 从台账反向点进来时优先按地址定位（applyDeepLink 内部会自己触发列表与日志的拉取）
  await applyDeepLink()
  // 只在"这个（集群 + Tab）还没有结果"时查一次（不要让人对着空白面板猜）；
  // 有上次的结果就直接显示结果与刷新时间——**进页面不等于刷新**。
  await autoRun(activeTab.value)
})

// 切 Tab：同样只在没有结果时查一次。已有结果直接显示，不重复下发。
watch(activeTab, (tab) => {
  nextTick(() => autoRun(tab))
})

onBeforeUnmount(() => {
  for (const h of timers) clearTimeout(h)
  timers.clear()
  if (nowTimer) clearInterval(nowTimer)
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
  flex-wrap: wrap;
  gap: 12px;
  margin-top: 10px;
  font-size: var(--fs-xs);
  color: var(--t3);
}
.foot-warn {
  color: var(--warn);
}
/* 「上次刷新」推到右侧：它是这份数据的"保质期标签"，不该和计数挤在一起。 */
.foot-time {
  margin-left: auto;
}
.detail-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
  font-size: var(--fs-xs);
}
.drawer-loading {
  padding: 24px 0;
  text-align: center;
  color: var(--t3);
  font-size: var(--fs-sm);
}
.logs-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
}
.logs-container {
  width: 200px;
}
/* 日志按原样呈现：pre-wrap 保留缩进与换行，等宽字体让堆栈/表格对得齐。
   行数上限由服务端动作目录兜住，这里只负责滚动，不做二次截断。 */
.log-pre {
  margin: 0;
  padding: 12px;
  max-height: calc(100vh - 220px);
  overflow: auto;
  background: var(--bg-sunken, rgba(0, 0, 0, 0.04));
  border-radius: 6px;
  font-family: var(--font-mono, ui-monospace, SFMono-Regular, Menlo, monospace);
  font-size: var(--fs-xs);
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-all;
}
</style>
