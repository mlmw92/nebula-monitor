<template>
  <div class="setup-view page-wrap">
    <div class="page-head">
      <div>
        <h2>Agent 部署引导</h2>
        <p class="sub">一键生成 Agent 安装命令。直连场景直接复制执行；网闸场景展开代理向导生成两侧配置。</p>
      </div>
    </div>

    <!-- 区块1：直连场景安装命令 -->
    <section class="glass section-block">
      <h3 class="section-title">直连场景（采集 Agent → Server）</h3>
      <p class="block-desc">适用于 Agent 可直接访问 Server 的常规部署。在目标主机以 root 执行以下命令即可完成安装并启动上报。</p>

      <div class="form-grid">
        <div class="form-item">
          <label>节点名</label>
          <el-input v-model="form.node" placeholder="留空则用 hostname" />
        </div>
        <div class="form-item">
          <label>分组</label>
          <el-input v-model="form.group" placeholder="default" />
        </div>
        <div class="form-item">
          <label>接入密钥</label>
          <el-input v-model="form.secret" type="password" show-password placeholder="Server 启用 agentAuth 时必填" />
        </div>
        <div class="form-item">
          <label>采集间隔(秒)</label>
          <el-input-number v-model="form.interval" :min="5" :max="300" controls-position="right" />
        </div>
      </div>

      <div class="cmd-box">
        <div class="cmd-header">
          <span class="cmd-label">安装命令</span>
          <el-button size="small" @click="copy(directCommand)">复制</el-button>
        </div>
        <pre class="cmd-text">{{ directCommand }}</pre>
      </div>

      <div class="actions">
        <el-button :loading="checking" @click="checkConn">连通性自检</el-button>
        <el-alert v-if="checkResult" :title="checkResult.msg" :type="checkResult.type" show-icon :closable="false" class="check-alert" />
      </div>
    </section>

    <!-- 区块2：网闸代理场景向导 -->
    <section class="glass section-block">
      <div class="collapse-head" @click="proxyOpen = !proxyOpen">
        <h3 class="section-title">网闸场景：Edge / Hub 代理部署</h3>
        <el-icon class="caret" :class="{ open: proxyOpen }"><ArrowDown /></el-icon>
      </div>

      <el-collapse-transition>
        <div v-show="proxyOpen" class="proxy-body">
          <p class="block-desc">
            适用于两个网区经网闸隔离、仅有 1 个开放端口的场景。在区 B 部署 Hub、区 A 部署 Edge 构成隧道，
            采集 Agent 的 serverURL 指向 Edge 本地口。网闸仅需开放 TCP 8443（Edge IP → Hub IP）。
          </p>

          <div class="proxy-grid">
            <!-- Hub 配置 -->
            <div class="proxy-col">
              <div class="proxy-col-head hub">
                <span class="dot"></span>
                <span>Hub Proxy（区 B · 监控中心侧）</span>
              </div>
              <div class="form-item">
                <label>TLS 监听地址</label>
                <el-input v-model="hubForm.listen" placeholder=":8443" />
              </div>
              <div class="form-item">
                <label>真实 Server 地址</label>
                <el-input v-model="hubForm.server" placeholder="http://127.0.0.1:8080" />
              </div>
              <div class="form-item">
                <label>TLS 证书路径</label>
                <el-input v-model="hubForm.tlsCert" placeholder="/etc/monitor-agent/certs/hub.crt" />
              </div>
              <div class="form-item">
                <label>TLS 私钥路径</label>
                <el-input v-model="hubForm.tlsKey" placeholder="/etc/monitor-agent/certs/hub.key" />
              </div>
              <div class="form-item">
                <label>CA 证书路径</label>
                <el-input v-model="hubForm.tlsCa" placeholder="/etc/monitor-agent/certs/ca.crt" />
              </div>
            </div>

            <!-- Edge 配置 -->
            <div class="proxy-col">
              <div class="proxy-col-head edge">
                <span class="dot"></span>
                <span>Edge Proxy（区 A · 被监控侧）</span>
              </div>
              <div class="form-item">
                <label>本地监听地址</label>
                <el-input v-model="edgeForm.listen" placeholder=":18080" />
              </div>
              <div class="form-item">
                <label>Hub 地址 host:port</label>
                <el-input v-model="edgeForm.hubAddr" placeholder="10.0.0.2:8443" />
              </div>
              <div class="form-item">
                <label>TLS 证书路径</label>
                <el-input v-model="edgeForm.tlsCert" placeholder="/etc/monitor-agent/certs/edge.crt" />
              </div>
              <div class="form-item">
                <label>TLS 私钥路径</label>
                <el-input v-model="edgeForm.tlsKey" placeholder="/etc/monitor-agent/certs/edge.key" />
              </div>
              <div class="form-item">
                <label>CA 证书路径</label>
                <el-input v-model="edgeForm.tlsCa" placeholder="/etc/monitor-agent/certs/ca.crt" />
              </div>
              <div class="form-item">
                <label>断连缓冲条数</label>
                <el-input-number v-model="edgeForm.bufferSize" :min="100" :max="100000" :step="100" controls-position="right" />
              </div>
              <div class="form-item">
                <label>并发隧道连接数</label>
                <el-input-number v-model="edgeForm.poolSize" :min="1" :max="10" controls-position="right" />
              </div>
            </div>
          </div>

          <!-- 生成结果 -->
          <div class="gen-section">
            <h4 class="gen-title">Hub 安装命令（区 B 执行）</h4>
            <div class="cmd-box">
              <div class="cmd-header">
                <span class="cmd-label">bash</span>
                <el-button size="small" @click="copy(hubCommand)">复制</el-button>
              </div>
              <pre class="cmd-text">{{ hubCommand }}</pre>
            </div>

            <h4 class="gen-title">Hub agent.yaml 模板</h4>
            <div class="cmd-box">
              <div class="cmd-header">
                <span class="cmd-label">yaml</span>
                <el-button size="small" @click="copy(hubYaml)">复制</el-button>
              </div>
              <pre class="cmd-text">{{ hubYaml }}</pre>
            </div>

            <h4 class="gen-title">Edge 安装命令（区 A 执行）</h4>
            <div class="cmd-box">
              <div class="cmd-header">
                <span class="cmd-label">bash</span>
                <el-button size="small" @click="copy(edgeCommand)">复制</el-button>
              </div>
              <pre class="cmd-text">{{ edgeCommand }}</pre>
            </div>

            <h4 class="gen-title">Edge agent.yaml 模板</h4>
            <div class="cmd-box">
              <div class="cmd-header">
                <span class="cmd-label">yaml</span>
                <el-button size="small" @click="copy(edgeYaml)">复制</el-button>
              </div>
              <pre class="cmd-text">{{ edgeYaml }}</pre>
            </div>
          </div>

          <!-- 部署步骤 -->
          <div class="deploy-steps">
            <h4 class="gen-title">部署步骤</h4>
            <ol class="steps-list">
              <li><b>区 B 部署 Hub</b>：在监控中心侧主机执行 Hub 安装命令，Hub 将监听 8443 接收隧道连接并转发至真实 Server。</li>
              <li><b>网闸开放端口</b>：在网闸配置中开放 TCP 8443，源 IP = 区 A 的 Edge 主机 IP，目的 IP = 区 B 的 Hub 主机 IP。</li>
              <li><b>区 A 部署 Edge</b>：在被监控区主机执行 Edge 安装命令，Edge 将监听本地 18080 并通过隧道连到 Hub。</li>
              <li><b>部署采集 Agent</b>：区 A 的普通采集 Agent 安装时 <code>--server</code> 指向 Edge 本地口（如 <code>http://&lt;EDGE_IP&gt;:18080</code>）。</li>
            </ol>
          </div>
        </div>
      </el-collapse-transition>
    </section>

    <!-- 代理状态 -->
    <section class="glass section-block">
      <h3 class="section-title">代理状态</h3>
      <p class="block-desc">展示当前已上报 proxy_* 指标的代理节点。数据来自时序库即时查询。</p>
      <el-table :data="proxyItems" v-loading="proxyLoading" stripe size="small" empty-text="暂无代理节点上报">
        <el-table-column prop="node" label="节点" min-width="140" />
        <el-table-column prop="mode" label="模式" width="80">
          <template #default="{ row }">
            <el-tag :type="row.mode === 'hub' ? 'primary' : 'success'" size="small">{{ row.mode }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="connActive" label="活跃连接" width="100" />
        <el-table-column prop="forwardTotal" label="累计转发" width="100" />
        <el-table-column prop="droppedTotal" label="累计丢弃" width="100" />
        <el-table-column prop="reconnectTotal" label="累计重连" width="100" />
        <el-table-column prop="bufferDepth" label="缓冲深度" width="100" />
      </el-table>
    </section>
  </div>
</template>

<script setup>
import { ref, reactive, computed, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { ArrowDown } from '@element-plus/icons-vue'
import http from '../api/http'

// 直连场景表单
const form = reactive({
  node: '',
  group: 'default',
  secret: '',
  interval: 15,
})

// 安装信息（来自 /api/v1/install-info）
const installInfo = ref({ serverURL: '', authEnabled: false, secret: '' })

// 连通性自检
const checking = ref(false)
const checkResult = ref(null)

// 代理向导
const proxyOpen = ref(false)
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

// 代理状态
const proxyItems = ref([])
const proxyLoading = ref(false)

// 直连安装命令
const directCommand = computed(() => {
  const srv = installInfo.value.serverURL || 'http://<SERVER>:8080'
  let cmd = `curl -fsSL ${srv}/install/agent-install.sh | bash -s -- --server ${srv}`
  if (form.node) cmd += ` --node ${form.node}`
  if (form.group) cmd += ` --group ${form.group}`
  if (form.interval && form.interval !== 15) cmd += ` --interval ${form.interval}`
  if (installInfo.value.authEnabled && form.secret) cmd += ` --secret ${form.secret}`
  return cmd
})

// Hub 命令
const hubCommand = computed(() => {
  const srv = installInfo.value.serverURL || 'http://<SERVER>:8080'
  let cmd = `curl -fsSL ${srv}/install/agent-install.sh | bash -s -- --mode hub --listen ${hubForm.listen} --server ${hubForm.server}`
  cmd += ` --tls-cert ${hubForm.tlsCert} --tls-key ${hubForm.tlsKey} --tls-ca ${hubForm.tlsCa}`
  cmd += ' --yes'
  if (installInfo.value.authEnabled && form.secret) cmd += ` --secret ${form.secret}`
  return cmd
})

// Hub agent.yaml
const hubYaml = computed(() => {
  return `mode: "hub"
node: "hub-proxy"
group: "proxy"
secret: "${form.secret}"
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

// Edge 命令
const edgeCommand = computed(() => {
  const srv = installInfo.value.serverURL || 'http://<SERVER>:8080'
  let cmd = `curl -fsSL ${srv}/install/agent-install.sh | bash -s -- --mode edge --listen ${edgeForm.listen} --hub-addr ${edgeForm.hubAddr}`
  cmd += ` --tls-cert ${edgeForm.tlsCert} --tls-key ${edgeForm.tlsKey} --tls-ca ${edgeForm.tlsCa}`
  cmd += ` --buffer-size ${edgeForm.bufferSize} --pool-size ${edgeForm.poolSize}`
  cmd += ' --yes'
  if (installInfo.value.authEnabled && form.secret) cmd += ` --secret ${form.secret}`
  return cmd
})

// Edge agent.yaml
const edgeYaml = computed(() => {
  return `mode: "edge"
node: "edge-proxy"
group: "proxy"
secret: "${form.secret}"
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

async function loadInstallInfo() {
  try {
    const info = await http.get('/api/v1/install-info')
    installInfo.value = info
    if (info.serverURL) hubForm.server = info.serverURL
  } catch (e) {
    console.error('加载安装信息失败', e)
  }
}

async function checkConn() {
  checking.value = true
  checkResult.value = null
  try {
    const headers = {}
    if (form.secret) headers['X-Agent-Secret'] = form.secret
    const resp = await fetch(`${installInfo.value.serverURL}/api/v1/agent/check`, { headers })
    if (resp.status === 200) {
      checkResult.value = { type: 'success', msg: '接入鉴权校验通过，Agent 可正常上报' }
    } else if (resp.status === 401) {
      checkResult.value = { type: 'error', msg: '接入密钥校验失败（401）：请检查密钥是否与 Server 一致' }
    } else {
      checkResult.value = { type: 'warning', msg: `无法确认（HTTP ${resp.status}），请确认 Server 可达` }
    }
  } catch (e) {
    checkResult.value = { type: 'error', msg: '连接失败：' + e.message }
  } finally {
    checking.value = false
  }
}

async function loadProxyStatus() {
  proxyLoading.value = true
  try {
    const data = await http.get('/api/v1/proxy/status')
    proxyItems.value = data.items || []
  } catch (e) {
    console.error('加载代理状态失败', e)
    proxyItems.value = []
  } finally {
    proxyLoading.value = false
  }
}

function copy(text) {
  navigator.clipboard.writeText(text).then(() => {
    ElMessage.success('已复制到剪贴板')
  }).catch(() => {
    ElMessage.warning('复制失败，请手动选择复制')
  })
}

onMounted(() => {
  loadInstallInfo()
  loadProxyStatus()
})
</script>

<style scoped>
.setup-view { padding: 20px; }
.page-head { margin-bottom: 20px; }
.page-head h2 { font-size: 20px; font-weight: 600; margin: 0 0 6px; }
.page-head .sub { font-size: 13px; color: var(--text-muted); margin: 0; }

.section-block { padding: 18px 20px; margin-bottom: 18px; border-radius: 12px; }
.section-title { font-size: 15px; font-weight: 600; margin: 0 0 12px; display: flex; align-items: center; gap: 8px; }
.section-title::before { content: ''; width: 3px; height: 14px; background: var(--accent); border-radius: 2px; }
.block-desc { font-size: 12px; color: var(--text-muted); margin: 0 0 14px; line-height: 1.6; }

.form-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 12px 16px; margin-bottom: 16px; }
.form-item { display: flex; flex-direction: column; gap: 4px; }
.form-item label { font-size: 12px; color: var(--text-muted); }

.cmd-box { background: rgba(0,0,0,0.3); border: 1px solid var(--border); border-radius: 8px; overflow: hidden; margin-bottom: 12px; }
.cmd-header { display: flex; justify-content: space-between; align-items: center; padding: 6px 12px; background: rgba(255,255,255,0.03); border-bottom: 1px solid var(--border); }
.cmd-label { font-size: 11px; color: var(--text-muted); font-family: var(--mono); }
.cmd-text { margin: 0; padding: 12px; font-family: var(--mono); font-size: 12px; color: var(--text); white-space: pre-wrap; word-break: break-all; line-height: 1.5; }

.actions { display: flex; align-items: center; gap: 12px; }
.check-alert { flex: 1; }

.collapse-head { display: flex; justify-content: space-between; align-items: center; cursor: pointer; }
.caret { transition: transform 0.2s; color: var(--text-muted); }
.caret.open { transform: rotate(180deg); }

.proxy-body { padding-top: 14px; }
.proxy-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 20px; margin-bottom: 18px; }
.proxy-col { display: flex; flex-direction: column; gap: 10px; }
.proxy-col-head { display: flex; align-items: center; gap: 8px; font-size: 13px; font-weight: 600; padding-bottom: 8px; border-bottom: 1px solid var(--border); margin-bottom: 4px; }
.proxy-col-head .dot { width: 8px; height: 8px; border-radius: 50%; }
.proxy-col-head.hub .dot { background: var(--accent); }
.proxy-col-head.edge .dot { background: var(--chart-green, #67C23A); }

.gen-section { margin-top: 8px; }
.gen-title { font-size: 13px; font-weight: 600; margin: 16px 0 8px; color: var(--text); }

.deploy-steps { margin-top: 8px; padding: 14px 16px; background: rgba(255,255,255,0.02); border-radius: 8px; border: 1px solid var(--border); }
.steps-list { margin: 8px 0 0; padding-left: 20px; font-size: 12px; color: var(--text-dim); line-height: 1.8; }
.steps-list b { color: var(--text); }
.steps-list code { background: rgba(0,0,0,0.3); padding: 1px 6px; border-radius: 3px; font-family: var(--mono); font-size: 11px; }

@media (max-width: 768px) {
  .proxy-grid { grid-template-columns: 1fr; }
}
</style>
