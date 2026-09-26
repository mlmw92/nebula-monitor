<template>
  <div>
    <!-- 告警统计看板 -->
    <div class="glass panel" style="margin-bottom: 16px">
      <div class="panel-title" style="margin-bottom: 12px">告警概览</div>
      <div v-if="statsError" class="alert-refresh-error">{{ statsError }}</div>
      <div class="alert-stats" v-loading="statsLoading">
        <div class="glass panel kpi">
          <div class="kpi-label">活跃告警</div>
          <div class="kpi-value red">{{ stats.firing }}</div>
        </div>
        <div class="glass panel kpi">
          <div class="kpi-label">紧急</div>
          <div class="kpi-value red">{{ stats.bySeverity.critical }}</div>
        </div>
        <div class="glass panel kpi">
          <div class="kpi-label">警告</div>
          <div class="kpi-value amber">{{ stats.bySeverity.warning }}</div>
        </div>
        <div class="glass panel kpi">
          <div class="kpi-label">信息</div>
          <div class="kpi-value cyan">{{ stats.bySeverity.info }}</div>
        </div>
        <div class="glass panel kpi">
          <div class="kpi-label">已抑制</div>
          <div class="kpi-value gray">{{ stats.suppressed }}</div>
        </div>
        <div class="glass panel kpi">
          <div class="kpi-label">24h 事件</div>
          <div class="kpi-value cyan">{{ stats.total }}</div>
        </div>
      </div>
    </div>

    <!-- 维护窗口 -->
    <div class="glass panel" style="margin-bottom: 16px">
      <div class="panel-title-row">
        <span class="panel-title" style="margin-bottom: 0">维护窗口</span>
        <el-switch
          v-model="maintenance.enabled"
          active-text="已开启"
          inactive-text="已关闭"
          @change="saveMaintenance"
        />
      </div>
      <template v-if="maintenance.enabled">
        <div class="maintenance-row">
          <div class="maintenance-item">
            <span class="maintenance-label">开始时间</span>
            <el-date-picker
              v-model="maintenanceStart"
              type="datetime"
              placeholder="选择开始时间"
              format="YYYY-MM-DD HH:mm"
              value-format="x"
              @change="saveMaintenance"
            />
          </div>
          <div class="maintenance-item">
            <span class="maintenance-label">结束时间</span>
            <el-date-picker
              v-model="maintenanceEnd"
              type="datetime"
              placeholder="选择结束时间"
              format="YYYY-MM-DD HH:mm"
              value-format="x"
              @change="saveMaintenance"
            />
          </div>
          <div class="maintenance-item" style="flex: 1">
            <span class="maintenance-label">原因</span>
            <el-input v-model="maintenance.reason" placeholder="如：版本升级、数据迁移..." @blur="saveMaintenance" />
          </div>
        </div>
        <div class="maintenance-hint">维护窗口期间，所有告警通知将被抑制，告警事件仍会正常记录</div>
      </template>
    </div>

    <!-- 告警事件：概览与维护状态之后优先展示，便于快速处置 -->
    <div class="glass panel" style="margin-bottom: 16px">
      <div v-if="refreshError" class="alert-refresh-error">{{ refreshError }}</div>
      <div class="panel-title-row">
        <span class="panel-title" style="margin-bottom: 0">告警事件</span>
        <div class="event-toolbar">
          <el-radio-group v-model="eventFilter" size="small">
            <el-radio-button value="firing">活跃</el-radio-button>
            <el-radio-button value="resolved">已恢复</el-radio-button>
            <el-radio-button value="">全部</el-radio-button>
          </el-radio-group>
          <el-button size="small" :disabled="!selected.length" @click="batchAck">批量确认 ({{ selected.length }})</el-button>
          <span class="muted event-toolbar-hint">确认后将从活跃列表移除，仍可在“全部”中查看</span>
          <el-button size="small" :loading="testing" @click="testAlert">测试事件</el-button>
        </div>
      </div>
      <el-table
        :data="pagedAlerts"
        stripe
        style="width: 100%"
        empty-text="暂无告警事件"
        @selection-change="onSelect"
        @row-dblclick="openDetail"
      >
        <el-table-column type="selection" width="45" :selectable="selectableAlert" />
        <el-table-column prop="ruleName" label="规则" min-width="140" />
        <el-table-column prop="node" label="节点" min-width="130" />
        <el-table-column label="级别" width="80">
          <template #default="{ row }">
            <el-tag :type="sevType(row.severity)" size="small" effect="dark">{{ sevLabel(row.severity) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="120">
          <template #default="{ row }">
            <el-tag
              :type="ackTagType(row)"
              size="small"
              :effect="row.state === 'firing' && ackStatus(row) === 'pending' ? 'dark' : 'plain'"
            >
              {{ ackStatusLabel(row) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="抑制" width="90">
          <template #default="{ row }">
            <el-tag v-if="row.suppressed" type="info" size="small" effect="plain">已抑制</el-tag>
            <span v-else class="muted">—</span>
          </template>
        </el-table-column>
        <el-table-column prop="message" label="详情" min-width="200" show-overflow-tooltip />
        <el-table-column label="时间" width="160">
          <template #default="{ row }">{{ fmt(row.startsAt || row.endsAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="80">
          <template #default="{ row }">
            <el-button link size="small" @click="openDetail(row)">详情</el-button>
          </template>
        </el-table-column>
      </el-table>
      <div style="margin-top: 12px; display: flex; justify-content: flex-end">
        <el-pagination
          v-model:current-page="evCurrentPage"
          v-model:page-size="evPageSize"
          :total="filteredAlerts.length"
          :page-sizes="[10, 20, 50, 100]"
          layout="total, sizes, prev, pager, next, jumper"
          background
        />
      </div>
    </div>

    <!-- 告警规则 -->
    <div class="glass panel" style="margin-bottom: 16px">
      <div class="panel-title-row">
        <span class="panel-title" style="margin-bottom: 0">告警规则</span>
        <div style="display: flex; gap: 8px; align-items: center">
          <el-button size="small" @click="exportRules">导出</el-button>
          <el-button size="small" @click="fileInput.click()">导入</el-button>
          <input ref="fileInput" type="file" accept="application/json,.json" style="display: none" @change="onFileChange" />
          <el-dropdown split-button type="primary" size="small" @command="onTemplateCmd">
          <span @click="newRule">新建规则</span>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item command="__blank">空白规则</el-dropdown-item>
              <el-dropdown-item v-for="t in templates" :key="t.name" :command="t.name">{{ t.name }}</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
        </div>
      </div>
      <el-alert
        v-if="ruleLoadError"
        type="error"
        :closable="false"
        show-icon
        class="rule-load-error"
        :title="'告警规则加载失败：' + ruleLoadError"
      />
      <div class="rule-filter-bar" style="display: flex; gap: 8px; align-items: center; margin-bottom: 12px; flex-wrap: wrap">
        <el-input
          v-model="ruleSearch"
          placeholder="搜索规则名称"
          clearable
          size="small"
          style="width: 220px"
        />
        <el-select
          v-model="ruleTypeFilter"
          placeholder="全部类型"
          clearable
          size="small"
          style="width: 150px"
        >
          <el-option label="阈值" value="" />
          <el-option label="主机离线" value="node_offline" />
          <el-option label="服务离线" value="service_down" />
          <el-option label="主从切换" value="role_change" />
          <el-option label="集群损坏" value="cluster_fault" />
          <el-option label="安全事件" value="security_event" />
        </el-select>
        <span class="muted" style="font-size: 13px">共 {{ filteredRules.length }} 条</span>
      </div>
      <el-table :data="pagedRules" stripe style="width: 100%" empty-text="暂无规则">
        <el-table-column prop="name" label="名称" min-width="140" />
        <el-table-column label="类型" width="120">
          <template #default="{ row }">
            <el-tag :type="typeTag(row)" size="small" effect="plain">{{ typeLabel(row) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="触发条件" min-width="200">
          <template #default="{ row }">
            <span class="mono">{{ conditionText(row) }}</span>
            <div v-if="row.quietPeriods && row.quietPeriods.length" class="muted" style="font-size: 13px">
              静默时段 {{ row.quietPeriods.length }} 个
            </div>
            <div v-if="row.escalation && row.escalation.enabled" class="muted" style="font-size: 13px">
              升级 {{ row.escalation.afterMinutes }}m{{ row.escalation.toSeverity ? '→' + sevLabel(row.escalation.toSeverity) : '' }}
            </div>
          </template>
        </el-table-column>
        <el-table-column label="级别" width="90">
          <template #default="{ row }">
            <el-tag :type="sevType(row.severity)" size="small" effect="dark">{{ sevLabel(row.severity) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="通知渠道" min-width="150">
          <template #default="{ row }">
            <template v-if="row.notify && row.notify.length">
              <el-tag v-for="c in row.notify" :key="c" size="small" style="margin: 0 4px 4px 0">{{ channelLabel(c) }}</el-tag>
            </template>
            <span v-else class="muted">仅平台展示</span>
          </template>
        </el-table-column>
        <el-table-column label="应用范围" min-width="140">
          <template #default="{ row }">
            <template v-if="row.scope === 'specified'">
              <el-tag type="warning" size="small">指定主机</el-tag>
              <span class="scope-count">{{ (row.nodes || []).length }} 台</span>
            </template>
            <el-tag v-else type="success" size="small">全部主机</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="持续" width="80">
          <template #default="{ row }">{{ row.for === '0' ? '立即' : row.for }}</template>
        </el-table-column>
        <el-table-column label="启用" width="80" align="center">
          <template #default="{ row }">
            <el-switch
              v-model="row.enabled"
              @change="toggleRule(row)"
            />
          </template>
        </el-table-column>
        <el-table-column label="静默" width="80" align="center">
          <template #default="{ row }">
            <el-switch
              v-model="row.silenced"
              @change="toggleSilence(row)"
            />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="130">
          <template #default="{ row }">
            <el-button link size="small" @click="edit(row)">编辑</el-button>
            <el-button link type="danger" size="small" @click="del(row.id)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
      <div style="margin-top: 12px; display: flex; justify-content: flex-end">
        <el-pagination
          v-model:current-page="currentPage"
          v-model:page-size="pageSize"
          :total="filteredRules.length"
          :page-sizes="[10, 20, 50, 100]"
          layout="total, sizes, prev, pager, next, jumper"
          background
        />
      </div>
    </div>

    <!-- 告警规则导入 -->
    <el-dialog v-model="importDialog" title="导入告警规则" width="440px">
      <div v-if="pendingImport">
        <p>文件：<b>{{ pendingImport.name }}</b>，共 {{ pendingImport.list.length }} 条规则。</p>
        <el-radio-group v-model="importMode" style="display: flex; flex-direction: column; gap: 8px; margin-top: 8px">
          <el-radio label="merge">合并导入（按 ID 更新已有规则、新增没有的规则）</el-radio>
          <el-radio label="replace">覆盖导入（先清空当前所有规则再导入）</el-radio>
        </el-radio-group>
        <p class="muted" style="font-size: 13px; margin-top: 10px">
          覆盖导入会删除当前全部规则，请谨慎操作。
        </p>
      </div>
      <template #footer>
        <el-button @click="importDialog = false">取消</el-button>
        <el-button type="primary" @click="confirmImport">开始导入</el-button>
      </template>
    </el-dialog>

    <!-- 高级：抑制与分组（P4） -->
    <div class="glass panel" style="margin-bottom: 16px">
      <div class="panel-title" style="margin-bottom: 12px">高级设置 · 抑制与分组</div>

      <div class="adv-section">
        <div class="adv-title">告警分组</div>
        <div class="adv-row">
          <el-switch v-model="grouping.enabled" active-text="已开启" inactive-text="已关闭" />
          <span class="muted">将相同标签的告警合并为一组，按等待/间隔汇总发送，减少通知风暴</span>
        </div>
        <div class="adv-row" v-if="grouping.enabled">
          <div class="adv-item">
            <span class="adv-label">分组标签</span>
            <el-select v-model="grouping.groupBy" class="group-by-select" multiple collapse-tags tooltip-effect="dark" placeholder="选择分组标签">
              <el-option label="规则名" value="name" />
              <el-option label="规则ID" value="rule" />
              <el-option label="节点" value="node" />
              <el-option label="实例" value="instance" />
              <el-option label="级别" value="severity" />
              <el-option label="指标" value="metric" />
            </el-select>
          </div>
          <div class="adv-item">
            <span class="adv-label">首次等待</span>
            <el-input v-model="grouping.groupWait" placeholder="30s" style="width: 110px" />
          </div>
          <div class="adv-item">
            <span class="adv-label">汇总间隔</span>
            <el-input v-model="grouping.groupInterval" placeholder="5m" style="width: 110px" />
          </div>
          <el-button type="primary" size="small" @click="saveGrouping">保存</el-button>
        </div>

        <!-- D1 风暴收敛：解决「一条通知上百行明细」 -->
        <div class="adv-row" v-if="grouping.enabled">
          <el-switch v-model="grouping.converge" active-text="风暴收敛" inactive-text="风暴收敛" />
          <span class="muted">默认开启：同规则多节点同时告警时合并为「头部告警 + 摘要 + Top N」，避免通知正文被明细淹没</span>
        </div>
        <div class="adv-row" v-if="grouping.enabled && grouping.converge">
          <div class="adv-item">
            <span class="adv-label">收敛维度</span>
            <el-select v-model="grouping.convergeBy" class="group-by-select" multiple collapse-tags tooltip-effect="dark" placeholder="选择收敛维度">
              <el-option label="规则ID" value="rule" />
              <el-option label="级别" value="severity" />
              <el-option label="规则名" value="name" />
              <el-option label="指标" value="metric" />
            </el-select>
          </div>
          <div class="adv-item">
            <span class="adv-label">收敛窗口</span>
            <el-input v-model="grouping.convergeWindow" placeholder="10m" style="width: 110px" />
          </div>
          <div class="adv-item">
            <span class="adv-label">明细条数</span>
            <el-input-number v-model="grouping.headCount" :min="1" :max="20" size="small" controls-position="right" />
          </div>
          <span class="muted">开启后按收敛维度聚合，不再按分组标签细分</span>
        </div>
      </div>

      <el-divider />

      <div class="adv-section">
        <div class="panel-title-row">
          <span class="adv-title" style="margin: 0">抑制规则</span>
          <el-button size="small" type="primary" @click="newInhibit">新增规则</el-button>
        </div>
        <el-table :data="inhibits" stripe style="width: 100%; margin-top: 8px" empty-text="暂无抑制规则">
          <el-table-column label="源匹配（触发时）" min-width="200">
            <template #default="{ row }">{{ inhibitText(row.source) }}</template>
          </el-table-column>
          <el-table-column label="目标匹配（被抑制）" min-width="200">
            <template #default="{ row }">{{ inhibitText(row.target) }}</template>
          </el-table-column>
          <el-table-column label="Equal" min-width="150">
            <template #default="{ row }">
              <el-tag v-for="k in (row.equal || [])" :key="k" size="small" style="margin: 0 4px 4px 0">{{ k }}</el-tag>
              <span v-if="!row.equal || !row.equal.length" class="muted">—</span>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="130">
            <template #default="{ row, $index }">
              <el-button link size="small" @click="editInhibit(row, $index)">编辑</el-button>
              <el-button link type="danger" size="small" @click="delInhibit($index)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>
    </div>

    <RuleModal v-if="editing" :rule="editing" :groups="groups" :channels="channelOptions" @close="editing = null" @saved="onSaved" />

    <!-- 抑制规则编辑 -->
    <el-dialog v-model="inhibitDialog" :title="inhibitEditIndex < 0 ? '新增抑制规则' : '编辑抑制规则'" width="620px">
      <div class="inh-block">
        <div class="inh-title">源匹配（当该告警处于告警中时）</div>
        <div class="inh-row"><span class="inh-label">规则ID 包含</span><el-input v-model="inhibitForm.sourceRule" placeholder="如 host-offline，留空不限" /></div>
        <div class="inh-row"><span class="inh-label">级别</span>
          <el-select v-model="inhibitForm.sourceSeverity" clearable placeholder="不限">
            <el-option label="紧急" value="critical" /><el-option label="警告" value="warning" /><el-option label="信息" value="info" />
          </el-select>
        </div>
        <div class="inh-row"><span class="inh-label">指标正则</span><el-input v-model="inhibitForm.sourceMetricRegex" placeholder="如 .* 或 cpu.*，留空不限" /></div>
      </div>
      <div class="inh-block">
        <div class="inh-title">目标匹配（将被抑制的告警）</div>
        <div class="inh-row"><span class="inh-label">级别</span>
          <el-select v-model="inhibitForm.targetSeverity" clearable placeholder="不限">
            <el-option label="紧急" value="critical" /><el-option label="警告" value="warning" /><el-option label="信息" value="info" />
          </el-select>
        </div>
        <div class="inh-row"><span class="inh-label">指标正则</span><el-input v-model="inhibitForm.targetMetricRegex" placeholder="如 .* 或 mem.*，留空不限" /></div>
      </div>
      <div class="inh-row"><span class="inh-label">Equal 标签</span>
        <el-select v-model="inhibitForm.equal" multiple collapse-tags placeholder="需相同的标签">
          <el-option label="节点" value="node" /><el-option label="实例" value="instance" /><el-option label="级别" value="severity" /><el-option label="规则ID" value="rule" />
        </el-select>
      </div>
      <template #footer>
        <el-button @click="inhibitDialog = false">取消</el-button>
        <el-button type="primary" @click="saveInhibit">保存</el-button>
      </template>
    </el-dialog>

    <!-- 告警事件详情 -->
    <el-drawer v-model="drawer" :title="detail?.ruleName || '告警详情'" size="480px" @closed="onDrawerClosed">
      <template v-if="detail">
        <div class="ev-field"><span>级别</span><el-tag :type="sevType(detail.severity)" effect="dark">{{ sevLabel(detail.severity) }}</el-tag></div>
        <div class="ev-field"><span>状态</span>{{ stateLabel(detail) }}</div>
        <div class="ev-field">
          <span>节点</span>
          <span>
            <el-link type="primary" @click="gotoNode(detail)">{{ detail.node }}</el-link>
            <span class="muted" v-if="detail.nodeIp"> ({{ detail.nodeIp }})</span>
          </span>
        </div>
        <div class="ev-field">
          <span>处置</span>
          <span>
            <el-tag :type="ackTagType(detail)" size="small" effect="plain">{{ ackStatusLabel(detail) }}</el-tag>
            <span v-if="ackInfo(detail)?.assignee" class="muted"> · 处理人 {{ ackInfo(detail).assignee }}</span>
          </span>
        </div>
        <div class="ev-field" v-if="ackInfo(detail)?.closeReason">
          <span>关闭原因</span><span>{{ ackInfo(detail).closeReason }}</span>
        </div>
        <div class="ev-field"><span>触发条件</span><span class="mono">{{ detail.metric }} {{ detail.operator }} {{ detail.threshold }}</span></div>
        <div class="ev-field"><span>触发值</span><span class="mono">{{ detail.value }}</span></div>
        <div class="ev-field"><span>详情</span><span>{{ detail.message }}</span></div>
        <div class="ev-field"><span>开始</span><span>{{ fmt(detail.startsAt) }}</span></div>
        <div class="ev-field"><span>恢复</span><span>{{ detail.state === 'resolved' ? fmt(detail.endsAt) : '—' }}</span></div>
        <div class="ev-chart-title" v-if="detail.metric">触发指标近 1 小时趋势</div>
        <div class="ev-chart" ref="chartRef" v-if="detail.metric"></div>

        <div class="ev-chart-title">处置记录</div>
        <div class="collab-timeline">
          <div v-for="(c, i) in ackInfo(detail)?.comments || []" :key="i" class="collab-item">
            <div class="collab-head">
              <span class="collab-user">{{ c.user }}</span>
              <span class="collab-time">{{ fmt(c.time) }}</span>
            </div>
            <div class="collab-text">{{ c.text }}</div>
          </div>
          <div v-if="!(ackInfo(detail)?.comments || []).length" class="muted">暂无处置记录</div>
        </div>
        <div class="collab-input">
          <el-input
            v-model="commentText"
            type="textarea"
            :rows="2"
            maxlength="2000"
            show-word-limit
            placeholder="补充处置说明（如「已扩容」「误报，已调整阈值」）…"
          />
        </div>
        <div class="ev-actions">
          <el-button
            v-if="detail.state === 'firing' && ackStatus(detail) === 'pending'"
            type="primary"
            size="small"
            @click="ackEvent(detail)"
          >
            认领
          </el-button>
          <el-button
            v-if="detail.state === 'firing' && ackStatus(detail) !== 'closed'"
            size="small"
            @click="assignEvent(detail)"
          >
            指派
          </el-button>
          <el-button
            v-if="detail.state === 'firing' && ackStatus(detail) !== 'closed'"
            size="small"
            @click="closeEvent(detail)"
          >
            关闭告警
          </el-button>
          <el-button v-if="ackStatus(detail) === 'closed'" size="small" @click="reopenEvent(detail)">重新打开</el-button>
          <el-button
            size="small"
            :disabled="!detail.node"
            title="查看该节点在告警前后 5 分钟内的集中日志"
            @click="gotoLogs(detail)"
          >
            查看日志
          </el-button>
          <el-button size="small" :loading="commenting" @click="commentEvent(detail)">评论</el-button>
          <el-button size="small" @click="drawer = false">返回</el-button>
        </div>
      </template>
    </el-drawer>
  </div>
</template>

<script setup>
import { ref, computed, watch, onMounted, onUnmounted, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import { Plus } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import * as echarts from 'echarts'
import http, { getToken } from '../api/http'
import RuleModal from './RuleModal.vue'

const cssVar = (name) => getComputedStyle(document.documentElement).getPropertyValue(name).trim()
const router = useRouter()
const rules = ref([])
// 告警规则前端分页
const currentPage = ref(1)
const pageSize = ref(10)
const ruleSearch = ref('')
const ruleTypeFilter = ref('')
const filteredRules = computed(() => {
  const kw = ruleSearch.value.trim().toLowerCase()
  const t = ruleTypeFilter.value
  return rules.value.filter((r) => {
    if (t && (r.type || '') !== t) return false
    if (kw && !((r.name || '').toLowerCase().includes(kw))) return false
    return true
  })
})
const pagedRules = computed(() => {
  const start = (currentPage.value - 1) * pageSize.value
  return filteredRules.value.slice(start, start + pageSize.value)
})
watch([rules, filteredRules], () => {
  const max = Math.max(1, Math.ceil(filteredRules.value.length / pageSize.value))
  if (currentPage.value > max) currentPage.value = max
})
watch([ruleSearch, ruleTypeFilter, pageSize], () => {
  currentPage.value = 1
})
const alerts = ref([])
const groups = ref([])
const templates = ref([])
const acks = ref({})
const editing = ref(null)
const eventFilter = ref('firing')
const testing = ref(false)
const selected = ref([])
const drawer = ref(false)
const detail = ref(null)
const chartRef = ref(null)
let chartInstance = null
const maintenance = ref({ enabled: false, start: 0, end: 0, reason: '' })
// 全部渠道及其展示名；enabled 由通知配置决定，未启用的渠道在新建规则时置灰。
const CHANNEL_META = [
  { value: 'email', label: '邮件' },
  { value: 'webhook', label: 'Webhook' },
  { value: 'dingtalk', label: '钉钉' },
  { value: 'feishu', label: '飞书' },
  { value: 'wecom', label: '企业微信' },
]
const channelOptions = ref(CHANNEL_META.map((c) => ({ ...c, enabled: true })))
let timer = null
let loadGeneration = 0
const refreshError = ref('')
const statsLoading = ref(false)
const statsError = ref('')
const ruleLoadError = ref('')
let maintenanceTimer = null

// P4：统计看板 / 抑制规则 / 分组配置
const stats = ref({ firing: 0, suppressed: 0, total: 0, bySeverity: { critical: 0, warning: 0, info: 0 } })
const inhibits = ref([])
const grouping = ref({
  enabled: false,
  groupBy: ['name'],
  groupWait: '30s',
  groupInterval: '5m',
  // D1 风暴收敛（默认开启：开启分组即默认收敛；关掉开关即显式写入 converge:false）
  converge: true,
  convergeBy: ['rule', 'severity'],
  convergeWindow: '10m',
  headCount: 5,
})
const inhibitDialog = ref(false)
const inhibitEditIndex = ref(-1)
const inhibitForm = ref(emptyInhibitForm())
function emptyInhibitForm() {
  return { sourceRule: '', sourceSeverity: '', sourceMetricRegex: '', targetSeverity: '', targetMetricRegex: '', equal: [] }
}

const activeCount = computed(() => alerts.value.filter((a) => a.state === 'firing').length)
const criticalCount = computed(() => alerts.value.filter((a) => a.state === 'firing' && a.severity === 'critical').length)
const warningCount = computed(() => alerts.value.filter((a) => a.state === 'firing' && a.severity === 'warning').length)

const filteredAlerts = computed(() => {
  if (!eventFilter.value) return alerts.value
  return alerts.value.filter((a) => a.state === eventFilter.value)
})

// 告警事件前端分页
const evCurrentPage = ref(1)
const evPageSize = ref(10)
const pagedAlerts = computed(() => {
  const start = (evCurrentPage.value - 1) * evPageSize.value
  return filteredAlerts.value.slice(start, start + evPageSize.value)
})
watch(filteredAlerts, () => {
  const max = Math.max(1, Math.ceil(filteredAlerts.value.length / evPageSize.value))
  if (evCurrentPage.value > max) evCurrentPage.value = max
})
watch([evPageSize, eventFilter], () => {
  evCurrentPage.value = 1
  loadAlerts()
})

function ackKey(e) {
  return `${e.ruleName}|${e.node}|${e.instance || ''}|${e.startsAt || 0}`
}
// 处置记录（含状态 / 处理人 / 关闭原因 / 评论）
function ackInfo(e) {
  return e ? acks.value[ackKey(e)] || null : null
}
// 处置状态；旧记录没有 status 字段，一律视为「已认领」——与后端 EffectiveStatus 同口径。
function ackStatus(e) {
  if (!e) return 'pending'
  if (e.state && e.state !== 'firing') return 'resolved'
  const info = ackInfo(e)
  return info ? info.status || 'ack' : 'pending'
}
function ackStatusLabel(e) {
  return { pending: '待处理', ack: '已认领', closed: '已关闭', resolved: '已恢复' }[ackStatus(e)] || '告警中'
}
function ackTagType(e) {
  return { pending: 'danger', ack: 'warning', closed: 'info', resolved: 'success' }[ackStatus(e)] || 'danger'
}
function sevType(s) {
  return { critical: 'danger', warning: 'warning', info: 'info' }[s] || 'info'
}
function sevLabel(s) {
  return { critical: '紧急', warning: '警告', info: '信息' }[s] || s
}
function stateLabel(e) {
  if (e.state === 'firing') return ackStatusLabel(e)
  return { firing: '告警中', resolved: '已恢复', pending: '待触发' }[e.state] || e.state
}
function channelLabel(v) {
  return (CHANNEL_META.find((c) => c.value === v) || {}).label || v
}
function fmt(ts) {
  if (!ts) return '-'
  return new Date(ts).toLocaleString('zh-CN', { hour12: false })
}

const maintenanceStart = computed({
  get: () => maintenance.value.start ? new Date(maintenance.value.start) : null,
  set: (v) => { maintenance.value.start = v ? (typeof v === 'number' ? v : v.getTime()) : 0 },
})
const maintenanceEnd = computed({
  get: () => maintenance.value.end ? new Date(maintenance.value.end) : null,
  set: (v) => { maintenance.value.end = v ? (typeof v === 'number' ? v : v.getTime()) : 0 },
})

async function loadMaintenance() {
  try {
    const mw = await http.get('/api/v1/maintenance')
    if (mw) maintenance.value = mw
  } catch (e) { /* ignore */ }
}

function saveMaintenance() {
  // 日期/原因修改会连续触发，防抖合并为一次保存
  clearTimeout(maintenanceTimer)
  maintenanceTimer = setTimeout(async () => {
    try {
      await http.put('/api/v1/maintenance', {
        enabled: maintenance.value.enabled,
        start: maintenance.value.start,
        end: maintenance.value.end,
        reason: maintenance.value.reason,
      })
      ElMessage.success('维护窗口已保存（热生效）')
    } catch (e) {
      ElMessage.error('保存维护窗口失败')
    }
  }, 400)
}

async function loadAlerts() {
  const generation = ++loadGeneration
  try {
    const state = eventFilter.value === 'firing' ? 'active' : eventFilter.value
    const suffix = state ? `?state=${encodeURIComponent(state)}` : ''
    const data = await http.get('/api/v1/alerts' + suffix)
    if (generation !== loadGeneration) return
    alerts.value = data.alerts || []
    refreshError.value = ''
    if (detail.value && !alerts.value.some((item) => item.ruleName === detail.value.ruleName && item.node === detail.value.node && item.state === detail.value.state && (item.startsAt || item.endsAt) === (detail.value.startsAt || detail.value.endsAt))) {
      drawer.value = false
    }
  } catch (e) {
    if (generation !== loadGeneration) return
    alerts.value = []
    refreshError.value = '告警数据刷新失败：' + (e.message || '请重新登录后重试')
    if (drawer.value && detail.value && detail.value.state === 'firing') drawer.value = false
  }
}

async function load() {
  try {
    rules.value = (await http.get('/api/v1/rules')).rules || []
    ruleLoadError.value = ''
  } catch (e) {
    ruleLoadError.value = e.message || '网络异常'
  }
  await loadAlerts()
  try {
    groups.value = (await http.get('/api/v1/groups')).groups || []
  } catch (e) { /* ignore */ }
  try {
    templates.value = (await http.get('/api/v1/rules/templates')).templates || []
  } catch (e) { /* ignore */ }
  try {
    acks.value = (await http.get('/api/v1/alerts/acks')).acks || {}
  } catch (e) { /* ignore */ }
  try {
    statsLoading.value = true
    stats.value = await http.get('/api/v1/alerts/stats')
    statsError.value = ''
  } catch (e) {
    statsError.value = '告警概览加载失败：' + (e.message || '网络异常')
  } finally {
    statsLoading.value = false
  }
  try {
    inhibits.value = (await http.get('/api/v1/inhibit')).rules || []
  } catch (e) { /* ignore */ }
  try {
    const g = await http.get('/api/v1/grouping')
    if (g && typeof g === 'object') grouping.value = g
  } catch (e) { /* ignore */ }
  try {
    const cfg = await http.get('/api/v1/notify')
    const enabled = {
      email: cfg.email && cfg.email.enabled,
      webhook: cfg.webhook && cfg.webhook.enabled,
      dingtalk: cfg.dingtalk && cfg.dingtalk.enabled,
      feishu: cfg.feishu && cfg.feishu.enabled,
      wecom: cfg.wecom && cfg.wecom.enabled,
    }
    channelOptions.value = CHANNEL_META.map((c) => ({ ...c, enabled: !!enabled[c.value] }))
  } catch (e) { /* ignore */ }
}

async function testAlert() {
  testing.value = true
  try {
    const r = await http.post('/api/v1/alerts/test')
    if (r.ok) {
      ElMessage.success('已发送测试告警事件，请查看事件列表或通知渠道')
    } else {
      ElMessageBox.alert(r.error || '测试失败', '触发失败', { type: 'error', confirmButtonText: '关闭' })
    }
    load()
  } catch (e) {
    ElMessageBox.alert(e.message || '请求失败', '触发失败', { type: 'error', confirmButtonText: '关闭' })
  } finally {
    testing.value = false
  }
}

function newRule() {
  // 设为空对象（truthy）以触发 RuleModal 渲染（v-if="editing"）
  editing.value = {}
}

// 告警规则导出 / 导入
const fileInput = ref(null)
const importDialog = ref(false)
const importMode = ref('merge') // merge | replace
const pendingImport = ref(null) // { list, name }

async function exportRules() {
  try {
    const token = getToken()
    const headers = {}
    if (token) headers['Authorization'] = 'Bearer ' + token
    const resp = await fetch('/api/v1/rules/export', { headers })
    if (!resp.ok) {
      ElMessage.error('导出失败（HTTP ' + resp.status + '）')
      return
    }
    const blob = await resp.blob()
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    const disp = resp.headers.get('Content-Disposition')
    let fname = 'alert-rules.json'
    if (disp && disp.includes('filename=')) {
      fname = decodeURIComponent(disp.split('filename=')[1].replace(/^"|"$/g, ''))
    }
    a.href = url
    a.download = fname
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    URL.revokeObjectURL(url)
    ElMessage.success('已导出告警规则')
  } catch (e) {
    ElMessage.error('导出失败：' + (e.message || e))
  }
}

async function onFileChange(e) {
  const input = e.target
  const file = input.files && input.files[0]
  if (!file) return
  try {
    const text = await file.text()
    const parsed = JSON.parse(text)
    const list = Array.isArray(parsed) ? parsed : parsed.rules
    if (!Array.isArray(list) || list.length === 0) {
      ElMessage.error('文件格式不正确（缺少 rules 数组或为空）')
      return
    }
    pendingImport.value = { list, name: file.name }
    importMode.value = 'merge'
    importDialog.value = true
  } catch (err) {
    ElMessage.error('解析失败：' + (err.message || err))
  } finally {
    input.value = '' // 允许重复选择同一文件
  }
}

async function confirmImport() {
  if (!pendingImport.value) return
  // 覆盖导入会删除现有全部规则，二次强确认
  if (importMode.value === 'replace') {
    try {
      await ElMessageBox.confirm(
        `即将清空当前全部 ${rules.value.length} 条规则并导入「${pendingImport.value.name}」中的 ${pendingImport.value.list.length} 条规则，此操作不可撤销。是否继续？`,
        '覆盖导入确认',
        { type: 'error', confirmButtonText: '确认覆盖导入', cancelButtonText: '取消' }
      )
    } catch { return }
  }
  try {
    const res = await http.post('/api/v1/rules/import', {
      rules: pendingImport.value.list,
      replace: importMode.value === 'replace',
    })
    ElMessage.success('导入成功：新增 ' + (res.created || 0) + ' 条，更新 ' + (res.updated || 0) + ' 条')
    importDialog.value = false
    pendingImport.value = null
    await load()
  } catch (err) {
    ElMessage.error('导入失败：' + (err.message || err))
  }
}

function onTemplateCmd(cmd) {
  if (cmd === '__blank') {
    newRule()
    return
  }
  const t = templates.value.find((x) => x.name === cmd)
  if (t) editing.value = { ...t }
}
function edit(rule) {
  editing.value = { ...rule }
}
async function del(id) {
  try {
    await ElMessageBox.confirm('确认删除该规则？', '提示', { type: 'warning' })
    await http.del('/api/v1/rules/' + id)
    ElMessage.success('已删除')
    load()
  } catch (e) { /* 取消 */ }
}
function onSaved() {
  editing.value = null
  load()
}
async function toggleRule(rule) {
  try {
    await http.post('/api/v1/rules/' + rule.id + '/toggle')
    ElMessage.success(rule.enabled ? '已启用' : '已停用')
    load()
  } catch (e) {
    rule.enabled = !rule.enabled
    ElMessage.error('操作失败')
  }
}
// 规则单次静默开关
async function toggleSilence(rule) {
  try {
    await http.post('/api/v1/rules/' + rule.id + '/toggle-silence', { silenced: rule.silenced })
    ElMessage.success(rule.silenced ? '已静默' : '已取消静默')
    load()
  } catch (e) {
    rule.silenced = !rule.silenced
    ElMessage.error('操作失败')
  }
}

// ===== 规则类型/触发条件的展示辅助 =====
const TYPE_LABELS = {
  '': '阈值',
  node_offline: '主机离线',
  service_down: '服务离线',
  role_change: '主从切换',
  cluster_fault: '集群损坏',
  security_event: '安全事件',}
const SERVICE_LABELS = {
  mysql: 'MySQL', postgres: 'PostgreSQL', redis: 'Redis', nginx: 'Nginx',
  kafka: 'Kafka', rocketmq: 'RocketMQ', docker: 'Docker', k8s: 'Kubernetes',
}
function typeLabel(row) {
  return TYPE_LABELS[row.type || ''] || '阈值'
}
function typeTag(row) {
  switch (row.type || '') {
    case 'node_offline':
    case 'service_down':
    case 'cluster_fault':
      return 'danger'
    case 'role_change':
      return 'warning'
    default:
      return 'info'
  }
}
function conditionText(row) {
  switch (row.type || '') {
    case 'node_offline':
      return '主机心跳离线 持续 ' + (row.for || '5m')
    case 'service_down':
      return (
        (SERVICE_LABELS[row.service] || row.service) +
        ' 离线：*_instance_up ' +
        (row.operator || '<=') +
        ' ' +
        (row.threshold != null ? row.threshold : 0.5) +
        ' 持续 ' + (row.for || '3m')
      )
    case 'role_change':
      return (
        (SERVICE_LABELS[row.service] || row.service) +
        ' 主从切换（' + (row.topology || 'cluster') + '）：检测到角色变化'
      )
    case 'cluster_fault':
      return (
        (SERVICE_LABELS[row.service] || row.service) +
        ' 集群状态损坏（' + (row.topology || 'cluster') + '）无主/多主，持续 ' + (row.for || '2m')
      )
    case 'security_event':
      return '安全事件：' + (row.category || '全部类别') + '，收到事件即触发'
    default:
      return row.metric + ' ' + (row.operator || '') + ' ' + (row.threshold != null ? row.threshold : '')
  }
}

function onSelect(rows) {
	selected.value = rows
}
function selectableAlert(row) {
  return row.state === 'firing'
}
function openDetail(row) {
  detail.value = row
  drawer.value = true
  nextTick(() => loadTrend(row))
}
function gotoNode(row) {
  router.push({ path: '/hosts', query: { node: row.node } })
}
// gotoLogs 跳到「该节点该时间段」的集中日志（C2）：把告警与原始日志串起来。
// 时间窗以告警开始时间为锚点、前后各留 5 分钟——这是「触发前后到底发生了什么」最常用的一段；
//   - 已恢复：窗口 = [开始-5m, 恢复+5m]
//   - 仍在告警：窗口 = [开始-5m, 现在]（想看更早/更晚可在日志页改时间范围）
function gotoLogs(row) {
  const start = row.startsAt || Date.now()
  const from = start - 5 * 60 * 1000
  const to = row.state === 'resolved' && row.endsAt ? row.endsAt + 5 * 60 * 1000 : Date.now()
  router.push({ path: '/logs', query: { node: row.node, from: String(from), to: String(to) } })
}
// ---- D4 告警协作处置：认领 / 指派 / 关闭 / 重新打开 / 评论 ----
// 处置只影响「谁在处理」，不改变监控条件的真实 firing 状态。
function collabPayload(row, extra = {}) {
  return {
    rule: row.ruleName,
    host: row.node,
    instance: row.instance || '',
    startsAt: row.startsAt || 0,
    ...extra,
  }
}

// 处置后只刷新数据、不关闭抽屉：抽屉的处置区读的是 acks，因此会自动更新。
async function runCollab(path, row, extra, okText) {
  try {
    await http.post(path, collabPayload(row, extra))
    ElMessage.success(okText)
    await load()
    return true
  } catch (e) {
    ElMessage.error(e.message || '操作失败')
    return false
  }
}

async function ackEvent(row) {
  await runCollab('/api/v1/alerts/ack', row, {}, '已认领')
}

async function assignEvent(row) {
  let value
  try {
    const res = await ElMessageBox.prompt('指派给（填写用户名）', '指派告警', {
      inputPlaceholder: '如 ops1',
      inputValidator: (v) => (v && v.trim() ? true : '请填写用户名'),
    })
    value = res.value
  } catch {
    return
  }
  await runCollab('/api/v1/alerts/ack', row, { assignee: value.trim() }, '已指派给 ' + value.trim())
}

async function closeEvent(row) {
  let reason
  try {
    const res = await ElMessageBox.prompt('关闭原因（便于日后回溯）', '关闭告警', {
      inputPlaceholder: '如 误报 / 已扩容 / 已重启服务',
    })
    reason = res.value || ''
  } catch {
    return
  }
  await runCollab('/api/v1/alerts/close', row, { reason }, '已关闭')
}

async function reopenEvent(row) {
  await runCollab('/api/v1/alerts/reopen', row, {}, '已重新打开，回到待处理')
}

async function commentEvent(row) {
  const text = commentText.value.trim()
  if (!text) {
    ElMessage.warning('请输入评论内容')
    return
  }
  commenting.value = true
  try {
    const ok = await runCollab('/api/v1/alerts/comment', row, { comment: text }, '已提交')
    if (ok) commentText.value = ''
  } finally {
    commenting.value = false
  }
}
async function batchAck() {
  if (!selected.value.length) return
  try {
    await Promise.all(selected.value.map((r) =>
      http.post('/api/v1/alerts/ack', { rule: r.ruleName, host: r.node, instance: r.instance || '', startsAt: r.startsAt }),
    ))
    ElMessage.success(`已确认 ${selected.value.length} 条`)
    await load()
  } catch (e) {
    ElMessage.error('批量确认失败')
  }
}

let lastTrendPoints = []
async function loadTrend(row) {
  if (!row.metric || !chartRef.value) return
  const end = Date.now()
  const start = end - 3600 * 1000
  let url = `/api/v1/query/range?node=${encodeURIComponent(row.node)}&metric=${encodeURIComponent(row.metric)}&start=${start}&end=${end}&step=60000`
  if (row.instance) url += `&labels.instance=${encodeURIComponent(row.instance)}`
  try {
    const data = await http.get(url)
    const pts = (data.series || []).map((p) => [p.time, p.value])
    lastTrendPoints = pts
    renderChart(pts, row)
  } catch (e) { /* ignore */ }
}
function renderChart(points, row) {
  if (chartInstance) { chartInstance.dispose(); chartInstance = null }
  chartInstance = echarts.init(chartRef.value)
  chartInstance.setOption({
    grid: { left: 48, right: 16, top: 24, bottom: 28 },
    tooltip: { trigger: 'axis' },
    xAxis: { type: 'time' },
    yAxis: { type: 'value', name: row.metric },
    series: [{
      type: 'line',
      data: points,
      showSymbol: false,
      smooth: true,
      areaStyle: { opacity: 0.15 },
      lineStyle: { color: cssVar('--danger') },
      itemStyle: { color: cssVar('--danger') },
      markLine: row.threshold
        ? { silent: true, symbol: 'none', data: [{ yAxis: row.threshold }], lineStyle: { color: cssVar('--warn'), type: 'dashed' }, label: { formatter: '阈值 ' + row.threshold } }
        : undefined,
    }],
  })
}
function onDrawerClosed() {
  if (chartInstance) { chartInstance.dispose(); chartInstance = null }
  detail.value = null
}

// ---- P4：分组配置 ----
async function saveGrouping() {
  try {
    await http.put('/api/v1/grouping', {
      enabled: grouping.value.enabled,
      groupBy: grouping.value.groupBy || ['name'],
      groupWait: grouping.value.groupWait || '30s',
      groupInterval: grouping.value.groupInterval || '5m',
      converge: !!grouping.value.converge,
      convergeBy: grouping.value.convergeBy?.length ? grouping.value.convergeBy : ['rule', 'severity'],
      convergeWindow: grouping.value.convergeWindow || '10m',
      headCount: Number(grouping.value.headCount) || 5,
    })
    ElMessage.success('分组配置已保存（热生效）')
  } catch (e) {
    ElMessage.error('保存分组配置失败')
  }
}

// ---- P4：抑制规则 ----
function inhibitText(ms) {
  if (!ms) return '—'
  const parts = []
  if (ms.match) for (const [k, v] of Object.entries(ms.match)) parts.push(`${k}=${v}`)
  if (ms.matchRegex) for (const [k, v] of Object.entries(ms.matchRegex)) parts.push(`${k}~${v}`)
  return parts.length ? parts.join('  &  ') : '—'
}

function buildInhibitRule() {
  const f = inhibitForm.value
  const src = { match: {}, matchRegex: {} }
  if (f.sourceSeverity) src.match.severity = f.sourceSeverity
  if (f.sourceRule) src.matchRegex.rule = f.sourceRule
  if (f.sourceMetricRegex) src.matchRegex.metric = f.sourceMetricRegex
  const tgt = { match: {}, matchRegex: {} }
  if (f.targetSeverity) tgt.match.severity = f.targetSeverity
  if (f.targetMetricRegex) tgt.matchRegex.metric = f.targetMetricRegex
  return { source: src, target: tgt, equal: f.equal || [] }
}

function formFromRule(r) {
  const f = emptyInhibitForm()
  if (r.source) {
    f.sourceSeverity = r.source.match?.severity || ''
    f.sourceRule = r.source.matchRegex?.rule || r.source.match?.rule || ''
    f.sourceMetricRegex = r.source.matchRegex?.metric || ''
  }
  if (r.target) {
    f.targetSeverity = r.target.match?.severity || ''
    f.targetMetricRegex = r.target.matchRegex?.metric || ''
  }
  f.equal = r.equal || []
  return f
}

function newInhibit() {
  inhibitEditIndex.value = -1
  inhibitForm.value = emptyInhibitForm()
  inhibitDialog.value = true
}
function editInhibit(row, idx) {
  inhibitEditIndex.value = idx
  inhibitForm.value = formFromRule(row)
  inhibitDialog.value = true
}
async function persistInhibits() {
  try {
    await http.put('/api/v1/inhibit', inhibits.value)
    ElMessage.success('抑制规则已保存（热生效）')
  } catch (e) {
    ElMessage.error('保存抑制规则失败')
  }
}
function saveInhibit() {
  const rule = buildInhibitRule()
  ;['source', 'target'].forEach((k) => {
    const ms = rule[k]
    if (Object.keys(ms.match).length === 0) delete ms.match
    if (Object.keys(ms.matchRegex).length === 0) delete ms.matchRegex
  })
  const list = [...inhibits.value]
  if (inhibitEditIndex.value >= 0) list[inhibitEditIndex.value] = rule
  else list.push(rule)
  inhibits.value = list
  inhibitDialog.value = false
  persistInhibits()
}
async function delInhibit(idx) {
  try {
    await ElMessageBox.confirm('确认删除该抑制规则？', '提示', { type: 'warning' })
    inhibits.value.splice(idx, 1)
    persistInhibits()
  } catch (e) { /* 取消 */ }
}

// 每 30s 自动刷新告警列表，确保实时性
onMounted(() => {
  load()
  loadMaintenance()
  timer = setInterval(load, 30000)
  // 换肤后重绘趋势图（ECharts 颜色来自 CSS 变量）
  window.addEventListener('nebula:theme-changed', onThemeChanged)
})

function onThemeChanged() {
  if (detail.value && chartRef.value) {
    renderChart(lastTrendPoints, detail.value)
  }
}

onUnmounted(() => {
  if (timer) clearInterval(timer)
  if (maintenanceTimer) clearTimeout(maintenanceTimer)
  window.removeEventListener('nebula:theme-changed', onThemeChanged)
  if (chartInstance) chartInstance.dispose()
})
</script>

<style scoped>
.alert-refresh-error {
  color: var(--danger);
  font-size: 13px;
  margin-bottom: 10px;
}
.rule-load-error {
  margin-bottom: 12px;
}
.panel-title-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.event-toolbar {
  display: flex;
  gap: 8px;
  align-items: center;
}
.event-toolbar-hint {
  font-size: 13px;
  white-space: nowrap;
}
.muted {
  color: var(--text-dim);
}
.ev-acked {
  margin-left: 6px;
  color: var(--text-dim);
}
.maintenance-row {
  display: flex;
  gap: 16px;
  margin-top: 12px;
  align-items: flex-start;
  flex-wrap: wrap;
}
.maintenance-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 200px;
}
.maintenance-label {
  font-size: 13px;
  color: var(--text-dim);
}
.maintenance-hint {
  margin-top: 10px;
  font-size: 13px;
  color: var(--warn);
  background: rgba(230, 162, 60, 0.08);
  padding: 6px 12px;
  border-radius: 4px;
}
.scope-count {
  margin-left: 6px;
  font-size: 13px;
  color: var(--text-dim);
}
.ev-field {
  display: flex;
  gap: 12px;
  padding: 8px 0;
  border-bottom: 1px solid var(--border);
  font-size: 13px;
}
.ev-field > span:first-child {
  width: 64px;
  color: var(--text-dim);
  flex-shrink: 0;
}
.ev-chart-title {
  margin: 16px 0 8px;
  font-size: 13px;
  color: var(--text-dim);
}
.ev-chart {
  width: 100%;
  height: 240px;
}
.collab-timeline {
  max-height: 220px;
  overflow-y: auto;
  border-left: 2px solid var(--el-border-color, #30363d);
  padding-left: 10px;
  margin-bottom: 10px;
}
.collab-item {
  margin-bottom: 10px;
}
.collab-head {
  display: flex;
  align-items: baseline;
  gap: 8px;
}
.collab-user {
  font-size: 13px;
  font-weight: 600;
}
.collab-time {
  font-size: 11.5px;
  color: var(--text-dim);
}
.collab-text {
  font-size: 13px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-word;
}
.collab-input {
  margin-bottom: 10px;
}
.ev-actions {
  margin-top: 16px;
  display: flex;
  gap: 8px;
}
.kpi-value.gray {
  color: var(--text-dim);
}
.sev-bar-wrap {
  font-size: 13px;
  color: var(--text);
  display: flex;
  gap: 16px;
  align-items: center;
}
.sev-dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  margin-right: 4px;
}
.sev-dot.danger { background: var(--danger); }
.sev-dot.warning { background: var(--warn); }
.sev-dot.info { background: var(--text-dim); }
.adv-section {
  padding: 6px 0;
}
.adv-title {
  font-size: 14px;
  font-weight: 600;
  margin-bottom: 10px;
  color: var(--text);
}
.adv-row {
  display: flex;
  align-items: center;
  gap: 14px;
  flex-wrap: wrap;
  margin-bottom: 12px;
}
.adv-item {
  display: flex;
  align-items: center;
  gap: 6px;
}
.adv-item:has(.group-by-select) {
  min-width: 300px;
}
.group-by-select {
  width: 220px;
}
.group-by-select :deep(.el-select__selection) {
  flex-wrap: nowrap;
  min-width: 0;
}
.group-by-select :deep(.el-select__selected-item) {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
}
.adv-label {
  font-size: 13px;
  color: var(--text-dim);
  white-space: nowrap;
}
.inh-block {
  background: rgba(255, 255, 255, 0.02);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 12px;
  margin-bottom: 12px;
}
.inh-title {
  font-size: 13px;
  color: var(--text-dim);
  margin-bottom: 10px;
}
.inh-row {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
}
.inh-label {
  width: 92px;
  font-size: 13px;
  color: var(--text-dim);
  flex-shrink: 0;
}
</style>
