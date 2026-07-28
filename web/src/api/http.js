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
  if (!r.ok) throw new Error('HTTP ' + r.status)
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
  // multipart 上传（不预设 Content-Type，由浏览器自动加 boundary）
  upload: async (p, formData) => {
    const token = getToken()
    const headers = token ? { Authorization: 'Bearer ' + token } : {}
    const r = await fetch(p, { method: 'POST', body: formData, headers })
    if (r.status === 401) {
      setToken('')
      window.dispatchEvent(new CustomEvent('auth-expired'))
      throw new Error('未登录或登录已过期')
    }
    if (!r.ok) throw new Error('HTTP ' + r.status)
    return r.json()
  },
}
