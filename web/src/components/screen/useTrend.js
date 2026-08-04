import http from '../../api/http'

// 聚合多节点 range 查询结果：按时间戳对齐，mode='avg' 取均值、'sum' 取求和
// byNode=true 时（如磁盘存在多挂载点/多设备），先按节点把同节点多条序列归并为单值，
// 再跨节点聚合，避免「磁盘数越多的节点权重越大」造成的均值失真。
export function aggregateSeries(responses, mode = 'avg', byNode = false) {
  let src = responses
  if (byNode) {
    // 每个 response 对应一个节点：先把节点内多挂载点/多设备按时间戳取均值
    src = (responses || []).map((r) => {
      const acc = new Map()
      for (const s of r?.series || []) {
        for (const p of s.points || []) {
          const e = acc.get(p.timestamp) || [0, 0]
          e[0] += p.value
          e[1] += 1
          acc.set(p.timestamp, e)
        }
      }
      const points = []
      for (const [ts, [sum, count]] of acc) points.push({ timestamp: ts, value: sum / count })
      return { series: [{ points }] }
    })
  }
  const acc = new Map()
  for (const r of src) {
    for (const s of r?.series || []) {
      for (const p of s.points || []) {
        const e = acc.get(p.timestamp) || [0, 0]
        e[0] += p.value
        e[1] += 1
        acc.set(p.timestamp, e)
      }
    }
  }
  const out = []
  for (const [ts, [sum, count]] of acc) out.push([ts, mode === 'sum' ? sum : sum / count])
  out.sort((a, b) => a[0] - b[0])
  return out
}

// 查询单节点指标 range（start/end/step 均为毫秒）
export async function fetchRange(node, metric, { minutes = 60, step = 60000, labels } = {}) {
  const end = Date.now()
  const start = end - minutes * 60000
  let url = `/api/v1/query/range?node=${encodeURIComponent(node)}&metric=${encodeURIComponent(metric)}&start=${start}&end=${end}&step=${step}`
  if (labels) {
    for (const [k, v] of Object.entries(labels)) url += `&labels.${encodeURIComponent(k)}=${encodeURIComponent(v)}`
  }
  try {
    const data = await http.get(url)
    return data?.series || []
  } catch (e) {
    console.error('range 查询失败', metric, e)
    return []
  }
}

// 集群聚合趋势：取节点列表前 max 台并发查询后聚合
export async function queryClusterTrend(nodeList, metric, mode = 'avg', { minutes = 60, step = 60000, max = 20, byNode = false } = {}) {
  const sample = (nodeList || []).slice(0, max)
  if (!sample.length) return []
  const results = await Promise.all(sample.map((n) => fetchRange(n, metric, { minutes, step })))
  return aggregateSeries(results, mode, byNode)
}

// 分钟级时间标签（HH:MM）
export function tsToLabel(ts) {
  const d = new Date(ts)
  const hh = String(d.getHours()).padStart(2, '0')
  const mm = String(d.getMinutes()).padStart(2, '0')
  return `${hh}:${mm}`
}
