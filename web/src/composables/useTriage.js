/* 首页「研判条」的规则引擎。
 *
 * 设计取舍（重要）：
 *  - 纯规则实现，**不接 LLM、不新增接口**：输入现有的活跃告警 + 节点列表 + 最新指标快照，
 *    输出「最该处理的一件事」。
 *  - 凡是无法从现有数据得出的结论一律不编。典型例子是"按当前增速约 N 小时后写满"——
 *    那需要一条真实的历史序列，而 /api/v1/nodes/latest 只给最新一个采样点，
 *    所以这里只陈述**已发生的事实**（当前值 / 阈值 / 级别），不做趋势外推。
 *  - 选谁：critical 优先，其次 startsAt 最早的（最早触发 = 已经烂了最久）。
 *  - 给什么：按告警名/指标名匹配模板，给出中文结论与一条可执行的处置建议。
 */

// 关键词 → 中文口径 + 处置建议。顺序即优先级（越靠前越"硬"）。
const TEMPLATES = [
  {
    key: 'disk',
    test: /磁盘|disk|空间|inode/i,
    metric: 'disk',
    label: '磁盘使用率',
    suggest: '清理日志与归档表，确认是否有异常写入；容量确实不足时扩容数据盘。',
  },
  {
    key: 'mem',
    test: /内存|memory|\bmem\b|swap/i,
    metric: 'mem',
    label: '内存使用率',
    suggest: '定位占用最高的进程，必要时重启发生泄漏的服务或提升实例规格。',
  },
  {
    key: 'cpu',
    test: /cpu|负载|load/i,
    metric: 'cpu',
    label: 'CPU 使用率',
    suggest: '查看慢查询与高占用进程，确认是否有突发流量或定时任务叠加。',
  },
  {
    key: 'net',
    test: /丢包|网络|packet|latency|rtt|拨测/i,
    metric: null,
    label: '网络质量',
    suggest: '检查链路、带宽占用与对端状态，必要时切换备用线路。',
  },
  {
    key: 'offline',
    test: /离线|offline|不可达|down|未上报/i,
    metric: null,
    label: '节点离线',
    suggest: '确认主机是否宕机、Agent 进程是否存活、网络与防火墙是否放通。',
  },
]

function matchTemplate(alert) {
  const hay = `${alert.ruleName || ''} ${alert.metric || ''}`
  return TEMPLATES.find((t) => t.test.test(hay)) || null
}

function severityRank(alert) {
  return (alert.severity || '').toLowerCase() === 'critical' ? 0 : 1
}

function pickAlert(alerts) {
  if (!alerts || !alerts.length) return null
  return [...alerts].sort((a, b) => {
    const r = severityRank(a) - severityRank(b)
    if (r !== 0) return r
    return new Date(a.startsAt || 0).getTime() - new Date(b.startsAt || 0).getTime()
  })[0]
}

function formatValue(v) {
  if (v === undefined || v === null || v === '') return ''
  const n = Number(v)
  if (Number.isNaN(n)) return String(v)
  return Number.isInteger(n) ? String(n) : String(Math.round(n * 100) / 100)
}

/**
 * @param {Array}  alerts    当前活跃告警（/api/v1/alerts?state=active）
 * @param {Array}  nodes     节点列表（/api/v1/nodes）
 * @param {Object} latestMap 最新指标快照（/api/v1/nodes/latest 的 metrics）
 * @returns {null|{target,headline,reason,suggestion,severity}}
 */
export function buildTriage(alerts = [], nodes = [], latestMap = {}) {
  const alert = pickAlert(alerts)
  if (!alert) return null

  const tpl = matchTemplate(alert)
  const label = tpl ? tpl.label : alert.metric || '指标'
  const host = alert.node || alert.instance || alert.nodeIp || '未知节点'
  const nodeInfo = nodes.find((n) => n.hostname === alert.node) || null
  const sev = (alert.severity || '').toLowerCase()
  const sevText = sev === 'critical' ? '紧急' : sev === 'warning' ? '警告' : '提示'

  // 数值优先取告警自带的触发值；缺失时回落到最新指标快照（都是真实数据，不估算）
  let value = formatValue(alert.value)
  if (!value && tpl && tpl.metric && latestMap[alert.node]) {
    value = formatValue(latestMap[alert.node][tpl.metric])
  }

  const detail = []
  detail.push(value ? `${label} ${value}` : label)
  if (alert.threshold !== undefined && alert.threshold !== null && alert.threshold !== '') {
    detail.push(`阈值 ${alert.operator || ''} ${formatValue(alert.threshold)}`)
  }

  const where = nodeInfo && nodeInfo.group ? `${host}（分组 ${nodeInfo.group}）` : host
  const headline = `最该处理：${where}`
  const reason = `${sevText}告警「${alert.ruleName || label}」，${detail.join('，')}。`
  const suggestion = tpl ? tpl.suggest : '前往告警中心查看该告警的触发条件与近 1 小时趋势。'

  return {
    target: host,
    headline,
    reason,
    suggestion,
    severity: sev,
    alert,
  }
}

export default buildTriage
