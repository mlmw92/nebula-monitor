// 通用数值格式化工具
export function fmtNum(v, d = 1) {
  if (v == null || isNaN(v)) return '-'
  return Number(v).toLocaleString('zh-CN', { maximumFractionDigits: d })
}

export function fmtInt(v) {
  return fmtNum(v, 0)
}

export function fmtPct(v) {
  if (v == null || isNaN(v)) return '-'
  return Number(v).toFixed(1) + '%'
}

export function fmtBytes(v) {
  if (v == null || isNaN(v)) return '-'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  let i = 0
  let n = Number(v)
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return n.toFixed(1) + ' ' + units[i]
}

// 根据中间件配置里的 fmt 标识格式化指标值
export function formatMetric(fmt, v) {
  switch (fmt) {
    case 'bytes':
      return fmtBytes(v)
    case 'pct':
      return fmtPct(v)
    case 'pctNodes':
      // 节点就绪：以数字展示（配合总量在 UI 显示）
      return fmtInt(v)
    case 'num0':
      return fmtInt(v)
    case 'num':
    default:
      return fmtNum(v)
  }
}
