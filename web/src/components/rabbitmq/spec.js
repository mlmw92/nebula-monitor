// RabbitMQ 展示规格：列/趋势/KPI 按需在此增删，不影响其它轻采集类型。
export default {
  type: 'rabbitmq',
  label: 'RabbitMQ',
  kpis: [
    { label: '总连接数', metric: 'rabbitmq_connections', agg: 'sum' },
    { label: '总消息数', metric: 'rabbitmq_queue_messages', agg: 'sum' },
    { label: '总消费者数', metric: 'rabbitmq_consumers', agg: 'sum' },
  ],
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
}
