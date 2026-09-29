// ClickHouse 展示规格。
export default {
  type: 'clickhouse',
  label: 'ClickHouse',
  kpis: [
    { label: 'TCP 连接', metric: 'clickhouse_tcp_connections', agg: 'sum' },
    { label: '运行中查询', metric: 'clickhouse_queries_running', agg: 'sum' },
  ],
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
}
