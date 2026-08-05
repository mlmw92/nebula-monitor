// 指标字典：与 internal/agent/collector 中各采集器上报的指标保持一致。
// 告警规则只需选择指标名(name)；后端按 name 查询最新值
// （disk_used_percent 在告警引擎中走真实磁盘汇总使用率）。
// 字段说明：
//   name        指标名（提交给后端的值）
//   label       中文名称
//   unit        单位
//   description 简明说明

export const metricGroups = [
  {
    category: 'CPU',
    metrics: [
      { name: 'cpu_usage', label: 'CPU 使用率', unit: '%', description: 'CPU 总体使用率，取值 0-100' },
      { name: 'cpu_cores', label: 'CPU 核心数', unit: '核', description: '逻辑 CPU 核心数量' },
    ],
  },
  {
    category: '进程',
    metrics: [
      { name: 'process_total', label: '进程总数', unit: '个', description: '主机当前运行的进程数量' },
    ],
  },
  {
    category: '内存',
    metrics: [
      { name: 'mem_used_percent', label: '内存使用率', unit: '%', description: '物理内存已用占比' },
      { name: 'mem_used_bytes', label: '内存已用', unit: 'Bytes', description: '已使用的物理内存大小' },
      { name: 'mem_total_bytes', label: '内存总量', unit: 'Bytes', description: '物理内存总大小' },
      { name: 'mem_available_bytes', label: '内存可用', unit: 'Bytes', description: '可供应用使用的内存大小' },
      { name: 'swap_used_percent', label: '交换分区使用率', unit: '%', description: 'Swap 交换分区使用占比' },
      { name: 'swap_used_bytes', label: '交换分区已用', unit: 'Bytes', description: '已使用的 Swap 大小' },
    ],
  },
  {
    category: '负载',
    metrics: [
      { name: 'load1', label: '系统负载(1分钟)', unit: '', description: '最近 1 分钟平均负载' },
      { name: 'load5', label: '系统负载(5分钟)', unit: '', description: '最近 5 分钟平均负载' },
      { name: 'load15', label: '系统负载(15分钟)', unit: '', description: '最近 15 分钟平均负载' },
    ],
  },
  {
    category: '磁盘',
    metrics: [
      { name: 'disk_used_percent', label: '磁盘使用率', unit: '%', description: '全部真实磁盘的汇总使用率（已过滤 tmpfs/overlay 等虚拟挂载）' },
      { name: 'disk_total', label: '磁盘总量', unit: 'Bytes', description: '全部真实磁盘的总容量' },
      { name: 'disk_used', label: '磁盘已用', unit: 'Bytes', description: '全部真实磁盘的已用容量' },
      { name: 'disk_read_rate', label: '磁盘读取速率', unit: 'Bytes/s', description: '磁盘设备读取速率（按设备维度）' },
      { name: 'disk_write_rate', label: '磁盘写入速率', unit: 'Bytes/s', description: '磁盘设备写入速率（按设备维度）' },
    ],
  },
  {
    category: '网络',
    metrics: [
      { name: 'network_recv_rate', label: '网络接收速率', unit: 'Bytes/s', description: '网卡接收速率（按网卡维度，排除 lo）' },
      { name: 'network_sent_rate', label: '网络发送速率', unit: 'Bytes/s', description: '网卡发送速率（按网卡维度，排除 lo）' },
    ],
  },
  {
    category: 'Redis',
    metrics: [
      { name: 'redis_instance_up', label: '实例存活状态', unit: '', description: 'Redis 实例是否可达（1=在线 0=离线）' },
      { name: 'redis_connected_clients', label: '连接客户端数', unit: '', description: '当前连接的客户端数量' },
      { name: 'redis_blocked_clients', label: '阻塞客户端数', unit: '', description: '被阻塞的客户端数量（BLPOP 等）' },
      { name: 'redis_used_memory', label: '已用内存', unit: 'Bytes', description: 'Redis 分配器分配的内存总量' },
      { name: 'redis_used_memory_rss', label: 'RSS 内存', unit: 'Bytes', description: '操作系统视角的 Redis 进程驻留内存' },
      { name: 'redis_used_memory_percent', label: '内存使用率', unit: '%', description: '已用内存占 maxmemory 的百分比' },
      { name: 'redis_memory_fragmentation_ratio', label: '内存碎片率', unit: '', description: 'used_memory_rss / used_memory，越接近 1 越好' },
      { name: 'redis_ops_per_sec', label: '每秒操作数', unit: 'ops/s', description: '瞬时每秒处理的命令数' },
      { name: 'redis_total_commands_processed', label: '累计命令数', unit: '', description: '服务器运行以来处理的命令总数' },
      { name: 'redis_rejected_connections', label: '拒绝连接数', unit: '', description: '因 maxclients 达到上限被拒绝的连接数' },
      { name: 'redis_evicted_keys', label: '淘汰键数', unit: '', description: '因 maxmemory 策略被淘汰的键数量' },
      { name: 'redis_expired_keys', label: '过期键数', unit: '', description: '因 TTL 过期被删除的键数量' },
      { name: 'redis_keys', label: '键数量', unit: '', description: 'db0 中的键数量' },
      { name: 'redis_hit_rate', label: '缓存命中率', unit: '%', description: 'keyspace_hits/(hits+misses)*100' },
      { name: 'redis_uptime_in_seconds', label: '运行时长', unit: 's', description: 'Redis 服务器运行时长（秒）' },
      { name: 'redis_replication_offset', label: '复制偏移量', unit: '', description: '主从复制偏移量' },
      { name: 'redis_replication_lag', label: '复制延迟', unit: 's', description: '从节点相对主节点的延迟秒数' },
      { name: 'redis_sentinel_masters', label: '哨兵监控主节点数', unit: '', description: '哨兵监控的 master 数量' },
      { name: 'redis_sentinel_slaves', label: '哨兵从节点数', unit: '', description: '哨兵视角下的 slave 数量' },
      { name: 'redis_sentinel_sentinels', label: '哨兵同伴数', unit: '', description: '哨兵集群中的 sentinel 数量' },
      { name: 'redis_cluster_state', label: '集群状态', unit: '', description: '集群是否正常（1=ok 0=fail）' },
      { name: 'redis_cluster_slots_assigned', label: '已分配槽位', unit: '', description: '集群已分配的 hash 槽数' },
      { name: 'redis_cluster_slots_ok', label: '正常槽位', unit: '', description: '集群状态正常的 hash 槽数' },
      { name: 'redis_cluster_known_nodes', label: '已知节点数', unit: '', description: '集群已知节点总数' },
      { name: 'redis_cluster_size', label: '集群主节点数', unit: '', description: '集群中 master 节点数量' },
    ],
  },
]

export const metrics = metricGroups.flatMap((g) => g.metrics.map((m) => ({ ...m, category: g.category })))

export const metricMap = Object.fromEntries(metrics.map((m) => [m.name, m]))
