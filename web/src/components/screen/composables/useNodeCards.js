// 大屏各 Tab 共用的「节点卡片」组装逻辑。
// 各 Tab 此前各自拼装节点对象，字段名容易漂移（例如主机名一处叫 name、一处叫 hostname），
// 导致下游组件取不到值。这里统一产出结构，主机名同时提供 hostname 与 name 两个别名。

/**
 * 按主机名去重。服务端理论上主机名唯一，此处兜底避免重复注册导致列表出现重复行。
 * @param {Array} nodes /api/v1/nodes 返回的节点列表
 */
export function dedupeNodes(nodes) {
  const seen = new Set()
  return (nodes || []).filter((n) => {
    const key = n?.hostname
    if (!key || seen.has(key)) return false
    seen.add(key)
    return true
  })
}

/**
 * 组装节点卡片。
 * @param {Array} nodes /api/v1/nodes 返回的节点列表
 * @param {Object} metrics /api/v1/nodes/latest 返回的按主机名索引的指标表
 * @returns {Array} 节点卡片列表
 */
export function buildNodeCards(nodes, metrics) {
  const m = metrics || {}
  return dedupeNodes(nodes).map((n) => {
    const x = m[n.hostname] || {}
    return {
      hostname: n.hostname,
      name: n.hostname,
      displayName: n.displayName || '',
      // 列表展示用：优先显示备注名，回落到主机名
      label: n.displayName || n.hostname,
      ip: n.ip || '-',
      group: n.group || '默认分组',
      online: n.status === 'online',
      cpu: x.cpu || 0,
      mem: x.mem || 0,
      disk: x.disk || 0,
      load1: x.load1 || 0,
      load5: x.load5 || 0,
      load15: x.load15 || 0,
      load: x.load1 || 0,
      netIn: x.netIn || 0,
      netOut: x.netOut || 0,
      diskIopsR: x.diskIopsR || 0,
      diskIopsW: x.diskIopsW || 0,
      netDrop: x.netDrop || 0,
      tcpRetrans: x.tcpRetrans || 0,
      memTotal: x.memTotal || 0,
      memUsed: x.memUsed || 0,
    }
  })
}
