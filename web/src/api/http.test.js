// http.js 是全部请求的唯一出口：token 注入、401 会话失效、错误文案提取都在这里，
// 因此单测优先覆盖它（服务端鉴权才是安全边界，这里保证前端行为不回归）。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import api, { getToken, setToken } from './http'

function jsonResponse(status, body) {
  return { status, ok: status >= 200 && status < 300, json: async () => body }
}

describe('http 请求封装', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('setToken / getToken 往返并落地 localStorage', () => {
    setToken('abc123')
    expect(getToken()).toBe('abc123')
    setToken('')
    expect(getToken()).toBe('')
    expect(localStorage.getItem('nebula_token')).toBeNull()
  })

  it('有 token 时附加 Authorization 头，无 token 时不附加', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { server: '1.24.0' }))
    vi.stubGlobal('fetch', fetchMock)

    setToken('tok-1')
    await api.get('/api/v1/version')
    expect(fetchMock.mock.calls[0][1].headers.Authorization).toBe('Bearer tok-1')

    setToken('')
    await api.get('/api/v1/version')
    expect(fetchMock.mock.calls[1][1].headers.Authorization).toBeUndefined()
  })

  it('2xx 返回解析后的 JSON', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(200, { nodes: [1, 2] })))
    await expect(api.get('/api/v1/nodes')).resolves.toEqual({ nodes: [1, 2] })
  })

  it('401 清空 token、派发 auth-expired 事件并抛错', async () => {
    setToken('stale-token')
    const onExpired = vi.fn()
    window.addEventListener('auth-expired', onExpired)
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(401, {})))

    await expect(api.get('/api/v1/nodes')).rejects.toThrow('未登录或登录已过期')
    expect(getToken()).toBe('')
    expect(onExpired).toHaveBeenCalledTimes(1)

    window.removeEventListener('auth-expired', onExpired)
  })

  it('非 2xx 优先展示后端 error 文案（如校验失败原因）', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(400, { error: '配置校验未通过: 未知操作' })))
    await expect(api.put('/api/v1/inhibit', {})).rejects.toThrow('配置校验未通过: 未知操作')
  })

  it('错误体不是 JSON 时回退为状态码文案', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      status: 500,
      ok: false,
      json: async () => { throw new Error('not json') },
    }))
    await expect(api.get('/api/v1/nodes')).rejects.toThrow('HTTP 500')
  })

  it('post / put 会序列化 body 并设置 Content-Type', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { status: 'ok' }))
    vi.stubGlobal('fetch', fetchMock)

    await api.post('/api/v1/groups', { name: 'g1' })
    const [, opts] = fetchMock.mock.calls[0]
    expect(opts.method).toBe('POST')
    expect(opts.headers['Content-Type']).toBe('application/json')
    expect(JSON.parse(opts.body)).toEqual({ name: 'g1' })
  })

  it('del 使用 DELETE 方法', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, {}))
    vi.stubGlobal('fetch', fetchMock)
    await api.del('/api/v1/nodes/web-01')
    expect(fetchMock.mock.calls[0][1].method).toBe('DELETE')
  })

  it('metricActive 正确拼装查询串', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { items: [] }))
    vi.stubGlobal('fetch', fetchMock)

    await api.metricActive({ category: 'cpu', node: 'web-01' })
    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/metrics/active?category=cpu&node=web-01')
  })
})
