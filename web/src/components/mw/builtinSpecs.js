// builtinSpecs —— 「轻采集」内置中间件的展示规格。
//
// 这 5 类（RabbitMQ/Elasticsearch/ClickHouse/Nacos/ZooKeeper）的采集都是
// 「存活 + 少量核心指标」，展示形态一致，因此共用一个通用组件（BuiltinTab.vue），
// 由本文件的 spec 驱动：列定义 / 详情趋势指标。
// 指标名必须与 internal/agent/collector 中各采集器实际产出的名字一致。

export const builtinSpecs = {
  rabbitmq: {
    label: 'RabbitMQ',
    columns: [
      { key: 'rabbitmq_connections', label: '连接数' },
      { key: 'rabbitmq_queues', label: '队列数' },
      { key: 'rabbitmq_queue_messages', label: '消息总数' },
      { key: 'rabbitmq_consumers', label: '消费者数' },
      { key: 'rabbitmq_publishers', label: '发布者数' },
      { key: 'rabbitmq_process_memory_bytes', label: '进程内存(B)' },
      { key: 'rabbitmq_fd_used', label: 'FD 已用' },
    ],
    trends: [
      { name: '连接数', metric: 'rabbitmq_connections' },
      { name: '消息总数', metric: 'rabbitmq_queue_messages' },
    ],
  },
  elasticsearch: {
    label: 'Elasticsearch',
    columns: [
      { key: 'es_cluster_status', label: '集群状态(0绿/1黄/2红)' },
      { key: 'es_nodes', label: '节点数' },
      { key: 'es_data_nodes', label: '数据节点' },
      { key: 'es_active_shards', label: '活跃分片' },
      { key: 'es_unassigned_shards', label: '未分配分片' },
    ],
    trends: [
      { name: '节点数', metric: 'es_nodes' },
      { name: '未分配分片', metric: 'es_unassigned_shards' },
    ],
  },
  clickhouse: {
    label: 'ClickHouse',
    columns: [
      { key: 'clickhouse_tcp_connections', label: 'TCP连接' },
      { key: 'clickhouse_http_connections', label: 'HTTP连接' },
      { key: 'clickhouse_queries_running', label: '运行中查询' },
      { key: 'clickhouse_merges_running', label: '合并数' },
      { key: 'clickhouse_uptime_seconds', label: '运行时长(s)' },
    ],
    trends: [
      { name: 'TCP连接', metric: 'clickhouse_tcp_connections' },
      { name: '运行中查询', metric: 'clickhouse_queries_running' },
    ],
  },
  nacos: {
    label: 'Nacos',
    columns: [],
    trends: [],
  },
  zookeeper: {
    label: 'ZooKeeper',
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
  },
}

export function builtinSpec(type) {
  return builtinSpecs[type] || { label: type, columns: [], trends: [] }
}
