<template>
  <div class="mw-tab">
    <RefreshBar :loading="loading" @refresh="load" />
    <!-- 空状态 -->
    <div v-if="!loading && instances.length === 0" class="empty-guide glass">
      <div class="empty-icon">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="64" height="64">
          <ellipse cx="12" cy="5" rx="9" ry="3"/>
          <path d="M3 5v6c0 1.66 4 3 9 3s9-1.34 9-3V5"/>
          <path d="M3 11v6c0 1.66 4 3 9 3s9-1.34 9-3v-6"/>
        </svg>
      </div>
      <h2 class="empty-title">尚未配置 MySQL 监控</h2>
      <p class="empty-desc">当前没有已采集的 MySQL 实例。请在运行 Agent 的节点上配置 MySQL 实例连接信息。</p>
      <p class="empty-hint">配置完成后约 15-30 秒数据将出现在此页面。</p>
    </div>

    <template v-if="instances.length > 0">
      <!-- KPI 概览卡片：与 Redis 统一使用 KpiCard 组件 -->
      <div class="kpi-row">
        <KpiCard :value="stats.total" label="实例总数" tone="total">
          <template #icon><el-icon :size="20"><Grid /></el-icon></template>
        </KpiCard>
        <KpiCard :value="stats.up" label="在线实例" tone="up">
          <template #icon><el-icon :size="20"><CircleCheck /></el-icon></template>
        </KpiCard>
        <KpiCard :value="stats.down" label="离线实例" tone="down">
          <template #icon><el-icon :size="20"><CircleClose /></el-icon></template>
        </KpiCard>
        <KpiCard :value="formatNum(stats.totalConnections)" label="总连接数" tone="conn">
          <template #icon><el-icon :size="20"><Connection /></el-icon></template>
        </KpiCard>
        <KpiCard :value="formatNum(stats.totalQPS)" label="总 QPS" tone="ops">
          <template #icon><el-icon :size="20"><DataLine /></el-icon></template>
        </KpiCard>
        <KpiCard :value="stats.totalSlowQueries" label="慢查询累计" tone="alert">
          <template #icon><el-icon :size="20"><Bell /></el-icon></template>
        </KpiCard>
      </div>

      <!-- 实例拓扑：与 Redis 对齐的主从/集群关系视图 -->
      <div class="chart-section glass" v-if="instances.length">
        <div class="section-title">实例拓扑</div>

        <!-- 集群组（Group Replication / InnoDB Cluster，多节点多主） -->
        <template v-if="topologyGroups.clusters.length">
          <div v-for="grp in topologyGroups.clusters" :key="'c-'+grp.name" class="topo-group">
            <div class="topo-group-header">
              <span class="topo-group-title">
                <el-icon :size="18"><Connection /></el-icon>
                <strong>集群 {{ grp.name || '未命名' }}</strong>
              </span>
              <span class="topo-meta">
                <span class="badge" :class="clusterHealthClass(grp)">{{ clusterHealthText(grp) }}</span>
                <span class="dim">节点 {{ (grp.masters.length + grp.slaves.length) || grp.nodes.length }}</span>
              </span>
            </div>
            <div class="topo-grid">
              <div v-for="i in grp.nodes" :key="'cn-'+i.instance"
                   class="rel-node rel-standalone" :class="{ 'is-down': !i.up }" @click="openDetail(i)">
                <div class="rel-node-name" :title="i.instance">
                  <span class="role-badge role-badge-m">P</span>
                  {{ i.name || i.instance }}
                </div>
                <div class="rel-node-meta">
                  <span :class="['dot', i.up ? 'up' : 'down']"></span>
                  <span>{{ i.up ? '在线' : '离线' }}</span>
                  <span class="dim">·</span>
                  <span>{{ i.role === 'master' ? 'PRIMARY' : (i.role === 'slave' ? 'SECONDARY' : i.role) }}</span>
                </div>
              </div>
            </div>
          </div>
        </template>

        <!-- 主从组 -->
        <template v-if="topologyGroups.replications.length">
          <div v-for="grp in topologyGroups.replications" :key="'r-'+grp.name" class="topo-group">
            <div class="topo-group-header">
              <span class="topo-group-title">
                <el-icon :size="18"><Connection /></el-icon>
                <strong>主从 {{ grp.name }}</strong>
              </span>
              <span class="topo-meta">
                <span class="badge" :class="clusterHealthClass(grp)">{{ clusterHealthText(grp) }}</span>
                <span class="dim">主库: {{ grp.masters.length }}</span>
                <span class="dim">· 从库: {{ grp.slaves.length }}</span>
              </span>
            </div>
            <div class="ms-tree">
              <div v-for="(m, idx) in grp.masters" :key="'rm-'+idx" class="ms-unit">
                <div class="rel-node rel-master ms-master" :class="{ 'is-down': !m.up }" @click="openDetail(m)">
                  <div class="rel-node-name" :title="m.instance">
                    <span class="role-badge role-badge-m">M</span>
                    {{ m.name || m.instance }}
                  </div>
                  <div class="rel-node-meta">
                    <span :class="['dot', m.up ? 'up' : 'down']"></span>
                    <span>{{ m.up ? '在线' : '离线' }}</span>
                    <span class="dim">·</span>
                    <span>{{ formatNum(m.threadsConnected) }} 连接</span>
                    <span class="dim">·</span>
                    <span>{{ formatNum(m.queriesPerSec) }} QPS</span>
                  </div>
                </div>
                <div v-if="grp.slavesByMaster[m.instance].length" class="ms-branch">
                  <div class="ms-branch-rail">
                    <span class="ms-rail-repl" title="数据复制方向：主库将 binlog 同步给从库">复制 ↓</span>
                    <span class="ms-rail-fo" title="主库宕机时对应从库提升为新主库">故障转移 ↑</span>
                  </div>
                  <div class="ms-slaves">
                    <div v-for="s in grp.slavesByMaster[m.instance]" :key="'rs-'+s.instance"
                         class="rel-node rel-slave ms-slave-card" :class="{ 'is-down': !s.up }" @click.stop="openDetail(s)">
                      <div class="ms-slave-head">
                        <span class="role-badge role-badge-s">S</span>
                        <span class="mono" :title="s.instance">{{ s.name || s.instance }}</span>
                      </div>
                      <div class="ms-slave-meta">
                        <span :class="['dot', s.up ? 'up' : 'down']"></span>
                        <span>{{ s.up ? '在线' : '离线' }}</span>
                        <span class="dim">·</span>
                        <span>{{ s.up ? formatNum(s.queriesPerSec) + ' QPS' : '离线' }}</span>
                        <span v-if="s.up && s.secondsBehindMaster != null" class="dim">· 延迟 {{ s.secondsBehindMaster }}s</span>
                      </div>
                    </div>
                  </div>
                </div>
              </div>
            </div>
            <div class="topo-legend">
              <span class="topo-legend-item"><span class="legend-line legend-solid"></span>数据复制（master → slave）</span>
              <span class="topo-legend-item"><span class="legend-line legend-dash"></span>故障转移（slave 升主，slave → master）</span>
            </div>
            <!-- 未关联主节点的从库（replicaOf 为空，常见于 agent 未升级二进制） -->
            <div v-if="grp.unlinkedSlaves.length" class="unlinked-block">
              <div class="unlinked-label">
                <span class="role-badge role-badge-s">S</span>
                未关联主节点的从库（{{ grp.unlinkedSlaves.length }} 个）— agent 升级后将自动关联
              </div>
              <div class="unlinked-list">
                <div v-for="s in grp.unlinkedSlaves" :key="'ul-'+s.instance"
                     class="rel-node rel-slave ms-slave-card" :class="{ 'is-down': !s.up }" @click.stop="openDetail(s)">
                  <div class="ms-slave-head">
                    <span class="role-badge role-badge-s">S</span>
                    <span class="mono" :title="s.instance">{{ s.name || s.instance }}</span>
                  </div>
                  <div class="ms-slave-meta">
                    <span :class="['dot', s.up ? 'up' : 'down']"></span>
                    <span>{{ s.up ? '在线' : '离线' }}</span>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </template>

        <!-- 独立实例 -->
        <template v-if="topologyGroups.standalones.length">
          <div class="topo-group">
            <div class="topo-group-header">
              <span class="topo-group-title">
                <el-icon :size="18"><Grid /></el-icon>
                <strong>独立实例</strong>
              </span>
              <span class="dim">共 {{ topologyGroups.standalones.length }} 个</span>
            </div>
            <div class="topo-grid">
              <div v-for="i in topologyGroups.standalones" :key="'sa-'+i.instance"
                   class="rel-node rel-standalone" :class="{ 'is-down': !i.up }" @click="openDetail(i)">
                <div class="rel-node-name" :title="i.instance">{{ i.name || i.instance }}</div>
                <div class="rel-node-meta">
                  <span :class="['dot', i.up ? 'up' : 'down']"></span>
                  <span>{{ i.up ? '在线' : '离线' }}</span>
                  <span class="dim">·</span>
                  <span>{{ i.instance }}</span>
                </div>
                <div class="rel-node-meta">
                  <span>{{ formatNum(i.threadsConnected) }} 连接</span>
                  <span class="dim">·</span>
                  <span>{{ formatNum(i.queriesPerSec) }} QPS</span>
                </div>
              </div>
            </div>
          </div>
        </template>
      </div>

      <!-- 实例列表 -->
      <div class="mw-list glass">
        <div class="mw-list-title">实例列表</div>
        <el-table :data="pagedInstances" class="mysql-table" style="width: 100%" @row-click="openDetail" :row-class-name="rowClass" size="small" stripe @sort-change="onSortChange">
          <el-table-column prop="instance" label="实例地址" min-width="180" show-overflow-tooltip />
          <el-table-column prop="name" label="名称" min-width="130" show-overflow-tooltip>
            <template #header>
              <el-tooltip content="Agent 配置中指定的实例别名/名称；未配置时为空" placement="top">
                <span>名称 <el-icon :size="12" style="vertical-align: middle; margin-left: 2px;"><QuestionFilled /></el-icon></span>
              </el-tooltip>
            </template>
            <template #default="{ row }">
              <span class="text-muted">{{ row.name || '-' }}</span>
            </template>
          </el-table-column>
          <el-table-column prop="role" label="角色" min-width="90">
            <template #default="{ row }">
              <MwRoleTag :role="row.role" />
            </template>
          </el-table-column>
          <el-table-column prop="version" label="版本" min-width="140" show-overflow-tooltip />
          <el-table-column label="状态" min-width="100">
            <template #default="{ row }">
              <MwStatusDot :status="row.up ? 'normal' : 'abnormal'" :label="row.up ? '正常' : '离线'" />
            </template>
          </el-table-column>
          <el-table-column prop="threadsConnected" label="连接数" min-width="110" sortable />
          <el-table-column prop="queriesPerSec" label="QPS" min-width="100" sortable />
          <el-table-column prop="bufferPoolHitRate" label="缓冲命中率" min-width="150" sortable>
            <template #default="{ row }">
              <span :class="hitRateClass(row.bufferPoolHitRate)">{{ row.bufferPoolHitRate ? row.bufferPoolHitRate.toFixed(1) + '%' : '-' }}</span>
            </template>
          </el-table-column>
          <el-table-column prop="secondsBehindMaster" label="复制延迟(s)" min-width="150" sortable />
          <el-table-column prop="uptime" label="运行时长" min-width="120">
            <template #default="{ row }">{{ formatUptime(row.uptime) }}</template>
          </el-table-column>
        </el-table>
        <div class="pager">
          <el-pagination background layout="total, sizes, prev, pager, next, jumper" :total="sortedInstances.length" :page-size="pageSize" :current-page="currentPage" :page-sizes="[10,20,50,100]" @current-change="v => currentPage = v" @size-change="v => { pageSize = v; currentPage = 1 }" />
        </div>
      </div>
    </template>

    <!-- 详情抽屉 -->
    <el-drawer v-model="drawerVisible" :title="detailTitle" size="50%" :destroy-on-close="true">
      <div v-if="selected" class="detail-content">
        <div class="detail-meta">
          <div class="meta-item"><span class="meta-label">实例</span><span class="mono">{{ selected.instance }}</span></div>
          <div class="meta-item"><span class="meta-label">节点</span>{{ selected.node }}</div>
          <div class="meta-item"><span class="meta-label">角色</span>{{ selected.role }}</div>
          <div class="meta-item"><span class="meta-label">版本</span>{{ selected.version }}</div>
          <div class="meta-item"><span class="meta-label">拓扑</span>{{ selected.topology }}</div>
          <div class="meta-item" v-if="selected.replicaOf"><span class="meta-label">主库</span>{{ selected.replicaOf }}</div>
        </div>
        <div class="detail-metrics">
          <div class="metric-grid">
            <div class="metric-cell"><div class="mc-label">连接数</div><div class="mc-value">{{ selected.threadsConnected }}</div></div>
            <div class="metric-cell"><div class="mc-label">活跃连接</div><div class="mc-value">{{ selected.threadsRunning }}</div></div>
            <div class="metric-cell"><div class="mc-label">最大连接</div><div class="mc-value">{{ selected.maxConnections }}</div></div>
            <div class="metric-cell"><div class="mc-label">QPS</div><div class="mc-value">{{ formatNum(selected.queriesPerSec) }}</div></div>
            <div class="metric-cell"><div class="mc-label">慢查询</div><div class="mc-value">{{ selected.slowQueries }}</div></div>
            <div class="metric-cell"><div class="mc-label">缓冲命中率</div><div class="mc-value">{{ selected.bufferPoolHitRate ? selected.bufferPoolHitRate.toFixed(1) + '%' : '-' }}</div></div>
            <div class="metric-cell"><div class="mc-label">行锁等待</div><div class="mc-value">{{ selected.rowLockWaits }}</div></div>
            <div class="metric-cell"><div class="mc-label">死锁</div><div class="mc-value">{{ selected.deadlocks }}</div></div>
            <div class="metric-cell"><div class="mc-label">复制延迟</div><div class="mc-value">{{ selected.secondsBehindMaster }}s</div></div>
            <div class="metric-cell"><div class="mc-label">Commit</div><div class="mc-value">{{ formatNum(selected.comCommit) }}</div></div>
            <div class="metric-cell"><div class="mc-label">Rollback</div><div class="mc-value">{{ formatNum(selected.comRollback) }}</div></div>
            <div class="metric-cell"><div class="mc-label">运行时长</div><div class="mc-value">{{ formatUptime(selected.uptime) }}</div></div>
          </div>
        </div>
        <div class="chart-box" ref="chartRef"></div>
      </div>
    </el-drawer>
  </div>
</template>

<script setup>
import { ref, onMounted, computed, nextTick, watch } from 'vue'
import * as echarts from 'echarts'
import http from '../../api/http'
import RefreshBar from '../RefreshBar.vue'
import KpiCard from '../KpiCard.vue'
import MwStatusDot from '../mw/MwStatusDot.vue'
import MwRoleTag from '../mw/MwRoleTag.vue'
import {
  Grid,
  CircleCheck,
  CircleClose,
  Connection,
  DataLine,
  Bell,
  QuestionFilled,
} from '@element-plus/icons-vue'

const loading = ref(true)
const instances = ref([])
const drawerVisible = ref(false)
const selected = ref(null)
const chartRef = ref(null)
let chartInstance = null

const stats = computed(() => {
  const s = { total: 0, up: 0, down: 0, totalConnections: 0, totalQPS: 0, totalSlowQueries: 0 }
  for (const i of instances.value) {
    s.total++
    if (i.up) s.up++; else s.down++
    s.totalConnections += i.threadsConnected || 0
    s.totalQPS += i.queriesPerSec || 0
    s.totalSlowQueries += i.slowQueries || 0
  }
  return s
})

const detailTitle = computed(() => selected.value ? `MySQL 详情 - ${selected.value.name || selected.value.instance}` : '详情')

// 按拓扑分组，对齐 Redis 的"实例拓扑"展示
const topologyGroups = computed(() => {
  const reps = instances.value.filter((i) => (i.topology || '').toLowerCase() === 'replication')
  const clusters = instances.value.filter((i) => (i.topology || '').toLowerCase() === 'cluster')
  const standalone = instances.value.filter((i) => {
    const t = (i.topology || '').toLowerCase()
    return t !== 'replication' && t !== 'cluster'
  })

  const groupBy = (list) => {
    const map = {}
    list.forEach((i) => {
      const g = i.group || 'default'
      ;(map[g] = map[g] || []).push(i)
    })
    return Object.keys(map).map((name) => {
      const items = map[name]
      const masters = items.filter((i) => (i.role || '').toLowerCase() === 'master')
      const slaves = items.filter((i) => (i.role || '').toLowerCase() === 'slave')
      const slavesByMaster = {}
      masters.forEach((m) => {
        slavesByMaster[m.instance] = slaves.filter((s) => s.replicaOf === m.instance)
      })
      const unlinkedSlaves = slaves.filter((s) => !s.replicaOf || !masters.some((m) => m.instance === s.replicaOf))
      return { name, masters, slaves, slavesByMaster, unlinkedSlaves, nodes: items }
    })
  }

  return {
    replications: groupBy(reps),
    clusters: groupBy(clusters),
    standalones: standalone,
  }
})

function clusterHealth(grp) {
  const items = grp.masters.concat(grp.slaves, grp.nodes || [])
  if (!items.length) return 'unknown'
  if (items.some((i) => !i.up)) return 'bad'
  if (items.some((i) => i.up && i.secondsBehindMaster != null && i.secondsBehindMaster > 30)) return 'warn'
  return 'good'
}
function clusterHealthClass(grp) {
  const h = clusterHealth(grp)
  return h === 'good' ? 'badge-ok' : h === 'warn' ? 'badge-warn' : h === 'bad' ? 'badge-down' : 'badge-unknown'
}
function clusterHealthText(grp) {
  const h = clusterHealth(grp)
  return h === 'good' ? '运行正常' : h === 'warn' ? '延迟偏高' : h === 'bad' ? '存在离线' : '未知'
}

async function load() {
  loading.value = true
  try {
    const data = await http.get('/api/v1/middleware/mysql/instances')
    instances.value = data.instances || []
  } catch (e) {
    console.error('加载 MySQL 实例失败', e)
  } finally {
    loading.value = false
  }
}

function openDetail(row) {
  selected.value = row
  drawerVisible.value = true
  nextTick(() => loadTrendChart(row))
}

async function loadTrendChart(row) {
  if (!chartRef.value) return
  if (chartInstance) { chartInstance.dispose(); chartInstance = null }
  chartInstance = echarts.init(chartRef.value)
  const end = Date.now()
  const start = end - 3600 * 1000
  try {
    const [qpsData, connData] = await Promise.all([
      http.get(`/api/v1/query/range?node=${row.node}&metric=mysql_queries_per_sec&start=${start}&end=${end}&step=60`),
      http.get(`/api/v1/query/range?node=${row.node}&metric=mysql_threads_connected&start=${start}&end=${end}&step=60`),
    ])
    const series = []
    if (qpsData.series) for (const s of qpsData.series) {
      if (s.labels?.instance === row.instance) series.push({ name: 'QPS', type: 'line', data: s.points.map(p => [p.timestamp, p.value]), smooth: true })
    }
    if (connData.series) for (const s of connData.series) {
      if (s.labels?.instance === row.instance) series.push({ name: '连接数', type: 'line', yAxisIndex: 1, data: s.points.map(p => [p.timestamp, p.value]), smooth: true })
    }
    chartInstance.setOption({
      tooltip: { trigger: 'axis' },
      legend: { data: series.map(s => s.name), textStyle: { color: '#8b949e' } },
      grid: { left: 50, right: 50, top: 40, bottom: 30 },
      xAxis: { type: 'time' },
      yAxis: [
        { type: 'value', name: 'QPS' },
        { type: 'value', name: '连接数' },
      ],
      series,
    })
  } catch (e) { console.error(e) }
}

function formatNum(n) { return n != null ? Number(n).toLocaleString() : '-' }
function formatUptime(s) { if (!s) return '-'; const d = Math.floor(s / 86400); const h = Math.floor((s % 86400) / 3600); return d > 0 ? `${d}天${h}小时` : `${h}小时` }
function hitRateClass(v) { if (!v) return ''; if (v >= 99) return 'metric-good'; if (v >= 90) return 'metric-warn'; return 'metric-bad' }
function rowClass({ row }) { return row.up ? '' : 'row-down' }

const currentPage = ref(1)
const pageSize = ref(10)
const sortState = ref({ prop: '', order: '' })
function onSortChange({ prop, order }) { sortState.value = { prop, order }; currentPage.value = 1 }
const sortedInstances = computed(() => {
  const arr = [...instances.value]
  const { prop, order } = sortState.value
  if (prop && order) {
    arr.sort((a, b) => {
      let av = a[prop], bv = b[prop]
      if (typeof av === 'string') return order === 'ascending' ? av.localeCompare(bv) : bv.localeCompare(av)
      av = av ?? 0; bv = bv ?? 0
      return order === 'ascending' ? av - bv : bv - av
    })
  }
  return arr
})
const pagedInstances = computed(() => sortedInstances.value.slice((currentPage.value - 1) * pageSize.value, currentPage.value * pageSize.value))
watch(instances, () => { currentPage.value = 1 })

onMounted(load)
</script>

<style scoped>
.mw-tab { padding: 4px 0; }
.empty-guide { text-align: center; padding: 48px 24px; }
.empty-icon { color: var(--text-muted); margin-bottom: 16px; }
.empty-title { font-size: 18px; font-weight: 600; margin: 0 0 8px; }
.empty-desc { color: var(--text-dim); margin: 0 0 8px; font-size: 13px; }
.empty-hint { color: var(--text-muted); font-size: 12px; }
.kpi-row { display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); gap: 12px; margin-bottom: 16px; }
.chart-section { padding: 16px; margin-bottom: 16px; }
.section-title { font-size: 14px; font-weight: 600; margin-bottom: 12px; color: var(--text); }
.mysql-table :deep(th) { white-space: nowrap; }
.text-muted { color: var(--text-muted); }
.dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 4px; }
.dot.up { background: #4ade80; box-shadow: 0 0 6px rgba(74, 222, 128, 0.5); }
.dot.down { background: #f87171; }
.metric-good { color: #4ade80; }
.metric-warn { color: var(--warn); }
.metric-bad { color: #f87171; }
:deep(.row-down) { opacity: 0.6; }
.detail-content { padding: 0 20px; }
.detail-meta { display: flex; flex-wrap: wrap; gap: 16px; margin-bottom: 24px; padding: 16px; background: rgba(255,255,255,0.03); border-radius: 8px; }
.meta-item { font-size: 13px; }
.meta-label { color: var(--text-muted); margin-right: 6px; }
.mono { font-family: var(--mono); }
.metric-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(140px, 1fr)); gap: 12px; margin-bottom: 24px; }
.metric-cell { padding: 12px; background: rgba(255,255,255,0.03); border-radius: 8px; text-align: center; }
.mc-label { font-size: 11px; color: var(--text-muted); margin-bottom: 4px; }
.mc-value { font-size: 18px; font-weight: 600; }
.chart-box { width: 100%; height: 300px; }

/* ===== 实例拓扑（对齐 Redis 拓扑展示） ===== */
.topo-group { margin-bottom: 22px; padding: 14px; border: 1px solid var(--border); border-radius: 10px; background: rgba(255,255,255,0.02); }
.topo-group-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 14px; flex-wrap: wrap; gap: 8px; }
.topo-group-title { display: inline-flex; align-items: center; gap: 6px; font-size: 14px; color: var(--text); }
.topo-group-title .el-icon { color: var(--accent); }
.topo-meta { display: inline-flex; align-items: center; gap: 10px; font-size: 12px; }
.dim { color: var(--text-muted); }
.badge { padding: 2px 8px; border-radius: 10px; font-size: 12px; font-weight: 600; }
.badge-ok { color: #4ade80; background: rgba(34, 197, 94, 0.15); }
.badge-warn { color: #fbbf24; background: rgba(234, 179, 8, 0.15); }
.badge-down { color: #f87171; background: rgba(239, 68, 68, 0.18); }
.badge-unknown { color: var(--text-muted); background: rgba(148, 163, 184, 0.12); }

.topo-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 12px; }
.rel-node { padding: 12px 14px; border-radius: 10px; cursor: pointer; border: 1px solid var(--border); background: var(--bg-elev); transition: transform 0.15s, box-shadow 0.15s; }
.rel-node:hover { transform: translateY(-2px); box-shadow: 0 4px 16px rgba(0,0,0,0.3); }
.rel-node.is-down { opacity: 0.6; }
.rel-node-name { font-size: 14px; font-weight: 600; color: var(--text); display: flex; align-items: center; gap: 6px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.rel-node-meta { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--text-dim); margin-top: 6px; }
.rel-standalone { border-left: 4px solid var(--chart-blue); }

/* 主从树 */
.ms-tree { display: flex; flex-direction: column; gap: 18px; margin-top: 6px; }
.ms-unit { display: flex; gap: 18px; align-items: stretch; flex-wrap: wrap; }
.rel-master { border-left: 4px solid var(--chart-orange); min-width: 220px; }
.ms-master { flex: 0 0 auto; }
.ms-branch { flex: 1; min-width: 240px; display: flex; flex-direction: column; gap: 10px; }
.ms-branch-rail { display: flex; gap: 14px; font-size: 11px; color: var(--text-muted); padding-left: 4px; }
.legend-line { display: inline-block; width: 26px; height: 0; vertical-align: middle; margin-right: 4px; }
.legend-solid { border-top: 2px solid var(--chart-orange); }
.legend-dash { border-top: 2px dashed var(--chart-green); }
.ms-slaves { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 12px; position: relative; padding-left: 16px; border-left: 2px solid var(--chart-orange); }
.ms-slave-card { border-left: 4px solid var(--chart-green); }
.ms-slave-head { display: flex; align-items: center; gap: 6px; font-size: 13px; font-weight: 600; color: var(--text); overflow: hidden; }
.ms-slave-head .mono { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ms-slave-meta { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--text-dim); margin-top: 6px; flex-wrap: wrap; }
.role-badge { display: inline-flex; align-items: center; justify-content: center; width: 18px; height: 18px; border-radius: 4px; font-size: 11px; font-weight: 700; color: #fff; flex: 0 0 auto; }
.role-badge-m { background: var(--chart-orange); }
.role-badge-s { background: var(--chart-green); }

.topo-legend { display: flex; gap: 18px; margin-top: 10px; font-size: 12px; color: var(--text-muted); flex-wrap: wrap; }
.unlinked-block { margin-top: 14px; padding: 10px 12px; border: 1px dashed var(--border); border-radius: 8px; background: rgba(245,158,11,0.04); }
.unlinked-label { font-size: 12px; color: var(--text-dim); display: flex; align-items: center; gap: 6px; margin-bottom: 10px; }
.unlinked-list { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 12px; }
</style>
