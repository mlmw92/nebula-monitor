// http.js — 统一 REST 请求封装，带 token 与 401 自动跳登录

const TOKEN_KEY = 'nebula_token'

export function getToken() {
  return localStorage.getItem(TOKEN_KEY) || ''
}
export function setToken(t) {
  if (t) localStorage.setItem(TOKEN_KEY, t)
  else localStorage.removeItem(TOKEN_KEY)
}

async function request(path, opts) {
  const token = getToken()
  const headers = opts && opts.headers ? { ...opts.headers } : {}
  if (token) headers['Authorization'] = 'Bearer ' + token
  const r = await fetch(path, { ...opts, headers })
  if (r.status === 401) {
    setToken('')
    // 触发登录态切换（App 监听 storage 或手动 dispatch）
    window.dispatchEvent(new CustomEvent('auth-expired'))
    throw new Error('未登录或登录已过期')
  }
  if (!r.ok) {
    // 优先展示后端返回的 error 文案（如校验失败原因），解析失败再退回状态码
    let msg = 'HTTP ' + r.status
    try {
      const body = await r.json()
      if (body && body.error) msg = body.error
    } catch (e) {
      /* 非 JSON 响应体，保留状态码文案 */
    }
    throw new Error(msg)
  }
  return r.json()
}

// 具名导出基础 GET，供指标浏览等高频查询入口直接使用，避免旧缓存或模块互操作
// 导致默认客户端对象上的 get 方法不可用。
export const get = (p) => request(p)

// 指标浏览页使用具名请求函数，避免不同构建环境下默认导出对象互操作异常。
export const metricCatalog = () => request('/api/v1/metrics/catalog')
export const metricActive = (params = {}) => {
  const q = new URLSearchParams(params).toString()
  return request('/api/v1/metrics/active' + (q ? '?' + q : ''))
}
// 先绑定客户端对象，再由对象内部的方法引用自身，避免 create/update
// 等方法调用未定义的 api 变量导致页面运行时异常。
const api = {
  get,
  post: (p, body) =>
    request(p, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }),
  put: (p, body) =>
    request(p, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }),
  del: (p) => request(p, { method: 'DELETE' }),
  // 修改登录密码
  changePassword: (oldPassword, newPassword) =>
    request('/api/v1/auth/change-password', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ oldPassword, newPassword }),
    }),
  // multipart 上传（XHR 实现以支持上传进度回调 onProgress(percent)）
  upload: (p, formData, onProgress) => {    const token = getToken()
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest()
      xhr.open('POST', p)
      if (token) xhr.setRequestHeader('Authorization', 'Bearer ' + token)
      xhr.responseType = 'json'
      xhr.upload.onprogress = (e) => {
        if (e.lengthComputable && typeof onProgress === 'function') {
          onProgress(Math.round((e.loaded / e.total) * 100))
        }
      }
      xhr.onload = () => {
        if (xhr.status === 401) {
          setToken('')
          window.dispatchEvent(new CustomEvent('auth-expired'))
          reject(new Error('未登录或登录已过期'))
          return
        }
        if (xhr.status >= 200 && xhr.status < 300) {
          resolve(xhr.response)
          return
        }
        // 尝试解析后端错误体
        let msg = 'HTTP ' + xhr.status
        try {
          const body = xhr.response || JSON.parse(xhr.responseText)
          if (body && body.error) msg = body.error
        } catch (_) { /* ignore */ }
        reject(new Error(msg))
      }
      xhr.onerror = () => reject(new Error('网络错误'))
      xhr.send(formData)
    })
  },
  // 指标目录（自动发现）
  metricCatalog,
  metricActive,
  // 智能分析（只读）
  analysisSummary: (windowHours = 168, refresh = false) => request('/api/v1/analysis/summary?windowHours=' + windowHours + (refresh ? '&refresh=true' : '')),
  analysisHost: (name, windowHours = 168, refresh = false) => request('/api/v1/analysis/hosts/' + encodeURIComponent(name) + '?windowHours=' + windowHours + (refresh ? '&refresh=true' : '')),
  // 自定义仪表盘 CRUD
  listDashboards: () => request('/api/v1/dashboards'),
  getDashboard: (id) => request('/api/v1/dashboards/' + id),
  createDashboard: (name, panels) => api.post('/api/v1/dashboards', { name, panels }),
  updateDashboard: (id, name, panels) => api.put('/api/v1/dashboards/' + id, { name, panels }),
  deleteDashboard: (id) => api.del('/api/v1/dashboards/' + id),
  // 历史数据导出为 CSV（带 token 的下载：fetch blob 再触发下载）
  exportMetricCSV: (params) => {
    const token = getToken()
    const url = '/api/v1/metrics/export?' + new URLSearchParams(params).toString()
    return fetch(url, { headers: token ? { Authorization: 'Bearer ' + token } : {} })
      .then((r) => {
        if (!r.ok) return r.json().then((b) => { throw new Error(b.error || 'HTTP ' + r.status) })
        return r.blob()
      })
      .then((blob) => {
        const a = document.createElement('a')
        const href = URL.createObjectURL(blob)
        a.href = href
        a.download = params.filename || 'metric_export.csv'
        document.body.appendChild(a)
        a.click()
        document.body.removeChild(a)
        URL.revokeObjectURL(href)
      })
  },
  // —— 角色权限管理（RBAC）——
  me: () => request('/api/v1/auth/me'),
  updateMe: (payload) => api.put('/api/v1/auth/me', payload),
  listUsers: () => request('/api/v1/users'),
  createUser: (payload) => api.post('/api/v1/users', payload),
  getUser: (username) => request('/api/v1/users/' + encodeURIComponent(username)),
  updateUser: (username, payload) => api.put('/api/v1/users/' + encodeURIComponent(username), payload),
  resetUserPassword: (username, newPassword) =>
    api.post('/api/v1/users/' + encodeURIComponent(username) + '/reset-password', { newPassword }),
  disableUser: (username) => api.post('/api/v1/users/' + encodeURIComponent(username) + '/disable', {}),
  enableUser: (username) => api.post('/api/v1/users/' + encodeURIComponent(username) + '/enable', {}),
  deleteUser: (username) => api.del('/api/v1/users/' + encodeURIComponent(username)),
  listRoles: () => request('/api/v1/roles'),
  createRole: (payload) => api.post('/api/v1/roles', payload),
  getRole: (name) => request('/api/v1/roles/' + encodeURIComponent(name)),
  updateRole: (name, payload) => api.put('/api/v1/roles/' + encodeURIComponent(name), payload),
  deleteRole: (name) => api.del('/api/v1/roles/' + encodeURIComponent(name)),
  permissionCatalog: () => request('/api/v1/permissions/catalog'),
  listGroups: () => request('/api/v1/groups'),
  // —— 告警事件管道（relabel / enrich / 消息模板）——
  getAlertPipeline: () => request('/api/v1/alert-pipeline'),
  saveAlertPipeline: (cfg) => api.put('/api/v1/alert-pipeline', cfg),
  previewAlertPipeline: (payload) => api.post('/api/v1/alert-pipeline/preview', payload),
  // —— 自监控与健康探针 ——
  getSelfStatus: () => request('/api/v1/self/status'),
  // 探针在 /api/v1 之外，且 /readyz 以 503 表达「未就绪」——那不是请求失败，
  // 因此不能走统一 request（它会把非 2xx 抛成错误，反而丢掉检查明细）。
  probeHealth: async () => {
    const headers = {}
    const token = getToken()
    if (token) headers['Authorization'] = 'Bearer ' + token
    const read = async (path) => {
      try {
        const r = await fetch(path, { headers })
        return { ok: r.ok, status: r.status, body: (await r.json().catch(() => ({}))) || {} }
      } catch (e) {
        return { ok: false, status: 0, body: {}, error: e.message }
      }
    }
    const [health, ready] = await Promise.all([read('/healthz'), read('/readyz')])
    return { health, ready }
  },
}

export default api
