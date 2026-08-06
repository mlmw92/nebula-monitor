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

export default {
  get: (p) => request(p),
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
  metricCatalog: () => request('/api/v1/metrics/catalog'),
  metricActive: (params = {}) => {
    const q = new URLSearchParams(params).toString()
    return request('/api/v1/metrics/active' + (q ? '?' + q : ''))
  },
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
}
