<template>
  <div class="redis-tab">
    <RefreshBar :loading="loading" :intervals="redisIntervals" v-model:interval="refreshInterval" @refresh="loadInstances" />
    <!-- 空状态：无实例时引导用户配置 -->
    <div v-if="!loading && instances.length === 0" class="empty-guide glass">
      <div class="empty-icon">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="64" height="64">
          <ellipse cx="12" cy="5" rx="9" ry="3"/>
          <path d="M3 5v6c0 1.66 4 3 9 3s9-1.34 9-3V5"/>
          <path d="M3 11v6c0 1.66 4 3 9 3s9-1.34 9-3v-6"/>
        </svg>
      </div>
      <h2 class="empty-title">尚未配置 Redis 监控</h2>
      <p class="empty-desc">当前没有已采集的 Redis 实例。请在运行 Agent 的节点上执行以下命令，按引导配置 Redis 实例：</p>
      <div class="empty-cmd">
        <code>{{ redisInstallCmd }}</code>
        <button class="copy-btn" @click="copyCmd" title="复制命令">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="14" height="14">
            <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
            <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
          </svg>
        </button>
      </div>
      <p class="empty-hint">若节点没有本地脚本，先下载到本地再交互运行：<code>{{ redisInstallCmdAlt }}</code></p>
      <p class="empty-hint">配置完成后约 15-30 秒数据将出现在此页面。详细配置说明请参阅 README.md。</p>
    </div>

    <!-- 有数据时的正常布局 -->
    <template v-if="instances.length > 0">
    <!-- ===== 区块1：统计概览卡片 ===== -->
    <div class="kpi-row">
      <KpiCard :value="stats.total" label="实例总数" tone="total">
        <template #icon><DatabaseIcon /></template>
      </KpiCard>
      <KpiCard :value="stats.up" label="在线实例" tone="up">
        <template #icon><CheckCircleIcon /></template>
      </KpiCard>
      <KpiCard :value="stats.down" label="离线实例" tone="down">
        <template #icon><XCircleIcon /></template>
      </KpiCard>
      <KpiCard :value="formatBytes(stats.totalMemory)" label="总内存使用" tone="mem">
        <template #icon><MemoryIcon /></template>
      </KpiCard>
      <KpiCard :value="stats.totalClients" label="总连接客户端" tone="conn">
        <template #icon><ConnectionIcon /></template>
      </KpiCard>
      <KpiCard :value="formatNum(stats.totalOps)" label="总 OPS" tone="ops">
        <template #icon><ActivityIcon /></template>
      </KpiCard>
      <KpiCard :value="stats.clusterCount" label="集群/哨兵组" tone="cluster">
        <template #icon><ClusterIcon /></template>
      </KpiCard>
      <KpiCard :value="stats.alertCount" label="健康风险" :tone="stats.alertCount > 0 ? 'alert' : 'ok'">
        <template #icon><AlertIcon /></template>
      </KpiCard>
    </div>

    <div v-if="stats.alertCount > 0" class="alert-summary glass">
      <div class="alert-summary-head">
        <AlertIcon />
        <span>检测到 {{ stats.alertCount }} 个实例存在健康风险</span>
      </div>
      <div class="alert-summary-desc">在线实例仍可连接；异常实例表示触发了页面健康检查规则，请优先查看下方实例列表的“异常原因”。</div>
      <div class="alert-summary-list">
        <span v-for="item in alertInstances.slice(0, 6)" :key="item.node + item.instance" class="issue-chip" @click="openDetail(item)">
          <span class="mono">{{ item.instance }}</span>
          <span class="issue-chip-reason">{{ issueReasons(item).join('；') }}</span>
        </span>
        <span v-if="alertInstances.length > 6" class="issue-chip muted">+{{ alertInstances.length - 6 }} 个</span>
      </div>
    </div>

    <!-- ===== 区块1.5：实例拓扑与集群关系 ===== -->
    <div class="chart-section glass" v-if="instances.length">
      <div class="section-title">实例拓扑</div>

      <!-- 集群组（多集群横向并排，单集群自适应宽度） -->
      <template v-if="topologyGroups.clusters.length">
        <div class="topo-clusters-grid">
        <div v-for="grp in topologyGroups.clusters" :key="'c-'+grp.name" class="topo-group topo-cluster-card">
          <div class="topo-group-header">
            <span class="topo-group-title">
              <ClusterIcon />
              <strong>集群 {{ grp.name || '未命名' }}</strong>
            </span>
            <span class="topo-meta">
              <span class="badge" :class="clusterHealthClass(grp)">
                {{ clusterHealthText(grp) }}
              </span>
              <span class="dim">masters: {{ grp.masters.length }}</span>
              <span class="dim">· replicas: {{ grp.slaves.length }}</span>
              <span class="dim">· 总节点 {{ grp.masters.length + grp.slaves.length }}</span>
            </span>
          </div>

          <!-- 主从复制与故障转移：复制 ↓ 实线（master→slave），故障转移 ↑ 虚线（slave 升主） -->
          <div class="ms-section">
            <div class="ms-section-label">
              <span class="ms-label-repl">数据复制 ↓ master → slave</span>
              <span class="ms-label-fo">故障转移 ↑ slave 升主</span>
            </div>
            <div class="ms-tree">
              <div v-for="(m, idx) in grp.masters" :key="'cm-'+m.instance" class="ms-unit">
                <!-- Master 节点 -->
                <div
                  class="rel-node rel-master ms-master"
                  :class="{ 'is-down': !m.up, 'is-alert': isAlert(m) }"
                  :style="{ borderLeftColor: slotColor(grp, idx) }"
                  @click="openDetail(m)"
                >
                  <div class="rel-node-name" :title="m.instance">
                    <span class="role-badge role-badge-m">M</span>
                    {{ m.instance }}
                  </div>
                  <div class="rel-node-meta">
                    <span :class="['dot', m.up ? 'up' : 'down']"></span>
                    <span>{{ m.up ? '在线' : '离线' }}</span>
                    <span class="dim">·</span>
                    <span>{{ formatNum(m.ops) }} ops/s</span>
                    <span class="dim">·</span>
                    <span>{{ formatBytes(m.usedMemory) }}</span>
                  </div>
                  <div class="rel-node-meta" v-if="m.up">
                    <span class="mono">{{ m.instance }}</span>
                  </div>
                </div>
                <!-- 复制链路 + failover 路径（按预聚合映射直接取，避免匹配失败） -->
                <div v-if="(grp.slavesByMaster[m.instance] || []).length" class="ms-branch">
                  <div class="ms-branch-rail">
                    <span class="ms-rail-repl" title="数据复制方向：master 将写入同步给 slave">复制 ↓</span>
                    <span class="ms-rail-fo" title="故障转移方向：master 宕机时，对应 slave 提升为新 master">故障转移 ↑</span>
                  </div>
                  <div class="ms-slaves">
                    <div
                      v-for="s in grp.slavesByMaster[m.instance]"
                      :key="'cs-'+s.instance"
                      class="rel-node rel-slave ms-slave-card"
                      :class="{ 'is-down': !s.up }"
                      @click.stop="openDetail(s)"
                    >
                      <div class="ms-slave-head">
                        <span class="role-badge role-badge-s">S</span>
                        <span class="mono" :title="s.instance">{{ s.instance }}</span>
                      </div>
                      <div class="ms-slave-meta">
                        <span :class="['dot', s.up ? 'up' : 'down']"></span>
                        <span>{{ s.up ? '在线' : '离线' }}</span>
                        <span class="dim">·</span>
                        <span>{{ s.up ? formatNum(s.ops) + ' ops/s' : '离线' }}</span>
                        <span v-if="s.up && s.replicationLag !== undefined && s.replicationLag !== null" class="dim">· lag {{ s.replicationLag }}s</span>
                        <span class="dim">·</span>
                        <span>{{ formatBytes(s.usedMemory) }}</span>
                      </div>
                    </div>
                  </div>
                </div>
              </div>
            </div>
          </div>
          <div class="topo-legend">
            <span class="topo-legend-item" title="master 将写入同步给 replica"><span class="legend-line legend-solid"></span>数据复制（master → slave）</span>
            <span class="topo-legend-item" title="master 宕机时对应 slave 提升为新 master"><span class="legend-line legend-dash"></span>故障转移（slave 升主，slave → master）</span>
          </div>
          <!-- 未关联主节点的从节点（replicaOf 为空，常见于 agent 未升级二进制） -->
          <div v-if="grp.unlinkedSlaves.length" class="unlinked-block">
            <div class="unlinked-label">
              <span class="role-badge role-badge-s">S</span>
              未关联主节点的从节点（{{ grp.unlinkedSlaves.length }} 个）— agent 升级后将自动关联
            </div>
            <div class="unlinked-list">
              <div v-for="s in grp.unlinkedSlaves" :key="'ul-'+s.instance"
                   class="rel-node rel-slave ms-slave-card"
                   :class="{ 'is-down': !s.up }"
                   @click.stop="openDetail(s)">
                <div class="ms-slave-head">
                  <span class="role-badge role-badge-s">S</span>
                  <span class="mono" :title="s.instance">{{ s.instance }}</span>
                </div>
                <div class="ms-slave-meta">
                  <span :class="['dot', s.up ? 'up' : 'down']"></span>
                  <span>{{ s.up ? '在线' : '离线' }}</span>
                  <span class="dim">·</span>
                  <span>{{ s.up ? formatNum(s.ops) + ' ops/s' : '离线' }}</span>
                  <span v-if="s.up && s.replicationLag !== undefined && s.replicationLag !== null" class="dim">· lag {{ s.replicationLag }}s</span>
                  <span class="dim">·</span>
                  <span>{{ formatBytes(s.usedMemory) }}</span>
                </div>
              </div>
            </div>
          </div>
        </div>
        </div>
      </template>

      <!-- 哨兵组 -->
      <template v-if="topologyGroups.sentinels.length">
        <div v-for="grp in topologyGroups.sentinels" :key="'s-'+grp.name" class="topo-group">
          <div class="topo-group-header">
            <span class="topo-group-title">
              <SentinelIcon />
              <strong>哨兵监控 {{ grp.name }}</strong>
            </span>
            <span class="topo-meta dim">
              sentinels: {{ grp.sentinels.length }} · masters: {{ grp.masters.length }}
            </span>
          </div>
          <div class="topo-relation topo-relation-sentinel">
            <div class="rel-edges rel-edges-left">
              <div v-for="(s, idx) in grp.sentinels" :key="'sn-'+idx" class="rel-node rel-sentinel" :class="{ 'is-down': !s.up }" @click="openDetail(s)">
                <div class="rel-node-name" :title="s.instance">{{ s.instance }}</div>
                <div class="rel-node-meta">
                  <span :class="['dot', s.up ? 'up' : 'down']"></span>
                  <span>{{ s.up ? '在线' : '离线' }}</span>
                  <span class="dim">·</span>
                  <span>监控 {{ s.sentinelMasters || 0 }} 个 master</span>
                  <span v-if="s.sentinelTilt" class="dim">· tilt</span>
                </div>
              </div>
            </div>
            <div class="rel-arrow">→ 监控 →</div>
            <div class="rel-edges rel-edges-right">
              <div v-for="(m, idx) in grp.masters" :key="'sm-'+idx" class="rel-node rel-master" :class="{ 'is-down': !m.up, 'is-alert': isAlert(m) }" @click="openDetail(m)">
                <div class="rel-node-name" :title="m.instance">{{ m.instance }}</div>
                <div class="rel-node-meta">
                  <span :class="['dot', m.up ? 'up' : 'down']"></span>
                  <span>{{ m.up ? '在线' : '离线' }}</span>
                  <span class="dim">·</span>
                  <span>repl offset {{ formatNum(m.replicationOffset) }}</span>
                </div>
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
              <ReplicaIcon />
              <strong>主从 {{ grp.name }}</strong>
            </span>
            <span class="topo-meta">
              <span class="badge" :class="clusterHealthClass(grp)">
                {{ clusterHealthText(grp) }}
              </span>
              <span class="dim">masters: {{ grp.masters.length }}</span>
              <span class="dim">· replicas: {{ grp.slaves.length }}</span>
            </span>
          </div>
          <div class="ms-tree">
            <div v-for="(m, idx) in grp.masters" :key="'rm-'+idx" class="ms-unit">
              <div class="rel-node rel-master ms-master" :class="{ 'is-down': !m.up, 'is-alert': isAlert(m) }" @click="openDetail(m)">
                <div class="rel-node-name" :title="m.instance">
                  <span class="role-badge role-badge-m">M</span>
                  {{ m.instance }}
                </div>
                <div class="rel-node-meta">
                  <span :class="['dot', m.up ? 'up' : 'down']"></span>
                  <span>{{ m.up ? '在线' : '离线' }}</span>
                  <span class="dim">·</span>
                  <span>{{ formatNum(m.ops) }} ops/s</span>
                  <span class="dim">·</span>
                  <span>{{ formatBytes(m.usedMemory) }}</span>
                </div>
              </div>
              <div v-if="grp.slaves.filter(s => s.replicaOf === m.instance).length" class="ms-branch">
                <div class="ms-branch-rail">
                  <span class="ms-rail-label">复制 ↓</span>
                  <span class="ms-rail-failover">↑ 故障转移</span>
                </div>
                <div class="ms-slaves">
                  <div v-for="s in grp.slaves.filter(ss => ss.replicaOf === m.instance)" :key="s.instance" class="rel-slave ms-slave" :class="{ 'is-down': !s.up }" @click.stop="openDetail(s)">
                    <span class="role-badge role-badge-s">S</span>
                    <span :title="s.instance">{{ s.instance }}</span>
                    <span :class="['dot', s.up ? 'up' : 'down']"></span>
                    <span class="dim">{{ s.up ? formatNum(s.ops) + ' ops/s' : '离线' }}</span>
                    <span v-if="s.up && s.replicationLag !== undefined && s.replicationLag !== null" class="dim">· lag {{ s.replicationLag }}s</span>
                  </div>
                </div>
              </div>
            </div>
          </div>
          <div class="topo-legend">
            <span class="topo-legend-item"><span class="legend-line legend-solid"></span>数据复制（master → slave）</span>
            <span class="topo-legend-item"><span class="legend-line legend-dash"></span>故障转移（slave 升主，slave → master）</span>
          </div>
        </div>
      </template>

      <!-- 独立实例 -->
      <template v-if="topologyGroups.standalones.length">
        <div class="topo-group">
          <div class="topo-group-header">
            <span class="topo-group-title">
              <ServerIcon />
              <strong>独立实例</strong>
            </span>
            <span class="dim">共 {{ topologyGroups.standalones.length }} 个</span>
          </div>
          <div class="topo-grid">
            <div v-for="(i, idx) in topologyGroups.standalones" :key="'sa-'+idx" class="rel-node rel-standalone" :class="{ 'is-down': !i.up, 'is-alert': isAlert(i) }" @click="openDetail(i)">
              <div class="rel-node-name" :title="i.instance">{{ i.name || i.instance }}</div>
              <div class="rel-node-meta">
                <span :class="['dot', i.up ? 'up' : 'down']"></span>
                <span>{{ i.up ? '在线' : '离线' }}</span>
                <span class="dim">·</span>
                <span>{{ i.instance }}</span>
              </div>
              <div class="rel-node-meta">
                <span>{{ formatNum(i.ops) }} ops/s</span>
                <span class="dim">·</span>
                <span>{{ formatBytes(i.usedMemory) }}</span>
                <span v-if="i.role === 'master' && i.connectedSlaves" class="dim">· {{ i.connectedSlaves }} slaves</span>
              </div>
            </div>
          </div>
        </div>
      </template>
    </div>

    <!-- ===== 区块2：实例列表 ===== -->
    <div class="table-section glass">
      <div class="table-toolbar">
        <div>
          <div class="section-title no-bar">实例列表</div>
          <div class="section-desc">核心运行指标。异常实例为页面本地判定结果，不等同于告警中心事件。</div>
        </div>
        <div class="toolbar-right">
          <el-select v-model="filterStatus" placeholder="状态" clearable size="small" style="width: 100px">
            <el-option label="全部" value="" />
            <el-option label="在线" value="up" />
            <el-option label="离线" value="down" />
          </el-select>
          <el-select v-model="filterTopology" placeholder="拓扑" clearable size="small" style="width: 120px">
            <el-option label="全部" value="" />
            <el-option label="单机" value="standalone" />
            <el-option label="主从" value="replication" />
            <el-option label="哨兵" value="sentinel" />
            <el-option label="集群" value="cluster" />
          </el-select>
          <el-input v-model="searchText" placeholder="搜索实例/节点" clearable size="small" style="width: 200px" :prefix-icon="SearchIcon" />
        </div>
      </div>
      <el-table :data="filteredInstances" style="width: 100%" @row-click="openDetail" :row-class-name="rowClass" size="small" stripe>
        <el-table-column label="节点" prop="node" width="140" show-overflow-tooltip />
        <el-table-column label="实例地址" prop="instance" width="200" show-overflow-tooltip>
          <template #default="{ row }">
            <span class="mono">{{ row.instance }}</span>
          </template>
        </el-table-column>
        <el-table-column label="角色" width="100">
          <template #default="{ row }">
            <span class="role-tag" :class="row.role">{{ roleLabel(row.role) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="拓扑" width="90">
          <template #default="{ row }">
            <span class="topo-tag">{{ topoLabel(row.topology) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="版本" prop="version" width="90" />
        <el-table-column label="状态" width="110">
          <template #default="{ row }">
            <el-tooltip v-if="isAlert(row)" :content="issueReasons(row).join('；')" placement="top">
              <span class="status-text status-issue"><span class="status-dot" :class="statusClass(row)"></span>异常</span>
            </el-tooltip>
            <span v-else class="status-text"><span class="status-dot" :class="statusClass(row)"></span>{{ row.up ? '在线' : '离线' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="异常原因" min-width="220" show-overflow-tooltip>
          <template #default="{ row }">
            <span v-if="isAlert(row)" class="issue-reasons">{{ issueReasons(row).join('；') }}</span>
            <span v-else class="dim-text">-</span>
          </template>
        </el-table-column>
        <el-table-column label="客户端数" width="90" align="right">
          <template #default="{ row }">
            <span class="mono">{{ formatNum(row.clients) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="内存使用" width="130">
          <template #default="{ row }">
            <div class="mem-cell">
              <div class="mem-text mono">{{ formatBytes(row.usedMemory) }}</div>
              <div class="bar" v-if="row.memPercent > 0">
                <div class="bar-fill" :class="memBarClass(row.memPercent)" :style="{ width: row.memPercent + '%' }"></div>
              </div>
              <div class="mem-pct" v-if="row.memPercent > 0">{{ row.memPercent }}%</div>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="OPS" width="90" align="right">
          <template #default="{ row }">
            <span class="mono">{{ formatNum(row.ops) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="命中率" width="80" align="right">
          <template #default="{ row }">
            <span class="mono" :class="hitRateClass(row.hitRate)">{{ row.hitRate }}%</span>
          </template>
        </el-table-column>
        <el-table-column label="碎片率" width="80" align="right">
          <template #default="{ row }">
            <span class="mono" :class="row.fragmentation > 1.5 ? 'rate-warn' : ''">{{ row.fragmentation }}</span>
          </template>
        </el-table-column>
        <el-table-column label="从节点" width="80" align="right">
          <template #default="{ row }">
            <span class="mono" v-if="row.role === 'master' && row.connectedSlaves">{{ row.connectedSlaves }}</span>
            <span class="dim" v-else>--</span>
          </template>
        </el-table-column>
        <el-table-column label="运行时长" width="100">
          <template #default="{ row }">
            <span class="mono">{{ formatUptime(row.uptime) }}</span>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- ===== 区块3：性能排行（横向柱状图）===== -->
    <div class="chart-section glass">
      <div class="section-title">性能排行 Top 10</div>
      <div class="section-desc">用于快速定位资源占用和请求压力最高的实例，排序随自动刷新增量更新。</div>
      <div class="bar-row">
        <div class="bar-item">
          <div class="bar-sub-title">内存使用量 Top 10</div>
          <div class="chart-note">展示 Redis 当前 used_memory，适合排查容量压力和内存倾斜。</div>
          <div :ref="el => setChartRef(el, 'memBar')" class="bar-chart"></div>
        </div>
        <div class="bar-item">
          <div class="bar-sub-title">OPS Top 10</div>
          <div class="chart-note">展示当前每秒命令处理量，适合识别热点实例。</div>
          <div :ref="el => setChartRef(el, 'opsBar')" class="bar-chart"></div>
        </div>
      </div>
    </div>

    <!-- ===== 区块4：缓存命中率 ===== -->
    <div class="chart-section glass">
      <div class="section-title">缓存命中率</div>
      <div class="section-desc">命中率低通常表示缓存穿透、过期策略或业务访问模式需要检查。</div>
      <div :ref="el => setChartRef(el, 'hitRateBar')" class="hitrate-chart"></div>
    </div>

    <!-- ===== 区块5：实例详情抽屉 ===== -->
    <el-drawer v-model="detailVisible" size="60%" :with-header="false" direction="rtl" class="detail-drawer">
      <div class="detail-content" v-if="selected">
        <!-- 实例元信息卡 -->
        <div class="detail-header">
          <div class="dh-left">
            <div class="dh-title">
              <span class="status-dot lg" :class="statusClass(selected)"></span>
              <span class="mono">{{ selected.instance }}</span>
            </div>
            <div class="dh-meta">
              <span class="role-tag" :class="selected.role">{{ roleLabel(selected.role) }}</span>
              <span class="topo-tag">{{ topoLabel(selected.topology) }}</span>
              <span class="meta-item">节点：{{ selected.node }}</span>
              <span class="meta-item" v-if="selected.version">版本：{{ selected.version }}</span>
              <span class="meta-item" v-if="selected.uptime > 0">运行：{{ formatUptime(selected.uptime) }}</span>
            </div>
          </div>
          <div class="dh-right">
            <div class="range-tabs">
              <button v-for="r in ranges" :key="r.value" :class="{ active: range === r.value }" @click="changeRange(r.value)">{{ r.label }}</button>
            </div>
          </div>
        </div>

        <!-- 关键指标快照 -->
        <div class="snapshot-grid" v-if="selected">
          <div class="snap-card">
            <div class="snap-label">碎片率</div>
            <div class="snap-value" :class="(selected.fragmentation||0) > 1.5 ? 'warn' : ''">{{ selected.fragmentation || 0 }}</div>
          </div>
          <div class="snap-card">
            <div class="snap-label">阻塞客户端</div>
            <div class="snap-value">{{ selected.blocked || 0 }}</div>
          </div>
          <div class="snap-card">
            <div class="snap-label">最大内存</div>
            <div class="snap-value">{{ formatBytes(selected.maxMemory) }}</div>
          </div>
          <div class="snap-card">
            <div class="snap-label">淘汰 / 过期 Key</div>
            <div class="snap-value">{{ formatNum(selected.evicted) }} / {{ formatNum(selected.expired) }}</div>
          </div>
          <div class="snap-card">
            <div class="snap-label">拒绝连接</div>
            <div class="snap-value">{{ formatNum(selected.rejected) }}</div>
          </div>
          <div class="snap-card" v-if="selected.role === 'master'">
            <div class="snap-label">从节点 / 复制偏移</div>
            <div class="snap-value">{{ selected.connectedSlaves || 0 }} / {{ formatNum(selected.replicationOffset) }}</div>
          </div>
          <div class="snap-card" v-if="selected.role === 'slave'">
            <div class="snap-label">复制延迟</div>
            <div class="snap-value" :class="(selected.replicationLag||0) > 10 ? 'warn' : ''">{{ selected.replicationLag || 0 }} s</div>
          </div>
          <div class="snap-card" v-if="selected.role === 'slave'">
            <div class="snap-label">复制偏移</div>
            <div class="snap-value">{{ formatNum(selected.replicationOffset) }}</div>
          </div>
        </div>

        <!-- 集群健康度（cluster 拓扑） -->
        <div class="snapshot-grid snapshot-cluster" v-if="selected && selected.topology === 'cluster'">
          <div class="snap-card snap-card-wide">
            <div class="snap-label">集群状态</div>
            <div class="snap-value" :class="clusterStateClass(selected)">
              {{ clusterStateText(selected) }}
            </div>
          </div>
          <div class="snap-card">
            <div class="snap-label">槽位 ok</div>
            <div class="snap-value">{{ metricText(selected.clusterSlotsOk) }}</div>
          </div>
          <div class="snap-card">
            <div class="snap-label">槽位 assigned</div>
            <div class="snap-value">{{ metricText(selected.clusterSlotsAssigned) }}</div>
          </div>
          <div class="snap-card">
            <div class="snap-label">槽位失败</div>
            <div class="snap-value" :class="selected.clusterSlotsFail > 0 ? 'warn' : 'ok'">{{ metricText(selected.clusterSlotsFail) }}</div>
          </div>
          <div class="snap-card">
            <div class="snap-label">集群大小</div>
            <div class="snap-value">{{ metricText(selected.clusterSize) }}</div>
          </div>
          <div class="snap-card">
            <div class="snap-label">已知节点</div>
            <div class="snap-value">{{ metricText(selected.clusterKnownNodes) }}</div>
          </div>
        </div>

        <!-- 哨兵监控（sentinel 拓扑） -->
        <div class="snapshot-grid snapshot-sentinel" v-if="selected && selected.topology === 'sentinel'">
          <div class="snap-card" v-if="selected.role === 'sentinel'">
            <div class="snap-label">监控 master</div>
            <div class="snap-value">{{ selected.sentinelMasters || 0 }}</div>
          </div>
          <div class="snap-card" v-if="selected.role === 'sentinel'">
            <div class="snap-label">监控 slave</div>
            <div class="snap-value">{{ selected.sentinelSlaves || 0 }}</div>
          </div>
          <div class="snap-card" v-if="selected.role === 'sentinel'">
            <div class="snap-label">同组 sentinel</div>
            <div class="snap-value">{{ selected.sentinelSentinels || 0 }}</div>
          </div>
          <div class="snap-card" v-if="selected.role === 'sentinel'">
            <div class="snap-label">Tilt 模式</div>
            <div class="snap-value" :class="(selected.sentinelTilt||0) > 0 ? 'warn' : 'ok'">{{ (selected.sentinelTilt||0) > 0 ? '是' : '否' }}</div>
          </div>
          <div class="snap-card" v-if="selected.role === 'master' && selected.replicaOf && selected.replicaOf.startsWith('sentinel:')">
            <div class="snap-label">受 Sentinel 监控</div>
            <div class="snap-value ok">{{ selected.replicaOf.slice('sentinel:'.length) }}</div>
          </div>
        </div>

        <!-- 趋势图网格 -->
        <div class="chart-grid">
          <div class="trend-card" v-for="chart in trendCharts" :key="chart.key">
            <div class="tc-head">
              <span class="tc-label">{{ chart.label }}</span>
              <span class="tc-value" v-if="chart.current !== null" :class="chart.valueClass">{{ chart.currentText }}</span>
            </div>
            <div :ref="el => setTrendRef(el, chart.key)" class="tc-chart"></div>
          </div>
        </div>
      </div>
    </el-drawer>
    </template><!-- /有数据时的正常布局 -->
  </div>
</template>

<script setup>
import { ref, reactive, computed, onMounted, onUnmounted, nextTick, watch, h } from 'vue'
import { Search } from '@element-plus/icons-vue'
import RefreshBar from '../RefreshBar.vue'
import KpiCard from '../KpiCard.vue'
import { ElMessage } from 'element-plus'
import http from '../../api/http'
import { echarts, initChart, COLORS } from '../../charts/echarts'

// ---- 图标组件（内联 SVG，渲染函数规避 runtime-only 无法编译 { template } 的问题） ----
function svgIcon(s) {
  const inner = s.replace(/^<svg[^>]*>/, '').replace(/<\/svg>\s*$/, '')
  return () => h('svg', { viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', 'stroke-width': 2, innerHTML: inner })
}
const DatabaseIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5v6c0 1.66 4 3 9 3s9-1.34 9-3V5"/><path d="M3 11v6c0 1.66 4 3 9 3s9-1.34 9-3v-6"/></svg>')
const CheckCircleIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><polyline points="9 12 12 15 16 10"/></svg>')
const XCircleIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><line x1="15" y1="9" x2="9" y2="15"/><line x1="9" y1="9" x2="15" y2="15"/></svg>')
const MemoryIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 19v-3"/><path d="M10 19v-3"/><path d="M14 19v-3"/><path d="M18 19v-3"/><path d="M8 11V9"/><path d="M16 11V9"/><path d="M12 11V9"/><path d="M2 15h20"/><path d="M2 7a2 2 0 0 1 2-2h16a2 2 0 0 1 2 2v1.1a2 2 0 0 0 0 3.837V17a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2v-5.063a2 2 0 0 0 0-3.837Z"/></svg>')
const ConnectionIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M13.144 10.144a4 4 0 1 0-5.742 0"/><path d="M11 14.48V17"/><circle cx="11" cy="19" r="2"/><path d="M16 9a5 5 0 0 1 4.516 2.861"/><path d="M19.922 12.633a5 5 0 0 1-.39 4.155"/><circle cx="21" cy="18" r="2"/></svg>')
const ActivityIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="22 12 18 12 15 21 9 3 6 12 2 12"/></svg>')
const SearchIcon = Search
const ClusterIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="3"/><circle cx="5" cy="5" r="2"/><circle cx="19" cy="5" r="2"/><circle cx="5" cy="19" r="2"/><circle cx="19" cy="19" r="2"/><line x1="6.5" y1="6.5" x2="9.5" y2="9.5"/><line x1="14.5" y1="9.5" x2="17.5" y2="6.5"/><line x1="6.5" y1="17.5" x2="9.5" y2="14.5"/><line x1="14.5" y1="14.5" x2="17.5" y2="17.5"/></svg>')
const SentinelIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 2l8 4v6c0 5-3.5 9-8 10-4.5-1-8-5-8-10V6z"/><path d="M9 12l2 2 4-4"/></svg>')
const ReplicaIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="4" width="8" height="6" rx="1"/><rect x="13" y="14" width="8" height="6" rx="1"/><path d="M11 7h6a2 2 0 0 1 2 2v5"/></svg>')
const AlertIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>')
const ServerIcon = svgIcon('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="2" y="3" width="20" height="8" rx="2"/><rect x="2" y="13" width="20" height="8" rx="2"/><line x1="6" y1="7" x2="6.01" y2="7"/><line x1="6" y1="17" x2="6.01" y2="17"/></svg>')

// ---- 数据 ----
const instances = ref([])
const activeAlerts = ref([])
const loading = ref(false)
const filterStatus = ref('')
const filterTopology = ref('')
const searchText = ref('')
const refreshInterval = ref(30)
const redisIntervals = [
  { label: '10秒', value: 10 },
  { label: '30秒', value: 30 },
  { label: '60秒', value: 60 },
]

// Server 真实地址（取自 /api/v1/install-info，参考「添加主机」功能）
const serverURL = ref('')
// 主命令：节点上已有 agent-install.sh（agent 安装时已自拷贝到 /etc/monitor-agent/），
// 直接本地运行可进入交互式向导（stdin 为终端，能正常 read）。
const redisInstallCmd = computed(() => 'bash /etc/monitor-agent/agent-install.sh redis')
// 备选命令：节点没有本地脚本时，先下载到本地再以交互方式运行。
// 注意：不要用 curl ... | bash 管道方式（管道会占用 stdin，交互向导无法输入）。
const redisInstallCmdAlt = computed(() =>
  (serverURL.value
    ? `curl -fsSL ${serverURL.value}/install/agent-install.sh`
    : 'curl -fsSL http://<server>:8080/install/agent-install.sh') +
  ' -o /tmp/agent-install.sh && bash /tmp/agent-install.sh redis'
)
function loadServerURL() {
  http
    .get('/api/v1/install-info')
    .then((info) => { if (info && info.serverURL) serverURL.value = info.serverURL })
    .catch(() => {})
}

// 统计
const stats = reactive({ total: 0, up: 0, down: 0, totalMemory: 0, totalClients: 0, totalOps: 0, clusterCount: 0, alertCount: 0 })

const alertsByInstance = computed(() => {
  const m = new Map()
  for (const alert of activeAlerts.value) {
    if (!alert.instance) continue
    const key = `${alert.node}|${alert.instance}`
    const list = m.get(key) || []
    list.push(alert)
    m.set(key, list)
  }
  return m
})

function instanceAlerts(i) {
  return alertsByInstance.value.get(`${i.node}|${i.instance}`) || []
}

function issueReasons(i) {
  const reasons = []
  if (!i.up) reasons.push('实例离线')
  for (const alert of instanceAlerts(i)) {
    reasons.push(alert.message || `${alert.ruleName || alert.rule || '告警规则'} 触发`)
  }
  return reasons
}

function isAlert(i) {
  return issueReasons(i).length > 0
}

const alertInstances = computed(() => instances.value.filter(isAlert))

function metricText(value) {
  return value === undefined || value === null ? '-' : value
}

function clusterStateText(instance) {
  if (instance.clusterState === 1) return 'ok · 健康'
  if (instance.clusterState === 0) return '异常'
  return '未采集'
}

function clusterStateClass(instance) {
  if (instance.clusterState === 1) return 'ok'
  if (instance.clusterState === 0) return 'warn'
  return ''
}

// 过滤
const filteredInstances = computed(() => {
  return instances.value.filter(i => {
    if (filterStatus.value === 'up' && !i.up) return false
    if (filterStatus.value === 'down' && i.up) return false
    if (filterTopology.value && i.topology !== filterTopology.value) return false
    if (searchText.value) {
      const s = searchText.value.toLowerCase()
      if (!i.instance.toLowerCase().includes(s) &&
          !i.node.toLowerCase().includes(s) &&
          !(i.name || '').toLowerCase().includes(s)) return false
    }
    return true
  })
})

// 拓扑分组：用于关系视图（基于后端 group 字段，不再正则猜测）
const topologyGroups = computed(() => {
  const clusters = {}, sentinels = {}, replications = {}, standalones = []
  for (const i of instances.value) {
    // 分组键用 group 优先：cluster/sentinel 同组各节点 group 相同（=agent 配置的实例名），
    // 而 name 在 cluster 下带地址后缀各不相同，用 name 会把同一集群打散成多张卡。
    const g = i.group || i.name || i.instance
    if (i.topology === 'cluster') {
      clusters[g] = clusters[g] || { name: g, masters: [], slaves: [], slavesByMaster: {}, unlinkedSlaves: [], topology: 'cluster' }
      if (i.role === 'slave' || i.role === 'replica') {
        clusters[g].slaves.push(i)
        // 按 replicaOf 预聚合到对应 master
        if (i.replicaOf) {
          clusters[g].slavesByMaster[i.replicaOf] = clusters[g].slavesByMaster[i.replicaOf] || []
          clusters[g].slavesByMaster[i.replicaOf].push(i)
        } else {
          // replicaOf 为空（旧 agent 未上报或无法确定主节点关系）→ 归入未关联区域，避免从节点丢失
          clusters[g].unlinkedSlaves.push(i)
        }
      } else clusters[g].masters.push(i)
    } else if (i.topology === 'sentinel') {
      sentinels[g] = sentinels[g] || { name: g, sentinels: [], masters: [], topology: 'sentinel' }
      if (i.role === 'sentinel') sentinels[g].sentinels.push(i)
      else sentinels[g].masters.push(i)
    } else if (i.topology === 'replication' || (i.replicaOf && i.topology !== 'cluster')) {
      // 主从模式：同一 group 下的 master 与 slave
      replications[g] = replications[g] || { name: g, masters: [], slaves: [], topology: 'replication' }
      if (i.role === 'slave' || i.role === 'replica') replications[g].slaves.push(i)
      else replications[g].masters.push(i)
    } else {
      standalones.push(i)
    }
  }
  return {
    clusters: Object.values(clusters),
    sentinels: Object.values(sentinels),
    replications: Object.values(replications),
    standalones,
  }
})

// ---- 集群 Slot 分片视图 ----
const CLUSTER_SLOT_TOTAL = 16384
const SLOT_PALETTE = ['#38bdf8', '#a78bfa', '#34d399', '#fbbf24', '#f472b6', '#22d3ee', '#fb923c', '#4ade80', '#818cf8', '#f87171']

// slotColor 返回组内第 idx 个 master 的分配色（与分片条/图例/master 卡片同色）
function slotColor(grp, idx) {
  return SLOT_PALETTE[idx % SLOT_PALETTE.length]
}

// slotRangeStart 解析区间起始槽位（"0-5460"→0；单槽 "7000"→7000）
function slotRangeStart(r) {
  const i = String(r).indexOf('-')
  if (i >= 0) return parseInt(String(r).slice(0, i), 10) || 0
  return parseInt(r, 10) || 0
}

// slotRangeCount 计算区间槽位数（"0-5460"→5461；单槽→1）
function slotRangeCount(r) {
  const i = String(r).indexOf('-')
  if (i >= 0) {
    const lo = parseInt(String(r).slice(0, i), 10) || 0
    const hi = parseInt(String(r).slice(i + 1), 10) || 0
    return hi >= lo ? hi - lo + 1 : 0
  }
  return 1
}

// slotRangeTotal 计算一个 master 所有区间的总槽位数
function slotRangeTotal(ranges) {
  return (ranges || []).reduce((s, r) => s + slotRangeCount(r), 0)
}

// clusterSlotView 展开组内所有 master 的 slot 区间为分段条数据
function clusterSlotView(grp) {
  const segs = []
  grp.masters.forEach((m, mi) => {
    for (const r of m.slotRanges || []) {
      segs.push({ master: m, range: r, count: slotRangeCount(r), color: slotColor(grp, mi) })
    }
  })
  // 按起始槽位排序，保证分片条从左到右递增
  return segs.sort((a, b) => slotRangeStart(a.range) - slotRangeStart(b.range))
}

// clusterSlotStats 汇总组内槽位分配情况
function clusterSlotStats(grp) {
  let assigned = 0
  for (const m of grp.masters) assigned += slotRangeTotal(m.slotRanges)
  if (assigned > CLUSTER_SLOT_TOTAL) assigned = CLUSTER_SLOT_TOTAL
  const unassigned = CLUSTER_SLOT_TOTAL - assigned
  return { assigned, unassigned, pct: Math.round((assigned / CLUSTER_SLOT_TOTAL) * 100) }
}

// 集群健康度判定（基于 topologyGroups 中 cluster 组的 masters 状态）
function clusterHealth(grp) {
  const total = grp.masters.length
  const up = grp.masters.filter(m => m.up).length
  if (total === 0) return { cls: 'down', text: '无 master' }
  if (up === 0) return { cls: 'down', text: '全部离线' }
  // 槽位失败（仅 cfg.Addr master 会上报该 metric，取该值；其他 master 字段为 0/undefined）
  const reportedFailSlots = grp.masters.find(m => m.clusterSlotsFail !== undefined && m.clusterSlotsFail !== null)?.clusterSlotsFail || 0
  if (reportedFailSlots > 0) return { cls: 'warn', text: `槽位失败 ${reportedFailSlots}` }
  // cluster_state 同理：仅 cfg.Addr master 上报；只有明确采集到且为 0 才视为异常，
  // 避免 cfg.Addr 不在采集链上（或 agent 版本未上报该字段）时把所有 master 的 0 误判为异常。
  const reportedState = grp.masters.find(m => m.clusterState === 0 || m.clusterState === 1)?.clusterState
  if (reportedState === 0) return { cls: 'warn', text: '集群状态异常' }
  return { cls: 'ok', text: '健康' }
}
function clusterHealthClass(grp) { return 'badge-' + clusterHealth(grp).cls }
function clusterHealthText(grp) { return clusterHealth(grp).text }

// ---- 图表引用 ----
const chartRefs = {}
const chartInstances = {}
const trendRefs = {}
const trendChartsMap = {}
const CHART_PALETTE = ['#38bdf8', '#22c55e', '#f59e0b', '#94a3b8', '#64748b']
const AXIS_COLOR = '#8aa0b8'
const SPLIT_COLOR = 'rgba(148, 163, 184, 0.12)'
const TOOLTIP_STYLE = {
  backgroundColor: 'rgba(11,17,32,0.94)',
  borderColor: 'rgba(148,163,184,0.24)',
  textStyle: { color: '#e5edf7' },
}

function setChartRef(el, key) {
  if (el) chartRefs[key] = el
}
function setTrendRef(el, key) {
  if (el) trendRefs[key] = el
}

// ---- 加载数据 ----
async function loadInstances() {
  loading.value = true
  try {
    const [instancesData, alertsData] = await Promise.all([
      http.get('/api/v1/middleware/redis/instances'),
      http.get('/api/v1/alerts?state=firing'),
    ])
    instances.value = instancesData.instances || []
    activeAlerts.value = (alertsData.alerts || []).filter(a => a.instance)
    computeStats()
    await nextTick()
    renderCharts()
  } catch (e) {
    console.error('加载 Redis 实例失败', e)
  } finally {
    loading.value = false
  }
}

function computeStats() {
  const list = instances.value
  stats.total = list.length
  stats.up = list.filter(i => i.up).length
  stats.down = list.filter(i => !i.up).length
  stats.totalMemory = list.reduce((s, i) => s + (i.usedMemory || 0), 0)
  stats.totalClients = list.reduce((s, i) => s + (i.clients || 0), 0)
  stats.totalOps = list.reduce((s, i) => s + (i.ops || 0), 0)
  // 集群/哨兵组数（按 group 去重，group 即同集群各节点相同的分组名）
  const clusterNames = new Set()
  for (const i of list) {
    if (i.topology === 'cluster' || i.topology === 'sentinel') {
      const key = i.group || i.name
      if (key) clusterNames.add(key)
    }
  }
  stats.clusterCount = clusterNames.size
  // 告警实例数
  stats.alertCount = list.filter(isAlert).length
}

// ---- 渲染概览图表 ----
function renderCharts() {
  renderMemBar()
  renderOpsBar()
  renderHitRateBar()
}

function getOrCreate(key) {
  if (!chartRefs[key]) return null
  if (!chartInstances[key] || chartInstances[key].isDisposed()) {
    chartInstances[key] = initChart(chartRefs[key])
  }
  return chartInstances[key]
}

function updateChart(chart, option) {
  chart.setOption(option, { notMerge: false, lazyUpdate: true })
}

function renderMemBar() {
  const chart = getOrCreate('memBar')
  if (!chart) return
  const sorted = [...instances.value].filter(i => i.usedMemory > 0).sort((a, b) => b.usedMemory - a.usedMemory).slice(0, 10).reverse()
  updateChart(chart, {
    tooltip: {
      trigger: 'axis', axisPointer: { type: 'shadow' },
      backgroundColor: 'rgba(11,17,32,0.92)', borderColor: 'rgba(34,211,238,0.3)', textStyle: { color: '#e5edf7' },
      formatter: (p) => `${p[0].name}<br/>${formatBytes(p[0].value)}`,
    },
    grid: { left: 10, right: 60, top: 8, bottom: 8, containLabel: true },
    xAxis: { type: 'value', axisLabel: { color: AXIS_COLOR, fontSize: 10, formatter: (v) => formatBytesShort(v) }, splitLine: { lineStyle: { color: SPLIT_COLOR } } },
    yAxis: { type: 'category', data: sorted.map(i => i.name || i.instance), axisLabel: { color: AXIS_COLOR, fontSize: 11, width: 120, overflow: 'truncate' }, axisLine: { lineStyle: { color: AXIS_COLOR } } },
    series: [{
      type: 'bar', data: sorted.map(i => i.usedMemory), barWidth: '55%',
      itemStyle: {
        borderRadius: [0, 4, 4, 0],
        color: (params) => {
          const val = params.value
          const max = sorted.length > 0 ? sorted[sorted.length - 1].usedMemory : 1
          const ratio = val / max
          if (ratio > 0.8) return '#ef4444'
          if (ratio > 0.6) return '#f59e0b'
          return '#22d3ee'
        },
      },
    }],
  })
}

function renderOpsBar() {
  const chart = getOrCreate('opsBar')
  if (!chart) return
  const sorted = [...instances.value].filter(i => i.ops > 0).sort((a, b) => b.ops - a.ops).slice(0, 10).reverse()
  updateChart(chart, {
    tooltip: {
      trigger: 'axis', axisPointer: { type: 'shadow' },
      backgroundColor: 'rgba(11,17,32,0.92)', borderColor: 'rgba(34,211,238,0.3)', textStyle: { color: '#e5edf7' },
      formatter: (p) => `${p[0].name}<br/>${formatNum(p[0].value)} ops/s`,
    },
    grid: { left: 10, right: 50, top: 8, bottom: 8, containLabel: true },
    xAxis: { type: 'value', axisLabel: { color: '#9fb3c8', fontSize: 10, formatter: (v) => formatNum(v) }, splitLine: { lineStyle: { color: 'rgba(34,211,238,0.08)' } } },
    yAxis: { type: 'category', data: sorted.map(i => i.name || i.instance), axisLabel: { color: '#9fb3c8', fontSize: 11, width: 120, overflow: 'truncate' }, axisLine: { lineStyle: { color: '#9fb3c8' } } },
    series: [{
      type: 'bar', data: sorted.map(i => i.ops), barWidth: '55%',
      itemStyle: { borderRadius: [0, 4, 4, 0], color: '#3b82f6' },
    }],
  })
}

function renderHitRateBar() {
  const chart = getOrCreate('hitRateBar')
  if (!chart) return
  const sorted = [...instances.value].filter(i => i.hitRate > 0).sort((a, b) => a.hitRate - b.hitRate)
  updateChart(chart, {
    tooltip: {
      trigger: 'axis', axisPointer: { type: 'shadow' },
      backgroundColor: 'rgba(11,17,32,0.92)', borderColor: 'rgba(34,211,238,0.3)', textStyle: { color: '#e5edf7' },
      formatter: (p) => `${p[0].name}<br/>命中率 ${p[0].value}%`,
    },
    grid: { left: 10, right: 30, top: 8, bottom: 8, containLabel: true },
    xAxis: { type: 'category', data: sorted.map(i => i.name || i.instance), axisLabel: { color: '#9fb3c8', fontSize: 10, interval: 0, rotate: sorted.length > 8 ? 30 : 0 }, axisLine: { lineStyle: { color: '#9fb3c8' } } },
    yAxis: { type: 'value', min: 0, max: 100, axisLabel: { color: '#9fb3c8', fontSize: 10, formatter: '{value}%' }, splitLine: { lineStyle: { color: 'rgba(34,211,238,0.08)' } } },
    series: [{
      type: 'bar', data: sorted.map(i => i.hitRate), barWidth: '40%',
      itemStyle: {
        borderRadius: [4, 4, 0, 0],
        color: (params) => {
          const v = params.value
          if (v < 50) return '#ef4444'
          if (v < 80) return '#f59e0b'
          return '#22c55e'
        },
      },
    }],
  })
}

// ---- 详情抽屉 ----
const detailVisible = ref(false)
const selected = ref(null)
const range = ref('1h')
const ranges = [
  { label: '近1小时', value: '1h' },
  { label: '今日', value: 'today' },
  { label: '昨日', value: 'yesterday' },
  { label: '近7天', value: '7d' },
  { label: '近30天', value: '30d' },
]

// 趋势图配置
const trendCharts = ref([])

function openDetail(row) {
  selected.value = row
  detailVisible.value = true
  buildTrendCharts(row)
  nextTick(() => loadTrendData())
}

function buildTrendCharts(row) {
  const list = [
    { key: 'mem', label: '内存使用率', metric: 'redis_used_memory_percent', unit: '%', color: '#38bdf8', current: row.memPercent, currentText: row.memPercent + '%' },
    { key: 'clients', label: '连接客户端数', metric: 'redis_connected_clients', unit: '', color: '#22c55e', current: row.clients, currentText: formatNum(row.clients) },
    { key: 'ops', label: '命令速率(OPS)', metric: 'redis_ops_per_sec', unit: '', color: '#f59e0b', current: row.ops, currentText: formatNum(row.ops) },
    { key: 'hitrate', label: '缓存命中率', metric: 'redis_hit_rate', unit: '%', color: '#22c55e', current: row.hitRate, currentText: row.hitRate + '%' },
    { key: 'keys', label: '键数量', metric: 'redis_keys', unit: '', color: '#94a3b8', current: row.keys, currentText: formatNum(row.keys) },
    { key: 'frag', label: '内存碎片率', metric: 'redis_memory_fragmentation_ratio', unit: '', color: '#64748b', current: null, currentText: '-' },
    { key: 'evicted', label: '淘汰键数', metric: 'redis_evicted_keys', unit: '', color: '#ef4444', current: null, currentText: '-' },
    { key: 'uptime', label: '运行时长', metric: 'redis_uptime_in_seconds', unit: 's', color: '#94a3b8', current: row.uptime, currentText: formatUptime(row.uptime) },
  ]
  if (row.role === 'slave') {
    list.push({ key: 'lag', label: '复制延迟(秒)', metric: 'redis_replication_lag', unit: 's', color: '#ef4444', current: null, currentText: '-' })
  }
  trendCharts.value = list
}

function changeRange(r) {
  range.value = r
  if (detailVisible.value) loadTrendData()
}

function getRangeMs() {
  const now = Date.now()
  switch (range.value) {
    case '1h': return { start: now - 3600000, end: now, step: 60000 }
    case 'today': {
      const d = new Date()
      return { start: new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime(), end: now, step: 300000 }
    }
    case 'yesterday': {
      const d = new Date()
      const yStart = new Date(d.getFullYear(), d.getMonth(), d.getDate() - 1).getTime()
      return { start: yStart, end: yStart + 86400000, step: 300000 }
    }
    case '7d': return { start: now - 7 * 86400000, end: now, step: 1800000 }
    case '30d': return { start: now - 30 * 86400000, end: now, step: 3600000 }
    default: return { start: now - 3600000, end: now, step: 60000 }
  }
}

async function loadTrendData() {
  if (!selected.value) return
  const { start, end, step } = getRangeMs()
  for (const chart of trendCharts.value) {
    try {
      const data = await http.get(`/api/v1/query/range?node=${selected.value.node}&metric=${chart.metric}&labels.instance=${selected.value.instance}&start=${start}&end=${end}&step=${step}`)
      const series = data.series || []
      let points = []
      if (series.length > 0) {
        points = (series[0].points || []).map(p => [p.timestamp, p.value])
      }
      const c = getOrCreateTrend(chart.key)
      if (c) {
        updateChart(c, {
          grid: { left: 48, right: 14, top: 8, bottom: 22, containLabel: true },
          tooltip: { trigger: 'axis', ...TOOLTIP_STYLE, borderColor: chart.color },
          xAxis: { type: 'time', axisLine: { lineStyle: { color: AXIS_COLOR } }, axisLabel: { color: AXIS_COLOR, fontSize: 10, hideOverlap: true }, splitLine: { show: false } },
          yAxis: { type: 'value', min: 0, axisLabel: { color: AXIS_COLOR, fontSize: 10 }, splitLine: { lineStyle: { color: SPLIT_COLOR } } },
          series: [{
            type: 'line', smooth: true, showSymbol: false, data: points,
            lineStyle: { color: chart.color, width: 2 },
            areaStyle: { color: chart.color + '18' },
          }],
        })
      }
    } catch (e) {
      console.error('加载趋势数据失败', chart.key, e)
    }
  }
}

function getOrCreateTrend(key) {
  if (!trendRefs[key]) return null
  if (!trendChartsMap[key] || trendChartsMap[key].isDisposed()) {
    trendChartsMap[key] = initChart(trendRefs[key])
  }
  return trendChartsMap[key]
}

// ---- 刷新（由顶部 RefreshBar 组件统一管理）----

// ---- 格式化工具 ----
function formatBytes(b) {
  if (!b || b <= 0) return '0 B'
  if (b >= 1 << 30) return (b / (1 << 30)).toFixed(2) + ' GB'
  if (b >= 1 << 20) return (b / (1 << 20)).toFixed(2) + ' MB'
  if (b >= 1 << 10) return (b / (1 << 10)).toFixed(1) + ' KB'
  return b.toFixed(0) + ' B'
}
function formatBytesShort(b) {
  if (b >= 1 << 30) return (b / (1 << 30)).toFixed(0) + 'G'
  if (b >= 1 << 20) return (b / (1 << 20)).toFixed(0) + 'M'
  if (b >= 1 << 10) return (b / (1 << 10)).toFixed(0) + 'K'
  return b.toFixed(0) + 'B'
}
function formatNum(n) {
  if (!n) return '0'
  if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M'
  if (n >= 1000) return (n / 1000).toFixed(1) + 'K'
  return n.toFixed(0)
}
function formatUptime(s) {
  if (!s || s <= 0) return '-'
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  if (d > 0) return `${d}天${h}时`
  const m = Math.floor((s % 3600) / 60)
  if (h > 0) return `${h}时${m}分`
  return `${m}分`
}
function roleLabel(r) {
  return { master: 'Master', slave: 'Slave', sentinel: 'Sentinel', unknown: '未知' }[r] || r
}
function topoLabel(t) {
  return { standalone: '单机', replication: '主从', sentinel: '哨兵', cluster: '集群', unknown: '未知' }[t] || t
}
function memBarClass(p) {
  if (p > 80) return 'red'
  if (p > 60) return 'amber'
  return 'green'
}
function hitRateClass(r) {
  if (r < 50) return 'sev-critical'
  if (r < 80) return 'sev-warning'
  return 'ok-text'
}
function rowClass({ row }) {
  if (!row.up) return 'row-down'
  return isAlert(row) ? 'row-issue' : ''
}

function statusClass(row) {
  if (!row.up) return 'down'
  return isAlert(row) ? 'issue' : 'up'
}

// 复制空状态引导命令到剪贴板
function copyCmd() {
  const cmd = redisInstallCmd.value
  if (navigator.clipboard) {
    navigator.clipboard.writeText(cmd).then(() => {
      ElMessage.success('命令已复制到剪贴板')
    }).catch(() => {})
  } else {
    // 降级：创建临时 textarea
    const ta = document.createElement('textarea')
    ta.value = cmd
    document.body.appendChild(ta)
    ta.select()
    try { document.execCommand('copy'); ElMessage.success('命令已复制到剪贴板') } catch (e) {}
    document.body.removeChild(ta)
  }
}

// ---- 生命周期 ----
onMounted(() => {
  loadInstances()
  loadServerURL()
  window.addEventListener('resize', handleResize)
})
onUnmounted(() => {
  window.removeEventListener('resize', handleResize)
  Object.values(chartInstances).forEach(c => c && c.dispose())
  Object.values(trendChartsMap).forEach(c => c && c.dispose())
})

function handleResize() {
  Object.values(chartInstances).forEach(c => c && c.resize())
  Object.values(trendChartsMap).forEach(c => c && c.resize())
}
</script>

<style scoped>
.redis-tab {
  padding: 16px;
}

/* 区块1：KPI 卡片（使用共享 KpiCard 组件，样式见 KpiCard.vue） */
.kpi-row {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
  gap: 10px;
  margin-bottom: 16px;
}

/* 通用 section */
.chart-section {
  padding: 16px 18px;
  margin-bottom: 16px;
}
.section-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 8px;
  display: flex;
  align-items: center;
  gap: 8px;
}
.section-title::before {
  content: '';
  width: 3px;
  height: 14px;
  background: var(--accent);
  border-radius: 2px;
}
.section-title.no-bar::before { display: none; }
.section-desc {
  font-size: 12px;
  color: var(--text-dim);
  line-height: 1.5;
  margin-bottom: 12px;
}
.chart-note {
  font-size: 12px;
  color: var(--text-dim);
  line-height: 1.5;
  margin-bottom: 8px;
}
.secondary-section {
  border-style: dashed;
}

/* 区块2：柱状图 */
.bar-row {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 16px;
}
.bar-item {}
.bar-sub-title {
  font-size: 12px;
  color: var(--text-dim);
  margin-bottom: 8px;
}
.bar-chart {
  width: 100%;
  height: 280px;
}

/* 区块4：命中率 */
.hitrate-chart {
  width: 100%;
  height: 260px;
}

/* 区块5：表格 */
.table-section {
  padding: 16px 18px;
}
.table-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}
.toolbar-right {
  display: flex;
  gap: 8px;
  align-items: center;
}
.role-tag {
  display: inline-block;
  padding: 2px 10px;
  border-radius: 12px;
  font-size: 11px;
  font-weight: 600;
}
.role-tag.master { background: rgba(220, 56, 45, 0.15); color: #ff6b6b; }
.role-tag.slave { background: rgba(34, 197, 94, 0.15); color: #4ade80; }
.role-tag.sentinel { background: rgba(245, 158, 11, 0.15); color: #fbbf24; }
.role-tag.unknown { background: rgba(107, 124, 147, 0.15); color: #94a3b8; }
.topo-tag {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
  background: rgba(56, 189, 248, 0.12);
  color: var(--info);
}
.status-dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  margin-right: 6px;
  vertical-align: middle;
}
.status-dot.up {
  background: var(--accent);
  box-shadow: 0 0 6px var(--accent-glow);
}
.status-dot.down {
  background: var(--danger);
}
.status-dot.issue {
  background: var(--warn);
  box-shadow: 0 0 6px rgba(245, 158, 11, 0.55);
}
.status-issue {
  color: #fbbf24;
}
.status-dot.lg {
  width: 12px;
  height: 12px;
}
.mem-cell {
  display: flex;
  align-items: center;
  gap: 8px;
}
.mem-cell .bar {
  flex: 1;
  height: 5px;
  background: rgba(255, 255, 255, 0.06);
  border-radius: 3px;
  overflow: hidden;
}
.mem-cell .bar-fill {
  height: 100%;
  border-radius: 3px;
}
.bar-fill.green { background: var(--accent); }
.bar-fill.amber { background: var(--warn); }
.bar-fill.red { background: var(--danger); }
.mem-text {
  font-size: 12px;
  min-width: 60px;
}
.mem-pct {
  font-size: 11px;
  color: var(--text-dim);
  min-width: 36px;
  text-align: right;
}
.issue-reasons {
  color: #fbbf24;
  font-size: 12px;
}
.dim-text {
  color: var(--text-dim);
}
.alert-summary {
  margin-bottom: 16px;
  padding: 14px 16px;
  border: 1px solid rgba(245, 158, 11, 0.28);
  background: rgba(245, 158, 11, 0.06);
}
.alert-summary-head {
  display: flex;
  align-items: center;
  gap: 8px;
  color: #fbbf24;
  font-weight: 600;
  font-size: 14px;
}
.alert-summary-head svg {
  width: 18px;
  height: 18px;
}
.alert-summary-desc {
  margin-top: 8px;
  color: var(--text-dim);
  font-size: 12px;
  line-height: 1.5;
}
.alert-summary-list {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 12px;
}
.issue-chip {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 6px 10px;
  border-radius: 999px;
  background: rgba(15, 23, 42, 0.58);
  border: 1px solid rgba(245, 158, 11, 0.22);
  color: var(--text);
  font-size: 12px;
  cursor: pointer;
}
.issue-chip:hover {
  border-color: rgba(245, 158, 11, 0.55);
}
.issue-chip.muted {
  color: var(--text-dim);
  cursor: default;
}
.issue-chip-reason {
  color: #fbbf24;
}
:deep(.row-down) {
  opacity: 0.6;
}
:deep(.row-issue) {
  --el-table-tr-bg-color: rgba(245, 158, 11, 0.04);
}
:deep(.el-table) {
  cursor: pointer;
}

/* 区块6：详情抽屉 */
:deep(.detail-drawer .el-drawer__body) {
  padding: 0;
}
.detail-content {
  padding: 20px 24px;
  height: 100%;
  overflow-y: auto;
}
.detail-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 20px;
  padding-bottom: 16px;
  border-bottom: 1px solid var(--border);
}
.dh-title {
  font-size: 18px;
  font-weight: 700;
  display: flex;
  align-items: center;
  gap: 10px;
}
.dh-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  margin-top: 10px;
  font-size: 12px;
  color: var(--text-dim);
}
.meta-item {}
.chart-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 14px;
}
.trend-card {
  background: rgba(255, 255, 255, 0.02);
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 12px 14px;
}
.tc-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 6px;
}
.tc-label {
  font-size: 12px;
  color: var(--text-dim);
}
.tc-value {
  font-size: 16px;
  font-weight: 700;
  font-family: var(--mono);
}
.tc-chart {
  width: 100%;
  height: 160px;
}

/* 空状态引导 */
.empty-guide {
  text-align: center;
  padding: 60px 24px;
  margin-bottom: 16px;
}
.empty-icon {
  color: var(--text-dim);
  opacity: 0.4;
  margin-bottom: 16px;
}
.empty-title {
  font-size: 18px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 8px;
}
.empty-desc {
  font-size: 13px;
  color: var(--text-dim);
  margin-bottom: 20px;
  max-width: 600px;
  margin-left: auto;
  margin-right: auto;
  line-height: 1.6;
}
.empty-cmd {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  background: rgba(0, 0, 0, 0.3);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 10px 16px;
  margin-bottom: 16px;
  max-width: 100%;
  overflow-x: auto;
}
.empty-cmd code {
  font-family: var(--mono);
  font-size: 12px;
  color: var(--accent);
  white-space: nowrap;
}
.copy-btn {
  background: none;
  border: none;
  color: var(--text-dim);
  cursor: pointer;
  padding: 4px;
  border-radius: 4px;
  display: flex;
  align-items: center;
  transition: color 0.15s, background 0.15s;
}
.copy-btn:hover {
  color: var(--accent);
  background: rgba(34, 211, 238, 0.1);
}
.empty-hint {
  font-size: 12px;
  color: var(--text-muted);
  margin-top: 8px;
  line-height: 1.6;
}
.empty-hint code {
  font-family: var(--mono);
  font-size: 11px;
  color: var(--text-dim);
  background: rgba(255, 255, 255, 0.05);
  padding: 1px 6px;
  border-radius: 3px;
}

/* 响应式 */
@media (max-width: 1200px) {
  .kpi-row { grid-template-columns: repeat(4, 1fr); }
  .pie-row { grid-template-columns: 1fr; }
  .bar-row { grid-template-columns: 1fr; }
  .chart-grid { grid-template-columns: 1fr; }
}

/* ==== 实例拓扑与集群关系 ==== */
.topo-group { margin-top: 16px; padding: 12px 14px; background: rgba(255,255,255,0.03); border: 1px solid rgba(255,255,255,0.06); border-radius: 10px; }
.topo-group:first-child { margin-top: 0; }
.topo-group-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 12px; gap: 12px; flex-wrap: wrap; }
.topo-group-title { display: inline-flex; align-items: center; gap: 8px; font-size: 14px; }
.topo-group-title svg { width: 18px; height: 18px; color: #93c5fd; }
.topo-meta { font-size: 12px; display: inline-flex; align-items: center; gap: 10px; }
.topo-meta .dim { color: rgba(255,255,255,0.45); }
.badge { display: inline-block; padding: 2px 8px; border-radius: 999px; font-size: 12px; font-weight: 500; }
.badge-ok { background: rgba(34,197,94,0.15); color: #4ade80; }
.badge-warn { background: rgba(234,179,8,0.15); color: #fbbf24; }
.badge-down { background: rgba(239,68,68,0.18); color: #f87171; }

/* cluster 关系：center + 外围 master */
.topo-relation { display: flex; align-items: center; gap: 16px; flex-wrap: wrap; }
.rel-node { background: rgba(255,255,255,0.05); border: 1px solid rgba(255,255,255,0.08); border-radius: 10px; padding: 10px 14px; min-width: 160px; cursor: pointer; transition: all .2s ease; }
.rel-node:hover { background: rgba(255,255,255,0.08); border-color: rgba(99,179,237,0.4); }
.rel-node.is-down { opacity: .55; }
.rel-node.is-alert { border-color: rgba(239,68,68,0.5); box-shadow: 0 0 0 1px rgba(239,68,68,0.2); }
.rel-center { background: linear-gradient(135deg, rgba(99,102,241,0.25), rgba(139,92,246,0.18)); border-color: rgba(139,92,246,0.4); font-weight: 600; }
.rel-center::after { content: ''; }
.rel-master { border-color: rgba(59,130,246,0.35); }
.rel-sentinel { border-color: rgba(234,179,8,0.35); background: rgba(234,179,8,0.06); }
.rel-standalone { border-color: rgba(148,163,184,0.3); }
.rel-edges { display: flex; gap: 12px; flex-wrap: wrap; flex: 1; }
.rel-edges-left, .rel-edges-right { flex: 1; min-width: 200px; }
.rel-edge { position: relative; }
.rel-edge::before { content: ''; position: absolute; left: -16px; top: 50%; width: 12px; height: 1px; background: linear-gradient(to right, rgba(139,92,246,0.5), rgba(59,130,246,0.5)); }
.topo-relation-sentinel { justify-content: space-between; }
.rel-arrow { color: rgba(234,179,8,0.8); font-weight: 600; font-size: 13px; padding: 0 4px; white-space: nowrap; }
.topo-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 12px; }
.rel-node-name { font-weight: 600; font-size: 13px; margin-bottom: 4px; word-break: break-all; }
.rel-node-meta { display: flex; align-items: center; gap: 6px; font-size: 12px; color: rgba(255,255,255,0.75); flex-wrap: wrap; }
.rel-node-meta .dim { color: rgba(255,255,255,0.4); }
.dot { width: 8px; height: 8px; border-radius: 50%; display: inline-block; }
.dot.up { background: #4ade80; box-shadow: 0 0 6px rgba(74,222,128,0.5); }
.dot.down { background: #f87171; }

/* ==== 详情抽屉：关键指标快照 ==== */
.snapshot-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(180px, 1fr)); gap: 12px; margin: 16px 0 4px; }
.snap-card { background: rgba(255,255,255,0.04); border: 1px solid rgba(255,255,255,0.08); border-radius: 10px; padding: 12px 14px; }
.snap-card-wide { grid-column: span 2; }
.snap-label { font-size: 12px; color: rgba(255,255,255,0.55); margin-bottom: 6px; }
.snap-value { font-size: 18px; font-weight: 600; font-family: 'JetBrains Mono', ui-monospace, monospace; }
.snap-value.ok { color: #4ade80; }
.snap-value.warn { color: #f87171; }

/* 实例列表：碎片率告警色 */
.rate-warn { color: #f87171; }

/* ==== Cluster 拓扑：master → replicas（旧样式保留，兼容哨兵组）==== */
.rel-edges { align-items: flex-start; }
.rel-master-block { display: flex; flex-direction: column; gap: 8px; min-width: 180px; }
.rel-slaves { display: flex; flex-direction: column; gap: 6px; padding-left: 14px; border-left: 2px dashed rgba(99,179,237,0.35); }
.rel-slave { display: flex; align-items: center; gap: 6px; font-size: 12px; padding: 6px 10px; background: rgba(99,102,241,0.06); border: 1px solid rgba(99,102,241,0.2); border-radius: 8px; cursor: pointer; transition: all .2s ease; flex-wrap: wrap; }
.rel-slave:hover { background: rgba(99,102,241,0.14); border-color: rgba(99,102,241,0.45); }
.rel-slave.is-down { opacity: .55; }
.rel-slave-tag { background: rgba(99,102,241,0.22); color: #a5b4fc; padding: 1px 6px; border-radius: 4px; font-size: 11px; font-weight: 600; }

/* ==== Slot 分片条 ==== */
.slot-section { margin-bottom: 14px; }
.slot-section-label { font-size: 12px; color: rgba(255,255,255,0.55); margin-bottom: 8px; letter-spacing: 0.02em; }
.slot-bar {
  display: flex; height: 16px; border-radius: 6px; overflow: hidden;
  background: rgba(255,255,255,0.05); border: 1px solid rgba(255,255,255,0.1);
  flex-basis: 0;
}
.slot-seg { min-width: 2px; flex-shrink: 1; flex-basis: 0; cursor: pointer; transition: filter .15s ease; }
.slot-seg:hover { filter: brightness(1.3); }
.slot-seg.is-down { opacity: .45; }
.slot-seg-empty { background: repeating-linear-gradient(45deg, rgba(148,163,184,0.15) 0 4px, transparent 4px 8px); cursor: default; }
.slot-legend { display: flex; flex-wrap: wrap; gap: 6px 16px; margin-top: 8px; }
.slot-legend-item {
  display: inline-flex; align-items: center; gap: 6px; font-size: 12px;
  padding: 3px 8px; border-radius: 6px; cursor: pointer;
  background: rgba(255,255,255,0.03); border: 1px solid transparent;
  transition: all .15s ease;
}
.slot-legend-item:hover { border-color: rgba(99,179,237,0.35); background: rgba(255,255,255,0.06); }
.slot-legend-item .dim { color: rgba(255,255,255,0.4); }
.slot-swatch { width: 10px; height: 10px; border-radius: 3px; flex-shrink: 0; }

/* ==== Master-Slave 层次树（复制 + 故障转移）==== */
.ms-tree { display: flex; flex-direction: column; gap: 14px; }
.ms-unit { display: flex; flex-direction: column; gap: 0; }
.ms-master { border-left: 3px solid transparent; min-width: 0; }
.ms-master .rel-node-name { display: flex; align-items: center; gap: 8px; }
.role-badge {
  display: inline-flex; align-items: center; justify-content: center;
  width: 18px; height: 18px; border-radius: 5px;
  font-size: 11px; font-weight: 700; flex-shrink: 0;
}
.role-badge-m { background: rgba(220,56,45,0.22); color: #ff8a80; border: 1px solid rgba(220,56,45,0.4); }
.role-badge-s { background: rgba(34,197,94,0.18); color: #4ade80; border: 1px solid rgba(34,197,94,0.35); width: 16px; height: 16px; font-size: 10px; border-radius: 4px; }
.slot-chip {
  font-size: 11px; font-family: var(--mono);
  padding: 1px 8px; border-radius: 4px; border: 1px dashed;
}
/* 分支：左轨为复制实线（向下），右侧虚线为 failover（向上）
   注：ms-branch-rail / ms-rail-repl / ms-rail-fo 等强化样式见下方"强化 rail 方向箭头"段 */
.ms-branch { display: flex; gap: 0; margin-left: 10px; }
.ms-slaves { display: flex; flex-direction: column; gap: 6px; flex: 1; padding: 4px 0; }
.ms-slave { position: relative; }
.ms-slave::before {
  content: ''; position: absolute; left: -10px; top: 50%;
  width: 10px; height: 1px; background: rgba(99,179,237,0.45);
}

/* ==== 拓扑图例 ==== */
.topo-legend {
  display: flex; gap: 20px; margin-top: 12px; padding-top: 10px;
  border-top: 1px dashed rgba(255,255,255,0.08);
}
.topo-legend-item { display: inline-flex; align-items: center; gap: 8px; font-size: 11px; color: rgba(255,255,255,0.5); }
.legend-line { display: inline-block; width: 24px; height: 0; }
.legend-solid { border-top: 2px solid rgba(99,179,237,0.65); }
.legend-dash { border-top: 2px dashed rgba(245,158,11,0.65); }

/* ==== 多集群横向并排（auto-fill），单集群自适应 ==== */
.topo-clusters-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(440px, 1fr));
  gap: 14px;
  margin-top: 12px;
}
.topo-cluster-card { margin-top: 0; height: fit-content; }
.name-source-hint {
  font-size: 10px; color: rgba(255,255,255,0.4);
  border: 1px dashed rgba(255,255,255,0.18); padding: 1px 7px;
  border-radius: 4px; cursor: help; margin-left: 4px;
  white-space: nowrap;
}
.name-source-hint:hover { color: rgba(147,197,253,0.9); border-color: rgba(147,197,253,0.45); }

/* ==== 主从复制 + 故障转移强化 ==== */
.ms-section { margin-top: 12px; }
.ms-section-label {
  display: flex; gap: 14px; margin-bottom: 10px; font-size: 11px; flex-wrap: wrap;
  letter-spacing: 0.02em;
}
.ms-label-repl { color: rgba(147,197,253,0.9); }
.ms-label-fo { color: rgba(251,191,36,0.9); }
.ms-rail-repl { font-size: 10px; color: rgba(147,197,253,0.9); writing-mode: vertical-lr; letter-spacing: 1px; font-weight: 600; }
.ms-rail-fo { font-size: 10px; color: rgba(251,191,36,0.9); writing-mode: vertical-lr; letter-spacing: 1px; font-weight: 600; }

/* 从节点完整卡片：在线状态/ops/lag/内存 */
.ms-slave-card {
  display: flex; flex-direction: column; align-items: flex-start;
  gap: 4px; min-width: 200px; max-width: 100%;
  padding: 8px 12px;
  background: rgba(34,197,94,0.06);
  border: 1px solid rgba(34,197,94,0.28);
  border-left: 3px solid rgba(34,197,94,0.55);
  cursor: pointer; transition: all .2s ease;
}
.ms-slave-card:hover { background: rgba(34,197,94,0.12); border-color: rgba(34,197,94,0.5); }
.ms-slave-card.is-down { opacity: .55; background: rgba(239,68,68,0.05); border-color: rgba(239,68,68,0.3); border-left-color: rgba(239,68,68,0.55); }
.ms-slave-head { display: flex; align-items: center; gap: 6px; font-weight: 600; font-size: 12px; }
.ms-slave-meta { display: flex; align-items: center; gap: 6px; font-size: 11px; color: rgba(255,255,255,0.75); flex-wrap: wrap; }
.ms-slave-meta .dim { color: rgba(255,255,255,0.4); }

/* ==== 未关联主节点的从节点（replicaOf 为空）==== */
.unlinked-block {
  margin-top: 12px; padding: 10px 14px;
  background: rgba(148,163,184,0.04); border: 1px dashed rgba(148,163,184,0.2);
  border-radius: 8px;
}
.unlinked-label {
  font-size: 11px; color: rgba(255,255,255,0.55); margin-bottom: 8px;
  display: flex; align-items: center; gap: 6px;
}
.unlinked-list { display: flex; flex-wrap: wrap; gap: 8px; }

/* ==== 强化 rail 方向箭头（复制 ↓ 实线 + 故障转移 ↑ 虚线）==== */
.ms-branch-rail {
  width: 64px; flex-shrink: 0;
  border-left: 2px solid rgba(147,197,253,0.65);
  display: flex; flex-direction: column; justify-content: space-between;
  padding: 2px 0 2px 8px; margin: 4px 0;
  position: relative;
}
.ms-branch-rail::after {
  content: ''; position: absolute; right: 8px; top: 4px; bottom: 4px;
  border-right: 2px dashed rgba(251,191,36,0.7);
}
.ms-rail-repl {
  font-size: 10px; color: rgba(147,197,253,0.9);
  writing-mode: vertical-lr; letter-spacing: 1px; font-weight: 600;
}
.ms-rail-fo {
  font-size: 10px; color: rgba(251,191,36,0.9);
  writing-mode: vertical-lr; letter-spacing: 1px; font-weight: 600;
  text-align: right;
}
</style>
