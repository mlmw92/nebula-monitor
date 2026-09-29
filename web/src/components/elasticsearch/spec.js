// Elasticsearch 展示规格。
export default {
  type: 'elasticsearch',
  label: 'Elasticsearch',
  kpis: [
    { label: '节点总数', metric: 'es_nodes', agg: 'max' },
    { label: '活跃分片', metric: 'es_active_shards', agg: 'sum' },
    { label: '未分配分片', metric: 'es_unassigned_shards', agg: 'sum' },
  ],
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
}
