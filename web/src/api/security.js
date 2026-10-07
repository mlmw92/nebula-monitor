// security.js — 安全中心 API 封装，复用 http 的 get。
import http, { getToken } from './http'

// 安全态势概览：合规评分均值、事件总数、风险主机数、FIM 变化数
export const getSecuritySummary = () => http.get('/api/v1/security/summary')

// 安全事件列表（按时间倒序），支持 limit/category/node 筛选
export const getSecurityEvents = (params = {}) => {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== '') {
      q.append(k, v)
    }
  }
  const s = q.toString()
  return http.get('/api/v1/security/events' + (s ? '?' + s : ''))
}

// 各主机安全基线评分与检查项明细
export const getSecurityBaselines = () => http.get('/api/v1/security/baselines')

// 导出合规矩阵 CSV（节点 × 检查项；该节点没上报的项留空，**空 ≠ 未通过**）。
//
// 与资产导出一致：文件名优先用服务端给的（自带导出时间，同一天导多份不会互相覆盖）；
// 失败时把服务端的报文抛出去——"点了没反应"是最糟的失败形态，
// 而这里恰好有两种常见失败要说清楚：没有 security:export 权限、还没有基线数据。
export async function exportComplianceMatrix(fallbackName = 'compliance-matrix.csv') {
  const token = getToken()
  const res = await fetch('/api/v1/security/baselines/export', {
    headers: token ? { Authorization: 'Bearer ' + token } : {},
  })
  if (!res.ok) {
    let msg = 'HTTP ' + res.status
    try {
      const body = await res.json()
      if (body && body.error) msg = body.error
    } catch (e) {
      /* 非 JSON 响应体：保留状态码文案 */
    }
    throw new Error(msg)
  }
  const disposition = res.headers.get('Content-Disposition') || ''
  const hit = /filename=([^;]+)/.exec(disposition)
  const name = hit ? hit[1].trim() : fallbackName
  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
  return name
}

// 受控 fail2ban 入侵防御：各节点防护状态（含 Agent 是否支持）
export const getDefenseStatuses = () => http.get('/api/v1/security/defense/status')

// 单节点防护状态
export const getDefenseStatus = (node) => http.get(`/api/v1/security/defense/status/${encodeURIComponent(node)}`)

// 创建防护任务：action 为 enable / disable / status
export const postDefenseAction = (node, action) =>
  http.post(`/api/v1/security/defense/${encodeURIComponent(node)}/${encodeURIComponent(action)}`)

// 防护任务列表（可按节点过滤）
export const getDefenseTasks = (node) => {
  const s = node ? `?node=${encodeURIComponent(node)}` : ''
  return http.get('/api/v1/security/defense/tasks' + s)
}

