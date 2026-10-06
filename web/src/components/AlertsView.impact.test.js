// 告警详情里的「资产与影响面」（全景表 1-05）。
//
// 为什么单独一个文件：告警页的既有用例把 el-table / el-drawer 全部 stub 掉了（只测
// 工具栏按钮的权限可见性），而影响面是**抽屉里的内容**，必须在抽屉真的渲染出来时才能断言。
//
// 守两件事：① 打开详情会去请求影响面（带 node + instance 两个标签）；
// ② 命中资产、近期变更与波及范围都出现在界面上（拿不到就说清楚，不静默留空）。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import http from '../api/http'

vi.mock('../api/http', () => ({
  default: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() },
  getToken: vi.fn(() => ''),
}))

const routerPush = vi.fn()
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: routerPush, replace: vi.fn() }),
  useRoute: () => ({ query: {} }),
}))

// 关系图（影响面里的波及范围）用它自己的接口，这里只要求它不报错。
vi.mock('echarts', () => ({
  init: vi.fn(() => ({ setOption: vi.fn(), resize: vi.fn(), dispose: vi.fn(), on: vi.fn(), off: vi.fn() })),
  graphic: { LinearGradient: vi.fn() },
}))

vi.mock('@element-plus/icons-vue', () => ({
  Plus: { name: 'Plus', template: '<i />' },
  Bell: { name: 'Bell', template: '<i />' },
  Printer: { name: 'Printer', template: '<i />' },
}))

vi.mock('../composables/useAuth', () => ({
  default: () => ({ can: () => true, principal: { loaded: true, permissions: [] } }),
}))

const ALERT = {
  id: 'ev-1',
  ruleName: 'Redis 内存使用率过高',
  node: 'web-01',
  instance: '127.0.0.1:6379',
  metric: 'redis_memory_used_percent',
  operator: '>',
  threshold: 80,
  value: 92,
  severity: 'warning',
  state: 'firing',
  message: '内存使用率 92%',
  startsAt: Date.now(),
}

const IMPACT = {
  node: 'web-01',
  instance: '127.0.0.1:6379',
  matched: [
    { id: 1, typeKey: 'host', typeTitle: '主机', naturalKey: 'web-01', name: 'web-01', node: 'web-01' },
    { id: 2, typeKey: 'middleware-instance', typeTitle: '中间件实例', naturalKey: 'redis:127.0.0.1:6379', name: 'dev-redis', node: 'web-01' },
  ],
  changes: [
    { assetId: 1, typeKey: 'host', naturalKey: 'web-01', field: 'maxmemory', old: '2gb', new: '1gb', source: 'manual', actor: 'ops', at: Date.now() },
  ],
  topology: {
    root: 'host|web-01',
    depth: 2,
    truncated: false,
    nodes: [{ key: 'host|web-01', id: 1, typeKey: 'host', typeTitle: '主机', naturalKey: 'web-01', name: 'web-01', node: 'web-01', status: 'online', root: true, depth: 0 }],
    edges: [],
  },
}

import AlertsView from './AlertsView.vue'

let wrappers = []

function mountView() {
  const w = mount(AlertsView, { global: { plugins: [ElementPlus] }, attachTo: document.body })
  wrappers.push(w)
  return w
}

beforeEach(() => {
  vi.clearAllMocks()
  wrappers = []
  document.body.innerHTML = ''
  http.get.mockImplementation(async (path) => {
    const p = String(path)
    if (p.includes('/api/v1/alerts/impact')) return IMPACT
    if (p.includes('/api/v1/alerts/trend')) return { points: [] }
    if (p.includes('/api/v1/alerts')) return { alerts: [ALERT], firing: 1, suppressed: 0, total: 1, bySeverity: { critical: 0, warning: 1, info: 0 } }
    if (p.includes('/api/v1/rules')) return { rules: [] }
    if (p.includes('/api/v1/alerts/acks')) return { acks: {} }
    return {}
  })
})

afterEach(() => {
  for (const w of wrappers) w.unmount()
  document.body.innerHTML = ''
})

describe('AlertsView 资产与影响面', () => {
  it('打开告警详情会带上 node 与 instance 请求影响面', async () => {
    const w = mountView()
    await flushPromises()

    const detail = w.findAll('button').find((b) => b.text().includes('详情'))
    expect(detail).toBeTruthy()
    await detail.trigger('click')
    await flushPromises()

    const calls = http.get.mock.calls.map((c) => String(c[0]))
    const impact = calls.find((p) => p.includes('/api/v1/alerts/impact'))
    expect(impact).toBeTruthy()
    expect(impact).toContain('node=web-01')
    expect(impact).toContain('instance=127.0.0.1%3A6379')
  })

  it('抽屉里展示命中资产、近期变更与波及范围', async () => {
    const w = mountView()
    await flushPromises()
    await w.findAll('button').find((b) => b.text().includes('详情')).trigger('click')
    await flushPromises()

    const text = document.body.textContent || ''
    expect(text).toContain('资产与影响面')
    expect(text).toContain('web-01')
    expect(text).toContain('dev-redis')
    expect(text).toContain('近期变更')
    expect(text).toContain('maxmemory')
    expect(text).toContain('2gb → 1gb')
    expect(text).toContain('波及范围')
  })

  it('没匹配到资产时给出原因，而不是留一片空白', async () => {
    http.get.mockImplementation(async (path) => {
      const p = String(path)
      if (p.includes('/api/v1/alerts/impact')) {
        return { node: 'web-01', instance: '', matched: [], changes: [], note: '台账里没有名为「web-01」的主机资产' }
      }
      if (p.includes('/api/v1/alerts/trend')) return { points: [] }
      if (p.includes('/api/v1/alerts')) return { alerts: [ALERT], firing: 1, bySeverity: { critical: 0, warning: 1, info: 0 } }
      return {}
    })
    const w = mountView()
    await flushPromises()
    await w.findAll('button').find((b) => b.text().includes('详情')).trigger('click')
    await flushPromises()

    const text = document.body.textContent || ''
    expect(text).toContain('台账里没有名为「web-01」的主机资产')
  })
})
