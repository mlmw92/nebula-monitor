// security.js — 安全中心 API 封装，复用 http 的 get。
import http from './http'

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
