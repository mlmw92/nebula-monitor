<template>
  <section class="view">
    <!-- 页头统一为 PageHeader：原来这里是自写的 h2 + 说明 + 两行操作区，
         与全站其它页面的标题字号（18px）和间距都不一致。
         长说明里的"离线节点无法下发"是真正的注意事项，保留在 desc 末尾而不是塞进 tooltip。 -->
    <PageHeader
      title="节点操作"
      desc="平台下发白名单动作 → 节点执行 → 回执与审计；指令随节点下一次上报下发，离线节点无法下发"
    >
      <template #actions>
        <span v-if="autoRefresh" class="muted">有任务未结束，已开启自动刷新</span>
        <el-button :loading="loading" @click="reload">
          <el-icon :size="13"><Refresh /></el-icon><span class="btn-txt">刷新</span>
        </el-button>
        <el-button v-if="canExec" type="primary" @click="openDispatch">
          <el-icon :size="13"><Plus /></el-icon><span class="btn-txt">下发操作</span>
        </el-button>
        <span v-else class="muted">当前账号只读：缺 ops:exec 权限（下发属高风险操作）</span>
      </template>
    </PageHeader>

    <el-alert v-if="loadError" type="error" :closable="false" show-icon :title="loadError" class="alert-gap" />

    <!-- 指标卡：只统计**当前筛选到的记录**（口径写在每张卡的 hint 里）。
         刻意不显示"近 24h 环比"这类没有数据支撑的数字——编一个好看的趋势，
         比不显示更糟：用户会拿它做判断。 -->
    <div class="kpi-row">
      <KpiCard :value="stat.total" label="操作记录" :hint="`当前筛选命中 ${shown.length} 条`" tone="ops">
        <template #icon><el-icon :size="20"><Operation /></el-icon></template>
      </KpiCard>
      <KpiCard :value="stat.rateText" label="成功率" :hint="`成功 ${stat.ok} · 失败 ${stat.failed} · 进行中 ${stat.pending}`" tone="ok">
        <template #icon><el-icon :size="20"><CircleCheck /></el-icon></template>
      </KpiCard>
      <KpiCard :value="stat.avgText" label="平均耗时" :hint="`基于 ${stat.doneCount} 条已结束任务`" tone="conn">
        <template #icon><el-icon :size="20"><Timer /></el-icon></template>
      </KpiCard>
      <KpiCard :value="stat.batches" label="涉及批次" :hint="`覆盖 ${stat.hosts} 个节点`" tone="cluster">
        <template #icon><el-icon :size="20"><Files /></el-icon></template>
      </KpiCard>
    </div>

    <div class="panel">
      <!-- 工具条：搜索 + 筛选 + 条件 chips + 列 / 密度 / 导出 -->
      <div class="op-toolbar">
        <el-input v-model="keyword" class="op-search" placeholder="搜索节点 / 批次 / 触发人 / 原因…" clearable>
          <template #prefix><el-icon :size="13"><Search /></el-icon></template>
        </el-input>
        <el-select v-model="filter.node" placeholder="全部节点" clearable style="width: 168px" @change="reload">
          <el-option v-for="n in nodes" :key="n.hostname" :label="n.hostname" :value="n.hostname" />
        </el-select>
        <el-select v-model="viewFilter.action" placeholder="全部动作" clearable style="width: 168px">
          <el-option v-for="a in actionOptions" :key="a.value" :label="a.label" :value="a.value" />
        </el-select>
        <el-select v-model="filter.state" placeholder="全部状态" clearable style="width: 140px" @change="reload">
          <el-option v-for="s in STATE_OPTIONS" :key="s.value" :label="s.label" :value="s.value" />
        </el-select>
        <el-select v-model="viewFilter.range" style="width: 136px">
          <el-option v-for="r in RANGE_OPTIONS" :key="r.value" :label="r.label" :value="r.value" />
        </el-select>
        <div class="op-tb-right">
          <el-dropdown trigger="click" :hide-on-click="false">
            <el-button>
              <el-icon :size="13"><Grid /></el-icon><span class="btn-txt">列</span>
            </el-button>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item v-for="c in COLS" :key="c.key" @click="toggleCol(c.key)">
                  <el-icon v-if="colVisible(c.key)" :size="13"><Check /></el-icon>
                  <span :class="{ 'col-off': !colVisible(c.key) }">{{ c.label }}</span>
                </el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
          <el-button :title="density ? '切换为宽松行高' : '切换为紧凑行高'" @click="density = !density">
            <el-icon :size="13"><Sort /></el-icon><span class="btn-txt">密度</span>
          </el-button>
          <el-button @click="exportRows(shown, '筛选结果')">
            <el-icon :size="13"><Download /></el-icon><span class="btn-txt">导出</span>
          </el-button>
        </div>
      </div>

      <!-- 已选条件：把"列表为什么变少了"说清楚，并且每一条都能单独撤掉 -->
      <div v-if="activeChips.length" class="op-chips">
        <span class="op-chips-label">筛选</span>
        <span v-for="c in activeChips" :key="c.key" class="op-chip" @click="clearChip(c.key)">
          {{ c.label }}<el-icon :size="11"><Close /></el-icon>
        </span>
        <span class="op-chip op-chip-clear" @click="clearAllChips">清空全部</span>
      </div>

      <!-- 批量条：选中才出现，避免常驻噪音（与资产台账共用 BatchBar） -->
      <BatchBar v-if="selection.length" :count="selection.length" @clear="clearSelection">
        <el-button size="small" @click="exportRows(selection, '选中记录')">导出选中</el-button>
        <el-button v-if="canExec" size="small" @click="rerunSelected">
          <el-icon :size="12"><RefreshRight /></el-icon><span class="btn-txt">重新执行</span>
        </el-button>
        <el-button v-if="canExec" size="small" type="danger" plain @click="bulkDelete">
          <el-icon :size="12"><Delete /></el-icon><span class="btn-txt">批量删除</span>
        </el-button>
      </BatchBar>


      <div class="op-table-wrap" :class="{ 'op-dense': density }">
        <el-table
          ref="tableRef"
          :data="pagedTasks"
          v-loading="loading"
          row-key="id"
          :default-sort="{ prop: 'createdAt', order: 'descending' }"
          empty-text="没有匹配的操作记录 —— 试试放宽筛选条件或清空关键词"
          style="width: 100%"
          @selection-change="onSelectionChange"
          @sort-change="onSortChange"
        >
          <!-- 多选：只有已结束的记录能删（服务端也会拦），因此这里不做限制，删除时逐条给结论 -->
          <el-table-column type="selection" width="42" :reserve-selection="true" />
          <el-table-column label="下发时间" width="152" prop="createdAt" sortable="custom" :sort-orders="['descending', 'ascending']">
            <template #default="{ row }">
              <div class="cell-2l">
                <b>{{ fmtClock(row.createdAt) }}</b>
                <span>{{ relTime(row.createdAt) }} · {{ fmtDate(row.createdAt) }}</span>
              </div>
            </template>
          </el-table-column>
          <!-- 节点列要放下「健康点 + 主机名 + 分组标签」三样：列宽给不够时
               最先被牺牲的是主机名（flex 收缩），而它恰恰是这一行最关键的信息。 -->
          <el-table-column v-if="colVisible('node')" label="节点" width="200">
            <template #default="{ row }">
              <div class="node-cell">
                <i class="ndot" :class="nodeHealth(row.node)" :title="nodeHealthTitle(row.node)" />
                <span class="mono nm" :title="row.node">{{ row.node }}</span>
                <span v-if="nodeGroup(row.node)" class="zone">{{ nodeGroup(row.node) }}</span>
              </div>
            </template>
          </el-table-column>
          <el-table-column v-if="colVisible('action')" label="动作" min-width="200">
            <template #default="{ row }">
              <div class="cell-2l">
                <b>{{ row.title || row.kind }}<span v-if="row.readOnly === false" class="tag-write">写操作</span></b>
                <span class="mono">{{ kindSummary(row) }}</span>
              </div>
            </template>
          </el-table-column>
          <el-table-column v-if="colVisible('batch')" label="批次" width="112">
            <template #default="{ row }">
              <el-button v-if="row.batchId" link type="primary" @click="filterByBatch(row.batchId)">{{ row.batchId }}</el-button>
              <span v-else class="muted">单条</span>
            </template>
          </el-table-column>
          <el-table-column v-if="colVisible('status')" label="状态" width="106">
            <template #default="{ row }">
              <span class="op-pill" :class="pillClass(row.state)">{{ stateLabel(row.state) }}</span>
            </template>
          </el-table-column>
          <el-table-column v-if="colVisible('who')" label="触发人 / 原因" min-width="176">
            <template #default="{ row }">
              <div class="who-cell">
                <i class="av" :class="{ bot: !row.operator }">{{ avatarText(row) }}</i>
                <div class="who-tx">
                  <b>{{ row.operator || '系统' }}</b>
                  <span>{{ row.reason || '未填写原因' }}</span>
                </div>
              </div>
            </template>
          </el-table-column>
          <el-table-column
            v-if="colVisible('dur')"
            label="耗时"
            width="146"
            prop="durationMs"
            sortable="custom"
            :sort-orders="['descending', 'ascending']"
          >
            <template #default="{ row }">
              <div v-if="row.durationMs" class="dur-cell">
                <span class="dur-bar"><i :class="durLevel(row)" :style="{ width: durWidth(row) }" /></span>
                <b>{{ row.durationMs }} ms</b>
              </div>
              <span v-else class="muted">—</span>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="214" fixed="right">
            <template #default="{ row }">
              <div class="ops-cell">
                <el-button link type="primary" @click="openResult(row)">查看结果</el-button>
                <el-button v-if="row.batchId" link @click="copyBatch(row)">复制批次</el-button>
                <!-- 排队中：可以撤回（还没发出去）；已下发的撤不回来，因此不显示取消 -->
                <el-button v-if="canExec && row.state === 'queued'" link type="warning" @click="cancelOne(row)">取消</el-button>
                <el-button v-if="canExec && isTerminal(row.state)" link type="danger" @click="removeOne(row)">删除</el-button>
              </div>
            </template>
          </el-table-column>
        </el-table>
      </div>

      <!-- 分页：服务端一次最多给 100 条（见 reload 的 limit），因此这里只是在**已加载的记录内**翻页；
           把"共多少条"写清楚，避免用户以为翻到底就没有更早的记录了。 -->
      <div class="op-pager">
        <span>显示 {{ rangeText }}，共 {{ shown.length }} 条</span>
        <span class="muted">· 每页 {{ pageSize }} 条</span>
        <el-pagination
          v-model:current-page="page"
          :page-size="pageSize"
          :total="shown.length"
          layout="prev, pager, next"
          small
          background
        />
      </div>
    </div>

    <!-- 下发：节点（可多选）→ 动作 → 参数 → 原因 -->
    <el-dialog v-model="dispatchVisible" title="下发操作" width="720px">
      <el-alert
        type="warning"
        :closable="false"
        show-icon
        class="alert-gap"
        title="指令会在选中的机器上执行。能否真正执行取决于每台机器自己的 agent.yaml（guards.ops）——默认只放行只读动作。"
      />
      <el-form label-width="90px">
        <el-form-item label="目标节点" required>
          <div class="node-picker">
            <el-select
              v-model="form.nodes"
              multiple filterable collapse-tags collapse-tags-tooltip
              placeholder="选择节点（可多选）"
              style="width: 100%"
              @change="onNodesChange"
            >
              <el-option
                v-for="n in nodes"
                :key="n.hostname"
                :label="`${n.hostname}（${n.status === 'online' ? '在线' : n.status}）`"
                :value="n.hostname"
                :disabled="n.status !== 'online'"
              />
            </el-select>
            <!-- 按分组整选：批量操作的真实起点通常就是"把 web 这组全选上" -->
            <el-dropdown v-if="groups.length" trigger="click" @command="selectGroup">
              <el-button>按分组选<el-icon :size="12"><ArrowDown /></el-icon></el-button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item v-for="g in groups" :key="g.name" :command="g.name">
                    {{ g.name }}（{{ groupNodeCount(g.name) }} 台在线）
                  </el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
            <el-button v-if="form.nodes.length" @click="form.nodes = []; onNodesChange()">清空</el-button>
          </div>
          <div class="field-hint">
            已选 {{ form.nodes.length }} 个节点（离线节点不可选：指令只能随上报响应送回）
          </div>
        </el-form-item>
        <el-form-item label="动作" required>
          <el-select v-model="form.kind" placeholder="选择动作" style="width: 100%" @change="onActionChange">
            <el-option-group v-for="g in actionGroups" :key="g.key" :label="g.key">
              <el-option
                v-for="a in g.actions"
                :key="a.kind"
                :value="a.kind"
                :label="a.title"
                :disabled="!actionUsable(a)"
              >
                <div class="act-option">
                  <span class="act-title">{{ a.title }}</span>
                  <span class="mono muted small">{{ a.kind }}</span>
                  <span v-if="!a.readOnly" class="tag-write">写操作</span>
                  <span class="act-count">{{ actionCoverage(a) }}</span>
                </div>
              </el-option>
            </el-option-group>
          </el-select>
          <div v-if="selectedAction" class="field-hint">{{ selectedAction.desc }}</div>
          <div v-if="form.nodes.length && capsLoaded && !anyUsable" class="field-hint warn">
            所选节点没有任何可执行动作：多半是 Agent 版本过低，或那些机器都没放行 guards.ops。
          </div>
        </el-form-item>
        <el-form-item v-for="p in paramSpecs" :key="p.name" :label="p.title || p.name" :required="p.required">
          <!-- fileId 不给手输：它必须来自一次真实上传，手填只会得到一个不存在的引用号 -->
          <template v-if="p.name === 'fileId'">
            <el-upload
              :show-file-list="false"
              :http-request="onFileUpload"
              :before-upload="beforeFileUpload"
              :disabled="uploading"
            >
              <el-button :loading="uploading">
                {{ uploadedFile ? '重新选择文件' : '选择要分发的文件' }}
              </el-button>
            </el-upload>
            <div v-if="uploadedFile" class="field-hint">
              已上传 <b>{{ uploadedFile.name }}</b>（{{ fmtBytes(uploadedFile.size) }}，sha256 {{ String(uploadedFile.sha256 || '').slice(0, 12) }}…）
              —— 引用号 <code>{{ form.params[p.name] }}</code>
            </div>
            <div v-else class="field-hint">
              单文件上限 256 KiB，覆盖不了的大文件请先自行拆分。上传后由服务端保管，下发时随目标机器的上报响应送达。
            </div>
          </template>
          <el-input v-else v-model="form.params[p.name]" :placeholder="p.example || p.desc" />
          <div v-if="p.desc" class="field-hint">{{ p.desc }}</div>
        </el-form-item>
        <el-form-item label="原因">
          <el-input v-model="form.reason" type="textarea" :rows="2" placeholder="选填，但强烈建议填：回看历史时「为什么执行它」往往比「谁执行的」更重要" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dispatchVisible = false">取消</el-button>
        <el-button
          type="primary"
          :loading="submitting"
          :disabled="!form.nodes.length || !form.kind || (form.kind === 'file.push' && !form.params.fileId)"
          @click="submitDispatch"
        >
          下发到 {{ form.nodes.length }} 个节点
        </el-button>
      </template>
    </el-dialog>

    <!-- 下发结果：逐节点给结论（部分成功是常态） -->
    <el-dialog v-model="batchVisible" title="批量下发结果" width="700px">
      <div v-if="batchResult">
        <el-alert
          :type="batchResult.failed === 0 ? 'success' : batchResult.created > 0 ? 'warning' : 'error'"
          :closable="false"
          show-icon
          class="alert-gap"
          :title="`批次 ${batchResult.batchId}：目标 ${batchResult.total} 台，已下发 ${batchResult.created} 台，未下发 ${batchResult.failed} 台`"
          :description="batchResult.created > 0 ? '每一台的结果见下方；未下发的那些给出了具体原因。' : '没有任何一台可以下发，原因见下方。'"
        />
        <div class="sec"><span>已下发（{{ batchResult.created }}）</span><span class="muted">等待节点领取，可在此列表里取消</span></div>
        <div v-for="it in batchResult.okItems" :key="it.node" class="result-row">
          <span class="mono">{{ it.node }}</span>
          <el-tag size="small" type="success">{{ it.taskId }}</el-tag>
        </div>
        <template v-if="batchResult.failedItems.length">
          <div class="sec"><span>未下发（{{ batchResult.failedItems.length }}）</span><span class="muted">逐台原因</span></div>
          <div v-for="it in batchResult.failedItems" :key="it.node" class="result-row">
            <span class="mono">{{ it.node }}</span>
            <span class="warn-text">{{ it.error }}</span>
          </div>
        </template>
      </div>
      <template #footer>
        <el-button @click="batchVisible = false">关闭</el-button>
        <el-button v-if="batchResult && batchResult.created > 0" type="warning" @click="cancelBatch">撤回本批未下发的任务</el-button>
        <el-button type="primary" @click="viewBatch">查看本批任务</el-button>
      </template>
    </el-dialog>

    <!-- 执行结果：右侧抽屉。输出可能很长（诊断包 7 个分节、每节几十行），
         抽屉比模态框好用——不挡列表，可以对着行看。
         每个分节 = 标题行 + 该节输出原文，两者必须在**同一个 v-for 里**：
         此前 `<pre>` 落在 v-for 之外，`sec` 成了未定义变量，一渲染就抛
         TypeError（"Cannot read properties of undefined"），表现是"点了没反应"，
         控制台之外看不到任何线索。 -->
    <el-drawer v-model="resultVisible" :title="resultTitle" size="560px" class="ops-result-drawer">
      <div v-if="resultTask" class="dr-body">
        <div class="dr-top">
          <span class="op-pill" :class="pillClass(resultTask.state)">{{ stateLabel(resultTask.state) }}</span>
          <span class="mono">{{ resultTask.node }}</span>
          <span class="muted">{{ resultTask.kind }}</span>
        </div>

        <div class="dr-sec">
          <h4>基本信息</h4>
          <dl class="kv">
            <dt>任务 ID</dt>
            <dd class="mono">{{ resultTask.id }}</dd>
            <dt>批次</dt>
            <dd>
              <span class="mono">{{ resultTask.batchId || '单条' }}</span>
              <el-button v-if="resultTask.batchId" link type="primary" @click="copyBatch(resultTask)">复制</el-button>
            </dd>
            <dt>触发人</dt>
            <dd>
              {{ resultTask.operator || '系统' }}
              <span v-if="resultTask.operatorIP" class="muted"> · {{ resultTask.operatorIP }}</span>
            </dd>
            <dt>触发原因</dt>
            <dd>{{ resultTask.reason || '未填写' }}</dd>
            <dt>参数</dt>
            <dd class="mono">{{ kindSummary(resultTask) === resultTask.kind ? '无' : kindSummary(resultTask) }}</dd>
            <dt>是否写操作</dt>
            <dd>{{ resultTask.readOnly === false ? '是（会改变目标机器状态）' : '否（只读）' }}</dd>
            <dt>总耗时</dt>
            <dd><b>{{ resultTask.durationMs ? resultTask.durationMs + ' ms' : '—' }}</b></dd>
            <dt>过期时间</dt>
            <dd class="mono">{{ fmtTime(resultTask.expireAt) }}</dd>
          </dl>
        </div>

        <div class="dr-sec">
          <h4>执行时间线</h4>
          <ul class="tl">
            <li v-for="(s, i) in timelineOf(resultTask)" :key="i" :class="s.cls">
              <b>{{ s.label }}</b>
              <span>{{ s.time }}<template v-if="s.delta"> · {{ s.delta }}</template></span>
            </li>
          </ul>
        </div>

        <div class="dr-sec">
          <h4>结果输出</h4>
          <el-alert
            v-if="resultTask.message"
            :type="resultTask.state === 'succeeded' ? 'success' : resultTask.state === 'failed' ? 'error' : 'info'"
            :closable="false"
            show-icon
            class="alert-gap"
            :title="resultTask.message"
          />
          <template v-if="resultSections.length">
            <div v-for="sec in resultSections" :key="sec.key" class="sec-block">
              <div class="sec">
                <span>{{ sec.key }}</span>
                <span class="muted">命令输出原文</span>
              </div>
              <pre class="out">{{ sec.value }}</pre>
            </div>
          </template>
          <el-empty v-else description="暂无输出（任务可能还在等待节点领取或执行）" />
        </div>
      </div>
      <template #footer>
        <el-button v-if="resultSections.length" @click="copyOutput(resultTask)">复制输出</el-button>
        <el-button v-if="canExec" @click="rerunOne(resultTask)">重新执行</el-button>
        <el-button type="primary" @click="resultVisible = false">关闭</el-button>
      </template>
    </el-drawer>
  </section>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
// 图标显式导入（本仓库其它页面都是这个做法）：靠 main.js 的全局注册虽然也能跑，
// 但组件从此隐式依赖一个"别处会注册图标"的前提——单测挂载时就会全部解析失败。
import {
  ArrowDown, Check, CircleCheck, Close, Delete, Download, Files,
  Grid, Operation, Plus, Refresh, RefreshRight, Search, Sort, Timer,
} from '@element-plus/icons-vue'
import BatchBar from './BatchBar.vue'
import KpiCard from './KpiCard.vue'
import PageHeader from './common/PageHeader.vue'
import { cancelOpsTasks, createOpsBatch, createOpsTask, deleteOpsTask, listOpsActions, listOpsTasks, uploadOpsFile } from '../api/ops'
import http from '../api/http'
import { useAuth } from '../composables/useAuth'

function fmtTime(ts) {
  if (!ts) return '—'
  return new Date(ts).toLocaleString('zh-CN', { hour12: false })
}

// 相对时间：任务列表关心的是"多久前下发的"（排队多久、是不是刚点的）
function relTime(ts) {
  if (!ts) return '—'
  const diff = Date.now() - ts
  if (diff < 60_000) return '刚刚'
  if (diff < 3600_000) return `${Math.floor(diff / 60_000)} 分钟前`
  if (diff < 86400_000) return `${Math.floor(diff / 3600_000)} 小时前`
  return `${Math.floor(diff / 86400_000)} 天前`
}

const auth = useAuth()
// 前端隐藏只是体验：服务端 ops:exec 是真正的边界（且属高风险权限）
const canExec = computed(() => auth.can('ops:exec'))

const STATE_OPTIONS = [
  { value: 'queued', label: '排队中' },
  { value: 'delivered', label: '已下发' },
  { value: 'running', label: '执行中' },
  { value: 'succeeded', label: '成功' },
  { value: 'failed', label: '失败' },
  { value: 'expired', label: '已超时' },
  { value: 'cancelled', label: '已取消' },
]
const STATE_LABELS = Object.fromEntries(STATE_OPTIONS.map((s) => [s.value, s.label]))
const stateLabel = (s) => STATE_LABELS[s] || s
// 状态 → 配色类（排队/下发/执行中是不定性，成功/失败/超时/取消是终态）
const STATE_CLASSES = {
  queued: 'queued', delivered: 'queued', running: 'queued',
  succeeded: 'online', failed: 'missing', expired: 'archived', cancelled: 'archived',
}
const stateClass = (s) => STATE_CLASSES[s] || 'archived'
const TERMINAL = ['succeeded', 'failed', 'expired', 'cancelled']
const isTerminal = (s) => TERMINAL.includes(s)

const tasks = ref([])
const nodes = ref([])
const groups = ref([])
const loading = ref(false)
const loadError = ref('')
const filter = ref({ node: '', state: '', batchId: '' })

const dispatchVisible = ref(false)
const submitting = ref(false)
const actions = ref([])
const support = ref({}) // node → 该节点放行的动作（批量：一次查全部选中节点）
const capsLoaded = ref(false)
const form = reactive({ nodes: [], kind: '', params: {}, reason: '' })

const batchVisible = ref(false)
const batchResult = ref(null)

const resultVisible = ref(false)
const resultTask = ref(null)

let timer = null
// 只有在存在未结束任务时才轮询：终态任务不会再变，常驻轮询只是白烧请求。
const autoRefresh = computed(() => tasks.value.some((t) => !isTerminal(t.state)))

onMounted(async () => {
  await loadNodes()
  await reload()
  timer = setInterval(() => {
    if (autoRefresh.value) reload()
  }, 5000)
})
onBeforeUnmount(() => {
  if (timer) clearInterval(timer)
})

async function loadNodes() {
  try {
    const res = await http.get('/api/v1/nodes')
    nodes.value = res.nodes || []
  } catch (e) {
    nodes.value = []
  }
  try {
    groups.value = (await http.get('/api/v1/groups')).groups || []
  } catch (e) {
    groups.value = []
  }
}

async function reload() {
  loading.value = true
  loadError.value = ''
  try {
    const res = await listOpsTasks({
      node: filter.value.node, state: filter.value.state, batchId: filter.value.batchId, limit: 100,
    })
    tasks.value = res.tasks || []
  } catch (e) {
    loadError.value = e.message || '加载操作任务失败'
    tasks.value = []
  } finally {
    loading.value = false
  }
}

function filterByBatch(batchId) {
  filter.value.batchId = batchId
  reload()
}
function clearBatchFilter() {
  filter.value.batchId = ''
  reload()
}
function groupNodeCount(group) {
  return nodes.value.filter((n) => n.group === group && n.status === 'online').length
}
function selectGroup(group) {
  const picked = nodes.value.filter((n) => n.group === group && n.status === 'online').map((n) => n.hostname)
  const merged = new Set([...form.nodes, ...picked])
  form.nodes = [...merged]
  onNodesChange()
}

// 动作目录按分组聚合（服务端已按「分组顺序 + 组内只读优先」排好）
const actionGroups = computed(() => {
  const out = []
  for (const a of actions.value) {
    let g = out.find((x) => x.key === a.group)
    if (!g) {
      g = { key: a.group, actions: [] }
      out.push(g)
    }
    g.actions.push(a)
  }
  return out
})
const selectedAction = computed(() => actions.value.find((a) => a.kind === form.kind) || null)
const paramSpecs = computed(() => (selectedAction.value ? selectedAction.value.params || [] : []))

// actionCoverage 说明"这个动作有多少比例的目标节点放行"——
// 批量下发时最需要的信息，否则用户只能在结果里一台台看失败原因。
function actionCoverage(a) {
  if (!form.nodes.length || !capsLoaded.value) return ''
  const n = form.nodes.filter((node) => (support.value[node] || []).includes(a.kind)).length
  return `${n}/${form.nodes.length} 台放行`
}
function actionUsable(a) {
  if (!capsLoaded.value || !form.nodes.length) return true // 未加载完不灰化，避免闪现全灰
  return form.nodes.some((node) => (support.value[node] || []).includes(a.kind))
}
const anyUsable = computed(() => actions.value.some((a) => actionUsable(a)))

/* ---- 文件分发：上传拿到引用号，再随任务下发 ---- */
const uploading = ref(false)
const uploadedFile = ref(null)

// 与服务端上限一致（model.OpsFileMaxBytes = 256KiB）：本地先拦一道，
// 免得用户等一次完整的失败往返才知道文件太大。
const FILE_MAX_BYTES = 256 * 1024

function fmtBytes(n) {
  if (!n) return '0 B'
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
  return (n / 1024 / 1024).toFixed(1) + ' MB'
}

function beforeFileUpload(file) {
  if (!file.size) {
    ElMessage.error('文件是空的，拒绝分发（空内容会直接把目标文件清空）')
    return false
  }
  if (file.size > FILE_MAX_BYTES) {
    ElMessage.error(`文件 ${fmtBytes(file.size)} 超过单次分发上限 ${fmtBytes(FILE_MAX_BYTES)}`)
    return false
  }
  return true
}

async function onFileUpload(option) {
  const fd = new FormData()
  fd.append('file', option.file)
  uploading.value = true
  try {
    const rec = await uploadOpsFile(fd)
    uploadedFile.value = rec
    form.params.fileId = rec.ref
    ElMessage.success('文件已上传：' + rec.name)
    option.onSuccess && option.onSuccess(rec)
  } catch (e) {
    uploadedFile.value = null
    form.params.fileId = ''
    ElMessage.error(e.message || '上传失败')
    option.onError && option.onError(e)
  } finally {
    uploading.value = false
  }
}

function kindSummary(row) {
  // 文件分发：文件名 + 目标路径比引用号有意义得多（引用号是机器看的）
  if (row.file && row.file.name) {
    const path = (row.params || {}).path
    return path ? `${row.file.name} → ${path}` : row.file.name
  }
  const params = row.params || {}
  const keys = Object.keys(params)
  if (!keys.length) return row.kind
  return keys.map((k) => `${k}=${params[k]}`).join(' ')
}

async function openDispatch() {
  form.nodes = filter.value.node ? [filter.value.node] : []
  form.kind = ''
  form.params = {}
  form.reason = ''
  uploadedFile.value = null
  capsLoaded.value = false
  support.value = {}
  dispatchVisible.value = true
  await Promise.all([loadActions(), loadCatalog()])
}

// 动作目录只需拉一次（与节点无关）
async function loadCatalog() {
  if (actions.value.length) return
  try {
    const res = await listOpsActions('')
    actions.value = res.actions || []
  } catch (e) {
    ElMessage.error(e.message || '加载动作目录失败')
  }
}

// 选中节点变化 → 一次查回所有目标节点的放行清单（N 个节点一次请求）
async function loadActions() {
  capsLoaded.value = false
  if (!form.nodes.length) {
    support.value = {}
    capsLoaded.value = true
    return
  }
  try {
    const res = await listOpsActions('', form.nodes)
    support.value = res.nodeCaps || {}
  } catch (e) {
    support.value = {}
    ElMessage.error(e.message || '查询节点放行情况失败')
  } finally {
    capsLoaded.value = true
  }
}
function onNodesChange() {
  loadActions()
  // 换节点后原动作可能一台都不放行 → 清掉，避免提交后一片失败
  if (form.kind && !actionUsable(selectedAction.value)) {
    form.kind = ''
    form.params = {}
  }
}

function onActionChange() {
  form.params = {}
  // 换动作时清掉已选文件：留着它会让"上一条动作选的文件"看起来像是本条动作要用的
  uploadedFile.value = null
  for (const p of paramSpecs.value) form.params[p.name] = ''
}

async function submitDispatch() {
  const a = selectedAction.value
  if (!a || !form.nodes.length) return
  const params = {}
  for (const [k, v] of Object.entries(form.params)) {
    if (v !== undefined && v !== null && v !== '') params[k] = v
  }
  const covered = form.nodes.filter((node) => (support.value[node] || []).includes(a.kind)).length
  const lines = [
    `将对 ${form.nodes.length} 个节点下发「${a.title}」${a.readOnly ? '（只读）' : '——这会改变这些机器的运行状态'}`,
  ]
  // 文件分发把话说具体：写的是哪个文件、写到哪、原文件会不会被覆盖——
  // 这是本通道里唯一"直接替换机器上文件"的动作，含糊的确认框等于没有确认。
  if (a.kind === 'file.push') {
    const fname = uploadedFile.value ? uploadedFile.value.name : '所选文件'
    lines.push(`会把 ${fname} 写入各节点的 ${form.params.path || '（未填目标路径）'}；目标位置上已有的文件会先改名备份`)
  }
  if (covered < form.nodes.length) {
    lines.push(`其中只有 ${covered} 台放行了该动作，其余会被跳过（逐台给出原因）`)
  }
  try {
    await ElMessageBox.confirm(lines.join('；'), '确认下发操作', {
      type: a.readOnly ? 'info' : 'warning', confirmButtonText: '下发', cancelButtonText: '取消',
    })
  } catch (e) {
    return
  }
  submitting.value = true
  try {
    const res = await createOpsBatch({ nodes: form.nodes, kind: a.kind, params, reason: form.reason })
    const b = res.batch || {}
    batchResult.value = {
      ...b,
      okItems: (b.items || []).filter((i) => i.ok),
      failedItems: (b.items || []).filter((i) => !i.ok),
    }
    dispatchVisible.value = false
    batchVisible.value = true
    await reload()
  } catch (e) {
    // 服务端的说明就是给用户看的下一步（去升级 Agent / 去改目标机器的 guards.ops），原样展示
    ElMessage.error(e.message || '下发失败')
    await loadActions()
  } finally {
    submitting.value = false
  }
}

async function viewBatch() {
  batchVisible.value = false
  if (batchResult.value && batchResult.value.batchId) filterByBatch(batchResult.value.batchId)
}

// 撤回本批**还没被领取**的任务：整批取消会逐条给结论，已下发的那些撤不回来。
async function cancelBatch() {
  if (!batchResult.value || !batchResult.value.batchId) return
  try {
    await ElMessageBox.confirm(
      '将撤回本批中「仍在排队」的任务；已被节点领取的无法撤回（会在结果里列出）。',
      '撤回本批任务', { type: 'warning', confirmButtonText: '撤回', cancelButtonText: '取消' },
    )
  } catch (e) {
    return
  }
  try {
    const res = await cancelOpsTasks({ batchId: batchResult.value.batchId })
    const r = res.result || {}
    if (r.failed > 0) {
      ElMessage.warning(`已撤回 ${r.cancelled} 条；${r.failed} 条无法撤回（多半已被节点领取）`)
    } else {
      ElMessage.success(`已撤回 ${r.cancelled} 条`)
    }
    await reload()
  } catch (e) {
    ElMessage.error(e.message || '撤回失败')
  }
}

async function cancelOne(row) {
  try {
    await ElMessageBox.confirm(`撤回对 ${row.node} 的「${row.title || row.kind}」（仍在排队，尚未下发）`,
      '撤回任务', { type: 'warning', confirmButtonText: '撤回', cancelButtonText: '取消' })
  } catch (e) {
    return
  }
  try {
    const res = await cancelOpsTasks({ ids: [row.id] })
    const r = res.result || {}
    if (r.cancelled === 1) ElMessage.success('已撤回')
    else ElMessage.error((r.items && r.items[0] && r.items[0].error) || '撤回失败')
    await reload()
  } catch (e) {
    ElMessage.error(e.message || '撤回失败')
  }
}

async function removeOne(row) {
  try {
    await ElMessageBox.confirm(`删除这条操作记录（${row.node} · ${row.title || row.kind}）？审计里仍保留"谁删了什么"。`,
      '删除记录', { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' })
  } catch (e) {
    return
  }
  try {
    await deleteOpsTask(row.id)
    ElMessage.success('已删除')
    await reload()
  } catch (e) {
    ElMessage.error(e.message || '删除失败')
  }
}

/* ================= 列表视图：搜索 / 排序 / 分页 / 列 / 密度 ================= */

// keyword、动作、时间范围是**在已加载的记录内**筛（服务端不提供这三项）；
// 节点 / 状态 / 批次仍走服务端（见 reload）——这样才筛得到"比最新 100 条更早"的记录。
const keyword = ref('')
const viewFilter = ref({ action: '', range: '' })
const RANGE_OPTIONS = [
  { value: '', label: '全部时间' },
  { value: '1h', label: '近 1 小时' },
  { value: '24h', label: '近 24 小时' },
  { value: '7d', label: '近 7 天' },
]
const RANGE_LABELS = Object.fromEntries(RANGE_OPTIONS.map((r) => [r.value, r.label]))
const RANGE_MS = { '1h': 3600_000, '24h': 86400_000, '7d': 7 * 86400_000 }

// 可隐藏的列（时间与操作不参与：一个是排序锚点，一个是唯一入口）
const COLS = [
  { key: 'node', label: '节点' },
  { key: 'action', label: '动作' },
  { key: 'batch', label: '批次' },
  { key: 'status', label: '状态' },
  { key: 'who', label: '触发人 / 原因' },
  { key: 'dur', label: '耗时' },
]
const hiddenCols = ref([])
const colVisible = (k) => !hiddenCols.value.includes(k)
function toggleCol(k) {
  const i = hiddenCols.value.indexOf(k)
  if (i >= 0) hiddenCols.value.splice(i, 1)
  else hiddenCols.value.push(k)
}

const density = ref(false)
const sort = ref({ key: 'createdAt', dir: 'desc' })
const page = ref(1)
const pageSize = 20
const selection = ref([])
const tableRef = ref(null)

// 状态 → pill 配色：进行中/排队是"不定性"，成功/失败是终态，取消/超时是"没成事但也不异常"
const PILL = {
  queued: 'warn', delivered: 'run', running: 'run',
  succeeded: 'ok', failed: 'err', expired: 'err', cancelled: 'muted',
}
const pillClass = (s) => PILL[s] || 'muted'

function fmtClock(ts) {
  if (!ts) return '—'
  return new Date(ts).toLocaleTimeString('zh-CN', { hour12: false })
}
function fmtDate(ts) {
  if (!ts) return ''
  const d = new Date(ts)
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}

// 节点名单映射：健康点与分组标签都靠它，避免每行都 find 一遍
const nodeMap = computed(() => Object.fromEntries(nodes.value.map((n) => [n.hostname, n])))
function nodeHealth(host) {
  const n = nodeMap.value[host]
  if (!n) return 'unknown' // 已不在节点清单里（退网 / 改名）
  return n.status === 'online' ? 'ok' : 'err'
}
function nodeHealthTitle(host) {
  const n = nodeMap.value[host]
  if (!n) return `${host} 已不在节点清单里（退网或改过名），指令不会被领取`
  return n.status === 'online' ? `${host} 在线（${n.group || '未分组'}）` : `${host} 离线：指令要等它下次上报才能送达`
}
function nodeGroup(host) {
  const n = nodeMap.value[host]
  return (n && n.group) || ''
}
function avatarText(row) {
  const who = (row.operator || '').trim()
  return who ? who[0].toUpperCase() : '⚡' // 无人触发 = 系统/告警联动
}

// 动作下拉：只列出**当前已加载记录里出现过**的动作，避免选了一个空结果
const actionOptions = computed(() => {
  const seen = new Map()
  for (const t of tasks.value) if (!seen.has(t.kind)) seen.set(t.kind, t.title || t.kind)
  return [...seen.entries()].map(([value, label]) => ({ value, label }))
})

// shown = 客户端筛选 + 排序后的全量（导出、统计、分页都以它为准）
const shown = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  const span = RANGE_MS[viewFilter.value.range] || 0
  const now = Date.now()
  let out = tasks.value.filter((t) => {
    if (viewFilter.value.action && t.kind !== viewFilter.value.action) return false
    if (span && now - (t.createdAt || 0) > span) return false
    if (!kw) return true
    return [t.node, t.batchId, t.operator, t.reason, t.kind, t.title, kindSummary(t)]
      .filter(Boolean)
      .join(' ')
      .toLowerCase()
      .includes(kw)
  })
  const dir = sort.value.dir === 'asc' ? 1 : -1
  const key = sort.value.key
  out = [...out].sort((a, b) => {
    const av = key === 'durationMs' ? a.durationMs || -1 : a.createdAt || 0
    const bv = key === 'durationMs' ? b.durationMs || -1 : b.createdAt || 0
    return (av - bv) * dir
  })
  return out
})
const pagedTasks = computed(() => shown.value.slice((page.value - 1) * pageSize, page.value * pageSize))
const rangeText = computed(() => {
  if (!shown.value.length) return '0 条'
  const from = (page.value - 1) * pageSize + 1
  const to = Math.min(page.value * pageSize, shown.value.length)
  return `${from}–${to} 条`
})

// 筛选条件变了回到第一页：否则会停在一个空页上（"怎么什么都没了"）
watch([keyword, viewFilter, () => filter.value.node, () => filter.value.state, () => filter.value.batchId], () => {
  page.value = 1
}, { deep: true })
// 记录变少时收敛页码（删除、筛选、自动刷新都会让它变少）
watch(shown, () => {
  const max = Math.max(1, Math.ceil(shown.value.length / pageSize))
  if (page.value > max) page.value = max
})

// 指标卡：只统计当前筛选到的记录。成功率的分母是**终态**（成功 + 失败/超时），
// "进行中"不计入——把还在跑的任务算成失败或成功都会让人误判。
const stat = computed(() => {
  const rows = shown.value
  const ok = rows.filter((t) => t.state === 'succeeded').length
  const failed = rows.filter((t) => t.state === 'failed' || t.state === 'expired').length
  const pending = rows.filter((t) => !isTerminal(t.state)).length
  const settled = ok + failed
  const done = rows.filter((t) => isTerminal(t.state) && t.durationMs)
  const avg = done.length ? Math.round(done.reduce((s, t) => s + t.durationMs, 0) / done.length) : 0
  return {
    total: rows.length,
    ok,
    failed,
    pending,
    doneCount: done.length,
    rateText: settled ? `${Math.round((ok / settled) * 100)}%` : '—',
    avgText: done.length ? `${avg} ms` : '—',
    batches: new Set(rows.map((t) => t.batchId).filter(Boolean)).size,
    hosts: new Set(rows.map((t) => t.node)).size,
  }
})

// 已选条件：把"列表为什么变少了"说清楚，并且每条都能单独撤掉
const activeChips = computed(() => {
  const out = []
  if (filter.value.node) out.push({ key: 'node', label: `节点：${filter.value.node}` })
  if (viewFilter.value.action) out.push({ key: 'action', label: `动作：${labelOfAction(viewFilter.value.action)}` })
  if (filter.value.state) out.push({ key: 'state', label: `状态：${stateLabel(filter.value.state)}` })
  if (viewFilter.value.range) out.push({ key: 'range', label: RANGE_LABELS[viewFilter.value.range] })
  if (filter.value.batchId) out.push({ key: 'batchId', label: `批次：${filter.value.batchId}` })
  if (keyword.value.trim()) out.push({ key: 'keyword', label: `搜索：${keyword.value.trim()}` })
  return out
})
function labelOfAction(kind) {
  const hit = actionOptions.value.find((a) => a.value === kind)
  return hit ? hit.label : kind
}
function clearChip(key) {
  if (key === 'node') { filter.value.node = ''; reload() }
  else if (key === 'state') { filter.value.state = ''; reload() }
  else if (key === 'batchId') clearBatchFilter()
  else if (key === 'action') viewFilter.value.action = ''
  else if (key === 'range') viewFilter.value.range = ''
  else if (key === 'keyword') keyword.value = ''
}
function clearAllChips() {
  keyword.value = ''
  viewFilter.value = { action: '', range: '' }
  filter.value = { node: '', state: '', batchId: '' }
  reload()
}

/* ================= 排序 / 导出 / 复制 ================= */

function onSortChange({ prop, order }) {
  // 取消排序 → 回到默认（最新在前）：列表默认必须有一个确定的顺序，
  // 否则自动刷新时行的位置会跳。
  if (!order) { sort.value = { key: 'createdAt', dir: 'desc' }; return }
  sort.value = { key: prop === 'durationMs' ? 'durationMs' : 'createdAt', dir: order === 'ascending' ? 'asc' : 'desc' }
}

function toCsv(rows) {
  const head = ['创建时间', '节点', '动作', '动作标识', '参数', '批次', '状态', '触发人', '原因', '耗时(ms)', '摘要']
  const esc = (v) => `"${String(v === undefined || v === null ? '' : v).replace(/"/g, '""')}"`
  const lines = [head.join(',')]
  for (const t of rows) {
    const params = kindSummary(t)
    lines.push([
      fmtTime(t.createdAt), t.node, t.title || t.kind, t.kind,
      params === t.kind ? '' : params, t.batchId || '', stateLabel(t.state),
      t.operator || '', t.reason || '', t.durationMs || '', t.message || '',
    ].map(esc).join(','))
  }
  // BOM：不带它 Excel 打开中文列会乱码（这是导出功能最常见的"看起来坏了"）
  return '\ufeff' + lines.join('\r\n')
}
function download(name, text) {
  const url = URL.createObjectURL(new Blob([text], { type: 'text/csv;charset=utf-8' }))
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  URL.revokeObjectURL(url)
}
function exportRows(rows, tag) {
  if (!rows || !rows.length) { ElMessage.warning('没有可导出的记录'); return }
  const d = new Date()
  const stamp = `${d.getFullYear()}${String(d.getMonth() + 1).padStart(2, '0')}${String(d.getDate()).padStart(2, '0')}-${String(d.getHours()).padStart(2, '0')}${String(d.getMinutes()).padStart(2, '0')}`
  download(`ops-tasks-${tag}-${stamp}.csv`, toCsv(rows))
  ElMessage.success(`已导出 ${rows.length} 条`)
}

// 复制：非 https / 未授权剪贴板时**如实报错并把内容显示出来**——
// 静默失败是最气人的（用户以为复制到了，粘出来却是旧内容）。
async function copyText(text, what) {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success(`${what}已复制`)
  } catch (e) {
    ElMessage.warning(`${what}复制失败（浏览器未授予剪贴板权限）：${text}`)
  }
}
function copyBatch(row) {
  if (!row || !row.batchId) return
  copyText(row.batchId, `批次号 ${row.batchId} `)
}
function copyOutput(row) {
  const data = (row && row.data) || {}
  const keys = Object.keys(data)
  if (!keys.length) { ElMessage.warning('这条任务没有输出可复制'); return }
  copyText(keys.map((k) => `===== ${k} =====\n${data[k]}`).join('\n\n'), '输出')
}

// 耗时条：**相对当前列表最慢一条**的比例。绝对值阈值在不同动作间没有可比性
// （诊断包几秒、查询状态几十毫秒），相对值才能一眼看出"这条明显慢"。
const maxDur = computed(() => Math.max(...shown.value.map((t) => t.durationMs || 0), 1))
function durWidth(row) {
  if (!row.durationMs) return '0%'
  return `${Math.max(6, Math.round((row.durationMs / maxDur.value) * 100))}%`
}
function durLevel(row) {
  const r = (row.durationMs || 0) / maxDur.value
  if (r >= 0.67) return 'slow'
  if (r >= 0.34) return 'mid'
  return ''
}

// 执行时间线：只用真实字段（创建 / 下发 / 完成），不编造中间步骤与耗时
function timelineOf(row) {
  if (!row) return []
  const t0 = row.createdAt || 0
  const off = (ts) => (ts && t0 ? `+${((ts - t0) / 1000).toFixed(2)}s` : '')
  const steps = [{ label: '任务已创建并入队', time: fmtTime(t0), delta: '', cls: 'ok' }]
  steps.push({
    label: row.deliveredAt ? '已下发至节点（随该节点上一次上报的响应送回）' : '等待节点下一次上报（离线节点不会领取）',
    time: row.deliveredAt ? fmtTime(row.deliveredAt) : '—',
    delta: off(row.deliveredAt),
    cls: row.deliveredAt ? 'ok' : 'run',
  })
  if (isTerminal(row.state)) {
    steps.push({
      label: row.state === 'succeeded' ? '执行完成并回传结果' : `执行结束：${stateLabel(row.state)}`,
      time: row.doneAt ? fmtTime(row.doneAt) : '—',
      delta: off(row.doneAt),
      cls: row.state === 'succeeded' ? 'ok' : 'err',
    })
  } else if (row.deliveredAt) {
    steps.push({ label: '节点执行中…', time: '—', delta: '', cls: 'run' })
  }
  return steps
}

/* ================= 多选与批量动作 ================= */

function onSelectionChange(rows) {
  selection.value = rows
}
function clearSelection() {
  selection.value = []
  if (tableRef.value) tableRef.value.clearSelection()
}
async function dispatchRow(row, reason) {
  return createOpsTask({ node: row.node, kind: row.kind, params: row.params || {}, reason })
}
async function rerunOne(row) {
  if (!row) return
  try {
    await ElMessageBox.confirm(
      `将按原样重新下发：${row.node} · ${row.title || row.kind}。写操作仍会受该机器 guards.ops 的约束。`,
      '重新执行', { type: 'info', confirmButtonText: '重新执行', cancelButtonText: '取消' },
    )
  } catch (e) {
    return
  }
  try {
    await dispatchRow(row, `重新执行 ${row.id}`)
    ElMessage.success('已重新下发')
    resultVisible.value = false
    await reload()
  } catch (e) {
    ElMessage.error(e.message || '重新下发失败')
  }
}
// 重新执行选中的记录：选中的动作可能各不相同，因此逐条下发
// （服务端的批量接口是"一个动作发多台"，不适用于"重跑这些历史记录"）。
async function rerunSelected() {
  const rows = selection.value
  if (!rows.length) return
  try {
    await ElMessageBox.confirm(
      `将按原样重新下发 ${rows.length} 条任务（同动作、同参数、原节点）；逐条给出结论，失败原因会合并提示。`,
      '重新执行', { type: 'info', confirmButtonText: '重新执行', cancelButtonText: '取消' },
    )
  } catch (e) {
    return
  }
  const failed = []
  for (const row of rows) {
    try {
      await dispatchRow(row, `重新执行 ${row.id}`)
    } catch (e) {
      failed.push(`${row.node}：${e.message || '失败'}`)
    }
  }
  const okCount = rows.length - failed.length
  if (failed.length) {
    ElMessage.warning(`已重新下发 ${okCount} 条；${failed.length} 条失败（${failed.slice(0, 2).join('；')}${failed.length > 2 ? ' …' : ''}）`)
  } else {
    ElMessage.success(`已重新下发 ${okCount} 条`)
  }
  clearSelection()
  await reload()
}
// 批量删除：只删已结束的（服务端也拦），未结束的**明说会被跳过**，
// 而不是整个操作失败或悄悄少删。
async function bulkDelete() {
  const rows = selection.value
  const deletable = rows.filter((r) => isTerminal(r.state))
  const blocked = rows.length - deletable.length
  if (!deletable.length) {
    ElMessage.warning(`选中的 ${rows.length} 条都还没结束，删不掉——先撤回排队中的，或等它们结束`)
    return
  }
  try {
    await ElMessageBox.confirm(
      `将删除 ${deletable.length} 条已结束的记录${blocked ? `；另有 ${blocked} 条未结束会被跳过（删掉"这条指令去哪了"就无从回答）` : ''}。删除会写审计，记录本身不可恢复。`,
      '批量删除记录', { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' },
    )
  } catch (e) {
    return
  }
  let okCount = 0
  const failed = []
  for (const row of deletable) {
    try {
      await deleteOpsTask(row.id)
      okCount++
    } catch (e) {
      failed.push(`${row.node}：${e.message || '失败'}`)
    }
  }
  if (failed.length) ElMessage.warning(`已删除 ${okCount} 条；${failed.length} 条失败（${failed[0]}${failed.length > 1 ? ' …' : ''}）`)
  else ElMessage.success(`已删除 ${okCount} 条`)
  clearSelection()
  await reload()
}

function openResult(row) {
  resultTask.value = row
  resultVisible.value = true
}

const resultTitle = computed(() => (resultTask.value ? `执行结果 · ${resultTask.value.title || resultTask.value.kind}` : '执行结果'))
const resultSections = computed(() => {
  const data = (resultTask.value && resultTask.value.data) || {}
  return Object.keys(data).map((key) => ({ key, value: data[key] }))
})
</script>

<style scoped>
/* 页面外壳：与资产台账 / 日志检索等页面保持同一套（此前本页漏了这几个类，
   于是标题下的说明文字其实**没有**变灰——文字层级全靠字号撑着）。 */
/* 内边距由 MainLayout 的 .content 统一提供，页面自己再加 16px 会形成双层留白，
   与主机列表等页面不一致。 */
.view {
  padding: 0;
}
.head-actions {
  margin-top: 10px;
  justify-content: flex-end;
}
/* 指标卡行：与资产台账同一套网格（窄窗口自动折成 2 列，不会挤成一条）
   注意：这个类必须在本页 scoped 样式里定义——它不像 .panel 那样是全局类。 */
.kpi-row {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 12px;
  margin-bottom: 12px;
}
.btn-txt {
  margin-left: 4px;
}
.muted {
  color: var(--text-dim);
  font-size: 13px;
}
.mono {
  font-family: var(--mono);
}
.small {
  font-size: 12px;
}
.node-picker {
  display: flex;
  gap: 8px;
  align-items: center;
  width: 100%;
}
.tag-write {
  display: inline-block;
  margin-left: 6px;
  padding: 0 6px;
  border: 1px solid rgba(255, 180, 84, 0.45);
  border-radius: 3px;
  background: rgba(255, 180, 84, 0.1);
  color: #ffb054;
  font-size: 11px;
  line-height: 16px;
}
.dot {
  display: inline-block;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  margin-right: 5px;
  vertical-align: 1px;
}
.dot.online {
  background: var(--accent);
}
.dot.missing {
  background: var(--danger);
}
.dot.queued {
  background: var(--warn);
}
.dot.archived {
  background: var(--text-dim);
}
.st-online {
  color: var(--accent);
}
.st-missing {
  color: var(--danger);
}
.st-queued {
  color: var(--warn);
}
.st-archived {
  color: var(--text-dim);
}
.act-option {
  display: flex;
  align-items: center;
  gap: 8px;
}
.act-title {
  font-weight: 500;
}
.act-count {
  margin-left: auto;
  color: var(--text-dim);
  font-size: 12px;
}
.result-row {
  display: flex;
  gap: 10px;
  align-items: baseline;
  padding: 3px 0;
  font-size: 13px;
}
.warn-text {
  color: var(--warn);
}
/* 分节 = 标题行（.sec）+ 输出块（.out）。.sec-block 只是成组的包裹元素，
   不留样式：标题行自己已有上下外边距（批量结果弹窗里也用同一个 .sec 当分节头），
   这里再加一层间距会让两处观感不一致。 */
.sec {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  margin: 14px 0 6px;
  font-size: 13px;
}
.out {
  margin: 0;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--fill-1);
  font-family: var(--mono);
  font-size: 12.5px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 320px;
  overflow: auto;
}
.field-hint {
  font-size: 12.5px;
  color: var(--text-dim);
  line-height: 1.5;
  margin-top: 4px;
}
.field-hint.warn {
  color: var(--warn);
}

/* ================= 列表视图（工具条 / 条件 chips / 批量条） ================= */
.op-toolbar {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  align-items: center;
  padding-bottom: 10px;
  border-bottom: 1px solid var(--border);
}
.op-search {
  width: 250px;
}
.op-tb-right {
  margin-left: auto;
  display: flex;
  gap: 8px;
  align-items: center;
}
.col-off {
  color: var(--text-muted);
}
.op-chips {
  display: flex;
  gap: 6px;
  align-items: center;
  flex-wrap: wrap;
  padding: 8px 0 2px;
}
.op-chips-label {
  font-size: 12px;
  color: var(--text-muted);
}
.op-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  height: 24px;
  padding: 0 9px;
  border-radius: 12px;
  border: 1px solid var(--border-strong);
  background: var(--accent-dim);
  color: var(--accent);
  font-size: 12px;
  cursor: pointer;
  transition: 0.15s;
}
.op-chip:hover {
  border-color: var(--accent);
}
.op-chip-clear {
  background: transparent;
  border-color: transparent;
  color: var(--text-muted);
}
.op-chip-clear:hover {
  color: var(--text);
}
/* ================= 表格单元 ================= */
.op-table-wrap {
  margin-top: 10px;
}
.op-dense :deep(.el-table__row td.el-table__cell) {
  padding: 3px 0;
}
.cell-2l {
  display: flex;
  flex-direction: column;
  line-height: 1.35;
}
.cell-2l b {
  font-weight: 500;
}
.cell-2l span {
  font-size: 11.5px;
  color: var(--text-muted);
}
.node-cell {
  display: flex;
  align-items: center;
  gap: 7px;
  min-width: 0;
}
.ndot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  flex: none;
}
.ndot.ok {
  background: var(--chart-green);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--chart-green) 16%, transparent);
}
.ndot.err {
  background: var(--danger);
  box-shadow: 0 0 0 3px var(--danger-dim);
}
.ndot.unknown {
  background: var(--text-muted);
  box-shadow: 0 0 0 3px rgba(127, 127, 127, 0.14);
}
.node-cell .nm {
  font-size: 12.5px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.zone {
  flex: none;
  padding: 0 5px;
  border: 1px solid var(--border);
  border-radius: 4px;
  background: var(--bg-elev);
  color: var(--text-muted);
  font-size: 10.5px;
}
/* 状态 pill：圆点 + 底色；排队/下发/执行中会呼吸，提示"还在动" */
.op-pill {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  height: 22px;
  padding: 0 9px;
  border-radius: 11px;
  font-size: 12px;
  white-space: nowrap;
}
.op-pill::before {
  content: '';
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: currentColor;
}
.op-pill.ok {
  background: color-mix(in srgb, var(--chart-green) 14%, transparent);
  color: var(--chart-green);
}
.op-pill.run {
  background: color-mix(in srgb, var(--violet) 16%, transparent);
  color: var(--violet);
}
.op-pill.warn {
  background: var(--warn-dim);
  color: var(--warn);
}
.op-pill.err {
  background: var(--danger-dim);
  color: var(--danger);
}
.op-pill.muted {
  background: rgba(127, 127, 127, 0.14);
  color: var(--text-dim);
}
.op-pill.run::before {
  animation: op-blink 1s infinite;
}
@keyframes op-blink {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.25;
  }
}
/* 触发人：首字母头像；无触发人 = 系统 / 告警联动，用闪电 */
.who-cell {
  display: flex;
  align-items: center;
  gap: 7px;
}
.av {
  width: 20px;
  height: 20px;
  border-radius: 50%;
  flex: none;
  display: grid;
  place-items: center;
  font-size: 10.5px;
  font-weight: 600;
  font-style: normal;
  background: var(--bg-elev);
  border: 1px solid var(--border-strong);
  color: var(--text-dim);
}
.av.bot {
  background: color-mix(in srgb, var(--violet) 16%, transparent);
  border-color: transparent;
  color: var(--violet);
}
.who-tx {
  display: flex;
  flex-direction: column;
  line-height: 1.3;
  min-width: 0;
}
.who-tx b {
  font-weight: 500;
  font-size: 12.5px;
}
.who-tx span {
  font-size: 11.5px;
  color: var(--text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
/* 耗时条：相对当前列表最慢一条的比例（绝对值阈值在不同动作间没有可比性） */
.dur-cell {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
}
.dur-bar {
  width: 40px;
  height: 4px;
  border-radius: 2px;
  background: rgba(127, 127, 127, 0.18);
  overflow: hidden;
  flex: none;
}
.dur-bar i {
  display: block;
  height: 100%;
  border-radius: 2px;
  background: var(--chart-green);
}
.dur-bar i.mid {
  background: var(--warn);
}
.dur-bar i.slow {
  background: var(--danger);
}
.dur-cell b {
  font-weight: 500;
  font-variant-numeric: tabular-nums;
  min-width: 58px;
  text-align: right;
}
/* 行内操作平时压低存在感，hover / 键盘聚焦时显出来 */
.ops-cell {
  display: flex;
  gap: 2px;
  justify-content: flex-end;
  opacity: 0.45;
  transition: 0.15s;
}
.op-table-wrap :deep(.el-table__row:hover) .ops-cell,
.op-table-wrap :deep(.el-table__row:focus-within) .ops-cell {
  opacity: 1;
}
.op-pager {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 2px 0;
  font-size: 12.5px;
  color: var(--text-dim);
}
.op-pager :deep(.el-pagination) {
  margin-left: auto;
}

/* ================= 抽屉内部 ================= */
.dr-body {
  display: flex;
  flex-direction: column;
  gap: 18px;
}
.dr-top {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.dr-sec h4 {
  margin: 0 0 10px;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted);
  letter-spacing: 0.4px;
  display: flex;
  align-items: center;
  gap: 8px;
}
.dr-sec h4::after {
  content: '';
  flex: 1;
  height: 1px;
  background: var(--border);
}
.kv {
  display: grid;
  grid-template-columns: 84px 1fr;
  gap: 7px 12px;
  margin: 0;
  font-size: 12.5px;
}
.kv dt {
  color: var(--text-muted);
}
.kv dd {
  margin: 0;
  word-break: break-all;
}
.tl {
  list-style: none;
  margin: 0;
  padding: 0 0 0 14px;
  border-left: 1px solid var(--border);
}
.tl li {
  position: relative;
  padding: 0 0 14px 12px;
  font-size: 12.5px;
}
.tl li:last-child {
  padding-bottom: 0;
}
.tl li::before {
  content: '';
  position: absolute;
  left: -19px;
  top: 5px;
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: var(--chart-green);
  box-shadow: 0 0 0 3px var(--bg-elev);
}
.tl li.run::before {
  background: var(--violet);
}
.tl li.err::before {
  background: var(--danger);
}
.tl li b {
  display: block;
  font-weight: 500;
}
.tl li span {
  color: var(--text-muted);
  font-size: 11.5px;
  font-family: var(--mono);
}
</style>

<!-- 抽屉被 Element Plus 传送到 body，scoped 选择器匹配不到，因此这里**不加 scoped**，
   用抽屉自带的类名做命名空间。 -->
<style>
/* 结果抽屉：诊断包有 7 个分节、每节输出（uname / df -h / ps / ss）可能几十行。
   抽屉本身就是"固定高度 + 正文滚动"，比模态框合适——标题、状态与页脚按钮始终在位，
   只有正文在滚。这里补一条页脚分隔线，并钉住页脚背景，避免长输出滚过时文字穿透。 */
.ops-result-drawer .el-drawer__body {
  overflow-y: auto;
}
.ops-result-drawer .el-drawer__footer {
  border-top: 1px solid var(--border);
  background: var(--bg-elev);
}
</style>
