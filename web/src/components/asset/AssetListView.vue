<template>
  <section class="view">
    <header class="view-head">
      <div class="head-row">
        <h2>资产台账</h2>
        <span class="muted">
          主机与中间件实例由 Agent 每轮上报自动发现；人工值不覆盖采集值，两者差异在详情里逐字段可见
        </span>
      </div>
    </header>

    <!-- 健康度：与其它监控页统一的 KpiCard 卡片行；数字与列表同一套条件，点卡片即下钻 -->
    <div class="kpi-row">
      <div class="kpi-click" title="清空筛选，查看全部资产" @click="drillAll">
        <KpiCard :value="summary.total" label="资产总数" hint="点击下钻" tone="total">
          <template #icon><el-icon :size="20"><Files /></el-icon></template>
        </KpiCard>
      </div>
      <div class="kpi-click" title="只看超过 30 分钟未上报的资产" @click="drillStatus('missing')">
        <KpiCard :value="summary.missing" label="失联" hint="超 30 分钟未上报" tone="down">
          <template #icon><el-icon :size="20"><WarningFilled /></el-icon></template>
        </KpiCard>
      </div>
      <div class="kpi-click" title="只看人工未指派责任人的资产" @click="drillNoOwner">
        <KpiCard :value="summary.noOwner" label="无责任人" hint="人工值未指派" tone="ops">
          <template #icon><el-icon :size="20"><UserFilled /></el-icon></template>
        </KpiCard>
      </div>
      <div class="kpi-click" title="只看同一字段人工值与采集值并存的资产" @click="drillConflict">
        <KpiCard :value="summary.conflict" label="人工 / 采集冲突" hint="同字段双值不一致" tone="mem">
          <template #icon><el-icon :size="20"><Switch /></el-icon></template>
        </KpiCard>
      </div>
      <div class="kpi-click kpi-static">
        <KpiCard :value="summary.changes" label="近 7 天变更" hint="含采集与人工" tone="conn">
          <template #icon><el-icon :size="20"><DataLine /></el-icon></template>
        </KpiCard>
      </div>
    </div>

    <div class="panel">
      <div class="filter-bar">
        <div class="field">
          <span class="field-label">类型</span>
          <el-select v-model="filter.type" placeholder="全部类型" clearable style="width: 150px">
            <el-option label="主机" value="host" />
            <el-option label="中间件实例" value="middleware-instance" />
          </el-select>
        </div>
        <div class="field">
          <span class="field-label">状态</span>
          <el-select v-model="filter.status" placeholder="全部状态" clearable style="width: 130px">
            <el-option label="在线" value="online" />
            <el-option label="失联" value="missing" />
            <el-option label="归档" value="archived" />
          </el-select>
        </div>
        <div class="field">
          <span class="field-label">来源</span>
          <el-select v-model="filter.source" placeholder="全部来源" clearable style="width: 130px">
            <el-option label="自动" value="auto" />
            <el-option label="人工" value="manual" />
            <el-option label="混合" value="mixed" />
          </el-select>
        </div>
        <div class="field">
          <span class="field-label">归属节点</span>
          <el-input
            v-model="filter.node"
            clearable
            placeholder="如 web-01"
            style="width: 150px"
            @keyup.enter="reload"
          />
        </div>
        <el-input
          v-model="filter.keyword"
          clearable
          placeholder="搜索资产名 / 自然键 / 属性值"
          style="width: 260px"
          @keyup.enter="reload"
        >
          <template #prefix><el-icon><Search /></el-icon></template>
        </el-input>
        <el-button type="primary" :loading="loading" @click="reload">查询</el-button>
        <el-button @click="resetFilter">重置</el-button>
        <!-- 下钻态可见且可撤销：否则「点了无责任人」之后列表为什么变少会没人说得清 -->
        <el-tag v-if="drill.ownerMissing" closable type="warning" @close="clearDrill">下钻：无责任人</el-tag>
        <el-tag v-if="drill.conflict" closable type="warning" @close="clearDrill">下钻：人工/采集冲突</el-tag>
      </div>

      <div class="action-bar">
        <el-button v-if="canWrite" type="primary" @click="openCreate">新建资产</el-button>
        <span v-if="canWrite" class="lock">assets:write</span>
        <span v-else class="muted">当前账号只读（缺 assets:write）</span>
        <span class="muted action-note">
          范围外资产按「不存在」返回，不做 403 区分；状态与来源由既有数据推导，不单独落库
        </span>
      </div>

      <el-alert v-if="loadError" type="error" :closable="false" show-icon :title="loadError" class="alert-gap" />

      <el-table
        :data="items"
        v-loading="loading"
        empty-text="没有匹配的资产（资产会在 Agent 首次上报后自动出现）"
        :row-class-name="rowClass"
        style="width: 100%"
        @row-click="openDetail"
      >
        <el-table-column label="资产名称" min-width="260">
          <template #default="{ row }">
            <div class="name">{{ row.name || row.naturalKey }}</div>
            <div class="sub">{{ subtitle(row) }}</div>
          </template>
        </el-table-column>
        <el-table-column label="类型" width="140">
          <template #default="{ row }">
            <span class="tag" :class="row.typeKey === 'host' ? 'host' : 'mw'">{{ typeLabel(row) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="归属节点" width="140">
          <template #default="{ row }">{{ row.node || '—' }}</template>
        </el-table-column>
        <el-table-column label="状态" width="110">
          <template #default="{ row }">
            <span class="dot" :class="row.status" />
            <span :class="'st-' + row.status">{{ statusLabel(row.status) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="最近上报" width="130">
          <template #default="{ row }">
            <span v-if="row.lastSeenAt">{{ relTime(row.lastSeenAt) }}</span>
            <span v-else class="muted">从未上报</span>
          </template>
        </el-table-column>
        <el-table-column label="来源" width="100">
          <template #default="{ row }">
            <span :class="'src-' + row.source">{{ sourceLabel(row.source) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="责任人" width="110">
          <template #default="{ row }">
            <span v-if="row.owner">{{ row.owner }}</span>
            <span v-else class="muted">未指派</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="180" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click.stop="openDetail(row)">详情</el-button>
            <el-button v-if="canWrite" link type="primary" @click.stop="openEdit(row)">编辑</el-button>
          </template>
        </el-table-column>
      </el-table>

      <div class="pager">
        <el-pagination
          background
          layout="total, sizes, prev, pager, next, jumper"
          :total="total"
          :page-size="pageSize"
          :current-page="page"
          :page-sizes="[10, 20, 50, 100]"
          @current-change="onPageChange"
          @size-change="onSizeChange"
        />
      </div>
    </div>

    <!-- 详情抽屉：原型的三 Tab（属性对比 / 变更历史 / 关联关系） -->
    <el-drawer v-model="detailVisible" size="780px" :title="detail ? detail.name || detail.naturalKey : '资产详情'">
      <div v-if="detail" class="detail">
        <div class="d-tags">
          <span class="tag" :class="detail.typeKey === 'host' ? 'host' : 'mw'">{{ typeLabel(detail) }}</span>
          <span class="tag" :class="detail.status">{{ statusLabel(detail.status) }}</span>
          <span class="tag">{{ detail.node || '无归属节点' }}</span>
          <span class="tag" v-if="detail.owner">{{ detail.owner }}</span>
        </div>
        <div class="d-meta">
          自然键 <code>{{ detail.naturalKey }}</code> · 资产 ID <code>{{ detail.displayId }}</code> ·
          首次发现 {{ fmtTime(detail.createdAt) }} ·
          最近上报 {{ detail.lastSeenAt ? relTime(detail.lastSeenAt) : '从未上报' }}
        </div>

        <el-tabs v-model="tab">
          <el-tab-pane :label="`属性对比 ${detail.attrs.length}`" name="attr">
            <el-alert
              v-if="detail.conflictCount > 0"
              type="warning"
              :closable="false"
              show-icon
              :title="`该资产有 ${detail.conflictCount} 个字段的人工值与采集值不一致，生效值取人工值`"
              class="alert-gap"
            />
            <div class="sec">
              <span>技术属性</span>
              <span class="muted">由 Agent 采集写入；人工值不覆盖采集值</span>
            </div>
            <el-table :data="techRows" empty-text="暂无采集属性" style="width: 100%">
              <el-table-column label="字段" width="150">
                <template #default="{ row }"><span class="mono">{{ row.key }}</span></template>
              </el-table-column>
              <el-table-column label="采集值" min-width="150">
                <template #default="{ row }">
                  <span v-if="row.discovery !== undefined" :class="{ struck: row.conflict }">{{ row.discovery }}</span>
                  <span v-else class="muted">—</span>
                </template>
              </el-table-column>
              <el-table-column label="人工值" min-width="150">
                <template #default="{ row }">
                  <span v-if="row.manual !== undefined">{{ row.manual }}</span>
                  <span v-else class="muted">—</span>
                </template>
              </el-table-column>
              <el-table-column label="生效值" min-width="200">
                <template #default="{ row }">
                  <span>{{ row.effective }}</span>
                  <span class="srcpill" :class="row.manual !== undefined ? 'man' : 'auto'">
                    {{ row.manual !== undefined ? '人工' : '自动' }}
                  </span>
                  <el-button
                    v-if="row.conflict && canWrite"
                    link
                    type="warning"
                    @click="restoreAttr(row.key)"
                  >恢复采集值</el-button>
                </template>
              </el-table-column>
            </el-table>

            <div class="sec">
              <span>管理属性</span>
              <span class="muted">仅人工维护，采集不写入（责任人固定用 owner 键）</span>
            </div>
            <el-table :data="manualRows" empty-text="暂无人工属性" style="width: 100%">
              <el-table-column label="字段" width="150">
                <template #default="{ row }"><span class="mono">{{ row.key }}</span></template>
              </el-table-column>
              <el-table-column label="人工值" min-width="200">
                <template #default="{ row }">{{ row.manual }}</template>
              </el-table-column>
              <el-table-column label="维护人 / 时间" min-width="200">
                <template #default="{ row }">
                  <span class="muted">{{ row.manualBy || '—' }} · {{ fmtTime(row.manualAt) }}</span>
                </template>
              </el-table-column>
            </el-table>

            <div class="sec">
              <span>归属节点</span>
              <span class="muted">资源范围锚点，接口与界面均不可变更</span>
            </div>
            <el-descriptions :column="1" border>
              <el-descriptions-item label="归属节点">{{ detail.node || '无' }}</el-descriptions-item>
            </el-descriptions>

            <div v-if="canWrite" class="drawer-actions">
              <el-button type="primary" @click="openEdit(detail)">编辑资产</el-button>
            </div>
          </el-tab-pane>

          <el-tab-pane :label="`变更历史 ${history.length}`" name="history">
            <div class="sec">
              <span>变更历史</span>
              <span class="muted">字段级 diff，仅在值真正变化时记录</span>
            </div>
            <el-timeline v-if="history.length">
              <el-timeline-item
                v-for="rec in history"
                :key="rec.at + rec.field + (rec.new || '')"
                :timestamp="fmtTime(rec.at)"
                :type="rec.kind === 'initial' ? 'success' : rec.source === 'manual' ? 'primary' : 'info'"
                placement="top"
              >
                <div class="tl-title">
                  <template v-if="rec.kind === 'initial'">资产建档</template>
                  <template v-else-if="rec.new">
                    更新 <span class="mono">{{ rec.field }}</span>
                  </template>
                  <template v-else>恢复采集值 <span class="mono">{{ rec.field }}</span></template>
                </div>
                <div v-if="rec.kind !== 'initial'" class="tl-diff">
                  <span class="struck">{{ rec.old || '（空）' }}</span> → <b>{{ rec.new || '（已清除）' }}</b>
                </div>
                <div class="muted">
                  来源 {{ rec.source === 'manual' ? '人工' : '采集' }}
                  <template v-if="rec.actor"> · 操作人 {{ rec.actor }}</template>
                </div>
              </el-timeline-item>
            </el-timeline>
            <el-empty v-else description="暂无变更" />
          </el-tab-pane>

          <el-tab-pane :label="`关联关系 ${links.length}`" name="links">
            <div class="sec">
              <span>关联关系</span>
              <span class="muted">自动发现时建立：中间件实例 runs_on 宿主主机</span>
            </div>
            <el-table :data="links" empty-text="暂无关联" style="width: 100%">
              <el-table-column label="方向" width="110">
                <template #default="{ row }">
                  <span :class="'rel-' + row.direction">{{ row.direction === 'out' ? '本资产 →' : '← 指向本资产' }}</span>
                </template>
              </el-table-column>
              <el-table-column label="关系" width="130">
                <template #default="{ row }"><span class="tag">{{ kindLabel(row.kind) }}</span></template>
              </el-table-column>
              <el-table-column label="对端资产" min-width="240">
                <template #default="{ row }">
                  <span class="mono">{{ row.peerKey }}</span>
                  <span class="muted">（{{ row.peerType === 'host' ? '主机' : '实例' }}）</span>
                </template>
              </el-table-column>
            </el-table>
            <p class="muted note">
              仅展示自动发现的直接关系，范围外的对端不返回。业务系统 / 分组等上层关系与人工关系维护属后续批次。
            </p>
          </el-tab-pane>
        </el-tabs>
      </div>
    </el-drawer>

    <!-- 新建：手工建档（人工来源）。表单项与列表列一一对应：
         资产名称→「资产名称」，类型+地址→「类型」，归属节点→「归属节点」，责任人→「责任人」；
         自然键由「实例类型 + 地址」自动拼出，不要求用户理解 <类型>:<地址> 的内部约定。 -->
    <el-dialog v-model="createVisible" title="新建资产" width="640px">
      <el-form label-width="100px">
        <el-form-item label="资产类型">
          <el-radio-group v-model="assetKind">
            <el-radio-button value="host">主机</el-radio-button>
            <el-radio-button value="instance">中间件实例</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item v-if="assetKind === 'host'" label="主机名">
          <el-input v-model="form.naturalKey" placeholder="与 Agent 上报的 hostname 一致，如 web-01" />
        </el-form-item>
        <template v-else>
          <el-form-item label="实例类型">
            <el-select v-model="instanceType" style="width: 100%">
              <el-option v-for="(label, key) in INSTANCE_LABELS" :key="key" :label="label" :value="key" />
            </el-select>
          </el-form-item>
          <el-form-item label="实例地址">
            <el-input v-model="instanceAddr" placeholder="host:port，如 127.0.0.1:6379" />
          </el-form-item>
          <el-form-item label="自然键">
            <el-input :model-value="naturalKeyPreview || '填写实例类型与地址后自动生成'" disabled />
          </el-form-item>
        </template>
        <el-form-item label="资产名称">
          <el-input v-model="form.name" placeholder="可留空，默认展示自然键" />
        </el-form-item>
        <el-form-item label="归属节点">
          <el-input
            v-model="form.node"
            :placeholder="assetKind === 'host' ? '留空则默认为主机自身' : '必须是你有权访问的节点；决定资源范围可见性'"
          />
        </el-form-item>
        <el-form-item label="责任人">
          <el-input v-model="ownerInput" placeholder="选填；以人工属性 owner 记录，列表按它展示与统计" />
        </el-form-item>
        <el-form-item label="其它属性">
          <div class="attr-editor">
            <div v-for="(row, i) in editAttrs" :key="i" class="attr-row">
              <el-input v-model="row.key" placeholder="属性名" style="width: 42%" />
              <el-input v-model="row.value" placeholder="值" style="width: 42%" />
              <el-button link type="danger" @click="editAttrs.splice(i, 1)">删除</el-button>
            </div>
            <el-button link type="primary" @click="editAttrs.push({ key: '', value: '' })">+ 添加属性</el-button>
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="submitCreate">提交</el-button>
      </template>
    </el-dialog>

    <!-- 编辑：可改资产名称 + 人工属性（采集值不受影响）。与列表对齐：名称 / 责任人 / 其它属性；
         责任人清空提交 = 恢复「未指派」（走 resetAttrs 删除人工值，而不是写入空值）。
         名称已预填当前值，未改动则不会提交（避免"提交了就报无内容可改"）。 -->
    <el-dialog v-model="editVisible" title="编辑资产" width="640px">
      <el-alert
        type="info"
        :closable="false"
        show-icon
        title="这里写入的是人工值：Agent 采集的值会原样保留，两者差异在详情里可对比。归属节点与自然键是资源范围锚点，不可修改。"
        class="alert-gap"
      />
      <el-form label-width="100px">
        <el-form-item label="资产">
          <el-input :model-value="editLabel" disabled />
        </el-form-item>
        <el-form-item label="资产名称">
          <el-input v-model="form.name" placeholder="留空或保持原值表示不修改；清空后列表中回落到自然键" />
        </el-form-item>
        <el-form-item label="归属节点">
          <el-input :model-value="form.node || '（无归属节点）'" disabled />
        </el-form-item>
        <el-form-item label="责任人">
          <el-input v-model="ownerInput" :placeholder="editHadOwner ? '清空并提交 = 恢复未指派' : '选填'" />
        </el-form-item>
        <el-form-item label="其它属性">
          <div class="attr-editor">
            <div v-for="(row, i) in editAttrs" :key="i" class="attr-row">
              <el-input v-model="row.key" placeholder="属性名" style="width: 42%" />
              <el-input v-model="row.value" placeholder="值" style="width: 42%" />
              <el-button link type="danger" @click="editAttrs.splice(i, 1)">删除</el-button>
            </div>
            <el-button link type="primary" @click="editAttrs.push({ key: '', value: '' })">+ 添加属性</el-button>
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="editVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="submitEdit">提交</el-button>
      </template>
    </el-dialog>
  </section>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  listAssets,
  getAsset,
  getAssetHistory,
  getAssetLinks,
  getAssetSummary,
  createAsset,
  updateAsset,
} from '../../api/asset'
import { useAuth } from '../../composables/useAuth'
// 与中间件 / 容器等页面统一的 KPI 卡片（顶部彩条 + 图标 + 数值）
import KpiCard from '../KpiCard.vue'

const auth = useAuth()
// 前端隐藏仅为体验：服务端 assets:write 是真正的边界（且属高风险权限，提交前二次确认）。
const canWrite = computed(() => auth.can('assets:write'))

const items = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const loading = ref(false)
const saving = ref(false)
const loadError = ref('')
const summary = ref({ total: 0, missing: 0, noOwner: 0, conflict: 0, changes: 0 })

const filter = ref({ type: '', status: '', source: '', node: '', keyword: '' })
// 摘要下钻的两个布尔条件（无责任人 / 有冲突）：不在下拉里，单独记状态以便显示与撤销
const drill = ref({ ownerMissing: false, conflict: false })

const detailVisible = ref(false)
const detail = ref(null)
const history = ref([])
const links = ref([])
const tab = ref('attr')

const createVisible = ref(false)
const editVisible = ref(false)
const editId = ref('')
const editAttrs = ref([])
const form = ref({ typeKey: 'host', naturalKey: '', name: '', node: '' })
// 表单与列表列一一对应：类型（主机/实例 + 实例类型）、责任人（owner 键）
const assetKind = ref('host')
const instanceType = ref('redis')
const instanceAddr = ref('')
const ownerInput = ref('')
const editHadOwner = ref(false)
// 编辑前的名称：用于判断「名称是否真的被改过」，未改则不提交该字段
const editOriginalName = ref('')

// 责任人的约定属性键，与服务端 asset.OwnerKey 一致
const OWNER_KEY = 'owner'

// 实例的自然键 = <类型>:<地址>，由表单自动拼出
const naturalKeyPreview = computed(() => {
  const addr = instanceAddr.value.trim()
  return instanceType.value && addr ? `${instanceType.value}:${addr}` : ''
})

const editLabel = computed(() => {
  const kind = form.value.typeKey === 'host' ? '主机' : '中间件实例'
  return `${form.value.naturalKey}（${kind}）`
})

// 类型展示：主机固定；实例用自然键前缀（<类型>:<地址>）映射成产品名
const INSTANCE_LABELS = {
  redis: 'Redis', mysql: 'MySQL', postgres: 'PostgreSQL', mongodb: 'MongoDB', nginx: 'Nginx',
  kafka: 'Kafka', rocketmq: 'RocketMQ', rabbitmq: 'RabbitMQ', kubernetes: 'Kubernetes',
  elasticsearch: 'Elasticsearch', clickhouse: 'ClickHouse', nacos: 'Nacos', zookeeper: 'ZooKeeper',
  fastdfs: 'FastDFS', docker: 'Docker',
}
function typeLabel(row) {
  if (row.typeKey === 'host') return '主机'
  const prefix = String(row.naturalKey || '').split(':')[0]
  return INSTANCE_LABELS[prefix] || prefix || '实例'
}
const STATUS_LABELS = { online: '在线', missing: '失联', archived: '归档' }
const statusLabel = (s) => STATUS_LABELS[s] || s
const SOURCE_LABELS = { auto: '自动', manual: '人工', mixed: '混合' }
const sourceLabel = (s) => SOURCE_LABELS[s] || s
const KIND_LABELS = { runs_on: 'runs_on 宿主机', member_of: 'member_of 集群', depends_on: 'depends_on 依赖', exposes: 'exposes 暴露' }
const kindLabel = (k) => KIND_LABELS[k] || k

function fmtTime(ts) {
  if (!ts) return '—'
  return new Date(ts).toLocaleString('zh-CN', { hour12: false })
}

// 相对时间：台账看的是「多久没上报了」，绝对时间在列表里反而要多算一步
function relTime(ts) {
  if (!ts) return '—'
  const diff = Date.now() - ts
  if (diff < 60_000) return '刚刚'
  if (diff < 3600_000) return `${Math.floor(diff / 60_000)} 分钟前`
  if (diff < 86400_000) return `${Math.floor(diff / 3600_000)} 小时前`
  return `${Math.floor(diff / 86400_000)} 天前`
}

// 副标题：主机看 OS 与规格，实例看拓扑/角色与版本；有冲突时优先提示冲突
function subtitle(row) {
  if (row.conflictCount > 0) {
    return `自然键 ${row.naturalKey} · ${row.conflictCount} 个字段的人工值与采集值不一致`
  }
  const v = row.values || {}
  if (row.typeKey === 'host') {
    const parts = [row.naturalKey]
    if (v.os) parts.push(v.os)
    if (v.cpuCores) parts.push(`${v.cpuCores}C`)
    if (v.memoryMB) parts.push(`${Math.round(Number(v.memoryMB) / 1024)}G`)
    return parts.join(' · ')
  }
  const parts = []
  if (v.topology) parts.push(v.topology)
  else if (v.role) parts.push(v.role)
  if (v.up) parts.push(v.up === 'true' ? '在线' : '离线')
  if (v.version) parts.push(v.version)
  return parts.length ? parts.join(' · ') : row.naturalKey
}

function rowClass({ row }) {
  if (row.conflictCount > 0) return 'row-conflict'
  if (row.status === 'missing') return 'row-missing'
  return ''
}

// 属性对比的两种分段：技术属性 = 有采集值（含冲突），管理属性 = 仅人工写
const attrMap = computed(() => detail.value && detail.value.attrs ? detail.value.attrs : [])
const techRows = computed(() => {
  const byKey = new Map()
  for (const attr of attrMap.value) {
    const row = byKey.get(attr.key) || { key: attr.key }
    if (attr.source === 'manual') {
      row.manual = attr.value
      row.manualBy = attr.updatedBy || ''
    } else {
      row.discovery = attr.value
    }
    byKey.set(attr.key, row)
  }
  const out = []
  for (const row of byKey.values()) {
    if (row.discovery === undefined && row.manual === undefined) continue
    row.conflict = row.discovery !== undefined && row.manual !== undefined
    row.effective = row.manual !== undefined ? row.manual : row.discovery
    if (row.discovery !== undefined) out.push(row)
  }
  return out.sort((a, b) => a.key.localeCompare(b.key))
})
const manualRows = computed(() => {
  const byKey = new Map()
  for (const attr of attrMap.value) {
    const row = byKey.get(attr.key) || { key: attr.key }
    if (attr.source === 'manual') {
      row.manual = attr.value
      row.manualBy = attr.updatedBy || ''
      row.manualAt = attr.updatedAt
    } else {
      row.discovery = attr.value
    }
    byKey.set(attr.key, row)
  }
  return [...byKey.values()]
    .filter((row) => row.manual !== undefined && row.discovery === undefined)
    .sort((a, b) => a.key.localeCompare(b.key))
})

// 列表与摘要共用同一套筛选参数，保证「点数字看到的」与「数字本身」一致
function filterParams() {
  const params = {
    type: filter.value.type,
    status: filter.value.status,
    source: filter.value.source,
    node: filter.value.node,
    keyword: filter.value.keyword,
  }
  if (drill.value.ownerMissing) params.ownerMissing = 'true'
  if (drill.value.conflict) params.conflict = 'true'
  return params
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const params = { ...filterParams(), limit: pageSize.value, offset: (page.value - 1) * pageSize.value }
    const [res, sum] = await Promise.all([listAssets(params), getAssetSummary(filterParams())])
    items.value = (res && res.assets) || []
    total.value = (res && res.total) || 0
    summary.value = sum || summary.value
  } catch (e) {
    loadError.value = e.message || '加载资产失败'
    items.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

function reload() {
  page.value = 1
  load()
}

function resetFilter() {
  filter.value = { type: '', status: '', source: '', node: '', keyword: '' }
  drill.value = { ownerMissing: false, conflict: false }
  reload()
}

// 健康度下钻：直接改筛选条件并回到第一页（改完的态会在筛选行以标签显示）
function drillAll() {
  resetFilter()
}
function drillStatus(status) {
  drill.value = { ownerMissing: false, conflict: false }
  filter.value = { ...filter.value, status }
  reload()
}
function drillNoOwner() {
  drill.value = { ownerMissing: true, conflict: false }
  reload()
}
function drillConflict() {
  drill.value = { conflict: true, ownerMissing: false }
  reload()
}
function clearDrill() {
  drill.value = { ownerMissing: false, conflict: false }
  reload()
}

function onPageChange(p) {
  page.value = p
  load()
}
function onSizeChange(size) {
  pageSize.value = size
  page.value = 1
  load()
}

async function loadDetail(id) {
  detail.value = await getAsset(id)
  const [hist, rel] = await Promise.all([getAssetHistory(id), getAssetLinks(id)])
  history.value = (hist && hist.records) || []
  links.value = (rel && rel.links) || []
}

async function openDetail(row) {
  detail.value = row
  history.value = []
  links.value = []
  tab.value = 'attr'
  detailVisible.value = true
  try {
    await loadDetail(row.id)
  } catch (e) {
    ElMessage.error(e.message || '加载资产详情失败')
  }
}

function openCreate() {
  assetKind.value = 'host'
  instanceType.value = 'redis'
  instanceAddr.value = ''
  ownerInput.value = ''
  form.value = { typeKey: 'host', naturalKey: '', name: '', node: '' }
  editAttrs.value = [{ key: '', value: '' }]
  createVisible.value = true
}

function openEdit(row) {
  editId.value = row.id
  // 名称预填当前值：否则用户看到空框，会以为「名称改不了」或担心提交后名称被清空。
  editOriginalName.value = row.name || ''
  form.value = { typeKey: row.typeKey, naturalKey: row.naturalKey, name: row.name || '', node: row.node || '' }
  ownerInput.value = row.owner || ''
  editHadOwner.value = !!row.owner
  // 预填当前人工值：采集值不预填，避免误以为「提交就会覆盖采集值」
  editAttrs.value = (row.attrs || [])
    .filter((a) => a.source === 'manual' && a.key !== OWNER_KEY)
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
  let typeKey = 'host'
  let naturalKey = form.value.naturalKey.trim()
  let node = form.value.node.trim()
  if (assetKind.value === 'instance') {
    typeKey = 'middleware-instance'
    naturalKey = naturalKeyPreview.value
    if (!instanceType.value || !instanceAddr.value.trim()) {
      ElMessage.warning('实例类型与实例地址为必填项')
      return
    }
  } else if (!naturalKey) {
    ElMessage.warning('主机名为必填项')
    return
  }
  // 主机的归属节点默认是它自己：主机资产的 Node 恒等于 hostname（与服务端自动发现一致）
  if (assetKind.value === 'host' && !node) node = naturalKey
  const attrs = attrsPayload(editAttrs.value)
  const owner = ownerInput.value.trim()
  if (owner) attrs[OWNER_KEY] = owner
  const payload = { typeKey, naturalKey, name: form.value.name.trim(), node, attrs }
  if (!(await confirmWrite(`将新建资产 ${naturalKey}（归属节点 ${node || '未指定'}）`))) return
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
  const id = editId.value
  const attrs = attrsPayload(editAttrs.value)
  const resetAttrs = []
  const owner = ownerInput.value.trim()
  if (owner) attrs[OWNER_KEY] = owner
  else if (editHadOwner.value) resetAttrs.push(OWNER_KEY)
  const name = form.value.name.trim()
  // 名称预填的是原值：只有真正改过才提交（服务端把「空名称」视为不修改）
  const nameChanged = name !== '' && name !== editOriginalName.value.trim()
  if (!nameChanged && !Object.keys(attrs).length && !resetAttrs.length) {
    ElMessage.warning('没有需要更新的内容')
    return
  }
  if (!(await confirmWrite(nameChanged ? `将资产名称改为「${name}」，并写入这些人工值；采集值不会被覆盖` : '将写入这些人工值；采集值不会被覆盖'))) return
  saving.value = true
  try {
    await updateAsset(id, { name: nameChanged ? name : '', attrs, resetAttrs })
    ElMessage.success('已保存人工值')
    editVisible.value = false
    await afterWrite(id)
  } catch (e) {
    ElMessage.error(e.message || '保存失败')
  } finally {
    saving.value = false
  }
}

// 恢复采集值：删掉该字段的人工值，让生效值回落到采集值（不是把采集值写回）
async function restoreAttr(key) {
  const id = detail.value.id
  if (!(await confirmWrite(`将清除字段「${key}」的人工值，生效值回落为采集值`))) return
  try {
    const updated = await updateAsset(id, { resetAttrs: [key] })
    detail.value = updated
    ElMessage.success('已恢复采集值')
    await afterWrite(id)
  } catch (e) {
    ElMessage.error(e.message || '恢复失败')
  }
}

// 写入后统一刷新：详情（若打开着当前资产）、列表与摘要
async function afterWrite(id) {
  if (detailVisible.value && detail.value && String(detail.value.id) === String(id)) {
    try {
      await loadDetail(id)
    } catch (e) {
      /* 详情刷新失败不影响列表刷新 */
    }
  }
  await load()
}

onMounted(load)
</script>

<style scoped>
/* 与 LogsView 等页面同一套头部约定：.view 自带内边距、h2 统一字号 */
.view {
  padding: 16px;
}
.view-head {
  margin-bottom: 12px;
}
.view-head .head-row {
  display: flex;
  align-items: baseline;
  gap: 12px;
  flex-wrap: wrap;
}
.view-head h2 {
  margin: 0;
  font-size: 18px;
}
.panel + .panel,
.kpi-row + .panel {
  margin-top: 12px;
}
/* 健康度卡片行：复用全局 KpiCard，与中间件 / 容器等页面同一视觉语言 */
.kpi-row {
  display: grid;
  /* 150px 下限保证 1080 宽的窗口下 5 张卡也在同一行，不会落单一张 */
  grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
  gap: 12px;
  margin-bottom: 12px;
}
.kpi-click {
  display: flex;
  cursor: pointer;
}
/* 卡片为子组件根节点：需要穿透一层才能撑满网格单元 */
.kpi-click :deep(.kpi-card) {
  width: 100%;
}
/* 「近 7 天变更」不可下钻，保留卡片样式但不给手型 */
.kpi-static {
  cursor: default;
}
/* 筛选条：面板内的一块浅底，把「筛选」与「操作」两行分开 */
.filter-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  padding: 12px;
  margin-bottom: 12px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: rgba(255, 255, 255, 0.02);
}
.field {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.field-label {
  font-size: 13px;
  color: var(--text-dim);
  white-space: nowrap;
}
.action-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-bottom: 12px;
}
.action-note {
  margin-left: auto;
  text-align: right;
}
/* 工具栏权限徽标：与原型的小锁样式对齐 */
.lock {
  display: inline-block;
  padding: 1px 5px;
  border: 1px solid rgba(255, 180, 84, 0.45);
  border-radius: 3px;
  background: rgba(255, 180, 84, 0.1);
  color: #ffb054;
  font-size: 11px;
  line-height: 16px;
}
/* 类型 / 状态 / 来源的配色：
   - 类型用 tag pill（与原型一致）；
   - 列表里的状态用「圆点 + 文字」（原型表格列如此）；
   - 列表里的来源用纯文字着色（原型表格列如此）。 */
.tag.host {
  background: var(--accent-dim);
  color: var(--accent);
}
.tag.mw {
  background: rgba(167, 139, 250, 0.16);
  color: var(--violet);
}
/* 抽屉头部仍用 tag 展示状态，因此 .tag.missing / .tag.archived 保留 */
.tag.missing {
  background: rgba(255, 93, 108, 0.16);
  color: var(--danger);
}
.tag.archived {
  background: rgba(255, 255, 255, 0.06);
  color: var(--text-dim);
}
/* 列表状态：圆点 + 文字 */
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
.dot.archived {
  background: var(--text-dim);
}
.st-online {
  color: var(--accent);
}
.st-missing {
  color: var(--danger);
}
.st-archived {
  color: var(--text-dim);
}
/* 列表来源：纯文字着色 */
.src-auto {
  color: var(--text-dim);
}
.src-manual {
  color: var(--violet);
}
.src-mixed {
  color: var(--warn);
  font-weight: 500;
}
.name {
  color: var(--accent);
  font-weight: 500;
}
.sub {
  color: var(--text-dim);
  font-size: 12px;
  margin-top: 2px;
}
.pager {
  display: flex;
  justify-content: flex-end;
  margin-top: 14px;
}
/* 行高亮需穿透到 Element Plus 生成的 tr 上（scoped 样式默认作用不到组件内部） */
:deep(.row-conflict) {
  background: rgba(255, 176, 32, 0.07);
}
:deep(.row-missing) {
  background: rgba(255, 80, 80, 0.07);
}
.d-tags {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}
.d-meta {
  margin: 10px 0 6px;
  font-size: 13px;
  color: var(--text-dim);
}
.d-meta code {
  background: rgba(255, 255, 255, 0.06);
  border-radius: 3px;
  padding: 1px 5px;
}
.sec {
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin: 16px 0 8px;
  font-size: 14px;
  font-weight: 600;
}
.sec .muted {
  font-weight: 400;
  font-size: 12px;
}
.muted {
  color: var(--text-dim);
  font-size: 13px;
}
.mono {
  font-family: var(--mono);
}
.struck {
  color: var(--text-dim);
  text-decoration: line-through;
  margin-right: 4px;
}
.srcpill {
  display: inline-block;
  margin-left: 8px;
  padding: 0 6px;
  border-radius: 3px;
  font-size: 11px;
  line-height: 18px;
}
.srcpill.auto {
  background: rgba(255, 255, 255, 0.06);
  color: var(--text-dim);
}
.srcpill.man {
  background: var(--accent-dim);
  color: var(--violet);
}
.tl-title {
  font-size: 14px;
}
.tl-diff {
  font-size: 13px;
  color: var(--text-dim);
  margin: 4px 0;
}
.rel-out {
  color: var(--accent);
}
.rel-in {
  color: var(--violet);
}
.note {
  margin-top: 14px;
  line-height: 20px;
}
.drawer-actions {
  margin-top: 18px;
}
.attr-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
}
.attr-editor {
  width: 100%;
}
</style>
