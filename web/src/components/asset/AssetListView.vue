<template>
  <section class="view">
    <PageHeader
      title="资产台账"
      desc="主机、中间件实例与 K8s 容器/工作负载由 Agent 每轮上报自动发现；人工值不覆盖采集值，两者差异在详情里逐字段可见"
    />

    <!-- 健康度：与其它监控页统一的 KpiCard 卡片行；数字与列表同一套条件，点卡片即下钻 -->
    <div class="kpi-row">
      <div class="kpi-click" title="清空筛选，查看全部资产" @click="drillAll">
        <KpiCard :value="summary.total" label="资产总数" hint="点击下钻" tone="total">
          <template #icon><el-icon :size="20"><Files /></el-icon></template>
        </KpiCard>
      </div>
      <div class="kpi-click" title="只看超过 30 分钟未上报的资产" @click="drillStatus('missing')">
        <KpiCard :value="summary.missing" label="失联" hint="超 30 分钟未上报" :tone="summary.missing > 0 ? 'down' : 'total'">
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
      <div class="kpi-click" title="只看已从台账隐藏（忽略）的资产；采集仍在继续，可逐个恢复" @click="drillIgnored">
        <KpiCard :value="summary.ignored" label="已忽略" hint="已从台账隐藏" tone="conn">
          <template #icon><el-icon :size="20"><Hide /></el-icon></template>
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
            <!-- 容器与工作负载是 K8s 清单上报的产物；它们**默认不计入上方数字**，
                 选中这里才会出现在列表里（服务端 asset.EphemeralTypes 的同一条规则）。 -->
            <el-option label="容器（Pod）" value="pod" />
            <el-option label="工作负载" value="workload" />
          </el-select>
        </div>
        <div class="field">
          <span class="field-label">状态</span>
          <el-select v-model="filter.status" placeholder="全部上报状态" clearable style="width: 150px">
            <el-option label="上报正常" value="online" />
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
        <div class="field">
          <span class="field-label">标签</span>
          <el-input
            v-model="filter.label"
            clearable
            placeholder="如 env:prod 或 env"
            style="width: 150px"
            title="按标签筛选：key:value 精确匹配值；只填 key 表示「存在该标签」"
            @keyup.enter="reload"
          />
        </div>
        <el-checkbox v-model="showIgnored" title="连已忽略的资产一起显示（它们带「已忽略」标记，可在这里恢复）">
          含已忽略
        </el-checkbox>
        <el-button type="primary" :loading="loading" @click="reload">查询</el-button>
        <el-button @click="resetFilter">重置</el-button>
        <!-- 下钻态可见且可撤销：否则「点了无责任人」之后列表为什么变少会没人说得清 -->
        <el-tag v-if="drill.ownerMissing" closable type="warning" @close="clearDrill">下钻：无责任人</el-tag>
        <el-tag v-if="drill.conflict" closable type="warning" @close="clearDrill">下钻：人工/采集冲突</el-tag>
        <el-tag v-if="drill.ignored" closable type="info" @close="clearDrill">下钻：已忽略</el-tag>
      </div>
      <!-- 把"数字里为什么没有容器"说清楚：否则用户看到集群里明明有几十个 Pod、
           顶部却只显示 12 个资产，只会怀疑数字算错。 -->
      <p class="muted note">
        容器与工作负载不计入上方数字：它们是滚动更新、扩缩容产生的短命对象，
        计入后「失联」会被替换掉的旧 Pod 填满、失去意义。把「类型」选成容器（Pod）或工作负载即可查看。
      </p>

      <div class="action-bar">
        <!-- 权限点不再做成按钮旁的小徽标：那是原型给开发看的标注，放在线上是噪声。
             需要时用悬停提示说明；真正缺权限的人看到的是下面那句只读提示。 -->
        <el-button
          v-if="canWrite"
          type="primary"
          title="需要 assets:write 权限；属高风险操作，提交前会二次确认"
          @click="openCreate"
        >新建资产</el-button>
        <span v-else class="muted">
          当前账号只读：缺 assets:write 权限，可在「权限模型 → 资产」中授予
        </span>
        <!-- 导出走独立权限点：一次把整份台账落盘。导的是**当前筛选命中的全部资产**，不是这一页。 -->
        <el-button
          v-if="canExport"
          :loading="exporting"
          title="导出当前筛选命中的全部资产（服务端导出，不受分页限制）；属高风险操作，需二次确认"
          @click="doExport"
        >导出清单 CSV</el-button>
        <span class="muted action-note">
          范围外资产按「不存在」返回，不做 403 区分；状态与来源由既有数据推导，不单独落库
        </span>
      </div>

      <el-alert v-if="loadError" type="error" :closable="false" show-icon :title="loadError" class="alert-gap" />

      <!-- 批量条：勾选后才出现。动作与单条一致（转派/标签/忽略/恢复），
           区别只在于"一次改一批"；结果逐条给结论（部分成功是常态）。 -->
      <BatchBar v-if="selection.length" :count="selection.length" @clear="clearSelection">
        <el-button size="small" @click="openBatchOwner">转派责任人</el-button>
        <el-button size="small" @click="openBatchLabel">打标签</el-button>
        <el-button size="small" @click="batchIgnore">忽略</el-button>
        <el-button size="small" @click="batchRestore">恢复</el-button>
      </BatchBar>

      <el-table
        ref="tableRef"
        :data="items"
        v-loading="loading"
        row-key="id"
        :row-class-name="rowClass"
        style="width: 100%"
        @row-click="onRowClick"
        @selection-change="onSelectionChange"
      >
        <template #empty>
          <EmptyState :icon="Files" :title="listEmptyTitle" :hints="listHints" />
        </template>
        <!-- 多选列：批量维护的入口。点复选框不应打开详情（那是在选行，不是在"看这一条"），
             见 onRowClick 里对 selection 列的判断。 -->
        <el-table-column type="selection" width="42" />
        <el-table-column label="资产名称" min-width="260">
          <template #default="{ row }">
            <div class="name">
              {{ row.name || row.naturalKey }}
              <!-- 已忽略的行只在「含已忽略」视图里出现，必须显式标记，否则会被当成正常台账资产 -->
              <span
                v-if="row.ignored"
                class="tag-ignored"
                :title="`已从台账隐藏${row.ignoredBy ? '（' + row.ignoredBy + '）' : ''}${row.ignoreReason ? '：' + row.ignoreReason : ''}`"
              >已忽略</span>
            </div>
            <div class="sub">{{ subtitle(row) }}</div>
            <div v-if="labelList(row).length" class="labels">
              <span v-for="lb in labelList(row)" :key="lb.key" class="label-chip">
                {{ lb.key }}<template v-if="lb.value">:{{ lb.value }}</template>
              </span>
            </div>
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
        <el-table-column label="上报状态" width="120">
          <template #default="{ row }">
            <!-- 列名与措辞刻意与「实例是否可达」区分开：两者是不同维度，同用"在线"会让人以为自相矛盾 -->
            <span
              class="dot"
              :class="row.status"
              title="上报正常 = 最近一次采集上报在 30 分钟内。它只表示 Agent 还在上报这条资产，不表示实例/容器本身可用（后者见名称下的副标题）"
            />
            <span :class="'st-' + row.status" :title="`最近上报 ${row.lastSeenAt ? relTime(row.lastSeenAt) : '从未'}`">
              {{ statusLabel(row.status) }}
            </span>
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
        <el-table-column label="操作" width="220" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click.stop="openDetail(row)">详情</el-button>
            <el-button v-if="canWrite" link type="primary" @click.stop="openEdit(row)">编辑</el-button>
            <!-- 低频且破坏性的动作收进「更多」：避免误点，也让行内保持清爽 -->
            <el-dropdown v-if="canWrite" trigger="click" @command="(cmd) => onRowCommand(cmd, row)">
              <el-button link type="primary" @click.stop>
                更多<el-icon :size="12"><ArrowDown /></el-icon>
              </el-button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item v-if="!row.ignored" command="ignore">
                    忽略（从台账隐藏）
                  </el-dropdown-item>
                  <el-dropdown-item v-else command="restore">
                    恢复（重新纳入台账）
                  </el-dropdown-item>
                  <!-- 采集资产删了会被下一轮上报重建，所以只对纯人工资产提供彻底删除 -->
                  <el-dropdown-item v-if="!row.hasDiscovery" command="purge" divided>
                    彻底删除（仅人工资产）
                  </el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
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

        <!-- 已忽略：必须显式说明"它还在、只是被隐藏了"，否则用户会以为资产被删了 -->
        <el-alert
          v-if="detail.ignored"
          type="warning"
          :closable="false"
          show-icon
          class="alert-gap"
          :title="`该资产已从台账隐藏（忽略）${detail.ignoredBy ? ' · ' + detail.ignoredBy : ''}${detail.ignoredAt ? ' · ' + fmtTime(detail.ignoredAt) : ''}`"
          :description="`${detail.ignoreReason ? '理由：' + detail.ignoreReason + '；' : ''}采集仍在继续，恢复后会看到最新状态。列表与统计默认不包含它。`"
        />
        <div v-if="detail.ignored && canWrite" class="drawer-actions">
          <el-button type="primary" @click="doRestore(detail)">恢复（重新纳入台账）</el-button>
        </div>

        <!-- 标签：与属性分开的分类维度 -->
        <div class="d-labels">
          <span class="d-labels-title">标签</span>
          <template v-if="labelList(detail).length">
            <span v-for="lb in labelList(detail)" :key="lb.key" class="label-chip">
              {{ lb.key }}<template v-if="lb.value">:{{ lb.value }}</template>
            </span>
          </template>
          <span v-else class="muted">未打标签（用于分类与筛选，如 env:prod、system:order）</span>
          <el-button v-if="canWrite" link type="primary" class="d-labels-edit" @click="openEdit(detail)">编辑标签</el-button>
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
            <el-table :data="techRows" style="width: 100%">
              <template #empty>
                <EmptyState
                  :icon="Files"
                  title="暂无采集属性"
                  :hints="['该资产还没有采集到的技术属性', '等下一轮上报后再看；仍未出现时确认对应采集项已开启']"
                />
              </template>
              <el-table-column label="字段" width="150">
                <template #default="{ row }"><span class="mono">{{ row.key }}</span></template>
              </el-table-column>
              <el-table-column label="采集值" min-width="150">
                <template #default="{ row }">
                  <span v-if="row.discovery !== undefined" :class="{ struck: row.conflict }">{{ attrText(row.key, row.discovery) }}</span>
                  <span v-else class="muted">—</span>
                </template>
              </el-table-column>
              <el-table-column label="人工值" min-width="150">
                <template #default="{ row }">
                  <span v-if="row.manual !== undefined">{{ attrText(row.key, row.manual) }}</span>
                  <span v-else class="muted">—</span>
                </template>
              </el-table-column>
              <el-table-column label="生效值" min-width="200">
                <template #default="{ row }">
                  <span>{{ attrText(row.key, row.effective) }}</span>
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
            <el-table :data="manualRows" style="width: 100%">
              <template #empty>
                <EmptyState
                  :icon="Files"
                  title="暂无人工属性"
                  :hints="['管理属性只由人工维护，采集不会写入', '在详情里补充责任人（owner 键）等字段后这里就有内容']"
                />
              </template>
              <el-table-column label="字段" width="150">
                <template #default="{ row }"><span class="mono">{{ row.key }}</span></template>
              </el-table-column>
              <el-table-column label="人工值" min-width="200">
                <template #default="{ row }">{{ attrText(row.key, row.manual) }}</template>
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
              <!-- 期望值（标杆）：配置巡检的合规偏差以它为基准，按资产类型只保留一个 -->
              <el-button v-if="!isBaseline" title="把本资产的当前配置设为该类型的期望值（配置巡检的合规偏差以它为基准）" @click="makeBaseline">
                设为期望值（标杆）
              </el-button>
              <template v-else>
                <span class="muted">本资产是「{{ typeLabel(detail) }}」类型的期望值</span>
                <el-button type="danger" plain @click="dropBaseline">清除期望值</el-button>
              </template>
            </div>

            <!-- 容器类资产（Pod / 工作负载）到容器页看日志与事件：只读跳转，
                 因此不受写权限门控——只读账号同样需要这条排障路径 -->
            <div v-if="detail.container" class="drawer-actions">
              <el-button
                :title="detail.typeKey === 'pod' ? '到容器页查看该 Pod 的日志与事件' : '到容器页查看该集群/命名空间的工作负载'"
                @click="gotoContainer(detail)"
              >
                {{ detail.typeKey === 'pod' ? '查看容器日志' : '查看容器工作负载' }}
              </el-button>
              <span class="muted">集群 {{ detail.container.cluster }} · {{ detail.container.namespace }}</span>
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
            <EmptyState
              v-else
              :icon="DataLine"
              title="暂无变更记录"
              :hints="['字段发生变化（采集值差异或人工值修改）时才记录一条', '运行态字段（如 up / status）刻意不参与比对']"
            />
          </el-tab-pane>

          <el-tab-pane :label="`关联关系 ${links.length}`" name="links">
            <div class="sec">
              <span>关联关系</span>
              <!-- 关系图：以本资产为中心的 N 跳邻域。只读，因此不受写权限门控。 -->
              <el-button size="small" style="margin-left: auto" @click="openTopology">关系图</el-button>
              <el-button v-if="canWrite" size="small" @click="openLinkAdd">
                添加关系
              </el-button>
            </div>
            <p class="muted">
              人工维护的关系优先于采集自动发现：被人工认领过的边不会被采集覆盖。
              自动建立的有两类：中间件实例 / K8s 容器 runs_on 宿主主机，以及 K8s 容器 member_of 所属工作负载。
            </p>
            <el-table :data="links" style="width: 100%">
              <template #empty>
                <EmptyState
                  :icon="Files"
                  title="暂无关联关系"
                  :hints="['runs_on（实例/容器 → 主机）与 member_of（容器 → 工作负载）由 Agent 上报后自动建立', '也可以用上方「添加关系」人工建立']"
                />
              </template>
              <el-table-column label="方向" width="105">
                <template #default="{ row }">
                  <span :class="'rel-' + row.direction">{{ row.direction === 'out' ? '本资产 →' : '← 指向本资产' }}</span>
                </template>
              </el-table-column>
              <el-table-column label="关系" width="130">
                <template #default="{ row }"><span class="tag">{{ kindLabel(row.kind) }}</span></template>
              </el-table-column>
              <el-table-column label="对端资产" min-width="200">
                <template #default="{ row }">
                  <span class="mono">{{ row.peerKey }}</span>
                  <span class="muted">（{{ peerTypeLabel(row.peerType) }}）</span>
                </template>
              </el-table-column>
              <el-table-column label="来源" width="80">
                <template #default="{ row }">
                  <span :class="row.source === 'manual' ? 'src-manual' : 'muted'">
                    {{ row.source === 'manual' ? '人工' : '采集' }}
                  </span>
                </template>
              </el-table-column>
              <el-table-column v-if="canWrite" label="操作" width="70" align="center">
                <template #default="{ row }">
                  <el-button type="danger" link size="small" @click="removeLink(row)">解除</el-button>
                </template>
              </el-table-column>
            </el-table>

            <!-- 被人工隐藏的关系（逻辑删除的结果）。必须能列出来并能恢复：
                 看不见的删除等于不可逆，用户会以为删错了就再也回不来。 -->
            <template v-if="suppressed.length">
              <div class="sec"><span>已人工隐藏 {{ suppressed.length }}</span></div>
              <el-table :data="suppressed" size="small" style="width: 100%">
                <el-table-column label="关系" width="130">
                  <template #default="{ row }"><span class="tag">{{ kindLabel(row.kind) }}</span></template>
                </el-table-column>
                <el-table-column label="对端资产" min-width="200">
                  <template #default="{ row }"><span class="mono">{{ row.peerKey }}</span></template>
                </el-table-column>
                <el-table-column label="隐藏者" width="110">
                  <template #default="{ row }">{{ row.createdBy || '—' }}</template>
                </el-table-column>
                <el-table-column v-if="canWrite" label="操作" width="70" align="center">
                  <template #default="{ row }">
                    <el-button link size="small" @click="restoreLink(row)">恢复</el-button>
                  </template>
                </el-table-column>
              </el-table>
              <p class="muted note">
                隐藏是逻辑删除：采集不会再自动建立这些关系。「恢复」把它们交还给采集 ——
                若采集仍在上报，下一次上报就会重新出现。
              </p>
            </template>
            <p class="muted note">
              这里只列**直接**关系，范围外的对端不返回。要看 N 跳邻域（影响面）用上方「关系图」：
              点节点即以它为中心重新展开。
            </p>
          </el-tab-pane>
        </el-tabs>
      </div>
    </el-drawer>

    <!-- 添加关系：方向 × 类型 × 对端。方向做成显式选择而不是让前端猜——
         服务端把 URL 里的资产当作边的基准，direction 决定对端是终点还是起点。 -->
    <el-dialog v-model="linkAddVisible" title="添加关系" width="560px">
      <el-form label-width="90px">
        <el-form-item label="方向">
          <el-radio-group v-model="linkForm.direction">
            <el-radio-button value="out">本资产 → 对端</el-radio-button>
            <el-radio-button value="in">对端 → 本资产</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="关系类型">
          <el-select v-model="linkForm.kind" style="width: 100%">
            <el-option v-for="k in LINK_KINDS" :key="k.value" :label="k.label" :value="k.value" />
          </el-select>
        </el-form-item>
        <el-form-item label="对端资产">
          <el-select
            v-model="linkForm.peer"
            filterable
            remote
            :remote-method="searchLinkPeers"
            :loading="peerLoading"
            placeholder="输入名称 / 自然键 / 节点搜索"
            style="width: 100%"
          >
            <el-option v-for="o in peerOptions" :key="o.value" :label="o.label" :value="o.value" />
          </el-select>
        </el-form-item>
      </el-form>
      <div class="muted">
        候选只列出你可见范围内的资产（范围外的对端即便手工填也会被服务端拒绝）。
        目前 runs_on 与 member_of 多由采集建立，depends_on 与 exposes 只能人工维护。
      </div>
      <template #footer>
        <el-button @click="linkAddVisible = false">取消</el-button>
        <el-button type="primary" :loading="linkBusy" @click="submitLinkAdd">确定</el-button>
      </template>
    </el-dialog>

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
        <el-form-item label="标签">
          <div class="attr-editor">
            <div class="muted label-hint">
              分类维度（如 env:prod、system:order），与「其它属性」不同：标签只用于筛选与展示，不参与巡检比对。
              删除某一行即删除该标签；键留空的行会被忽略。
            </div>
            <div v-for="(row, i) in editLabels" :key="i" class="attr-row">
              <el-input v-model="row.key" placeholder="标签键，如 env" style="width: 42%" />
              <el-input v-model="row.value" placeholder="值，如 prod（可留空）" style="width: 42%" />
              <el-button link type="danger" @click="removeLabelRow(i)">删除</el-button>
            </div>
            <el-button link type="primary" @click="addLabelRow">+ 添加标签</el-button>
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="editVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="submitEdit">提交</el-button>
      </template>
    </el-dialog>

    <!-- 批量转派责任人：留空即报错（要清空请点底部的「清空责任人」），
         避免"输入框忘了填"就把一整批责任人静默清掉。 -->
    <el-dialog v-model="batchOwner.visible" title="批量转派责任人" width="540px">
      <el-alert
        type="info"
        :closable="false"
        show-icon
        class="alert-gap"
        :title="`把选中的 ${selection.length} 条资产的责任人统一改成下面的值（写入人工属性 owner，采集值不受影响）`"
      />
      <el-form label-width="90px">
        <el-form-item label="责任人">
          <el-input
            v-model="batchOwner.owner"
            placeholder="如 张三 / sre-team"
            @keyup.enter="submitBatchOwner(false)"
          />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="batchOwner.visible = false">取消</el-button>
        <el-button @click="submitBatchOwner(true)">清空责任人</el-button>
        <el-button type="primary" :loading="batchBusy" @click="submitBatchOwner(false)">保存转派</el-button>
      </template>
    </el-dialog>

    <!-- 批量打标签：写入会覆盖同名标签，删除会移除该键。
         两种方式放在一起，是因为"给这批机器打上 env=prod"与"把打错的 env 摘掉"是同一件事的两面。 -->
    <el-dialog v-model="batchLabel.visible" title="批量打标签" width="540px">
      <el-alert
        type="info"
        :closable="false"
        show-icon
        class="alert-gap"
        title="标签是分类维度（如 env=prod、system=order），与「其它属性」不同：只用于筛选与展示，不参与巡检比对。"
      />
      <el-form label-width="90px">
        <el-form-item label="方式">
          <el-radio-group v-model="batchLabel.mode">
            <el-radio value="set">写入 / 覆盖</el-radio>
            <el-radio value="remove">删除该标签</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="标签键">
          <el-input v-model="batchLabel.key" placeholder="如 env" />
        </el-form-item>
        <el-form-item v-if="batchLabel.mode === 'set'" label="值">
          <el-input v-model="batchLabel.value" placeholder="如 prod（可留空 = 只标记存在该键）" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="batchLabel.visible = false">取消</el-button>
        <el-button type="primary" :loading="batchBusy" @click="submitBatchLabel">提交</el-button>
      </template>
    </el-dialog>

    <!-- 批量维护结果：逐条结论。部分成功是常态（范围外的按「不存在」计入），
         因此必须逐条说明原因，而不是只弹一句"成功 3 条"。 -->
    <el-dialog v-model="batchResultVisible" :title="batchTitle" width="660px">
      <div v-if="batchResult">
        <el-alert
          :type="batchResult.failed === 0 ? 'success' : batchResult.ok > 0 ? 'warning' : 'error'"
          :closable="false"
          show-icon
          class="alert-gap"
          :title="`目标 ${batchResult.total} 条：成功 ${batchResult.ok} 条，失败 ${batchResult.failed} 条`"
          :description="batchResultHint"
        />
        <template v-if="failedItems.length">
          <div class="batch-sec">
            <span>未成功（{{ failedItems.length }}）</span>
            <span class="muted">逐条原因</span>
          </div>
          <div v-for="it in failedItems" :key="it.id" class="batch-row">
            <span class="mono">{{ it.node || '—' }}</span>
            <span class="batch-name">{{ it.name || 'ast_' + it.id }}</span>
            <span class="warn-text">{{ it.error }}</span>
          </div>
        </template>
      </div>
      <template #footer>
        <el-button type="primary" @click="batchResultVisible = false">知道了</el-button>
      </template>
    </el-dialog>

    <!-- 关系图：以本资产为中心的 N 跳邻域。
         点节点=「以它为中心」重新展开——图上的"走下去"就是换中心，
         比在抽屉与弹窗之间来回切更贴合看图的直觉。 -->
    <el-dialog v-model="topoVisible" :title="topoTitle" width="900px" @opened="renderTopology" @closed="disposeChart">
      <div class="topo-bar">
        <el-radio-group v-model="topoDepth" size="small" @change="openTopology">
          <el-radio-button :value="1">1 跳</el-radio-button>
          <el-radio-button :value="2">2 跳</el-radio-button>
          <el-radio-button :value="3">3 跳</el-radio-button>
        </el-radio-group>
        <span class="muted">点节点可以「以它为中心」重新展开</span>
        <span class="muted topo-legend">实线 = 采集发现 · 虚线 = 人工维护 · 红圈 = 失联</span>
      </div>
      <el-alert
        v-if="topoTruncated"
        type="warning"
        :closable="false"
        show-icon
        title="节点数达到上限，图只展开了部分关系"
        description="缩小跳数，或直接以目标资产为中心查看。静默省略会让人以为关系就这么多。"
        class="alert-gap"
      />
      <el-alert v-if="topoError" type="error" :closable="false" show-icon :title="topoError" class="alert-gap" />
      <div v-show="!topoError" ref="topoChartRef" class="topo-chart"></div>
    </el-dialog>
  </section>
</template>

<script setup>
import { ref, reactive, computed, onMounted, onBeforeUnmount, nextTick } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import * as echarts from 'echarts'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  listAssets,
  getAsset,
  getAssetHistory,
  getAssetLinks,
  getAssetTopology,
  createAssetLink,
  deleteAssetLink,
  restoreAssetLink,
  getAssetSummary,
  createAsset,
  updateAsset,
  listInspectBaselines,
  setAssetBaseline,
  clearAssetBaseline,
  ignoreAsset,
  restoreAsset,
  purgeAsset,
  updateAssetLabels,
  batchAssets,
  exportAssets,
} from '../../api/asset'
import { useAuth } from '../../composables/useAuth'
import { Files, DataLine } from '@element-plus/icons-vue'
import PageHeader from '../common/PageHeader.vue'
import EmptyState from '../common/EmptyState.vue'
// 与中间件 / 容器等页面统一的 KPI 卡片（顶部彩条 + 图标 + 数值）
import KpiCard from '../KpiCard.vue'
// 批量条与节点操作页共用（同一个视觉与「取消选择」位置）
import BatchBar from '../BatchBar.vue'

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
const summary = ref({ total: 0, missing: 0, noOwner: 0, conflict: 0, changes: 0, ignored: 0 })

// 列表空态：区分「台账本身就是空的」与「被筛选掉了」，并指向下一步动作
const listEmptyTitle = computed(() => (summary.value.total > 0 ? '没有匹配的资产' : '资产台账为空'))
const listHints = computed(() => {
  if (summary.value.total > 0) {
    return [
      `台账共 ${summary.value.total} 条资产，当前筛选条件下没有匹配项`,
      '清空上方筛选条件，或切换「含已忽略」再查一次',
    ]
  }
  return [
    '资产在 Agent 首次上报后自动出现：确认至少有一台 Agent 已连上 Server',
    '也可以直接人工建档，新建的资产会立即出现在列表里',
  ]
})

const filter = ref({ type: '', status: '', source: '', node: '', keyword: '', label: '', ignored: '' })
// 摘要下钻的三个条件（无责任人 / 有冲突 / 已忽略）：不在下拉里，单独记状态以便显示与撤销
const drill = ref({ ownerMissing: false, conflict: false, ignored: false })

const detailVisible = ref(false)
const detail = ref(null)
const history = ref([])
const links = ref([])
// 被人工隐藏（逻辑删除）的关系：必须让用户看见并能恢复
const suppressed = ref([])
const tab = ref('attr')

// ---- 关系的人工维护 ----
// 类型清单与服务端 asset.LinkKind 对齐：这里的下拉只是可选项，真正的边界在服务端
// （未知类型一律 400）。
const LINK_KINDS = [
  { value: 'runs_on', label: 'runs_on · 运行于' },
  { value: 'member_of', label: 'member_of · 归属' },
  { value: 'depends_on', label: 'depends_on · 依赖' },
  { value: 'exposes', label: 'exposes · 暴露' },
]
const linkAddVisible = ref(false)
const linkBusy = ref(false)
const route = useRoute()
const router = useRouter()
const linkForm = reactive({ direction: 'out', kind: 'runs_on', peer: '' })
const peerOptions = ref([])
const peerLoading = ref(false)
// 各资产类型当前的期望值（标杆）来源，用于抽屉里显示"本资产是不是标杆"
const baselines = ref([])

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
// 标签编辑：表单里的行 + 原标签（后者用于推断"哪些键被删掉了"）
const editLabels = ref([])
const editOriginalLabels = ref({})

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
  // K8s 容器/工作负载的标题只能看 typeKey，**不能走下面的自然键前缀**：
  // 它们的自然键是 <集群>/<命名空间>/<kind>/<名称>，按 ':' 切会切出 apiserver 的 scheme（https）。
  if (row.typeKey === 'pod') return '容器（Pod）'
  if (row.typeKey === 'workload') return '工作负载'
  if (row.typeKey === 'host') return '主机'
  const prefix = String(row.naturalKey || '').split(':')[0]
  return INSTANCE_LABELS[prefix] || prefix || '实例'
}
// 关系对端的类型标签：不复用 typeLabel（它要 row），但语义必须一致——
// 否则「容器 → 主机」的 runs_on 会被显示成「实例 → 主机」。
const PEER_TYPE_LABELS = { host: '主机', 'middleware-instance': '实例', pod: '容器', workload: '工作负载' }
const peerTypeLabel = (t) => PEER_TYPE_LABELS[t] || t
// 「上报状态」：描述 Agent 是否还在上报这条资产，与实例/容器自身是否可用无关。
// 措辞刻意避开"在线/离线"——那是采集侧的探活结果，两者同词会让人觉得自相矛盾。
const STATUS_LABELS = { online: '上报正常', missing: '失联', archived: '归档' }
const statusLabel = (s) => STATUS_LABELS[s] || s
// 容器状态（Docker 采集上报的原始值）中文化：台账里"为什么不可达"要靠它说清
const CONTAINER_STATUS_LABELS = {
  running: '运行中', exited: '已退出', paused: '已暂停', created: '已创建',
  restarting: '重启中', removing: '删除中', dead: '异常退出',
}
const containerStatusLabel = (s) => CONTAINER_STATUS_LABELS[s] || `容器 ${s}`
// 属性值的展示形式：原始布尔/状态码对运维没有阅读价值
function attrText(key, value) {
  if (value === undefined || value === null || value === '') return ''
  const s = String(value)
  if (key === 'up') return s === 'true' ? '可达' : '不可达'
  if (key === 'status') return containerStatusLabel(s)
  return s
}
const SOURCE_LABELS = { auto: '自动', manual: '人工', mixed: '混合' }
const sourceLabel = (s) => SOURCE_LABELS[s] || s
// member_of 的落点从"归属集群"改成了"归属工作负载"（Pod → Deployment/StatefulSet/DaemonSet）：
// 集群维度由自然键前缀表达，不再单独建边。标签跟着改成中性的「归属」。
const KIND_LABELS = { runs_on: 'runs_on 运行于', member_of: 'member_of 归属', depends_on: 'depends_on 依赖', exposes: 'exposes 暴露' }
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
  // 这里是「实例/容器自身是否可用」——采集侧的结论，与「上报状态」列是两个维度：
  // 容器退出了，Agent 仍然会把它（连 up=false 一起）每轮上报，于是上报状态依旧是"上报正常"。
  if (v.status) parts.push(containerStatusLabel(v.status))
  else if (v.up) parts.push(v.up === 'true' ? '实例可达' : '实例不可达')
  if (v.version) parts.push(v.version)
  else if (v.image) parts.push(v.image)
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

// 本资产是否是其资产类型的期望值（标杆）：抽屉据此在「设为」与「清除」之间切换。
const isBaseline = computed(() => {
  const d = detail.value
  if (!d) return false
  const b = baselines.value.find((x) => x.typeKey === d.typeKey)
  return !!(b && String(b.assetId) === String(d.id))
})

// 「含已忽略」开关：映射到 filter.ignored（with / 空串=默认隐藏）
const showIgnored = computed({
  get: () => filter.value.ignored === 'with',
  set: (v) => {
    filter.value = { ...filter.value, ignored: v ? 'with' : '' }
    reload()
  },
})

// labelFingerprint 把标签集规范化成可比较的字符串（按键排序）：
// 直接 JSON.stringify 会受键顺序影响，出现"没改却判定改了"的假阳性。
function labelFingerprint(labels) {
  return Object.keys(labels || {})
    .sort()
    .map((k) => `${k}=${labels[k]}`)
    .join('\n')
}

// labelList 把标签对象转成可渲染的数组（键排序，展示稳定）
function labelList(row) {
  const labels = (row && row.labels) || {}
  return Object.keys(labels)
    .sort()
    .map((key) => ({ key, value: labels[key] }))
}

// 标签行操作（编辑对话框内）
function addLabelRow() {
  editLabels.value.push({ key: '', value: '' })
}
function removeLabelRow(idx) {
  editLabels.value.splice(idx, 1)
}

// 行内「更多」菜单：忽略 / 恢复 / 彻底删除
function onRowCommand(cmd, row) {
  if (cmd === 'ignore') doIgnore(row)
  else if (cmd === 'restore') doRestore(row)
  else if (cmd === 'purge') doPurge(row)
}

// 忽略：从台账隐藏。采集仍在继续，因此这不是删除，也不会被下一轮上报复活。
async function doIgnore(row) {
  const reason = await ElMessageBox.prompt(
    `${row.name || row.naturalKey}\n\n忽略后该资产从台账与统计中隐藏（采集仍在继续），可随时恢复。`,
    '忽略资产',
    { type: 'warning', confirmButtonText: '忽略', cancelButtonText: '取消', inputPlaceholder: '忽略理由（可选，便于日后理解为什么藏了它）' },
  ).then((r) => r.value).catch(() => null)
  if (reason === null) return
  try {
    await ignoreAsset(row.id, reason || '')
    ElMessage.success('已忽略（可在筛选栏勾选「含已忽略」查看或恢复）')
    await afterWrite(row.id)
  } catch (e) {
    ElMessage.error(e.message || '忽略失败')
  }
}

// 恢复：重新纳入台账
async function doRestore(row) {
  try {
    await restoreAsset(row.id)
    ElMessage.success('已恢复到台账')
    await afterWrite(row.id)
  } catch (e) {
    ElMessage.error(e.message || '恢复失败')
  }
}

// 彻底删除：仅纯人工资产（采集资产会被下一轮上报重建，服务端也会拒绝）
async function doPurge(row) {
  if (!(await confirmWrite(`将彻底删除「${row.name || row.naturalKey}」及其变更历史与快照，删除后不可恢复`))) return
  try {
    await purgeAsset(row.id)
    ElMessage.success('已彻底删除')
    detailVisible.value = false
    await afterWrite(row.id)
  } catch (e) {
    ElMessage.error(e.message || '删除失败')
  }
}

// 列表与摘要共用同一套筛选参数，保证「点数字看到的」与「数字本身」一致
function filterParams() {
  const params = {
    type: filter.value.type,
    status: filter.value.status,
    source: filter.value.source,
    node: filter.value.node,
    keyword: filter.value.keyword,
    label: filter.value.label,
    // ignored 空串=默认隐藏已忽略；with=连已忽略一起看
    ignored: filter.value.ignored,
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
  filter.value = { type: '', status: '', source: '', node: '', keyword: '', label: '', ignored: '' }
  drill.value = { ownerMissing: false, conflict: false, ignored: false }
  reload()
}

// 健康度下钻：直接改筛选条件并回到第一页（改完的态会在筛选行以标签显示）。
// 每次下钻都先清掉其它下钻态：否则"先看无责任人、再点已忽略"会叠加成两个条件，
// 界面上的数字与列表就会对不上，而用户很难发现是残留条件造成的。
function clearDrillState() {
  drill.value = { ownerMissing: false, conflict: false, ignored: false }
  filter.value = { ...filter.value, ignored: '' }
}
function drillAll() {
  resetFilter()
}
function drillStatus(status) {
  clearDrillState()
  filter.value = { ...filter.value, status }
  reload()
}
function drillNoOwner() {
  clearDrillState()
  drill.value.ownerMissing = true
  reload()
}
function drillConflict() {
  clearDrillState()
  drill.value.conflict = true
  reload()
}
// 已忽略下钻：看"我藏起来的那些资产"，可逐个恢复
function drillIgnored() {
  clearDrillState()
  drill.value.ignored = true
  filter.value = { ...filter.value, ignored: 'only' }
  reload()
}
function clearDrill() {
  clearDrillState()
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
  // 标杆列表一并取：抽屉要能回答"本资产是不是该类型的期望值"
  const [hist, rel, bl] = await Promise.all([getAssetHistory(id), getAssetLinks(id), listInspectBaselines()])
  history.value = (hist && hist.records) || []
  applyLinks(rel)
  baselines.value = (bl && bl.baselines) || []
}

// applyLinks 把关联载荷落进状态：可见的边 + 被人工隐藏的边。
// 两者永远一起更新（写接口返回的也是同一个载荷），避免"表格已更新、隐藏区还是旧的"。
function applyLinks(payload) {
  links.value = (payload && payload.links) || []
  suppressed.value = (payload && payload.suppressed) || []
}

async function openDetail(row) {
  detail.value = row
  history.value = []
  links.value = []
  suppressed.value = []
  tab.value = 'attr'
  detailVisible.value = true
  try {
    await loadDetail(row.id)
  } catch (e) {
    ElMessage.error(e.message || '加载资产详情失败')
  }
}

/* ================= 关联关系的人工维护 ================= */

// linkAddress 把一条「读出来的边」翻译成写接口的寻址参数。
//
// 读接口返回的 direction 与 peer 就是写接口需要的全部信息，原样回传即可——
// 前端不自己算方向，也就不会出现"以为是出边、实际删了入边"。
function linkAddress(row) {
  return { toType: row.peerType, toKey: row.peerKey, kind: row.kind, direction: row.direction }
}

function openLinkAdd() {
  linkForm.direction = 'out'
  linkForm.kind = 'runs_on'
  linkForm.peer = ''
  peerOptions.value = []
  linkAddVisible.value = true
}

// searchLinkPeers 搜索对端候选：直接复用台账列表接口。
// 它已按资源范围裁剪，因此这里不必再判断"这个人能不能看到那台资产"。
async function searchLinkPeers(keyword) {
  const kw = (keyword || '').trim()
  if (!kw) {
    peerOptions.value = []
    return
  }
  peerLoading.value = true
  try {
    const resp = await listAssets({ keyword: kw, limit: 20 })
    peerOptions.value = ((resp && resp.assets) || [])
      // 自己跟自己建关系服务端也会拒，从候选里剔掉体验更好
      .filter((a) => String(a.id) !== String(detail.value && detail.value.id))
      .map((a) => ({
        value: a.typeKey + '|' + a.naturalKey,
        label: (a.name || a.naturalKey) + '（' + typeLabel(a) + '）',
      }))
  } catch (e) {
    ElMessage.error(e.message || '搜索资产失败')
  } finally {
    peerLoading.value = false
  }
}

async function submitLinkAdd() {
  if (!linkForm.peer) {
    ElMessage.warning('请选择对端资产')
    return
  }
  // 选项值形如「<类型>|<自然键>」；自然键本身含冒号（如 redis:127.0.0.1:6379），
  // 因此只按**第一个**分隔符切开，而不是 split 后取前两段。
  const sep = linkForm.peer.indexOf('|')
  linkBusy.value = true
  try {
    applyLinks(await createAssetLink(detail.value.id, {
      toType: linkForm.peer.slice(0, sep),
      toKey: linkForm.peer.slice(sep + 1),
      kind: linkForm.kind,
      direction: linkForm.direction,
    }))
    linkAddVisible.value = false
    ElMessage.success('已添加关系')
  } catch (e) {
    ElMessage.error(e.message || '添加关系失败')
  } finally {
    linkBusy.value = false
  }
}

async function removeLink(row) {
  // 解除 = 逻辑删除：服务端会记一条抑制，采集不会再把它建回来。
  // 这不是"少显示一行"，而是表达了「这条关系不存在」这个判断，所以先把后果说清楚。
  try {
    await ElMessageBox.confirm(
      '解除后采集不会再自动建立这条关系（服务端会记一条抑制记录，可在「已人工隐藏」里恢复）。确认解除？',
      '解除关系',
      { type: 'warning', confirmButtonText: '解除', cancelButtonText: '取消' }
    )
  } catch {
    return
  }
  try {
    applyLinks(await deleteAssetLink(detail.value.id, linkAddress(row)))
    ElMessage.success('已解除，可在「已人工隐藏」里恢复')
  } catch (e) {
    ElMessage.error(e.message || '解除关系失败')
  }
}

async function restoreLink(row) {
  try {
    applyLinks(await restoreAssetLink(detail.value.id, linkAddress(row)))
    ElMessage.success('已恢复：这条关系交还给采集')
  } catch (e) {
    ElMessage.error(e.message || '恢复关系失败')
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
  // 标签：预填当前值，并在提交时对比出"被删掉的键"（不需要额外的删除态）
  editOriginalLabels.value = { ...(row.labels || {}) }
  editLabels.value = Object.entries(row.labels || {}).map(([key, value]) => ({ key, value }))
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

  // 标签：本次表单里的键值；原标签里不在其中的键就是要删除的
  const labels = {}
  for (const row of editLabels.value) {
    const key = (row.key || '').trim()
    if (key) labels[key] = (row.value || '').trim()
  }
  const remove = Object.keys(editOriginalLabels.value).filter((k) => !(k in labels))
  const labelsChanged = labelFingerprint(labels) !== labelFingerprint(editOriginalLabels.value)

  if (!nameChanged && !labelsChanged && !Object.keys(attrs).length && !resetAttrs.length) {
    ElMessage.warning('没有需要更新的内容')
    return
  }
  const what = []
  if (nameChanged) what.push(`资产名称改为「${name}」`)
  if (labelsChanged) what.push('更新标签')
  if (Object.keys(attrs).length || resetAttrs.length) what.push('写入人工值')
  if (!(await confirmWrite(`将${what.join('、')}；采集值不会被覆盖`))) return
  saving.value = true
  try {
    // 名称与人工值走台账接口；标签有独立接口（职责分开：属性 vs 分类维度）
    if (nameChanged || Object.keys(attrs).length || resetAttrs.length) {
      await updateAsset(id, { name: nameChanged ? name : '', attrs, resetAttrs })
    }
    if (labelsChanged) await updateAssetLabels(id, labels, remove)
    ElMessage.success('已保存')
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

// 把本资产的当前配置设为该资产类型的期望值（标杆）：配置巡检的合规偏差以它为基准。
async function makeBaseline() {
  const d = detail.value
  if (!(await confirmWrite(`将把 ${d.name || d.naturalKey} 的当前配置设为「${typeLabel(d)}」类型的期望值（每个类型只保留一个）`))) return
  try {
    await setAssetBaseline(d.id)
    ElMessage.success('已设为期望值')
    await afterWrite(d.id)
  } catch (e) {
    ElMessage.error(e.message || '设置期望值失败')
  }
}

// 清除该资产所属类型的期望值：此后只做快照前后比对，不再产出合规偏差。
async function dropBaseline() {
  const d = detail.value
  if (!(await confirmWrite(`将清除「${typeLabel(d)}」类型的期望值，此后不再做合规偏差比对`))) return
  try {
    await clearAssetBaseline(d.id)
    ElMessage.success('已清除期望值')
    await afterWrite(d.id)
  } catch (e) {
    ElMessage.error(e.message || '清除期望值失败')
  }
}

/* ================= 批量维护与清单导出 ================= */

const canExport = computed(() => auth.can('assets:export'))
const exporting = ref(false)
const selection = ref([])
const tableRef = ref(null)
const batchBusy = ref(false)
const batchResult = ref(null)
const batchResultVisible = ref(false)
const batchOwner = reactive({ visible: false, owner: '' })
const batchLabel = reactive({ visible: false, key: '', value: '', mode: 'set' })

function onSelectionChange(rows) {
  selection.value = rows
}
function clearSelection() {
  selection.value = []
  if (tableRef.value) tableRef.value.clearSelection()
}
// 点复选框列只应改变选中态，不该顺带打开详情抽屉（用户是在选行，不是在"看这一条"）
function onRowClick(row, column) {
  if (column && column.type === 'selection') return
  openDetail(row)
}

const failedItems = computed(() => (batchResult.value ? batchResult.value.items.filter((i) => !i.ok) : []))
const batchTitle = computed(() => (batchResult.value ? `批量${batchResult.value.opLabel}结果` : '批量维护结果'))
const batchResultHint = computed(() => {
  const r = batchResult.value
  if (!r) return ''
  if (r.ok === 0) return r.message || '没有任何一条被更新，原因见下方。'
  if (r.failed === 0) return '全部成功。'
  return '部分成功：未成功的给出了具体原因；资源范围外的资产按「不存在」处理。'
})

// runBatch 是所有批量动作的统一出口：结果面板、部分成功的呈现、清空选中与刷新列表
// 只写一遍。各动作自己拼一遍的话，迟早在某个分支上漏掉"刷新列表"或"清空选中"。
async function runBatch(op, payload, actionLabel) {
  const ids = selection.value.map((r) => r.id)
  if (!ids.length) return
  batchBusy.value = true
  try {
    const { status, body } = await batchAssets({ ids, op, ...payload })
    // 没有 batch 字段 = 请求本身就没被接受（旧服务端 405、网关 404 之类），
    // 这时**不能**弹"逐条结论"面板：面板是给"每条改没改成"用的，
    // 用它去呈现"整个请求失败了"只会显示成"成功 0 条、失败 0 条"这种自相矛盾的话。
    if (!body || !body.batch) {
      ElMessage.error((body && body.error) || `批量维护未被执行（HTTP ${status}）：请确认服务端已升级到支持批量接口的版本`)
      return
    }
    const b = body.batch
    batchResult.value = {
      opLabel: b.opLabel || actionLabel,
      total: b.total || ids.length,
      ok: b.ok || 0,
      failed: b.failed || 0,
      items: b.items || [],
      message: (body && body.error) || '',
    }
    // 409（一条都没成功）也照常弹面板：items 里的逐条原因才是用户要看的东西
    batchResultVisible.value = true
    if (status >= 500) ElMessage.error('服务端处理失败，请稍后重试')
    clearSelection()
    await load()
  } catch (e) {
    ElMessage.error(e.message || '批量维护失败')
  } finally {
    batchBusy.value = false
  }
}

function openBatchOwner() {
  batchOwner.owner = ''
  batchOwner.visible = true
}
async function submitBatchOwner(clear) {
  const n = selection.value.length
  if (!n) return
  const owner = batchOwner.owner.trim()
  if (!clear && !owner) {
    ElMessage.warning('请填写责任人；要清空请点「清空责任人」')
    return
  }
  const what = clear ? `清空 ${n} 条资产的责任人` : `把 ${n} 条资产的责任人统一改为「${owner}」`
  if (!(await confirmWrite(`${what}；写入的是人工值，采集值不受影响`))) return
  batchOwner.visible = false
  await runBatch(clear ? 'ownerClear' : 'owner', clear ? {} : { owner }, clear ? '清除责任人' : '转派责任人')
}

function openBatchLabel() {
  batchLabel.key = ''
  batchLabel.value = ''
  batchLabel.mode = 'set'
  batchLabel.visible = true
}
async function submitBatchLabel() {
  const key = batchLabel.key.trim()
  if (!key) {
    ElMessage.warning('标签键不能为空')
    return
  }
  const n = selection.value.length
  const value = batchLabel.value.trim()
  const what =
    batchLabel.mode === 'set'
      ? `给 ${n} 条资产写入标签 ${key}${value ? '=' + value : ''}（同名标签会被覆盖）`
      : `从 ${n} 条资产上删除标签「${key}」`
  if (!(await confirmWrite(what))) return
  batchLabel.visible = false
  await runBatch('labels', batchLabel.mode === 'set' ? { labels: { [key]: value } } : { remove: [key] }, '打标签')
}

async function batchIgnore() {
  const n = selection.value.length
  if (!n) return
  const reason = await ElMessageBox.prompt(
    `把选中的 ${n} 条资产从台账隐藏（忽略）：采集仍在继续、可随时恢复，列表与统计默认不再计入。`,
    '批量忽略',
    {
      type: 'warning',
      confirmButtonText: '忽略',
      cancelButtonText: '取消',
      inputPlaceholder: '忽略理由（可选，便于日后理解为什么藏了它们）',
    },
  )
    .then((r) => r.value)
    .catch(() => null)
  if (reason === null) return
  await runBatch('ignore', { reason: reason || '' }, '忽略')
}

async function batchRestore() {
  const n = selection.value.length
  if (!n) return
  if (!(await confirmWrite(`把选中的 ${n} 条资产恢复到台账（解除忽略）`))) return
  await runBatch('restore', {}, '恢复')
}

// 导出清单：走服务端导出（与列表同一套筛选、不受分页限制），
// 因此导出的就是"筛出来的那一份"，而不是当前这一页。
async function doExport() {
  if (!(await confirmWrite('导出当前筛选命中的全部资产（不受分页限制，含责任人、标签与归属节点），以 CSV 下载'))) {
    return
  }
  exporting.value = true
  try {
    const name = await exportAssets(filterParams())
    ElMessage.success(`已导出 ${name}`)
  } catch (e) {
    ElMessage.error(e.message || '导出失败')
  } finally {
    exporting.value = false
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

/* ===== 跨页联动（容器页 ⇄ 台账）===== */

// openFromQuery 处理 ?id=<资产 ID>：容器页的「台账」按钮就是这么跳过来的。
//
// 只认领一次（消费后清掉 query）：否则用户关掉抽屉再刷新页面，抽屉会自己又弹出来。
async function openFromQuery() {
  const id = String(route.query.id || '').trim()
  if (!id) return
  router.replace({ path: '/assets', query: {} })
  try {
    const item = await getAsset(id)
    if (item && item.id) openDetail(item)
  } catch (e) {
    // 范围外与已删除在这里是同一句话：不给范围探测留信息（与服务端的 404 口径一致）
    ElMessage.warning('未找到该资产：可能已被删除，或不在你的资源范围内')
  }
}

// gotoContainer 跳到容器页看这个 Pod / 工作负载。
//
// 身份取自服务端解出的 container 字段（不是前端 split 自然键）：集群是 apiserver 地址
// （含 `://`），自己解析一次就可能解错，而解错的联动不报错、只会跳到别的对象上。
function gotoContainer(item) {
  const c = item && item.container
  if (!c) return
  const query = { cluster: c.cluster, namespace: c.namespace }
  if (item.typeKey === 'pod') {
    query.pod = c.name
  } else {
    // 工作负载没有单一日志可看：只定位到集群 + 命名空间，并切到工作负载 Tab
    query.tab = 'workloads'
  }
  router.push({ path: '/container', query })
}

/* ================= 关系图（拓扑） ================= */

const topoVisible = ref(false)
const topoError = ref('')
const topo = ref(null)
const topoDepth = ref(2)
const topoChartRef = ref(null)
let topoChart = null

const topoTitle = computed(() => {
  const root = topo.value && (topo.value.nodes || []).find((n) => n.root)
  return root ? `关系图 · ${root.name || root.naturalKey}` : '关系图'
})
// 截断必须说出来：静默省略会让人以为"关系就这么多"，从而漏掉真实的影响面——
// 那正是这张图存在的意义。
const topoTruncated = computed(() => !!(topo.value && topo.value.truncated))

// 类型 → 颜色。与图例共用同一份定义，避免"图上是蓝的、图例说是绿的"。
const TOPO_COLORS = {
  host: '#4a9df0',
  'middleware-instance': '#00d9a3',
  pod: '#e6a23c',
  workload: '#8b5cf6',
}

// openTopology 打开关系图（默认以当前详情抽屉里的资产为中心）。
function openTopology() {
  if (!detail.value) return
  topoVisible.value = true
  loadTopology(detail.value.id)
}

// loadTopology 取某资产的邻域并重绘。点节点也走这里（换中心）。
async function loadTopology(id) {
  topoError.value = ''
  try {
    topo.value = await getAssetTopology(id, { depth: topoDepth.value })
  } catch (e) {
    topo.value = null
    topoError.value = e.message || '加载关系图失败'
  }
  await nextTick()
  renderTopology()
}

// renderTopology 把邻域画成力导向图。节点大小按跳数（中心最大）、
// 颜色按类型、红圈表示失联；边用虚线区分人工维护。
function renderTopology() {
  const data = topo.value
  if (!data || !topoChartRef.value) return
  if (!topoChart) topoChart = echarts.init(topoChartRef.value)
  const nodes = (data.nodes || []).map((n) => ({
    id: n.key,
    name: n.name || n.naturalKey,
    symbolSize: n.root ? 52 : n.depth === 1 ? 38 : 28,
    itemStyle: {
      color: TOPO_COLORS[n.typeKey] || '#909399',
      // 已从台账隐藏的资产画成半透明：它仍在关系里，但已不是"要治理的对象"
      opacity: n.ignored ? 0.45 : 1,
      borderColor: n.status === 'online' ? 'transparent' : '#f56c6c',
      borderWidth: n.status === 'online' ? 0 : 2,
    },
    // 原始节点挂在 data 上：tooltip 与点击回调都要用
    raw: n,
    label: { show: true, fontSize: 11 },
  }))
  const links = (data.edges || []).map((e) => ({
    source: e.from,
    target: e.to,
    raw: e,
    lineStyle: { type: e.source === 'manual' ? 'dashed' : 'solid', width: 1.4, color: '#9aa4b2' },
    label: { show: true, formatter: e.kind, fontSize: 10, color: '#8a94a6' },
  }))

  topoChart.setOption(
    {
      tooltip: {
        formatter: (p) => {
          if (p.dataType === 'edge') {
            const e = p.data.raw || {}
            return `${e.kind}<br/>${e.from}<br/>↓<br/>${e.to}<br/>来源：${e.source === 'manual' ? '人工维护' : '采集发现'}`
          }
          const n = (p.data && p.data.raw) || {}
          const where = n.depth ? `距中心 ${n.depth} 跳` : '（中心）'
          return `${n.typeTitle || ''}：${n.name || n.naturalKey}<br/>归属节点：${n.node || '—'}<br/>状态：${statusLabel(n.status)}<br/>${where}`
        },
      },
      series: [
        {
          type: 'graph',
          layout: 'force',
          roam: true,
          draggable: true,
          data: nodes,
          links,
          // 力导向参数刻意收敛：关系图节点数不多，斥力太大只会散成一团看不清
          force: { repulsion: 320, edgeLength: 130, gravity: 0.08 },
          emphasis: { focus: 'adjacency' },
          edgeSymbol: ['none', 'arrow'],
          edgeSymbolSize: 7,
        },
      ],
    },
    true
  )
  topoChart.off('click')
  topoChart.on('click', (p) => {
    const n = p && p.data && p.data.raw
    if (!n || n.root) return
    loadTopology(n.id)
  })
}

function disposeChart() {
  if (topoChart) {
    topoChart.dispose()
    topoChart = null
  }
}
onBeforeUnmount(disposeChart)

onMounted(async () => {
  await load()
  await openFromQuery()
})
</script>

<style scoped>
/* 页头统一走 PageHeader；内容边距交给 MainLayout 的 .content，各页不再自带缩进 */
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
  border-radius: var(--r-md);
  background: var(--fill-1);
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
  background: var(--fill-2);
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
/* 标签：分类维度的展示（列表名称下与抽屉头部各一处） */
.labels {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-top: 4px;
}
.label-chip {
  display: inline-block;
  padding: 0 6px;
  border-radius: 4px;
  background: rgba(127, 127, 127, 0.14);
  color: var(--text-dim);
  font-size: 11.5px;
  line-height: 16px;
  font-family: var(--mono);
}
/* 已忽略标记：只在「含已忽略」视图里出现，必须显眼 */
.tag-ignored {
  display: inline-block;
  margin-left: 6px;
  padding: 0 6px;
  border: 1px solid var(--bd-strong);
  border-radius: 3px;
  color: var(--text-dim);
  font-size: 11px;
  line-height: 15px;
  vertical-align: 1px;
}
.d-labels {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  margin: 10px 0 4px;
}
.d-labels-title {
  font-size: 13px;
  color: var(--text-dim);
  margin-right: 2px;
}
.d-labels-edit {
  margin-left: auto;
}
.label-hint {
  line-height: 18px;
  margin-bottom: 6px;
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
  background: var(--fill-2);
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
/* 批量结果面板：与节点操作页同一形态（分节标题 + 逐条一行）。
   scoped 样式不跨组件，因此 .warn-text 这类小类必须在本页也定义一份。 */
.batch-sec {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  margin: 14px 0 6px;
  font-size: 13px;
}
.batch-row {
  display: flex;
  gap: 10px;
  align-items: baseline;
  padding: 3px 0;
  font-size: 13px;
}
.batch-name {
  flex: none;
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.warn-text {
  color: var(--warn);
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
  background: var(--fill-2);
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
/* 「来源 = 人工」用强调色标出：被人工认领过的边不会被采集覆盖，
   这是关系表里最需要一眼看清的属性（其余边都是采集自动建立的）。 */
.src-manual {
  color: var(--accent);
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
/* 关系图：工具条（跳数 + 图例）与画布 */
.topo-bar {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 10px;
}
.topo-legend {
  margin-left: auto;
}
.topo-chart {
  width: 100%;
  height: 520px;
}
</style>
