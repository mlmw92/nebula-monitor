<template>
  <div class="hosts-view">
    <!-- 顶部操作栏 -->
    <div class="glass panel toolbar">
      <div class="toolbar-left">
        <el-radio-group v-model="statusFilter" size="small">
          <el-radio-button value="">全部 ({{ nodes.length }})</el-radio-button>
          <el-radio-button value="online">在线 ({{ onlineCount }})</el-radio-button>
          <el-radio-button value="offline">离线 ({{ offlineCount }})</el-radio-button>
          <el-radio-button value="warning">异常 ({{ warningCount }})</el-radio-button>
        </el-radio-group>
      </div>
      <div class="toolbar-right">
        <el-select v-model="groupFilter" placeholder="分组" clearable size="small" style="width: 120px">
          <el-option v-for="g in groups" :key="g.name" :value="g.name" :label="g.name" />
        </el-select>
        <el-button :icon="Setting" size="small" plain @click="showGroupManage = true">分组管理</el-button>
        <el-input
          v-model="keyword"
          placeholder="搜索主机名 / IP"
          size="small"
          clearable
          style="width: 200px"
        >
          <template #prefix><el-icon><Search /></el-icon></template>
        </el-input>
        <el-popover placement="bottom-end" :width="220" trigger="click">
          <template #reference>
            <el-button :icon="Operation" size="small" plain>列设置</el-button>
          </template>
          <div class="col-set">
            <div class="col-set-title">显示列</div>
            <el-checkbox-group v-model="selectedCols" class="col-set-group">
              <el-checkbox v-for="c in colOptions" :key="c.key" :value="c.key" :label="c.label" />
            </el-checkbox-group>
          </div>
        </el-popover>
        <el-button type="primary" :icon="Plus" size="small" @click="openAddNode">添加主机</el-button>
      </div>
    </div>

    <!-- 刷新控制条 -->
    <div class="glass panel refresh-bar">
      <div class="refresh-left">
        <el-tooltip :content="refreshInterval === 0 ? '已暂停自动刷新' : `${countdown}s 后刷新`" placement="top">
          <el-button :icon="Refresh" size="small" circle @click="manualRefresh" />
        </el-tooltip>
        <span class="refresh-text" v-if="lastRefresh">上次刷新：{{ lastRefresh }}</span>
        <el-tag v-if="loadError" type="danger" size="small" effect="dark">{{ loadError }}</el-tag>
      </div>
      <div class="refresh-right">
        <span class="refresh-label">自动刷新</span>
        <el-select v-model="refreshInterval" size="small" style="width: 100px" @change="onIntervalChange">
          <el-option :value="0" label="关闭" />
          <el-option :value="10" label="10 秒" />
          <el-option :value="20" label="20 秒" />
          <el-option :value="30" label="30 秒" />
          <el-option :value="60" label="60 秒" />
        </el-select>
        <span class="countdown" v-if="refreshInterval > 0">{{ countdown }}s</span>
      </div>
    </div>

    <!-- 主机列表 -->
    <div class="glass panel">
      <el-table
        :data="pagedNodes"
        stripe
        style="width: 100%"
        empty-text="暂无主机"
        @row-click="(r) => goDetail(r)"
        @sort-change="onSortChange"
        class="host-table"
        :row-class-name="rowClass"
      >
        <el-table-column v-if="colVisible('host')" label="主机名称 / IP" prop="hostname" sortable="custom" min-width="160">
          <template #default="{ row }">
            <div class="host-name">
              <div class="hn-top">
                <OsIcon :os="row.os" />
                <span class="hn-name">{{ row.displayName || row.hostname }}</span>
                <el-button
                  link
                  size="small"
                  :icon="Edit"
                  class="hn-edit"
                  title="编辑显示名"
                  @click.stop="editName(row)"
                />
              </div>
              <span class="hn-ip">{{ row.ip || '-' }}</span>
            </div>
          </template>
        </el-table-column>

        <el-table-column v-if="colVisible('status')" label="状态" prop="status" sortable="custom" min-width="80">
          <template #default="{ row }">
            <span class="status-led" :class="row.status === 'online' ? 'on' : 'off'"></span>
            <span :class="['status-text', row.status === 'online' ? 'on' : 'off']">
              {{ row.status === 'online' ? '在线' : '离线' }}
            </span>
          </template>
        </el-table-column>

        <el-table-column v-if="colVisible('group')" label="分组" prop="group" sortable="custom" min-width="100">
          <template #default="{ row }">
            <el-dropdown trigger="click" @command="(cmd) => changeGroup(row, cmd)" @click.stop>
              <span class="group-tag clickable" @click.stop>
                {{ row.group || 'default' }}
                <el-icon class="group-arrow"><ArrowDown /></el-icon>
              </span>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item
                    v-for="g in groups"
                    :key="g.name"
                    :command="g.name"
                    :disabled="g.name === (row.group || 'default')"
                  >
                    {{ g.name }}
                  </el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
          </template>
        </el-table-column>

        <el-table-column v-if="colVisible('version')" label="Agent 版本" prop="version" sortable="custom" min-width="110">
          <template #default="{ row }">
            <span class="ver-cell" :class="verClass(row.version)">
              <span v-if="needUpgrade(row)" class="ver-dot"></span>{{ row.version || '-' }}
            </span>
          </template>
        </el-table-column>

        <el-table-column v-if="colVisible('cpu')" label="CPU" prop="cpu" sortable="custom" min-width="90">
          <template #default="{ row }">
            <div class="usage-cell" v-if="hasMetric(row)">
              <div class="mini-bar">
                <div class="mini-bar-fill" :class="rateClass(m(row).cpu)" :style="{ width: pct(m(row).cpu) + '%' }"></div>
              </div>
              <span :class="['rate-sm', rateClass(m(row).cpu)]">{{ fmtNum(m(row).cpu) }}%</span>
            </div>
            <span v-else class="dim">--</span>
          </template>
        </el-table-column>

        <el-table-column v-if="colVisible('mem')" label="内存" prop="mem" sortable="custom" min-width="90">
          <template #default="{ row }">
            <div class="usage-cell" v-if="hasMetric(row)">
              <div class="mini-bar">
                <div class="mini-bar-fill" :class="rateClass(m(row).mem)" :style="{ width: pct(m(row).mem) + '%' }"></div>
              </div>
              <span :class="['rate-sm', rateClass(m(row).mem)]">{{ fmtNum(m(row).mem) }}%</span>
            </div>
            <span v-else class="dim">--</span>
          </template>
        </el-table-column>

        <el-table-column v-if="colVisible('disk')" label="磁盘使用" prop="disk" sortable="custom" min-width="100">
          <template #default="{ row }">
            <div class="usage-cell" v-if="hasMetric(row)">
              <div class="mini-bar">
                <div class="mini-bar-fill" :class="rateClass(m(row).disk)" :style="{ width: pct(m(row).disk) + '%' }"></div>
              </div>
              <span :class="['rate-sm', rateClass(m(row).disk)]">{{ fmtNum(m(row).disk) }}%</span>
            </div>
            <span v-else class="dim">--</span>
          </template>
        </el-table-column>

        <el-table-column v-if="colVisible('netIn')" label="流量↓/↑" prop="netIn" sortable="custom" min-width="120" align="right">
          <template #default="{ row }">
            <div class="net-traffic" v-if="hasMetric(row)">
              <div>↓ {{ fmtRate(m(row).netIn) }}/s</div>
              <div class="net-up">↑ {{ fmtRate(m(row).netOut) }}/s</div>
            </div>
            <span v-else class="dim">--</span>
          </template>
        </el-table-column>

        <el-table-column v-if="colVisible('load1')" label="负载" prop="load1" sortable="custom" min-width="70">
          <template #default="{ row }">
            <span :class="['rate-sm', loadClass(row)]">{{ fmtNum(m(row).load1, 2) }}</span>
          </template>
        </el-table-column>

        <el-table-column v-if="colVisible('diskRW')" label="磁盘读写" prop="diskRead" sortable="custom" min-width="120">
          <template #default="{ row }">
            <span class="rate mono sm">
              R{{ fmtRate(m(row).diskRead) }}
              <small class="sep">/</small>
              W{{ fmtRate(m(row).diskWr) }}
            </span>
          </template>
        </el-table-column>

        <el-table-column label="操作" min-width="170" fixed="right">
          <template #default="{ row }">
            <el-button link size="small" @click.stop="goDetail(row)">详情</el-button>
            <el-button link size="small" type="warning" @click.stop="upgrade(row)">升级</el-button>
            <el-button link size="small" type="danger" @click.stop="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <div class="pagination-wrap">
        <el-pagination
          v-model:current-page="currentPage"
          :page-size="pageSize"
          :page-sizes="[20, 50, 100]"
          layout="total, sizes, prev, pager, next, jumper"
          :total="filteredNodes.length"
          background
          @size-change="(s) => (pageSize = s)"
        />
      </div>
    </div>

    <!-- 添加主机 / 部署 Agent 抽屉 -->
    <el-drawer v-model="showAddModal" title="添加主机" direction="rtl" size="520px" :close-on-click-modal="false">
      <div class="drawer-body">
        <el-alert type="info" :closable="false" show-icon class="add-tip">
          按部署场景选择安装方式：常规直连直接执行安装命令；跨网闸隔离场景使用 Edge/Hub 代理隧道。
        </el-alert>

        <el-radio-group v-model="deployScene" size="small" class="scene-tabs">
          <el-radio-button value="direct">直连场景</el-radio-button>
          <el-radio-button value="proxy">网闸代理</el-radio-button>
        </el-radio-group>

        <!-- 直连场景 -->
        <div v-if="deployScene === 'direct'" class="scene-pane">
          <div class="drawer-section">
            <h4 class="section-label">安装命令</h4>
            <p class="section-desc">在目标机器以 root 执行以下命令即可完成安装并自动上报：</p>
            <div class="cmd-box">
              <div class="cmd-header"><span class="cmd-label">bash</span><el-button size="small" @click="copy(installInfo.command)">复制</el-button></div>
              <pre class="cmd-text">{{ installInfo.command }}</pre>
            </div>
            <div class="actions">
              <el-button :loading="checking" @click="checkConn">连通性自检</el-button>
              <el-alert v-if="checkResult" :title="checkResult.msg" :type="checkResult.type" show-icon :closable="false" class="check-alert" />
            </div>
          </div>
        </div>

        <!-- 网闸代理场景 -->
        <div v-else class="scene-pane">
          <div class="drawer-section">
            <h4 class="section-label">说明</h4>
            <p class="block-desc">
              适用于两个网区经网闸隔离、仅有 1 个开放端口的场景。区 B 部署 Hub、区 A 部署 Edge 构成隧道，
              采集 Agent 的 serverURL 指向 Edge 本地口。网闸仅需开放 TCP 8443（Edge → Hub）。
            </p>
          </div>

          <div class="drawer-section">
            <h4 class="section-label">Hub Proxy（区 B · 监控中心侧）</h4>
            <div class="form-item"><label>TLS 监听地址</label><el-input v-model="hubForm.listen" placeholder=":8443" /></div>
            <div class="form-item"><label>真实 Server 地址</label><el-input v-model="hubForm.server" placeholder="http://127.0.0.1:8080" /></div>
            <div class="form-item"><label>TLS 证书路径</label><el-input v-model="hubForm.tlsCert" placeholder="/etc/monitor-agent/certs/hub.crt" /></div>
            <div class="form-item"><label>TLS 私钥路径</label><el-input v-model="hubForm.tlsKey" placeholder="/etc/monitor-agent/certs/hub.key" /></div>
            <div class="form-item"><label>CA 证书路径</label><el-input v-model="hubForm.tlsCa" placeholder="/etc/monitor-agent/certs/ca.crt" /></div>
          </div>

          <div class="drawer-section">
            <h4 class="section-label">Edge Proxy（区 A · 被监控侧）</h4>
            <div class="form-item"><label>本地监听地址</label><el-input v-model="edgeForm.listen" placeholder=":18080" /></div>
            <div class="form-item"><label>Hub 地址 host:port</label><el-input v-model="edgeForm.hubAddr" placeholder="10.0.0.2:8443" /></div>
            <div class="form-item"><label>TLS 证书路径</label><el-input v-model="edgeForm.tlsCert" placeholder="/etc/monitor-agent/certs/edge.crt" /></div>
            <div class="form-item"><label>TLS 私钥路径</label><el-input v-model="edgeForm.tlsKey" placeholder="/etc/monitor-agent/certs/edge.key" /></div>
            <div class="form-item"><label>CA 证书路径</label><el-input v-model="edgeForm.tlsCa" placeholder="/etc/monitor-agent/certs/ca.crt" /></div>
            <div class="form-row">
              <div class="form-item"><label>断连缓冲条数</label><el-input-number v-model="edgeForm.bufferSize" :min="100" :max="100000" :step="100" controls-position="right" /></div>
              <div class="form-item"><label>并发隧道连接数</label><el-input-number v-model="edgeForm.poolSize" :min="1" :max="10" controls-position="right" /></div>
            </div>
          </div>

          <div class="drawer-section">
            <h4 class="section-label">Hub 安装命令（区 B 执行）</h4>
            <div class="cmd-box"><div class="cmd-header"><span class="cmd-label">bash</span><el-button size="small" @click="copy(hubCommand)">复制</el-button></div><pre class="cmd-text">{{ hubCommand }}</pre></div>
          </div>

          <div class="drawer-section">
            <h4 class="section-label">Hub agent.yaml 模板</h4>
            <div class="cmd-box"><div class="cmd-header"><span class="cmd-label">yaml</span><el-button size="small" @click="copy(hubYaml)">复制</el-button></div><pre class="cmd-text">{{ hubYaml }}</pre></div>
          </div>

          <div class="drawer-section">
            <h4 class="section-label">Edge 安装命令（区 A 执行）</h4>
            <div class="cmd-box"><div class="cmd-header"><span class="cmd-label">bash</span><el-button size="small" @click="copy(edgeCommand)">复制</el-button></div><pre class="cmd-text">{{ edgeCommand }}</pre></div>
          </div>

          <div class="drawer-section">
            <h4 class="section-label">Edge agent.yaml 模板</h4>
            <div class="cmd-box"><div class="cmd-header"><span class="cmd-label">yaml</span><el-button size="small" @click="copy(edgeYaml)">复制</el-button></div><pre class="cmd-text">{{ edgeYaml }}</pre></div>
          </div>

          <div class="drawer-section deploy-steps">
            <h4 class="section-label">部署步骤</h4>
            <ol class="steps-list">
              <li><b>区 B 部署 Hub</b>：在监控中心侧主机执行 Hub 安装命令，监听 8443 接收隧道连接并转发至真实 Server。</li>
              <li><b>网闸开放端口</b>：开放 TCP 8443，源 IP = 区 A 的 Edge 主机 IP，目的 IP = 区 B 的 Hub 主机 IP。</li>
              <li><b>区 A 部署 Edge</b>：在被监控区主机执行 Edge 安装命令，监听本地 18080 并通过隧道连到 Hub。</li>
              <li><b>部署采集 Agent</b>：区 A 的采集 Agent 安装时 <code>--server</code> 指向 Edge 本地口（如 <code>http://&lt;EDGE_IP&gt;:18080</code>）。</li>
            </ol>
          </div>
        </div>
      </div>
    </el-drawer>

    <!-- 分组管理弹窗 -->
    <GroupManage
      v-if="showGroupManage"
      :groups="groups"
      :nodes="nodes"
      @close="showGroupManage = false"
      @changed="load"
    />

    <!-- 编辑显示名弹窗 -->
    <el-dialog v-model="showNameModal" title="编辑显示名" width="420px" @closed="nameInput = ''">
      <p style="color: var(--text-dim); margin-bottom: 12px; font-size: 13px">
        为「{{ nameTarget }}」设置一个便于识别的显示名（别名）。留空则恢复使用原始主机名，不影响 Agent 上报的真实主机名。
      </p>
      <el-input
        v-model="nameInput"
        placeholder="如：Web 生产机-01"
        maxlength="64"
        show-word-limit
        @keyup.enter="saveName"
      />
      <template #footer>
        <el-button @click="showNameModal = false">取消</el-button>
        <el-button type="primary" @click="saveName">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, computed, reactive, onMounted, onUnmounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { Plus, Search, Setting, ArrowDown, Refresh, Edit, Operation } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import http from '../api/http'
import OsIcon from './OsIcon.vue'
import GroupManage from './GroupManage.vue'

const router = useRouter()
const nodes = ref([])
const latestAgentVersion = ref('')
const metrics = ref({})
const groups = ref([])
const statusFilter = ref('')
const groupFilter = ref('')
const keyword = ref('')
const currentPage = ref(1)
const pageSize = ref(20)

// 刷新控制
const refreshInterval = ref(20) // 0=关闭, >0=秒
const countdown = ref(20)
const lastRefresh = ref('')
const loadError = ref('')

const showAddModal = ref(false)
const showGroupManage = ref(false)
const showNameModal = ref(false)
const nameInput = ref('')
const nameTarget = ref('')

// ===== 添加主机：部署场景与安装信息 =====
const installInfo = ref({ serverURL: '', authEnabled: false, secret: '', command: '' })
const deployScene = ref('direct') // direct | proxy

// 网闸代理：Hub / Edge 表单
const hubForm = reactive({
  listen: ':8443',
  server: 'http://127.0.0.1:8080',
  tlsCert: '/etc/monitor-agent/certs/hub.crt',
  tlsKey: '/etc/monitor-agent/certs/hub.key',
  tlsCa: '/etc/monitor-agent/certs/ca.crt',
})
const edgeForm = reactive({
  listen: ':18080',
  hubAddr: '10.0.0.2:8443',
  tlsCert: '/etc/monitor-agent/certs/edge.crt',
  tlsKey: '/etc/monitor-agent/certs/edge.key',
  tlsCa: '/etc/monitor-agent/certs/ca.crt',
  bufferSize: 1000,
  poolSize: 2,
})

// 连通性自检
const checking = ref(false)
const checkResult = ref(null)

// 服务端密钥（安装 Server 时确定）：直连命令已由后端拼接；网闸命令/YAML 复用此值
const serverSecret = computed(() => (installInfo.value.secret || '').replace(/^ --secret /, '').trim())

// Hub 安装命令
const hubCommand = computed(() => {
  const srv = installInfo.value.serverURL || 'http://<SERVER>:8080'
  let cmd = `curl -fsSL ${srv}/install/agent-install.sh | bash -s -- --mode hub --listen ${hubForm.listen} --server ${hubForm.server}`
  cmd += ` --tls-cert ${hubForm.tlsCert} --tls-key ${hubForm.tlsKey} --tls-ca ${hubForm.tlsCa}`
  cmd += ' --yes'
  if (installInfo.value.authEnabled) cmd += installInfo.value.secret
  return cmd
})

// Hub agent.yaml 模板
const hubYaml = computed(() => {
  return `mode: "hub"
node: "hub-proxy"
group: "proxy"
secret: "${serverSecret.value}"
interval: 15
serverURL: "${hubForm.server}"

proxy:
  listen: "${hubForm.listen}"
  tlsCert: "${hubForm.tlsCert}"
  tlsKey: "${hubForm.tlsKey}"
  tlsCa: "${hubForm.tlsCa}"
  serverURL: "${hubForm.server}"
`
})

// Edge 安装命令
const edgeCommand = computed(() => {
  const srv = installInfo.value.serverURL || 'http://<SERVER>:8080'
  let cmd = `curl -fsSL ${srv}/install/agent-install.sh | bash -s -- --mode edge --listen ${edgeForm.listen} --hub-addr ${edgeForm.hubAddr}`
  cmd += ` --tls-cert ${edgeForm.tlsCert} --tls-key ${edgeForm.tlsKey} --tls-ca ${edgeForm.tlsCa}`
  cmd += ` --buffer-size ${edgeForm.bufferSize} --pool-size ${edgeForm.poolSize}`
  cmd += ' --yes'
  if (installInfo.value.authEnabled) cmd += installInfo.value.secret
  return cmd
})

// Edge agent.yaml 模板
const edgeYaml = computed(() => {
  return `mode: "edge"
node: "edge-proxy"
group: "proxy"
secret: "${serverSecret.value}"
interval: 15
serverURL: "https://${edgeForm.hubAddr}"

proxy:
  listen: "${edgeForm.listen}"
  hubAddr: "${edgeForm.hubAddr}"
  tlsCert: "${edgeForm.tlsCert}"
  tlsKey: "${edgeForm.tlsKey}"
  tlsCa: "${edgeForm.tlsCa}"
  bufferSize: ${edgeForm.bufferSize}
  poolSize: ${edgeForm.poolSize}
`
})

let loadTimer = null
let countdownTimer = null
let visible = true

const onlineCount = computed(() => nodes.value.filter((n) => n.status === 'online').length)
const offlineCount = computed(() => nodes.value.filter((n) => n.status !== 'online').length)
const warningCount = computed(() => nodes.value.filter((n) => nodeSeverity(n) >= 50 && n.status === 'online').length)

// 列设置：可勾选展示哪些列
const colOptions = [
  { key: 'host', label: '主机名称/IP' },
  { key: 'status', label: '状态' },
  { key: 'group', label: '分组' },
  { key: 'version', label: 'Agent 版本' },
  { key: 'cpu', label: 'CPU' },
  { key: 'mem', label: '内存' },
  { key: 'disk', label: '磁盘使用' },
  { key: 'netIn', label: '流量↓/↑' },
  { key: 'load1', label: '负载' },
  { key: 'diskRW', label: '磁盘读写' },
]
const selectedCols = ref(colOptions.map((c) => c.key))
function colVisible(k) {
  return selectedCols.value.includes(k)
}

// 排序状态：默认按分组聚合（同组异常优先）；点击表头切换到按列排序
const sortProp = ref('group')
const sortOrder = ref('ascending')
function onSortChange({ prop, order }) {
  sortProp.value = order ? prop : ''
  sortOrder.value = order || ''
}

// IP 升序比较：点分十进制按四段数值比较，非标准 IP 退化为字符串比较
function ipCompare(a, b) {
  const pa = String(a || '').split('.')
  const pb = String(b || '').split('.')
  const isNum = (p) => p.length === 4 && p.every((x) => /^\d+$/.test(x))
  if (isNum(pa) && isNum(pb)) {
    for (let i = 0; i < 4; i++) {
      const x = +pa[i]
      const y = +pb[i]
      if (x !== y) return x - y
    }
    return 0
  }
  return String(a || '').localeCompare(String(b || ''))
}

// 取某列的排序值
function sortValue(n, prop) {
  switch (prop) {
    case 'hostname': return n.hostname
    case 'status': return n.status
    case 'group': return n.group || 'default'
    case 'version': return n.version || ''
    case 'cpu': return m(n).cpu
    case 'mem': return m(n).mem
    case 'disk': return m(n).disk
    case 'netIn': return m(n).netIn
    case 'load1': return m(n).load1
    case 'diskRead': return m(n).diskRead
    case 'severity': return nodeSeverity(n)
    default: return ''
  }
}

// 仅过滤（不含排序）
const filteredNodes = computed(() => {
  let arr = nodes.value
  if (statusFilter.value === 'online') arr = arr.filter((n) => n.status === 'online')
  else if (statusFilter.value === 'offline') arr = arr.filter((n) => n.status !== 'online')
  else if (statusFilter.value === 'warning') arr = arr.filter((n) => nodeSeverity(n) >= 50)
  if (groupFilter.value) arr = arr.filter((n) => (n.group || 'default') === groupFilter.value)
  if (keyword.value) {
    const k = keyword.value.toLowerCase()
    arr = arr.filter(
      (n) =>
        n.hostname.toLowerCase().includes(k) ||
        (n.displayName || '').toLowerCase().includes(k) ||
        (n.ip || '').toLowerCase().includes(k)
    )
  }
  return arr
})

// 排序：默认按分组聚合（同组异常优先、再按 IP）；手动排序时按列（同值按分组、IP 稳定）
const sortedNodes = computed(() => {
  const arr = filteredNodes.value.slice()
  if (!sortProp.value) {
    arr.sort((a, b) => {
      const d = nodeSeverity(b) - nodeSeverity(a)
      return d !== 0 ? d : ipCompare(a.ip, b.ip)
    })
    return arr
  }
  const dir = sortOrder.value === 'ascending' ? 1 : -1
  arr.sort((a, b) => {
    const va = sortValue(a, sortProp.value)
    const vb = sortValue(b, sortProp.value)
    if (va === vb) {
      // 同值：先按分组聚合，再按 IP 稳定
      const ga = a.group || 'default'
      const gb = b.group || 'default'
      if (ga !== gb) return ga.localeCompare(gb)
      return ipCompare(a.ip, b.ip)
    }
    if (va == null || va === '') return 1
    if (vb == null || vb === '') return -1
    if (typeof va === 'number' && typeof vb === 'number') return (va - vb) * dir
    return String(va).localeCompare(String(vb)) * dir
  })
  return arr
})

const pagedNodes = computed(() => {
  const start = (currentPage.value - 1) * pageSize.value
  return sortedNodes.value.slice(start, start + pageSize.value)
})

// 过滤条件变化时回到第一页
watch([statusFilter, groupFilter, keyword], () => {
  currentPage.value = 1
})

function nodeSeverity(n) {
  if (n.status !== 'online') return 100
  const mm = metrics.value[n.hostname]
  if (!mm) return 0
  let s = 0
  for (const k of ['cpu', 'mem', 'disk']) {
    const v = mm[k]
    if (typeof v === 'number') {
      if (v >= 90) s = Math.max(s, 80)
      else if (v >= 70) s = Math.max(s, 50)
    }
  }
  return s
}

function rowClass({ row }) {
  if (row.status !== 'online') return 'row-offline'
  if (nodeSeverity(row) >= 80) return 'row-critical'
  if (nodeSeverity(row) >= 50) return 'row-warning'
  return ''
}

function verClass(v) {
  if (!v || v === 'dev') return 'dev'
  return 'release'
}

function m(row) {
  return metrics.value[row.hostname] || {}
}
function hasMetric(row) {
  const v = metrics.value[row.hostname]
  return v && typeof v.disk === 'number'
}
function pct(v) {
  if (typeof v !== 'number') return 0
  return Math.min(100, Math.max(0, v))
}
function rateClass(v) {
  if (typeof v !== 'number') return ''
  if (v >= 90) return 'red'
  if (v >= 70) return 'amber'
  return 'green'
}
function loadClass(row) {
  const v = m(row).load1
  if (typeof v !== 'number') return ''
  if (v >= 8) return 'red'
  if (v >= 4) return 'amber'
  return 'green'
}
function fmtNum(v, d = 1) {
  if (typeof v !== 'number') return '--'
  return v.toFixed(d)
}
function fmtRate(v) {
  if (typeof v !== 'number') return '--'
  if (v >= 1048576) return (v / 1048576).toFixed(2) + 'M'
  if (v >= 1024) return (v / 1024).toFixed(1) + 'K'
  return v.toFixed(0) + 'B'
}

async function load() {
  if (!visible) return
  loadError.value = ''
  try {
    const [nd, md, gd] = await Promise.all([
      http.get('/api/v1/nodes'),
      http.get('/api/v1/nodes/latest'),
      http.get('/api/v1/groups'),
    ])
    nodes.value = nd.nodes || []
    metrics.value = md.metrics || {}
    groups.value = gd.groups || []
    lastRefresh.value = new Date().toLocaleTimeString('zh-CN', { hour12: false })
  } catch (e) {
    loadError.value = '数据加载失败：' + (e.message || '未知错误')
    console.error('[HostsView] load error:', e)
  }
}

function manualRefresh() {
  load()
  countdown.value = refreshInterval.value
}

function onIntervalChange() {
  countdown.value = refreshInterval.value
  restartTimers()
}

function restartTimers() {
  if (loadTimer) { clearInterval(loadTimer); loadTimer = null }
  if (countdownTimer) { clearInterval(countdownTimer); countdownTimer = null }
  if (refreshInterval.value > 0) {
    countdown.value = refreshInterval.value
    loadTimer = setInterval(() => {
      if (visible) load()
      countdown.value = refreshInterval.value
    }, refreshInterval.value * 1000)
    countdownTimer = setInterval(() => {
      if (countdown.value > 0) countdown.value--
    }, 1000)
  }
}

function openAddNode() {
  showAddModal.value = true
  checkResult.value = null
  http
    .get('/api/v1/install-info')
    .then((info) => {
      installInfo.value = info
      if (info.serverURL) hubForm.server = info.serverURL
    })
    .catch(() => {
      installInfo.value = { serverURL: '', authEnabled: false, secret: '' }
      ElMessage.error('获取安装信息失败，请检查 Server 是否正常')
    })
}

async function changeGroup(row, group) {
  if (group === (row.group || 'default')) return
  try {
    await http.put('/api/v1/nodes/' + row.hostname + '/group', { group })
    ElMessage.success(`${row.hostname} 已移入分组「${group}」`)
    load()
  } catch (e) {
    ElMessage.error('修改分组失败：' + (e.message || ''))
  }
}

function editName(row) {
  nameTarget.value = row.hostname
  nameInput.value = row.displayName || ''
  showNameModal.value = true
}

async function saveName() {
  const target = nameTarget.value
  try {
    await http.put('/api/v1/nodes/' + target + '/display-name', { displayName: nameInput.value.trim() })
    ElMessage.success('显示名已保存')
    showNameModal.value = false
    load()
  } catch (e) {
    ElMessage.error('保存失败：' + (e.message || ''))
  }
}

function copy(text) {
  navigator.clipboard.writeText(text).then(() => {
    ElMessage.success('已复制到剪贴板')
  }).catch(() => {
    const ta = document.createElement('textarea')
    ta.value = text
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    document.body.removeChild(ta)
    ElMessage.success('已复制到剪贴板')
  })
}

async function checkConn() {
  checking.value = true
  checkResult.value = null
  const srv = installInfo.value.serverURL || ''
  try {
    await http.get('/api/v1/ping', { params: { server: srv } })
    checkResult.value = { type: 'success', msg: 'Server 可达：' + srv }
  } catch (e) {
    checkResult.value = { type: 'error', msg: 'Server 不可达：' + (e.message || srv) }
  } finally {
    checking.value = false
  }
}

async function remove(row) {
  try {
    await ElMessageBox.confirm(
      `确认删除主机「${row.displayName || row.hostname}」？删除后该主机的监控数据将一并清除，且不可恢复。`,
      '删除主机',
      {
        type: 'warning',
        confirmButtonText: '删除',
        cancelButtonText: '取消',
        confirmButtonClass: 'el-button--danger',
        closeOnClickModal: false,
      }
    )
    await http.del('/api/v1/nodes/' + row.hostname)
    ElMessage.success('已删除')
    load()
  } catch (e) {
    /* 取消 */
  }
}

async function upgrade(row) {
  try {
    await ElMessageBox.confirm(
      '确认升级主机 ' + row.hostname + ' 的 Agent？\n升级期间 Agent 会短暂离线后自动恢复。',
      'Agent 升级',
      { type: 'warning', confirmButtonText: '升级', cancelButtonText: '取消' }
    )
    await http.post('/api/v1/nodes/' + row.hostname + '/upgrade', {})
    ElMessage.success('升级任务已下发，Agent 将在下次心跳时执行（约 15-30s 内生效）')
  } catch (e) {
    if (e && e.message && !e.message.includes('cancel')) {
      ElMessage.error('升级失败：' + e.message)
    }
  }
}

function goDetail(row) {
  router.push('/node/' + row.hostname)
}

// 最新可用 Agent 版本（server 版本即 CDN 中 agent 二进制版本）
async function loadVersion() {
  try {
    const v = await http.get('/api/v1/version')
    latestAgentVersion.value = v.server || ''
  } catch (e) { /* ignore */ }
}

// 该节点 Agent 版本是否为最新（用于 Agent 版本列红点提示）
function needUpgrade(row) {
  return !!latestAgentVersion.value && !!row.version && row.version !== latestAgentVersion.value
}

function onVis() {
  visible = document.visibilityState === 'visible'
  if (visible) {
    load()
    restartTimers()
  } else {
    if (loadTimer) { clearInterval(loadTimer); loadTimer = null }
    if (countdownTimer) { clearInterval(countdownTimer); countdownTimer = null }
  }
}

onMounted(() => {
  load()
  loadVersion()
  restartTimers()
  document.addEventListener('visibilitychange', onVis)
})
onUnmounted(() => {
  if (loadTimer) clearInterval(loadTimer)
  if (countdownTimer) clearInterval(countdownTimer)
  document.removeEventListener('visibilitychange', onVis)
})

defineExpose({ reload: load })
</script>

<style scoped>
.hosts-view {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 16px;
  flex-wrap: wrap;
  gap: 10px;
}
.toolbar-left,
.toolbar-right {
  display: flex;
  gap: 10px;
  align-items: center;
  flex-wrap: wrap;
}

/* 刷新控制条 */
.refresh-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 16px;
}
.refresh-left,
.refresh-right {
  display: flex;
  align-items: center;
  gap: 10px;
}
.refresh-text {
  font-size: 12px;
  color: var(--text-dim);
  font-family: var(--mono);
}
.refresh-label {
  font-size: 12px;
  color: var(--text-dim);
}
.countdown {
  font-size: 12px;
  font-family: var(--mono);
  color: var(--accent);
  min-width: 28px;
  text-align: right;
}

/* 主机名/IP */
.host-name {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.hn-top {
  display: flex;
  align-items: center;
  gap: 6px;
}
.hn-name {
  color: var(--text);
  font-weight: 600;
  font-size: 13px;
}
.hn-ip {
  color: var(--text-dim);
  font-size: 11px;
  font-family: var(--mono);
}
.hn-edit {
  margin-left: 2px;
  color: var(--text-muted);
}
.hn-edit:hover {
  color: var(--accent);
}
.ver-cell {
  display: inline-block;
  font-size: 11px;
  font-family: var(--mono);
  padding: 1px 7px;
  border-radius: 4px;
  line-height: 18px;
}
.ver-cell.dev {
  color: var(--text-muted);
  background: rgba(255, 255, 255, 0.04);
}
.ver-cell.release {
  color: var(--accent);
  background: var(--accent-dim);
}
.ver-dot {
  display: inline-block;
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--danger);
  margin-right: 5px;
  vertical-align: middle;
  box-shadow: 0 0 0 2px rgba(245, 108, 108, 0.25);
}

/* 状态指示灯 */
.status-led {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  margin-right: 6px;
  vertical-align: middle;
}
.status-led.on {
  background: var(--chart-green);
  box-shadow: 0 0 6px var(--chart-green);
}
.status-led.off {
  background: var(--danger);
}
.status-text {
  font-size: 12px;
  vertical-align: middle;
}
.status-text.on { color: var(--chart-green); }
.status-text.off { color: var(--danger); }

/* 分组标签 */
.group-tag {
  font-size: 12px;
  color: var(--text-dim);
}
.group-tag.clickable {
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  gap: 2px;
  padding: 2px 8px;
  border-radius: 4px;
  transition: background 0.15s;
}
.group-tag.clickable:hover {
  background: rgba(255, 255, 255, 0.06);
  color: var(--text);
}
.group-arrow {
  font-size: 10px;
  opacity: 0.6;
}

/* 迷你进度条 */
.usage-cell {
  display: flex;
  align-items: center;
  gap: 6px;
}
.mini-bar {
  flex: 1;
  height: 5px;
  background: rgba(255, 255, 255, 0.06);
  border-radius: 3px;
  overflow: hidden;
  min-width: 40px;
}
.mini-bar-fill {
  height: 100%;
  border-radius: 3px;
  transition: width 0.3s;
}
.mini-bar-fill.green { background: var(--accent); }
.mini-bar-fill.amber { background: var(--warn); }
.mini-bar-fill.red { background: var(--danger); }

/* 数值 */
.rate {
  font-family: var(--mono);
  font-size: 13px;
  color: var(--text);
}
.rate.sm { font-size: 11px; }
.rate.mono { font-family: var(--mono); }
.rate-sm {
  font-family: var(--mono);
  font-size: 11px;
  color: var(--text-dim);
  white-space: nowrap;
}
.rate-sm.green { color: var(--accent); }
.rate-sm.amber { color: var(--warn); }
.rate-sm.red { color: var(--danger); }
.rate small {
  font-size: 10px;
  margin-left: 1px;
  color: var(--text-dim);
}
.rate .sep {
  margin: 0 3px;
  color: var(--text-muted);
}
.dim {
  color: var(--text-muted);
  font-size: 12px;
}

/* 行状态高亮 */
.host-table :deep(.el-table__row) {
  cursor: pointer;
}
/* 表头不换行，避免“Agent 版本”等列头被挤压换行 */
.host-table :deep(th .cell) {
  white-space: nowrap;
}
.net-traffic {
  line-height: 1.5;
  font-family: var(--mono);
  font-size: 12px;
}
.net-traffic .net-up {
  color: var(--text-muted);
}
.host-table :deep(.row-offline) {
  background: var(--danger-dim) !important;
}
.host-table :deep(.row-critical) {
  background: rgba(244, 63, 94, 0.08) !important;
}
.host-table :deep(.row-warning) {
  background: var(--warn-dim) !important;
}
.pagination-wrap {
  margin-top: 14px;
  display: flex;
  justify-content: flex-end;
}
.cmd-box :deep(.el-textarea__inner) {
  font-family: var(--mono);
  font-size: 12px;
  color: var(--accent);
  background: rgba(0, 0, 0, 0.35) !important;
}

/* 列设置弹窗 */
.col-set {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.col-set-title {
  font-size: 12px;
  color: var(--text-dim);
  margin-bottom: 2px;
}
.col-set-group {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

/* ===== 添加主机 Drawer ===== */
.drawer-body {
  display: flex;
  flex-direction: column;
  gap: 14px;
  overflow-y: auto;
  padding-right: 4px;
}
.add-tip {
  margin-bottom: 2px;
}
.scene-tabs {
  margin-bottom: 2px;
  display: flex;
  gap: 8px;
}
.scene-pane {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.drawer-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.section-label {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
  margin: 0;
  position: relative;
  padding-left: 10px;
}
.section-label::before {
  content: '';
  position: absolute;
  left: 0;
  top: 2px;
  bottom: 2px;
  width: 3px;
  background: var(--accent);
  border-radius: 2px;
}
.section-desc {
  font-size: 12px;
  line-height: 1.7;
  color: var(--text-muted);
  margin: 0;
}
.form-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.form-item label {
  font-size: 12px;
  color: var(--text-dim);
}
.form-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}
.actions {
  display: flex;
  align-items: center;
  gap: 12px;
}
.check-alert {
  flex: 1;
  margin: 0;
}
.block-desc {
  font-size: 12px;
  line-height: 1.7;
  color: var(--text-dim);
  background: rgba(255, 255, 255, 0.03);
  border: 1px solid rgba(255, 255, 255, 0.06);
  border-radius: 8px;
  padding: 10px 14px;
  margin: 0;
}
.deploy-steps {
  background: rgba(255, 255, 255, 0.02);
  border: 1px solid rgba(255, 255, 255, 0.06);
  border-radius: 8px;
  padding: 12px 14px;
}
.steps-list {
  margin: 6px 0 0;
  padding-left: 18px;
  display: flex;
  flex-direction: column;
  gap: 5px;
  font-size: 12px;
  line-height: 1.7;
  color: var(--text-dim);
}
.steps-list code {
  font-family: var(--mono);
  background: rgba(0, 0, 0, 0.3);
  padding: 1px 5px;
  border-radius: 3px;
  color: var(--accent);
  font-size: 11px;
}
.steps-list b {
  color: var(--text);
}
.cmd-box {
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 8px;
  overflow: hidden;
  background: rgba(0, 0, 0, 0.35);
}
.cmd-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 6px 10px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.06);
  background: rgba(255, 255, 255, 0.02);
}
.cmd-label {
  font-family: var(--mono);
  font-size: 11px;
  color: var(--text-muted);
}
.cmd-text {
  margin: 0;
  padding: 10px 12px;
  font-family: var(--mono);
  font-size: 11px;
  line-height: 1.65;
  color: var(--accent);
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 200px;
  overflow: auto;
}

/* Drawer 覆盖：标题栏与 body 样式微调 */
.drawer-body :deep(.el-drawer__header) {
  margin-bottom: 0;
  padding-bottom: 14px;
  border-bottom: 1px solid var(--border);
}
.drawer-body :deep(.el-drawer__body) {
  padding: 16px 18px;
}
</style>
