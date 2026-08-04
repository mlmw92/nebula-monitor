import http from '../../api/http'

// 把一次 range 查询的返回值归一化成 series 数组。
// fetchRange 直接返回 series 数组，但历史调用方可能传入 { series: [...] } 原始响应，两种都兼容。
function toSeriesList(resp) {
  if (Array.isArray(resp)) return resp
  if (Array.isArray(resp?.series)) return resp.series
  return []
}

// 聚合多节点 range 查询结果：按时间戳对齐，mode='avg' 取均值、'sum' 取求和
// byNode=true 时（如磁盘存在多挂载点/多设备），先按节点把同节点多条序列归并为单值，
// 再跨节点聚合，避免「磁盘数越多的节点权重越大」造成的均值失真。
export function aggregateSeries(responses, mode = 'avg', byNode = false) {
  let src = (responses || []).map(toSeriesList)
  if (byNode) {
    // 每个 response 对应一个节点：先把节点内多挂载点/多设备按时间戳取均值
    src = src.map((seriesList) => {
      const acc = new Map()
      for (const s of seriesList) {
        for (const p of s?.points || []) {
          const e = acc.get(p.timestamp) || [0, 0]
          e[0] += p.value
          e[1] += 1
          acc.set(p.timestamp, e)
        }
      }
      const points = []
      for (const [ts, [sum, count]] of acc) points.push({ timestamp: ts, value: sum / count })
      return [{ points }]
    })
  }
  const acc = new Map()
  for (const seriesList of src) {
    for (const s of seriesList) {
      for (const p of s?.points || []) {
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

// 把单节点多条序列（如多网卡、多磁盘）按时间戳合并成 [[ts, value], ...]
// mode='sum' 求和（网卡收发合计），'avg' 取均值。
export function mergeSeries(resp, mode = 'sum') {
  const acc = new Map()
  for (const s of toSeriesList(resp)) {
    for (const p of s?.points || []) {
      const e = acc.get(p.timestamp) || [0, 0]
      e[0] += p.value
      e[1] += 1
      acc.set(p.timestamp, e)
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
  const sample = (nodeList || []).filter(Boolean).slice(0, max)
  if (!sample.length) return []
  const results = await Promise.all(sample.map((n) => fetchRange(n, metric, { minutes, step })))
  return aggregateSeries(results, mode, byNode)
}

// 按主机分别返回趋势（不做跨节点聚合），用于需要区分来源的场景（如多套高可用集群的网络流量）。
// 返回 [{ node, points: [[ts, value], ...] }, ...]，节点内多网卡/多磁盘按 mode 合并。
export async function queryPerNodeTrend(nodeList, metric, { minutes = 60, step = 60000, max = 20, mode = 'sum' } = {}) {
  const sample = (nodeList || []).filter(Boolean).slice(0, max)
  if (!sample.length) return []
  const results = await Promise.all(sample.map((n) => fetchRange(n, metric, { minutes, step })))
  return sample.map((node, i) => ({ node, points: mergeSeries(results[i], mode) }))
}

// 分钟级时间标签（HH:MM）
export function tsToLabel(ts) {
  const d = new Date(ts)
  const hh = String(d.getHours()).padStart(2, '0')
  const mm = String(d.getMinutes()).padStart(2, '0')
  return `${hh}:${mm}`
}
