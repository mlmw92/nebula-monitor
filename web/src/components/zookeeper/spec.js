// ZooKeeper 展示规格。
export default {
  type: 'zookeeper',
  label: 'ZooKeeper',
  kpis: [
    { label: '活跃连接', metric: 'zookeeper_alive_connections', agg: 'sum' },
  ],
  columns: [
    { key: 'zookeeper_avg_latency', label: '平均延迟(ms)' },
    { key: 'zookeeper_outstanding_requests', label: '未处理请求' },
    { key: 'zookeeper_alive_connections', label: '活跃连接' },
    { key: 'zookeeper_znode_count', label: 'Znode 数' },
  ],
  trends: [
    { name: '平均延迟(ms)', metric: 'zookeeper_avg_latency' },
    { name: '活跃连接', metric: 'zookeeper_alive_connections' },
  ],
}
