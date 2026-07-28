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
]

export const metrics = metricGroups.flatMap((g) => g.metrics.map((m) => ({ ...m, category: g.category })))

export const metricMap = Object.fromEntries(metrics.map((m) => [m.name, m]))
