// ops.js — 统一下行操作通道 API 封装。
//
// 只做「路径拼接 + 参数过滤」：权限（ops:read / ops:exec）与资源范围都在服务端校验，
// 前端隐藏按钮只是体验，不是安全边界。
import http from './http'

const withQuery = (path, params = {}) => {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== '') q.append(k, v)
  }
  const s = q.toString()
  return path + (s ? '?' + s : '')
}

// 动作目录；带 node 时一并返回该节点**本机放行**的动作清单与操作权限点。
export const listOpsActions = (node = '') => http.get(withQuery('/api/v1/ops/actions', { node }))

// 任务列表（node / state / kind / limit）
export const listOpsTasks = (params = {}) => http.get(withQuery('/api/v1/ops/tasks', params))

export const getOpsTask = (id) => http.get('/api/v1/ops/tasks/' + encodeURIComponent(id))

// 下发一条操作任务：{ node, kind, params, reason }
// 服务端可能返回 409（节点不支持该动作 / 节点不在线）与 400（参数不合法），
// 两类错误的下一步完全不同，界面必须原样展示服务端给的说明。
export const createOpsTask = (payload) => http.post('/api/v1/ops/tasks', payload)
