<template>
  <div class="node-view">
    <!-- 面包屑 + 主机切换 + 状态 -->
    <div class="breadcrumb">
      <el-icon class="bc-home"><HomeFilled /></el-icon>
      <span class="bc-item bc-link" @click="$router.push('/hosts')">主机监控</span>
      <el-icon class="bc-sep"><ArrowRight /></el-icon>
      <el-select
        v-model="selected"
        filterable
        size="small"
        placeholder="选择主机"
        class="bc-host-select"
        @change="onSelect"
      >
        <el-option
          v-for="n in nodes"
          :key="n.hostname"
          :value="n.hostname"
          :label="n.hostname + ' (' + (n.group || 'default') + ')'"
        />
      </el-select>
      <template v-if="current">
        <span class="bc-ip mono">{{ current.ip }}</span>
        <span class="status-pill" :class="currentStatus">
          <i class="dot"></i>{{ currentStatus === 'online' ? '在线' : '离线' }}
        </span>
      </template>
    </div>

    <el-tabs v-model="activeTab" type="border-card" class="node-tabs">
      <!-- ============ Tab 1：主机概览 ============ -->
      <el-tab-pane label="主机概览" name="overview">
        <!-- 设备信息 -->
        <div class="section-title">设备信息</div>
        <div class="device-grid">
          <div class="dev-item"><span>主机名</span><strong class="with-copy">{{ hostTitle(current) }}<el-icon class="copy-btn" title="复制" @click="copyText(current?.hostname)"><DocumentCopy /></el-icon></strong></div>
          <div class="dev-item"><span>IP 地址</span><strong class="with-copy mono">{{ current?.ip || '-' }}<el-icon class="copy-btn" title="复制" @click="copyText(current?.ip)"><DocumentCopy /></el-icon></strong></div>
          <div class="dev-item"><span>操作系统</span><strong>{{ current?.os || '-' }}</strong></div>
          <div class="dev-item"><span>运行天数</span><strong class="mono">{{ uptimeDays }} 天（{{ bootTimeText }}）</strong></div>
          <div class="dev-item"><span>Agent 版本</span><strong class="mono">v{{ current?.version || '-' }}</strong></div>
          <div class="dev-item"><span>CPU 型号</span><strong :title="hostInfo?.cpuModel">{{ hostInfo?.cpuModel || '-' }}</strong></div>
          <div class="dev-item"><span>CPU 核数</span><strong class="mono">{{ hostInfo?.cpuCores || '-' }} 核</strong></div>
          <div class="dev-item">
            <span>系统负载</span>
            <strong class="mono">{{ rt.load1 }} / {{ rt.load5 }} / {{ rt.load15 }}</strong>
          </div>
        </div>

        <!-- 系统情况：环形图 -->
        <div class="section-title">系统情况</div>
        <div class="gauge-row">
          <div class="gauge-card">
            <div :ref="(el) => setRef(el, 'cpuGauge')" class="gauge"></div>
            <div class="gauge-label">CPU 使用率</div>
            <div class="gauge-sub">{{ hostInfo?.cpuCores || '-' }} 核</div>
          </div>
          <div class="gauge-card">
            <div :ref="(el) => setRef(el, 'memGauge')" class="gauge"></div>
            <div class="gauge-label">内存使用率</div>
            <div class="gauge-sub">{{ memUsedText }} / {{ memTotalText }}</div>
          </div>
          <div class="gauge-card">
            <div :ref="(el) => setRef(el, 'diskGauge')" class="gauge"></div>
            <div class="gauge-label">磁盘使用率</div>
            <div class="gauge-sub">{{ diskUsedText }} / {{ diskTotalText }}</div>
          </div>
          <div class="gauge-card">
            <div :ref="(el) => setRef(el, 'swapGauge')" class="gauge"></div>
            <div class="gauge-label">SWAP 使用率</div>
            <div class="gauge-sub">{{ swapUsedText }} / {{ swapTotalText }}</div>
          </div>
        </div>

        <!-- 端口状态 -->
        <div v-if="portStatuses.length > 0" class="port-section">
          <div class="section-title">端口状态</div>
          <div class="port-list">
            <div v-for="ps in portStatuses" :key="ps.port" class="port-item" :class="{ up: ps.up, down: !ps.up }">
              <span class="port-dot"></span>
              <span class="port-num">{{ ps.port }}</span>
              <span class="port-state">{{ ps.up ? '在线' : '离线' }}</span>
              <span v-if="ps.latency" class="port-latency">{{ ps.latency.toFixed(1) }}ms</span>
            </div>
          </div>
        </div>

        <!-- 实时趋势 + IO -->
        <div class="section-title">实时趋势</div>
        <div class="panel-hint">说明：以下为通过 WebSocket 实时上报的采样（每秒 1 条，横轴为采样时刻，仅保留最近 60 个点）。</div>
        <el-alert
          v-if="!rtReady && currentStatus === 'online'"
          type="info"
          :closable="false"
          show-icon
          class="rt-waiting"
          title="正在等待实时数据…（WebSocket 连接中，通常 1-2 秒内开始上报）"
        />
        <el-alert
          v-if="currentStatus === 'offline'"
          type="warning"
          :closable="false"
          show-icon
          class="rt-waiting"
          title="主机离线，实时数据不可用。主机恢复上报后此处会自动更新。"
        />
        <div class="metric-grid">
          <div class="metric-card">
            <div class="mc-head"><span class="mc-label">CPU 使用率</span><span class="mc-value" :class="rateClass(rt.cpu)">{{ rt.cpu }}<small>%</small></span></div>
            <div :ref="(el) => setRef(el, 'cpu')" class="mc-chart"></div>
            <div class="mc-desc">{{ rtInfo.cpu }}</div>
          </div>
          <div class="metric-card">
            <div class="mc-head"><span class="mc-label">内存使用率</span><span class="mc-value" :class="rateClass(rt.mem)">{{ rt.mem }}<small>%</small></span></div>
            <div :ref="(el) => setRef(el, 'mem')" class="mc-chart"></div>
            <div class="mc-desc">{{ rtInfo.mem }}</div>
          </div>
          <div class="metric-card">
            <div class="mc-head">
              <span class="mc-label">磁盘 IO</span>
              <div class="mc-stats">
                <span class="mc-stat"><i class="dot" style="background:var(--chart-blue)"></i>读取 <b style="color:var(--chart-blue)">{{ rt.diskRead }}</b></span>
                <span class="mc-stat"><i class="dot" style="background:var(--warn)"></i>写入 <b style="color:var(--warn)">{{ rt.diskWrite }}</b></span>
              </div>
            </div>
            <div :ref="(el) => setRef(el, 'diskio')" class="mc-chart"></div>
            <div class="mc-desc">{{ rtInfo.diskio }}</div>
          </div>
          <div class="metric-card">
            <div class="mc-head">
              <span class="mc-label">网络 IO</span>
              <div class="mc-stats">
                <span class="mc-stat"><i class="dot" style="background:var(--accent)"></i>接收 <b style="color:var(--accent)">{{ rt.net }}</b></span>
                <span class="mc-stat"><i class="dot" style="background:var(--violet)"></i>发送 <b style="color:var(--violet)">{{ rt.netSent }}</b></span>
              </div>
            </div>
            <div :ref="(el) => setRef(el, 'netio')" class="mc-chart"></div>
            <div class="mc-desc">{{ rtInfo.netio }}</div>
          </div>
        </div>

        <!-- 进程 TOP10 + 在线用户 + 告警 -->
        <div class="bottom-row">
          <div class="bottom-col">
            <div class="section-title section-head">
              进程占用 Top 10
              <el-input v-model="procSearch" placeholder="搜索进程名 / PID" clearable size="small" :prefix-icon="Search" class="proc-search" />
            </div>
            <el-table :data="filteredProcs" stripe size="small" max-height="320" :default-sort="{ prop: 'cpu', order: 'descending' }">
              <el-table-column prop="pid" label="PID" width="90" sortable />
              <el-table-column prop="name" label="进程名" min-width="160" show-overflow-tooltip />
              <el-table-column label="CPU %" width="120" sortable :sort-method="(a, b) => a.cpu - b.cpu">
                <template #default="{ row }">
                  <div class="proc-bar">
                    <div class="bar"><div class="bar-fill cyan" :style="{ width: Math.min(row.cpu, 100) + '%' }"></div></div>
                    <span class="mono">{{ row.cpu.toFixed(1) }}</span>
                  </div>
                </template>
              </el-table-column>
              <el-table-column label="MEM %" width="120" sortable :sort-method="(a, b) => a.mem - b.mem">
                <template #default="{ row }">
                  <div class="proc-bar">
                    <div class="bar"><div class="bar-fill" :class="memClass(row.mem)" :style="{ width: Math.min(row.mem, 100) + '%' }"></div></div>
                    <span class="mono">{{ row.mem.toFixed(1) }}</span>
                  </div>
                </template>
              </el-table-column>
            </el-table>
            <el-empty v-if="!filteredProcs.length" description="无进程数据" :image-size="50" />
          </div>
          <div class="bottom-col">
            <div class="section-title">
              在线 SSH 用户
              <el-tag size="small" type="info" effect="plain" style="margin-left: 8px">{{ hostInfo?.onlineUsers?.length || 0 }} 人</el-tag>
            </div>
            <el-table v-if="hostInfo?.onlineUsers?.length" :data="hostInfo.onlineUsers" stripe size="small" class="user-table">
              <el-table-column prop="user" label="用户" width="120" />
              <el-table-column prop="terminal" label="终端" width="110" />
              <el-table-column prop="loginAt" label="登录时间" min-width="150" />
              <el-table-column prop="from" label="来源 IP" min-width="130" show-overflow-tooltip />
            </el-table>
            <el-empty v-else description="无在线用户" :image-size="60" />

            <div class="section-title" style="margin-top: 16px">告警事件</div>
            <el-table :data="alertEvents" stripe size="small" max-height="220">
              <el-table-column label="时间" width="160">
                <template #default="{ row }">{{ fmtTime(row.startsAt || row.endsAt) }}</template>
              </el-table-column>
              <el-table-column prop="ruleName" label="规则" min-width="120" show-overflow-tooltip />
              <el-table-column label="状态" width="80">
                <template #default="{ row }">
                  <el-tag :type="row.state === 'firing' ? 'danger' : 'success'" size="small" effect="dark">{{ row.state === 'firing' ? '触发' : '恢复' }}</el-tag>
                </template>
              </el-table-column>
              <el-table-column label="级别" width="80">
                <template #default="{ row }">
                  <el-tag :type="sevType(row.severity)" size="small" effect="dark">{{ sevLabel(row.severity) }}</el-tag>
                </template>
              </el-table-column>
            </el-table>
            <el-empty v-if="!alertEvents.length" description="暂无告警" :image-size="50" />
          </div>
        </div>
      </el-tab-pane>

      <!-- ============ Tab 2：基础监控 ============ -->
      <el-tab-pane label="基础监控" name="monitor">
        <div class="tab-header">
          <span class="panel-title" style="margin: 0">基础监控</span>
        </div>
        <div class="panel-hint">说明：今日 / 昨天展示当天 0–24 点真实曲线；近 7 天 / 近 30 天展示每日平均值（每个点代表一天）。每个指标可独立切换时间范围。</div>
        <div class="monitor-grid">
          <div class="monitor-panel" v-for="p in monitorPanels" :key="p.key">
            <div class="monitor-panel-head">
              <span class="monitor-panel-title">{{ p.title }}</span>
              <el-select v-if="p.nic" v-model="netIface" size="small" @change="onNetIfaceChange" style="width: 120px">
                <el-option v-for="i in netIfaces" :key="i" :value="i" :label="i === 'all' ? '全部网卡' : i" />
              </el-select>
            </div>
            <div class="monitor-panel-tools">
              <el-radio-group v-model="panelRange[p.key]" size="small" @change="() => onPanelRangeChange(p.key)">
                <el-radio-button value="today">今日</el-radio-button>
                <el-radio-button value="yesterday">昨天</el-radio-button>
                <el-radio-button value="week">近7天</el-radio-button>
                <el-radio-button value="month">近30天</el-radio-button>
              </el-radio-group>
            </div>
            <div class="monitor-panel-desc">{{ p.desc }}</div>
            <div :ref="(el) => setMonitorRef(el, p.key)" class="monitor-chart"></div>
          </div>
        </div>
      </el-tab-pane>

      <!-- ============ Tab 3：进程监控 ============ -->
      <el-tab-pane label="进程监控" name="processes">
        <div class="tab-header">
          <span class="panel-title" style="margin: 0">进程监控</span>
          <div class="process-tools">
            <el-input v-model="procFullSearch" placeholder="搜索进程名 / PID / 命令" clearable size="small" :prefix-icon="Search" class="proc-full-search" />
            <el-button size="small" @click="loadProcessFull(selected)" :loading="loadingProcessFull">
              <el-icon><Refresh /></el-icon> 刷新
            </el-button>
          </div>
        </div>
        <el-alert
          v-if="loadErrors.process"
          type="error"
          :closable="false"
          show-icon
          class="load-error-bar"
        >
          <template #title>进程数据加载失败：{{ loadErrors.process }}
            <el-button link type="primary" @click="loadProcessFull(selected)">重试</el-button>
          </template>
        </el-alert>
        <el-table :data="filteredProcessFull" stripe size="small" v-loading="loadingProcessFull" max-height="calc(100vh - 320px)"
          :default-sort="{ prop: 'cpu', order: 'descending' }" table-layout="fixed">
          <el-table-column prop="name" label="进程名" min-width="140" show-overflow-tooltip sortable />
          <el-table-column label="CPU %" width="90" sortable :sort-method="(a, b) => a.cpu - b.cpu">
            <template #default="{ row }"><span class="mono">{{ row.cpu.toFixed(1) }}%</span></template>
          </el-table-column>
          <el-table-column label="内存" width="100" sortable :sort-method="(a, b) => (a.memBytes || 0) - (b.memBytes || 0)">
            <template #default="{ row }"><span class="mono">{{ fmtBytes(row.memBytes) }}</span></template>
          </el-table-column>
          <el-table-column label="网络IO ↓|↑" width="110">
            <template #default="{ row }">
              <span class="mono io-cell">{{ fmtBytes(row.netRead) }}</span> / <span class="mono io-cell">{{ fmtBytes(row.netWrite) }}</span>
            </template>
          </el-table-column>
          <el-table-column label="磁盘IO ↓|↑" width="110">
            <template #default="{ row }">
              <span class="mono io-cell">{{ fmtBytes(row.readBytes) }}</span> / <span class="mono io-cell">{{ fmtBytes(row.writeBytes) }}</span>
            </template>
          </el-table-column>
          <el-table-column prop="status" label="状态" width="85" show-overflow-tooltip>
            <template #default="{ row }">
              <el-tag :type="statusTagType(row.status)" size="small" effect="dark">{{ statusLabel(row.status) }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="打开文件" width="100" sortable :sort-method="(a, b) => (a.fds || 0) - (b.fds || 0)">
            <template #default="{ row }"><span class="mono">{{ row.fds || 0 }}</span></template>
          </el-table-column>
          <el-table-column prop="cmdline" label="启动命令" min-width="220" show-overflow-tooltip />
        </el-table>
        <div class="process-footer" v-if="processFullList.length > 0">
          <span class="process-count">共 {{ processFullList.length }} 条进程</span>
        </div>
        <el-empty v-if="!loadingProcessFull && !filteredProcessFull.length" description="无进程数据" :image-size="50" />
      </el-tab-pane>

      <!-- ============ Tab 4：端口监控 ============ -->
      <el-tab-pane label="端口监控" name="ports">
        <div class="tab-header">
          <span class="panel-title" style="margin: 0">端口监控</span>
          <div class="process-tools">
            <el-input v-model="portSearch" placeholder="搜索地址 / 端口 / 进程" clearable size="small" :prefix-icon="Search" class="proc-full-search" />
            <el-button size="small" @click="loadListeners(selected)" :loading="loadingListeners">
              <el-icon><Refresh /></el-icon> 刷新
            </el-button>
          </div>
        </div>
        <el-alert
          v-if="loadErrors.listeners"
          type="error"
          :closable="false"
          show-icon
          class="load-error-bar"
        >
          <template #title>监听端口加载失败：{{ loadErrors.listeners }}
            <el-button link type="primary" @click="loadListeners(selected)">重试</el-button>
          </template>
        </el-alert>
        <el-table :data="filteredListeners" stripe size="small" v-loading="loadingListeners" max-height="calc(100vh - 320px)"
          :default-sort="{ prop: 'port', order: 'ascending' }" table-layout="fixed">
          <el-table-column label="监听地址" min-width="140" show-overflow-tooltip>
            <template #default="{ row }"><span class="mono">{{ formatListenerAddr(row) }}</span></template>
          </el-table-column>
          <el-table-column prop="port" label="端口" width="80" sortable />
          <el-table-column prop="protocol" label="协议" width="75" show-overflow-tooltip>
            <template #default="{ row }">
              <el-tag :type="row.protocol.includes('tcp') ? '' : 'warning'" size="small">{{ row.protocol }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="process" label="使用端口的进程" min-width="120" show-overflow-tooltip />
          <el-table-column prop="exePath" label="进程路径" min-width="200" show-overflow-tooltip>
            <template #default="{ row }"><span class="mono">{{ row.exePath || '-' }}</span></template>
          </el-table-column>
        </el-table>
        <div class="process-footer" v-if="listenerList.length > 0">
          <span class="process-count">共 {{ listenerList.length }} 条监听</span>
        </div>
        <el-empty v-if="!loadingListeners && !filteredListeners.length" description="无监听端口数据" :image-size="50" />
      </el-tab-pane>

      <!-- ============ Tab 5：防火墙监控 ============ -->
      <el-tab-pane label="防火墙监控" name="firewall">
        <!-- 防火墙整体状态卡片 -->
        <div class="fw-status-card" v-loading="loadingFirewallStatus">
          <template v-if="firewallStatus">
            <div class="fw-status-row">
              <div class="fw-status-item">
                <span class="fw-status-label">防火墙后端</span>
                <el-tag :type="firewallStatus.supported ? 'success' : 'info'" size="small" effect="dark">{{ fwBackendDisplay }}</el-tag>
              </div>
              <div class="fw-status-item">
                <span class="fw-status-label">运行状态</span>
                <span :class="['fw-dot', firewallStatus.running ? 'on' : 'off']"></span>
                <span class="fw-status-val">{{ firewallStatus.running ? '运行中' : '未运行' }}</span>
              </div>
              <div class="fw-status-item">
                <span class="fw-status-label">开机自启</span>
                <span class="fw-status-val">{{ firewallStatus.enabled ? '是' : '否' }}</span>
              </div>
              <div class="fw-status-item" v-if="firewallStatus.version">
                <span class="fw-status-label">版本</span>
                <span class="fw-status-val mono">{{ firewallStatus.version }}</span>
              </div>
              <div class="fw-status-item" v-if="firewallStatus.defaultZone">
                <span class="fw-status-label">默认区域</span>
                <span class="fw-status-val">{{ firewallStatus.defaultZone }}</span>
              </div>
              <div class="fw-status-item" v-if="firewallStatus.activeZones">
                <span class="fw-status-label">活动区域</span>
                <span class="fw-status-val">{{ firewallStatus.activeZones }}</span>
              </div>
              <div class="fw-status-item">
                <span class="fw-status-label">规则数</span>
                <span class="fw-status-val">{{ firewallStatus.ruleCount }}</span>
              </div>
            </div>
            <div class="fw-status-msg" v-if="firewallStatus.message">{{ firewallStatus.message }}</div>
          </template>
          <el-empty v-else-if="loadErrors.firewallStatus && !loadingFirewallStatus" description="防火墙状态加载失败" :image-size="40" />
          <el-empty v-else-if="!loadingFirewallStatus" description="无防火墙状态数据（旧版本 Agent 或未上报）" :image-size="40" />
        </div>
        <div class="tab-header">
          <span class="panel-title" style="margin: 0">防火墙规则</span>
          <div class="process-tools">
            <el-input v-model="fwSearch" placeholder="搜索协议 / 端口 / 动作 / 链" clearable size="small" :prefix-icon="Search" class="proc-full-search" />
            <el-button size="small" @click="loadFirewallRules(selected)" :loading="loadingFirewall">
              <el-icon><Refresh /></el-icon> 刷新
            </el-button>
          </div>
        </div>
        <el-alert
          v-if="loadErrors.firewall"
          type="error"
          :closable="false"
          show-icon
          class="load-error-bar"
        >
          <template #title>防火墙规则加载失败：{{ loadErrors.firewall }}
            <el-button link type="primary" @click="loadFirewallRules(selected)">重试</el-button>
          </template>
        </el-alert>
        <el-table :data="filteredFirewall" stripe size="small" v-loading="loadingFirewall" max-height="calc(100vh - 320px)"
          table-layout="fixed">
          <el-table-column prop="backend" label="后端" width="95" show-overflow-tooltip>
            <template #default="{ row }">
              <el-tag size="small" effect="dark">{{ row.backend }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="chain" label="链/区域" width="100" show-overflow-tooltip />
          <el-table-column prop="ruleNum" label="#" width="45" align="center" />
          <el-table-column prop="action" label="动作" width="85" show-overflow-tooltip>
            <template #default="{ row }">
              <el-tag :type="fwActionType(row.action)" size="small">{{ row.action || '-' }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="protocol" label="协议" width="70" show-overflow-tooltip />
          <el-table-column prop="srcAddr" label="源地址" width="120" show-overflow-tooltip>
            <template #default="{ row }"><span class="mono">{{ row.srcAddr || '-' }}</span></template>
          </el-table-column>
          <el-table-column prop="dstPort" label="目标端口" width="90" show-overflow-tooltip>
            <template #default="{ row }"><span class="mono">{{ row.dstPort || '-' }}</span></template>
          </el-table-column>
          <el-table-column prop="options" label="完整规则" min-width="250" show-overflow-tooltip>
            <template #default="{ row }"><span class="mono fw-options">{{ row.options || '-' }}</span></template>
          </el-table-column>
        </el-table>
        <div class="process-footer" v-if="firewallRuleList.length > 0">
          <span class="process-count">共 {{ firewallRuleList.length }} 条规则（{{ firewallBackend }}）</span>
        </div>
        <el-empty v-if="!loadingFirewall && !filteredFirewall.length" description="无防火墙数据或未启用防火墙" :image-size="50" />
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<script setup>
import { ref, computed, reactive, onMounted, onUnmounted, watch, nextTick } from 'vue'
import { HomeFilled, ArrowRight, DocumentCopy, Search, Refresh } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { useRoute } from 'vue-router'
import http from '../api/http'
import { initChart, areaOption, areaMultiOption, monitorOption, gaugeOption, COLORS, rateShort } from '../charts/echarts'

let socket = null
let reconnectTimer = null
let nodeTimer = null
let alertTimer = null

const nodes = ref([])
const selected = ref('')
const route = useRoute()
const activeTab = ref('overview')
const procs = ref([])
const alertEvents = ref([])
const netIface = ref('all')
const netIfaces = ref(['all'])
const loadingNode = ref(false)
const autoRefresh = ref(true)
const procSearch = ref('')
const portStatuses = ref([])
// 实时数据是否已收到首帧（用于等待提示，避免首屏全 0 误导）
const rtReady = ref(false)
// 快照接口加载失败信息（进程/端口/防火墙），用于区分「加载失败」与「确实无数据」
const loadErrors = reactive({ process: '', listeners: '', firewall: '', firewallStatus: '' })

const rt = reactive({
  cpu: 0, mem: 0, disk: 0, net: '0 B/s', netSent: '0 B/s',
  netRecvTotal: '0 B', netSentTotal: '0 B',
  swap: 0, diskRead: '0 B/s', diskWrite: '0 B/s',
  load1: 0, load5: 0, load15: 0,
})

const nameToKey = {
  cpu_usage: 'cpu', mem_used_percent: 'mem', disk_used_percent: 'disk',
  network_recv_rate: 'net', network_sent_rate: 'netSent', swap_used_percent: 'swap',
  network_recv_total: 'netRecvTotal', network_sent_total: 'netSentTotal',
  disk_read_rate: 'diskRead', disk_write_rate: 'diskWrite',
  load1: 'load1', load5: 'load5', load15: 'load15',
}
const ioKeys = ['net', 'netSent', 'diskRead', 'diskWrite']
const byteKeys = ['netRecvTotal', 'netSentTotal']

// 实时趋势图缓冲：按指标拆分（磁盘 IO=读取/写入，网络 IO=接收/发送）
const buffers = { cpu: [], mem: [], diskRead: [], diskWrite: [], net: [], netSent: [] }
const charts = {}
const refs = {}
const setRef = (el, key) => { if (el) refs[key] = el }

// 实时趋势图定义：单序列(cpu/mem) 与 多序列(磁盘 IO/网络 IO)
const rtChartKeys = ['cpu', 'mem', 'diskio', 'netio']
const rtChartSeries = {
  cpu: ['cpu'],
  mem: ['mem'],
  diskio: ['diskRead', 'diskWrite'],
  netio: ['net', 'netSent'],
}
// 统一刷新所有实时趋势图（根据各自缓冲序列更新 series）
function refreshRealtime() {
  for (const k of rtChartKeys) {
    const c = charts[k]
    if (!c) continue
    c.setOption({ series: rtChartSeries[k].map((b) => ({ data: buffers[b] })) })
  }
}

// 渲染调度：WS 每秒推送多条指标，把 4 个趋势图 + 4 个仪表盘的
// setOption 合并到同一帧内执行（rAF），避免主线程被高频重绘拖慢
let renderRaf = 0
function scheduleRender() {
  if (renderRaf) return
  renderRaf = requestAnimationFrame(() => {
    renderRaf = 0
    if (document.hidden) return // 页面不可见时不渲染（数据继续缓冲）
    refreshRealtime()
    updateGauges()
  })
}

const monitorPanels = [
  { key: 'cpu', title: 'CPU 使用率', type: 'percent', metrics: [{ name: 'cpu_usage', label: 'CPU' }], desc: 'CPU 占用百分比；今日/昨天为当天真实曲线，近7/30天为每日平均值。' },
  { key: 'mem', title: '内存使用率', type: 'percent', metrics: [{ name: 'mem_used_percent', label: '内存' }], desc: '物理内存占用百分比；横轴与时间维度同 CPU。' },
  { key: 'load', title: '系统负载', type: 'load', metrics: [{ name: 'load1', label: '1分钟' }, { name: 'load5', label: '5分钟' }, { name: 'load15', label: '15分钟' }], desc: '系统平均负载三条折线（1/5/15 分钟），数值约等或超过 CPU 核心数表示偏忙。' },
  { key: 'swap', title: 'SWAP 使用率', type: 'percent', metrics: [{ name: 'swap_used_percent', label: 'SWAP' }], desc: '交换分区占用百分比。' },
  { key: 'disk', title: '磁盘占用率', type: 'percent', desc: '所有磁盘空间汇总占用百分比。' },
  { key: 'diskio', title: '磁盘 IO', type: 'rate', sumDevices: true, metrics: [{ name: 'disk_read_rate', label: '读取' }, { name: 'disk_write_rate', label: '写入' }], desc: '磁盘读取/写入速率（所有磁盘汇总），纵轴自适应 KB/s·MB/s。' },
  { key: 'net', title: '网络流量', type: 'rate', nic: true, metrics: [{ name: 'network_recv_rate', label: '接收' }, { name: 'network_sent_rate', label: '发送' }], desc: '网络接收/发送速率，右上角可切换网卡查看。' },
]
const panelColors = {
  cpu: COLORS.cyan,
  mem: COLORS.purple,
  load: [COLORS.cyan, COLORS.amber, COLORS.red],
  swap: COLORS.amber,
  disk: COLORS.blue,
  diskio: [COLORS.blue, COLORS.amber],
  net: [COLORS.green, COLORS.purple],
}
// 每个面板独立的时间范围（今日/昨天/近7天/近30天）
const panelRange = reactive({})
monitorPanels.forEach((p) => { panelRange[p.key] = 'today' })
const monitorEls = {}
const monitorCharts = {}
// 实时趋势卡片含义说明（横轴为最近约 60 次采样的时间，每秒一条）
const rtInfo = {
  cpu: '实时 CPU 占用百分比，最近 60 秒（每秒采样）。',
  mem: '实时物理内存占用百分比，最近 60 秒。',
  diskio: '实时磁盘读取 / 写入速率，最近 60 秒（蓝=读取，橙=写入）。',
  netio: '实时网络接收 / 发送速率，最近 60 秒（绿=接收，紫=发送）。',
}
function ensureMonitorChart(key) {
  if (monitorEls[key] && !monitorCharts[key]) {
    const c = initChart(monitorEls[key])
    c.setOption(monitorOption({}))
    monitorCharts[key] = c
  }
  return monitorCharts[key]
}
const setMonitorRef = (el, key) => {
  if (el) {
    monitorEls[key] = el
    if (activeTab.value === 'monitor') {
      nextTick(() => {
        ensureMonitorChart(key)
        if (monitorCharts[key]) monitorCharts[key].resize()
        loadMonitor(selected.value)
      })
    }
  } else {
    delete monitorEls[key]
    if (monitorCharts[key]) { monitorCharts[key].dispose(); delete monitorCharts[key] }
  }
}

// 主机名展示：有别名时显示 原始名（别名：别名）
function hostTitle(n) {
  if (!n) return '-'
  const dn = n.displayName && n.displayName.trim() ? n.displayName.trim() : ''
  return dn ? `${n.hostname}（别名：${dn}）` : n.hostname
}

const current = computed(() => nodes.value.find((n) => n.hostname === selected.value) || null)
const currentStatus = computed(() => (current.value?.status === 'online' ? 'online' : 'offline'))
const hostInfo = computed(() => current.value?.hostInfo || null)
const uptimeDays = computed(() => {
  const bt = hostInfo.value?.bootTime
  if (!bt) return '-'
  return Math.floor((Date.now() / 1000 - bt) / 86400)
})
const bootTimeText = computed(() => {
  const bt = hostInfo.value?.bootTime
  if (!bt) return '-'
  return fmtTime(bt)
})

// 环形图子标签：已用 / 总量
const memTotalText = computed(() => hostInfo.value?.memoryTotal ? fmtBytes(hostInfo.value.memoryTotal) : '-')
const memUsedText = computed(() => {
  const t = hostInfo.value?.memoryTotal
  return t ? fmtBytes(t * num(rt.mem) / 100) : '-'
})
const diskTotalText = computed(() => hostInfo.value?.diskTotal ? fmtBytes(hostInfo.value.diskTotal) : '-')
const diskUsedText = computed(() => hostInfo.value?.diskUsed ? fmtBytes(hostInfo.value.diskUsed) : '-')
const swapTotalText = computed(() => '')
const swapUsedText = computed(() => (num(rt.swap) > 0 ? round1(rt.swap) + '%' : '-'))

// 进程搜索过滤
const filteredProcs = computed(() => {
  const q = (procSearch.value || '').trim().toLowerCase()
  if (!q) return procs.value
  return procs.value.filter((p) => (p.name || '').toLowerCase().includes(q) || String(p.pid).includes(q))
})

// 环形图阈值配色：0-60 绿 / 60-80 橙 / 80-100 红
function gaugeColor(v) {
  const n = num(v)
  return n >= 80 ? COLORS.red : n >= 60 ? COLORS.amber : COLORS.green
}

// 一键复制
function copyText(t) {
  if (!t) return
  const s = String(t)
  if (navigator.clipboard?.writeText) {
    navigator.clipboard.writeText(s)
      .then(() => ElMessage.success('已复制: ' + s))
      .catch(() => fallbackCopy(s, '已复制: '))
  } else {
    fallbackCopy(s, '已复制: ')
  }
}

// 非安全上下文（HTTP）下 navigator.clipboard 不可用，用 execCommand 兜底
function fallbackCopy(text, prefix) {
  const ta = document.createElement('textarea')
  ta.value = text
  ta.style.position = 'fixed'
  ta.style.top = '-9999px'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.focus()
  ta.select()
  try {
    document.execCommand('copy')
    ElMessage.success((prefix || '已复制到剪贴板') + text)
  } catch {
    ElMessage.error('复制失败，请手动复制')
  } finally {
    document.body.removeChild(ta)
  }
}

function num(v) { const n = Number(v); return isFinite(n) ? n : 0 }
function round1(v) { return Number((Number(v) || 0).toFixed(1)) }
function fmtRate(bps) {
  const b = Number(bps || 0)
  if (b >= 1 << 30) return (b / (1 << 30)).toFixed(2) + ' GB/s'
  if (b >= 1 << 20) return (b / (1 << 20)).toFixed(2) + ' MB/s'
  if (b >= 1 << 10) return (b / (1 << 10)).toFixed(2) + ' KB/s'
  return b.toFixed(0) + ' B/s'
}
function fmtBytes(bytes) {
  const b = Number(bytes || 0)
  if (b >= 1 << 30) return (b / (1 << 30)).toFixed(2) + ' GB'
  if (b >= 1 << 20) return (b / (1 << 20)).toFixed(2) + ' MB'
  if (b >= 1 << 10) return (b / (1 << 10)).toFixed(2) + ' KB'
  return b.toFixed(0) + ' B'
}
function fmtTime(ts) {
  if (!ts) return '-'
  // 同时兼容 Unix 秒（如 bootTime）与 Unix 毫秒（如告警事件 startsAt/endsAt）：
  // 数值 < 1e12 视为秒（10 位 ~2025 年）；否则视为毫秒（13 位）。
  let n = num(ts)
  if (n < 1e12) n *= 1000
  const d = new Date(n)
  if (isNaN(d.getTime())) return '-'
  const p = (x) => String(x).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}
function sevType(s) {
  return { critical: 'danger', warning: 'warning', info: 'info' }[s] || 'info'
}
function sevLabel(s) {
  return { critical: '紧急', warning: '警告', info: '信息' }[s] || s
}
function rateClass(v) { const n = num(v); return n >= 90 ? 'red' : n >= 70 ? 'amber' : 'green' }
function memClass(v) { return num(v) >= 50 ? 'amber' : 'green' }

// ---------- WebSocket 实时指标 ----------
function connectWS(name) {
  if (socket) { try { socket.close() } catch (e) {} socket = null }
  // 切换主机后重置等待态，直到收到新主机首帧数据
  rtReady.value = false
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  const url = `${proto}://${location.host}/ws?topic=metrics&node=${encodeURIComponent(name)}`
  socket = new WebSocket(url)
  socket.onmessage = (ev) => {
    try {
      const msg = JSON.parse(ev.data)
      if (msg.type !== 'metrics' || !msg.data) return
      rtReady.value = true
      msg.data.forEach((d) => {
        const key = nameToKey[d.name]
        if (!key) return
        const val = d.value
        if (ioKeys.includes(key)) rt[key] = fmtRate(val)
        else if (byteKeys.includes(key)) rt[key] = fmtBytes(val)
        else rt[key] = round1(val)
        if (buffers[key]) {
          buffers[key].push([d.timestamp, val])
          if (buffers[key].length > 60) buffers[key].shift()
        }
      })
      scheduleRender()
    } catch (e) { /* ignore */ }
  }
  socket.onclose = () => {
    if (autoRefresh.value) reconnectTimer = setTimeout(() => { if (selected.value && autoRefresh.value) connectWS(selected.value) }, 3000)
  }
  socket.onerror = () => { try { socket.close() } catch (e) {} }
}

function updateGauges() {
  const setGauge = (chart, v) => {
    if (!chart) return
    const c = gaugeColor(v)
    chart.setOption({ series: [{ data: [{ value: num(v) }], progress: { itemStyle: { color: c } }, detail: { color: c } }] })
  }
  setGauge(charts.cpuGauge, rt.cpu)
  setGauge(charts.memGauge, rt.mem)
  setGauge(charts.diskGauge, rt.disk)
  setGauge(charts.swapGauge, rt.swap)
}

// ---------- 数据加载 ----------
// ============ 进程监控（完整列表） ============
const processFullList = ref([])
const procFullSearch = ref('')
const loadingProcessFull = ref(false)

async function loadProcessFull(hostname) {
  if (!hostname) return
  loadingProcessFull.value = true
  try {
    const data = await http.get('/api/v1/processes?hostname=' + encodeURIComponent(hostname))
    processFullList.value = Array.isArray(data.processes) ? data.processes : []
    loadErrors.process = ''
  } catch (err) {
    console.error('加载进程列表失败:', err)
    processFullList.value = []
    loadErrors.process = err.message || '加载失败'
  } finally {
    loadingProcessFull.value = false
  }
}

const filteredProcessFull = computed(() => {
  const q = procFullSearch.value.trim().toLowerCase()
  if (!q) return processFullList.value
  return processFullList.value.filter(p =>
    (p.name || '').toLowerCase().includes(q) ||
    String(p.pid || '').includes(q) ||
    (p.cmdline || '').toLowerCase().includes(q)
  )
})

function statusTagType(status) {
  switch ((status || '').toLowerCase()) {
    case 'running': return 'success'
    case 'sleep': case 'sleeping': return 'info'
    case 'stop': case 'stopped': return 'warning'
    case 'zombie': return 'danger'
    default: return ''
  }
}
function statusLabel(status) {
  switch ((status || '').toLowerCase()) {
    case 'running': return '运行中'
    case 'sleep': case 'sleeping': return '休眠中'
    case 'stop': case 'stopped': return '已停止'
    case 'zombie': return '僵尸'
    default: return status || '-'
  }
}

// ============ 端口监控（监听端口列表） ============
const listenerList = ref([])
const portSearch = ref('')
const loadingListeners = ref(false)

async function loadListeners(hostname) {
  if (!hostname) return
  loadingListeners.value = true
  try {
    const data = await http.get('/api/v1/query/listeners?hostname=' + encodeURIComponent(hostname))
    listenerList.value = Array.isArray(data.listeners) ? data.listeners : []
    loadErrors.listeners = ''
  } catch (err) {
    console.error('加载监听端口失败:', err)
    listenerList.value = []
    loadErrors.listeners = err.message || '加载失败'
  } finally {
    loadingListeners.value = false
  }
}

const filteredListeners = computed(() => {
  const q = portSearch.value.trim().toLowerCase()
  if (!q) return listenerList.value
  return listenerList.value.filter(l =>
    l.addr.toLowerCase().includes(q) ||
    String(l.port).includes(q) ||
    (l.process || '').toLowerCase().includes(q)
  )
})

function formatListenerAddr(row) {
  if (!row.addr || row.addr === '0.0.0.0' || row.addr === '::') return ':' + row.port
  return row.addr + ':' + row.port
}

// ============ 防火墙监控（规则列表） ============
const firewallRuleList = ref([])
const fwSearch = ref('')
const loadingFirewall = ref(false)
const firewallStatus = ref(null)
const loadingFirewallStatus = ref(false)

async function loadFirewallRules(hostname) {
  if (!hostname) return
  loadingFirewall.value = true
  try {
    const data = await http.get('/api/v1/query/firewall?hostname=' + encodeURIComponent(hostname))
    firewallRuleList.value = Array.isArray(data.rules) ? data.rules : []
    loadErrors.firewall = ''
  } catch (err) {
    console.error('加载防火墙规则失败:', err)
    firewallRuleList.value = []
    loadErrors.firewall = err.message || '加载失败'
  } finally {
    loadingFirewall.value = false
  }
}

async function loadFirewallStatus(hostname) {
  if (!hostname) return
  loadingFirewallStatus.value = true
  try {
    const data = await http.get('/api/v1/query/firewall/status?hostname=' + encodeURIComponent(hostname))
    firewallStatus.value = data.status || null
    loadErrors.firewallStatus = ''
  } catch (err) {
    console.error('加载防火墙状态失败:', err)
    firewallStatus.value = null
    loadErrors.firewallStatus = err.message || '加载失败'
  } finally {
    loadingFirewallStatus.value = false
  }
}

const fwBackendDisplay = computed(() => {
  const s = firewallStatus.value
  if (!s || !s.backend) return '-'
  if (s.backend === 'none') return s.running ? '未运行' : '未启用'
  return s.backend
})

const filteredFirewall = computed(() => {
  const q = fwSearch.value.trim().toLowerCase()
  if (!q) return firewallRuleList.value
  return firewallRuleList.value.filter(r =>
    (r.backend || '').toLowerCase().includes(q) ||
    (r.chain || '').toLowerCase().includes(q) ||
    (r.action || '').toLowerCase().includes(q) ||
    (r.protocol || '').toLowerCase().includes(q) ||
    (r.dstPort || '').toLowerCase().includes(q) ||
    (r.options || '').toLowerCase().includes(q)
  )
})

const firewallBackend = computed(() => {
  if (!firewallRuleList.value.length) return '-'
  return firewallRuleList.value[0].backend || '-'
})

function fwActionType(action) {
  switch ((action || '').toUpperCase()) {
    case 'ACCEPT': return 'success'
    case 'DROP': return 'danger'
    case 'REJECT': return 'danger'
    case 'LOG': return 'warning'
    case 'RETURN': return 'info'
    default: return ''
  }
}

async function loadNodes() {
  try {
    const data = await http.get('/api/v1/nodes')
    const list = data.nodes || []
    if (!selected.value && list.length) selected.value = list[0].hostname
    nodes.value = list
  } catch (e) { /* ignore */ }
}

async function loadProcesses(name) {
  try {
    const data = await http.get('/api/v1/processes?hostname=' + encodeURIComponent(name))
    procs.value = (data.processes || []).slice(0, 10)
  } catch (e) { /* ignore */ }
}

async function loadAlerts(name) {
  try {
    const data = await http.get('/api/v1/alerts?node=' + encodeURIComponent(name) + '&limit=50')
    alertEvents.value = data.alerts || []
  } catch (e) { /* ignore */ }
}

async function loadPortStatuses(name) {
  try {
    const upData = await http.get(`/api/v1/query/latest?node=${encodeURIComponent(name)}&metric=port_up`)
    const latData = await http.get(`/api/v1/query/latest?node=${encodeURIComponent(name)}&metric=port_latency`)
    const upMap = {}
    if (upData.series) for (const s of upData.series) {
      const port = s.labels?.port
      if (port && s.points?.length > 0) upMap[port] = s.points[s.points.length - 1].value > 0
    }
    const latMap = {}
    if (latData.series) for (const s of latData.series) {
      const port = s.labels?.port
      if (port && s.points?.length > 0) latMap[port] = s.points[s.points.length - 1].value
    }
    // 兼容仅返回单点的旧 Server（Point 不携带 labels，通常无法聚合端口）。
    if (!Object.keys(upMap).length && upData.point?.labels?.port) {
      upMap[upData.point.labels.port] = upData.point.value > 0
    }
    if (!Object.keys(latMap).length && latData.point?.labels?.port) {
      latMap[latData.point.labels.port] = latData.point.value
    }
    portStatuses.value = Object.keys(upMap).map(port => ({
      port,
      up: upMap[port],
      latency: latMap[port] || 0,
    })).sort((a, b) => a.port.localeCompare(b.port))
  } catch (e) { portStatuses.value = [] }
}

function onSelect(name) {
  reloadHost(name)
}

// ---------- 基础监控历史 ----------
function round2(v) { return Number((Number(v) || 0).toFixed(2)) }

function rangeBounds(mode) {
  const now = Date.now()
  const ds = new Date()
  ds.setHours(0, 0, 0, 0)
  const dayStart = ds.getTime()
  switch (mode) {
    case 'today': return [dayStart, now]
    case 'yesterday': return [dayStart - 86400000, dayStart]
    case 'week': return [dayStart - 6 * 86400000, now]
    case 'month': return [dayStart - 29 * 86400000, now]
  }
  return [now - 86400000, now]
}

// 抓取单指标原始序列（时间戳为毫秒，与后端 querier 一致）
async function fetchRaw(name, metric, start, end, step) {
  const d = await http.get(`/api/v1/query/range?node=${encodeURIComponent(name)}&metric=${encodeURIComponent(metric)}&start=${start}&end=${end}&step=${step}`)
  return (d.series || []).map((s) => ({
    labels: s.labels || {},
    points: (s.points || []).map((pt) => [pt.timestamp, Number(pt.value)]),
  }))
}

// 多序列按时间戳求和合并为一条
function sumSeriesByTs(list) {
  const m = new Map()
  for (const s of list) for (const [ts, v] of s.points) m.set(ts, (m.get(ts) || 0) + Number(v))
  return Array.from(m.entries()).map(([ts, v]) => [ts, round2(v)]).sort((a, b) => a[0] - b[0])
}

// 按自然日分桶求均值（用于近7/30天视图）
function dailyAvg(points) {
  const m = new Map()
  for (const [ts, v] of points) {
    const d = new Date(ts)
    d.setHours(0, 0, 0, 0)
    const k = d.getTime()
    const e = m.get(k) || [0, 0]
    e[0] += Number(v)
    e[1] += 1
    m.set(k, e)
  }
  return Array.from(m.entries()).map(([k, e]) => [k, round2(e[0] / e[1])]).sort((a, b) => a[0] - b[0])
}

async function loadDiskOccupancy(name, start, end, step) {
  const [usedD, totalD] = await Promise.all([
    http.get(`/api/v1/query/range?node=${encodeURIComponent(name)}&metric=disk_used&start=${start}&end=${end}&step=${step}`),
    http.get(`/api/v1/query/range?node=${encodeURIComponent(name)}&metric=disk_total&start=${start}&end=${end}&step=${step}`),
  ])
  const sumByTs = (series) => {
    const m = new Map()
    for (const s of series || []) for (const p of s.points || []) {
      const ts = p.timestamp
      m.set(ts, (m.get(ts) || 0) + Number(p.value))
    }
    return m
  }
  const used = sumByTs(usedD.series)
  const total = sumByTs(totalD.series)
  const pts = []
  for (const [ts, t] of total) if (t > 0) pts.push([ts, Number(((used.get(ts) || 0) / t * 100).toFixed(2))])
  pts.sort((a, b) => a[0] - b[0])
  return pts
}

// 网络流量：按网卡过滤/汇总，返回接收、发送两条序列
async function loadNetSeries(name, start, end, step) {
  const raw = []
  for (const m of ['network_recv_rate', 'network_sent_rate']) {
    const d = await http.get(`/api/v1/query/range?node=${encodeURIComponent(name)}&metric=${m}&start=${start}&end=${end}&step=${step}`)
    ;(d.series || []).forEach((s) => raw.push({
      name: m,
      labels: s.labels || {},
      points: (s.points || []).map((pt) => [pt.timestamp, Number(pt.value)]),
    }))
  }
  const ifaces = new Set()
  raw.forEach((s) => { if (s.labels.iface) ifaces.add(s.labels.iface) })
  netIfaces.value = ['all', ...Array.from(ifaces).sort()]
  if (netIface.value !== 'all' && !ifaces.has(netIface.value)) netIface.value = 'all'
  const pick = (mname) => {
    let ss = raw.filter((s) => s.name === mname)
    if (netIface.value !== 'all') ss = ss.filter((s) => s.labels.iface === netIface.value)
    return sumSeriesByTs(ss)
  }
  return [
    { name: '接收', data: pick('network_recv_rate') },
    { name: '发送', data: pick('network_sent_rate') },
  ]
}

function xFormatterFor(daily) {
  if (daily) {
    return (val) => { const d = new Date(val); const z = (x) => String(x).padStart(2, '0'); return `${z(d.getMonth() + 1)}-${z(d.getDate())}` }
  }
  return (val) => { const d = new Date(val); const z = (x) => String(x).padStart(2, '0'); return `${z(d.getHours())}:${z(d.getMinutes())}` }
}

async function loadMonitor(name, onlyKey) {
  if (!name) return
  for (const p of monitorPanels) {
    if (onlyKey && p.key !== onlyKey) continue
    const chart = monitorCharts[p.key]
    if (!chart) continue
    const mode = panelRange[p.key] || 'today'
    const [start, end] = rangeBounds(mode)
    const daily = mode === 'week' || mode === 'month'
    const step = daily ? 3600000 : 1800000 // 毫秒
    try {
      let series = []
      if (p.key === 'disk') {
        series = [{ name: '磁盘', data: await loadDiskOccupancy(name, start, end, step) }]
      } else if (p.key === 'net') {
        series = await loadNetSeries(name, start, end, step)
      } else {
        const rawByMetric = {}
        for (const m of p.metrics) rawByMetric[m.name] = await fetchRaw(name, m.name, start, end, step)
        series = p.metrics.map((m) => {
          const list = rawByMetric[m.name] || []
          const data = p.sumDevices ? sumSeriesByTs(list) : (list.length ? list[0].points : [])
          return { name: m.label, data }
        })
      }
      // daily 模式直接用原始小时级数据（step=1h），不再按日聚合——
      // 部署初期只有 1-2 天数据时，dailyAvg 会坍缩成单点导致折线无法渲染。
      // 近7天≈168点、近30天≈720点，ECharts time 轴可平滑处理。
      const colors = panelColors[p.key]
      const yFormatter = p.type === 'percent' ? (v) => v + '%'
        : p.type === 'rate' ? rateShort
        : (v) => round1(v)
      const tipFmt = p.type === 'percent' ? (v) => (v == null ? '-' : round1(v) + '%')
        : p.type === 'rate' ? (v) => (v == null ? '-' : fmtRate(v))
        : (v) => (v == null ? '-' : round1(v))
      chart.setOption(monitorOption({
        yMin: 0,
        yMax: p.type === 'percent' ? 100 : undefined,
        xMin: daily ? start : undefined,
        xMax: daily ? end : undefined,
        yFormatter,
        tipFormatter: tipFmt,
        xFormatter: xFormatterFor(daily),
        series: series.map((s, i) => ({ name: s.name, color: Array.isArray(colors) ? colors[i] : colors, data: s.data })),
      }), true)
    } catch (e) { console.warn('[monitor] 加载失败', p.key, mode, e) }
  }
}

function onNetIfaceChange() {
  if (activeTab.value === 'monitor') loadMonitor(selected.value, 'net')
}

function initMonitorCharts() {
  for (const p of monitorPanels) {
    ensureMonitorChart(p.key)
  }
  Object.values(monitorCharts).forEach((c) => c.resize())
}

function onPanelRangeChange(key) {
  if (activeTab.value === 'monitor') loadMonitor(selected.value, key)
}

// 切换/进入主机或切换 Tab 时，按当前激活的 Tab 重新拉取对应快照数据，
// 避免切主机后端口/防火墙等 Tab 仍显示上一台主机的数据（不刷新）。
function loadActiveTab(node) {
  if (!node) return
  switch (activeTab.value) {
    case 'monitor':
      nextTick(() => {
        initMonitorCharts()
        loadMonitor(node)
        setTimeout(() => Object.values(monitorCharts).forEach((c) => c.resize()), 200)
      })
      break
    case 'processes':
      loadProcessFull(node)
      break
    case 'ports':
      loadListeners(node)
      break
    case 'firewall':
      loadFirewallRules(node)
      loadFirewallStatus(node)
      break
    case 'overview':
      // 切回概览页时重建实时图/环形图（v-if 会销毁旧 DOM，需重新绑定）
      nextTick(() => {
        initRealtimeCharts()
        setTimeout(() => rtChartKeys.forEach((k) => charts[k] && charts[k].resize()), 200)
      })
      break
  }
}

// 主机切换（下拉框或路由变化）时的公共刷新逻辑
function reloadHost(name) {
  selected.value = name
  connectWS(name)
  loadProcesses(name)
  loadAlerts(name)
  loadPortStatuses(name)
  loadActiveTab(name)
}

watch(activeTab, () => {
  loadActiveTab(selected.value)
})

function initRealtimeCharts() {
  // 先释放已有实例（切回概览页重复初始化时避免泄漏/空白）
  for (const k of rtChartKeys) {
    if (charts[k]) { charts[k].dispose(); delete charts[k] }
  }
  const defs = {
    cpu: { multi: false, color: COLORS.cyan, unit: '%' },
    mem: { multi: false, color: COLORS.purple, unit: '%' },
    diskio: { multi: true, defs: [{ name: '读取', color: COLORS.blue }, { name: '写入', color: COLORS.amber }] },
    netio: { multi: true, defs: [{ name: '接收', color: COLORS.green }, { name: '发送', color: COLORS.purple }] },
  }
  for (const k of rtChartKeys) {
    if (refs[k]) {
      charts[k] = initChart(refs[k])
      const d = defs[k]
      charts[k].setOption(d.multi ? areaMultiOption(d.defs) : areaOption(d.color, d.unit))
    }
  }
  const gaugeColors = { cpuGauge: COLORS.green, memGauge: COLORS.green, diskGauge: COLORS.green, swapGauge: COLORS.green }
  for (const g of ['cpuGauge', 'memGauge', 'diskGauge', 'swapGauge']) {
    if (charts[g]) { charts[g].dispose(); delete charts[g] }
    if (refs[g]) {
      charts[g] = initChart(refs[g])
      charts[g].setOption(gaugeOption(gaugeColors[g], ''))
    }
  }
  updateGauges()
}

// 路由参数变化（从列表点击不同主机，/node/:name 复用组件实例）时同步切换节点
watch(
  () => route.params.name,
  (name) => {
    if (!name) return
    reloadHost(name)
  }
)

onMounted(async () => {
  if (route.params.name) selected.value = route.params.name
  await loadNodes()
  if (selected.value) {
    connectWS(selected.value)
    loadProcesses(selected.value)
    loadProcessFull(selected.value)
    loadAlerts(selected.value)
    loadPortStatuses(selected.value)
  }
  await nextTick()
  initRealtimeCharts()
  nodeTimer = setInterval(loadNodes, 15000)
  alertTimer = setInterval(() => { if (selected.value) loadAlerts(selected.value) }, 30000)
})

onUnmounted(() => {
  if (socket) { try { socket.close() } catch (e) {} }
  if (reconnectTimer) clearTimeout(reconnectTimer)
  if (nodeTimer) clearInterval(nodeTimer)
  if (alertTimer) clearInterval(alertTimer)
  if (renderRaf) cancelAnimationFrame(renderRaf)
  renderRaf = 0
  Object.values(charts).forEach((c) => c.dispose && c.dispose())
  Object.values(monitorCharts).forEach((c) => c.dispose && c.dispose())
})
</script>

<style scoped>
.node-view { display: flex; flex-direction: column; gap: 16px; }

/* Tabs */
.node-tabs { border-radius: 8px; overflow: hidden; }
.node-tabs :deep(.el-tabs__header) { background: rgba(255,255,255,0.04); margin: 0; }
.node-tabs :deep(.el-tabs__content) { padding: 16px 20px; }
.tab-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 14px; }
.port-section { margin-top: 16px; }
.port-list { display: flex; flex-wrap: wrap; gap: 8px; }
.port-item { display: inline-flex; align-items: center; gap: 6px; padding: 6px 12px; border-radius: 6px; font-size: 13px; background: rgba(255,255,255,0.04); }
.port-item.up { border-left: 3px solid var(--accent); }
.port-item.down { border-left: 3px solid var(--danger); opacity: 0.7; }
.port-dot { width: 6px; height: 6px; border-radius: 50%; }
.port-item.up .port-dot { background: var(--accent); box-shadow: 0 0 4px var(--accent-glow); }
.port-item.down .port-dot { background: var(--danger); }
.port-num { font-family: var(--mono); font-weight: 600; }
.port-state { color: var(--text-dim); font-size: 13px; }
.port-latency { color: var(--text-muted); font-size: 13px; }

.section-title {
  margin: 18px 0 10px;
  font-size: 14px;
  font-weight: 600;
  color: var(--text-main);
  padding-left: 10px;
  border-left: 3px solid var(--el-color-primary);
}
.section-head { display: flex; align-items: center; justify-content: space-between; }
.proc-search { width: 220px; }

/* 设备信息 */
.device-grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; }
.dev-item {
  min-width: 0;
  padding: 10px 12px;
  border-radius: 6px;
  background: rgba(255,255,255,0.035);
  border: 1px solid var(--border-soft);
}
.dev-item span { display: block; margin-bottom: 6px; font-size: 13px; font-weight: 400; color: var(--label); }
.dev-item strong {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 14px;
  font-weight: 600;
  color: var(--text-main);
  font-variant-numeric: tabular-nums;
}

/* 环形图 */
/* 面包屑 */
.breadcrumb { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; font-size: 13px; color: var(--text-dim); padding: 0 2px; }
.breadcrumb .bc-host-select { width: 220px; }
.breadcrumb .bc-ip { margin-left: 2px; color: var(--text-muted); font-size: 13px; }
.breadcrumb .bc-home { color: var(--el-color-primary); }
.breadcrumb .bc-sep { font-size: 13px; opacity: 0.6; }
.breadcrumb .bc-item { color: var(--text-dim); }
.breadcrumb .bc-link { cursor: pointer; transition: color 0.15s; }
.breadcrumb .bc-link:hover { color: var(--el-color-primary); }

.status-pill { display: inline-flex; align-items: center; gap: 6px; margin-left: 10px; padding: 3px 10px; border-radius: 999px; font-size: 13px; font-weight: 600; }
.status-pill .dot { width: 8px; height: 8px; border-radius: 50%; background: currentColor; box-shadow: 0 0 6px currentColor; }
.status-pill.online { color: var(--chart-green); background: rgba(34, 197, 94, 0.12); }
.status-pill.offline { color: var(--danger); background: var(--danger-dim); }

/* 复制按钮 */
.copyable { display: inline-flex; align-items: center; gap: 4px; }
.copy-btn { cursor: pointer; opacity: 0.45; transition: opacity 0.15s, color 0.15s; }
.copy-btn:hover { opacity: 1; color: var(--el-color-primary); }
.with-copy { display: flex; align-items: center; gap: 6px; }

.gauge-row { display: grid; grid-template-columns: repeat(4, 1fr); gap: 16px; }
.gauge-card {
  display: flex;
  flex-direction: column;
  align-items: center;
  padding: 12px;
  border-radius: 8px;
  background: rgba(255,255,255,0.03);
  border: 1px solid var(--border-soft);
}
.gauge { width: 100%; height: 130px; }
.gauge-label { text-align: center; margin-top: 4px; font-size: 13px; color: var(--text-dim); }
.gauge-sub { text-align: center; margin-top: 2px; font-size: 13px; color: var(--text-main); opacity: 0.85; font-variant-numeric: tabular-nums; }

/* 实时趋势 */
.metric-grid { display: grid; grid-template-columns: repeat(4, 1fr); gap: 16px; }
.metric-card { padding: 14px 16px; border-radius: 8px; background: rgba(255,255,255,0.03); border: 1px solid var(--border-soft); }
.mc-head { display: flex; justify-content: space-between; align-items: baseline; margin-bottom: 8px; }
.mc-label { font-size: 14px; font-weight: 400; color: var(--label); letter-spacing: 0.2px; }
.mc-value { font-size: 26px; font-weight: 700; font-family: var(--mono); line-height: 1.1; }
.mc-value small { font-size: 13px; font-weight: 400; margin-left: 2px; }
.mc-value.green { color: var(--accent); }
.mc-value.amber { color: var(--warn); }
.mc-value.red { color: var(--danger); }
.mc-value.cyan { color: var(--info); }
.mc-stats { display: flex; gap: 16px; align-items: baseline; }
.mc-stat { display: inline-flex; align-items: baseline; gap: 5px; font-size: 13px; color: var(--text-dim); }
.mc-stat b { font-family: var(--mono); font-weight: 700; font-size: 15px; }
.mc-stat .dot { width: 8px; height: 8px; border-radius: 50%; display: inline-block; }
.mc-chart { height: 100px; }
.mc-desc { margin-top: 8px; font-size: 13px; line-height: 1.4; color: var(--text-dim); opacity: 0.8; }

/* 说明文字 */
.panel-hint { margin: 8px 0 12px; font-size: 13px; line-height: 1.5; color: var(--text-dim); opacity: 0.85; }

/* 实时等待 / 加载失败提示 */
.rt-waiting { margin-bottom: 12px; }
.load-error-bar { margin-bottom: 12px; }
.load-error-bar :deep(.el-button--primary) { font-size: 13px; }

/* IO */

.cyan { color: var(--info); }
.amber { color: var(--warn); }

/* 底部：进程 + 用户/告警 */
.bottom-row { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; }
.user-list { display: flex; flex-wrap: wrap; gap: 8px; padding: 8px 0; }
.user-tag { font-family: var(--mono); }
.proc-bar { display: flex; align-items: center; gap: 8px; }
.proc-bar .bar { flex: 1; height: 5px; background: rgba(255,255,255,0.08); border-radius: 3px; overflow: hidden; }
.bar-fill { height: 100%; border-radius: 3px; }
.bar-fill.green { background: var(--accent); }
.bar-fill.amber { background: var(--warn); }
.bar-fill.cyan { background: var(--info); }
.proc-bar span { width: 40px; text-align: right; font-size: 13px; color: var(--text-dim); }

/* 基础监控 */
.monitor-grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 16px; }
.monitor-panel { padding: 12px 14px; border-radius: 8px; background: rgba(255,255,255,0.03); border: 1px solid var(--border-soft); }
.monitor-panel-head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; gap: 8px; }
.monitor-panel-tools { margin-bottom: 6px; }
.monitor-panel-title { font-size: 13px; color: var(--text-main); font-weight: 600; }
.monitor-panel-desc { margin-bottom: 6px; font-size: 13px; line-height: 1.4; color: var(--text-dim); opacity: 0.8; }
.monitor-chart { height: 240px; }

@media (max-width: 1100px) {
  .device-grid, .gauge-row, .metric-grid { grid-template-columns: repeat(2, 1fr); }
  .bottom-row, .monitor-grid { grid-template-columns: 1fr; }
}
@media (max-width: 640px) {
  .device-grid, .gauge-row, .metric-grid { grid-template-columns: 1fr; }
}

/* 进程监控 Tab */
.process-tools { display: flex; align-items: center; gap: 8px; }
.proc-full-search { width: 280px; }
.process-footer { margin-top: 10px; font-size: 13px; color: var(--text-dim); }
.process-count { font-family: var(--mono); }
.io-cell { color: var(--text-muted); }
.fw-options { color: var(--text-dim); font-size: 13px; }

/* 防火墙状态卡片 */
.fw-status-card {
  margin-bottom: 14px;
  padding: 12px 16px;
  border: 1px solid var(--border, #2a3346);
  border-radius: 8px;
  background: rgba(255, 255, 255, 0.03);
  min-height: 56px;
}
.fw-status-row { display: flex; flex-wrap: wrap; gap: 10px 28px; align-items: center; }
.fw-status-item { display: flex; align-items: center; gap: 6px; font-size: 13px; }
.fw-status-label { color: var(--text-muted, #8a93a6); }
.fw-status-val { color: var(--text, #e6e9f0); font-weight: 500; }
.fw-dot { width: 8px; height: 8px; border-radius: 50%; display: inline-block; }
.fw-dot.on { background: #2ec27e; box-shadow: 0 0 6px rgba(46,194,126,.7); }
.fw-dot.off { background: #8a93a6; }
.fw-status-msg { margin-top: 8px; font-size: 13px; color: var(--text-dim, #8a93a6); }
</style>
