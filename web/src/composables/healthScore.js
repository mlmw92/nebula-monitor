// 系统健康度统一计算口径：首页概览与数据大屏共用。

const PRESSURE_CONFIG = [
  { key: 'cpu', label: 'CPU', warnAt: 70, weight: 15 },
  { key: 'mem', label: '内存', warnAt: 80, weight: 12 },
  { key: 'disk', label: '磁盘', warnAt: 85, weight: 12 },
]

function average(values) {
  return values.length ? values.reduce((sum, value) => sum + value, 0) / values.length : 0
}

export function calculateAlertScore(alerts = []) {
  const critical = alerts.filter((alert) => (alert.severity || '').toLowerCase() === 'critical').length
  const warning = alerts.filter((alert) => (alert.severity || '').toLowerCase() === 'warning').length
  return Math.max(0, 100 - critical * 5 - warning * 2)
}

/**
 * @param {Array} nodes /api/v1/nodes 返回的节点
 * @param {Object} metrics 按主机名索引的最新指标
 * @param {Array} alerts 当前活跃告警
 */
export function calculateSystemHealth(nodes = [], metrics = {}, alerts = []) {
  const total = nodes.length
  const offline = nodes.filter((node) => node.status !== 'online').length
  const online = total - offline
  const pressure = PRESSURE_CONFIG.map((config) => {
    const values = nodes
      .map((node) => metrics[node.hostname]?.[config.key])
      .filter((value) => typeof value === 'number' && !Number.isNaN(value))
    const avgVal = average(values)
    const avgOver = average(values.map((value) => Math.max(0, value - config.warnAt)))
    return {
      ...config,
      rate: avgVal,
      avgOver,
      warnCount: values.filter((value) => value >= config.warnAt).length,
      badCount: values.filter((value) => value >= config.warnAt + 15).length,
      count: values.length,
    }
  })

  const criticalAlerts = alerts.filter((alert) => (alert.severity || '').toLowerCase() === 'critical').length
  const warningAlerts = alerts.filter((alert) => (alert.severity || '').toLowerCase() === 'warning').length
  const alertScore = calculateAlertScore(alerts)
  let score = 100
  if (total > 0) {
    score -= (offline / total) * 60
    pressure.forEach((item) => { score -= (item.avgOver / 100) * item.weight })
    score -= 100 - alertScore
    score = Math.max(0, Math.round(score))
  }

  return {
    score,
    statusText: total === 0 ? '无数据' : score >= 90 ? '健康' : score >= 70 ? '轻微风险' : '风险较高',
    rank: total === 0 ? 'unknown' : score >= 90 ? 'good' : score >= 70 ? 'warn' : 'bad',
    total,
    online,
    offline,
    criticalAlerts,
    warningAlerts,
    alertScore,
    pressure,
  }
}
