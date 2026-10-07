// 审计页的时间范围与分页（批次 18 前端）。
//
// 为什么要有这个文件：审计入库后不再被 2000 条截断，"能查到多久以前"要靠时间范围与翻页才够得着。
// 两条口径错了都不会报错，只会让人看到一份**看起来完整的假列表**：
//   ① 改筛选条件若不回到第 1 页，第 3 页 + 新条件会查出空列表 → 看起来像"没有记录"；
//   ② 时间范围若不进查询串，界面选了"近 7 天"却仍在查全部 → 看起来像"这段时间没操作"。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import http, { getToken, setToken } from '../api/http'
import { useAuth } from '../composables/useAuth'
import AuditView from './AuditView.vue'

vi.mock('../api/http', () => ({
  default: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() },
  getToken: vi.fn(() => ''),
  setToken: vi.fn(),
}))

// 审计页用路由 query 认领「只看这一次操作」（深链 /audit?requestId=xxx），
// 并用 router.replace 把被关掉的条件从地址栏摘掉；测试里用 mock 观察这两件事。
const routerPush = vi.fn()
const routerReplace = vi.fn()
let routeQuery = {}
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: routerPush, replace: routerReplace }),
  useRoute: () => ({ query: routeQuery, path: '/audit' }),
}))

// jsdom 未实现 ResizeObserver，Element Plus 的表格会用到它。
if (!globalThis.ResizeObserver) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

const oneEvent = {
  time: '2026-10-05T12:00:00Z',
  user: 'admin',
  method: 'POST',
  path: '/api/v1/assets',
  status: 200,
  succeeded: true,
  category: 'management',
  remoteIP: '10.0.0.1',
}

function mountView() {
  return mount(AuditView, {
    global: { plugins: [ElementPlus] },
    attachTo: document.body,
  })
}

function lastURL() {
  return http.get.mock.calls[http.get.mock.calls.length - 1][0]
}

describe('AuditView 时间范围与分页', () => {
  let wrapper

  beforeEach(() => {
    vi.clearAllMocks()
    http.get.mockImplementation(async (path) => {
      if (path.startsWith('/api/v1/audit/events')) {
        // 总数刻意远大于本页条数：分页要显示的是**同条件总数**，不是本页条数
        return { events: [oneEvent], total: 120 }
      }
      return {}
    })
  })

  afterEach(() => {
    if (wrapper) wrapper.unmount()
    wrapper = undefined
    document.body.innerHTML = ''
  })

  it('首屏不带 offset，分页显示服务端返回的总数', async () => {
    wrapper = mountView()
    await flushPromises()

    expect(lastURL()).toContain('/api/v1/audit/events')
    expect(lastURL()).not.toContain('offset=')
    expect(lastURL()).toContain('limit=100')
    // 分页总数来自响应里的 total（120），不是本页 1 条
    expect(wrapper.find('.el-pagination__total').text()).toContain('120')
  })

  it('翻到第 2 页时带 offset，改筛选条件后回到第 1 页', async () => {
    wrapper = mountView()
    await flushPromises()

    const pageTwo = wrapper.findAll('.el-pager li').find((li) => li.text() === '2')
    expect(pageTwo).toBeTruthy()
    await pageTwo.trigger('click')
    await flushPromises()
    expect(lastURL()).toContain('offset=100')

    // 改操作者 → 必须回到第 1 页：否则「第 3 页 + 新条件」会查出空列表，看起来像没有记录
    const userInput = wrapper.findAll('input').find((i) => i.attributes('placeholder') === '操作者')
    expect(userInput).toBeTruthy()
    await userInput.setValue('admin')
    await userInput.trigger('keyup.enter')
    await flushPromises()
    expect(lastURL()).not.toContain('offset=')
    expect(lastURL()).toContain('user=admin')
  })

  it('旧页请求晚于新筛选返回时，不得覆盖新条件的记录', async () => {
    wrapper = mountView()
    await flushPromises()

    let resolveOld
    http.get.mockImplementationOnce(() => new Promise((resolve) => { resolveOld = resolve }))
    const userInput = wrapper.findAll('input').find((i) => i.attributes('placeholder') === '操作者')
    await userInput.setValue('old')
    await userInput.trigger('keyup.enter')
    expect(lastURL()).toContain('user=old')

    http.get.mockResolvedValueOnce({ events: [{ ...oneEvent, user: 'new' }], total: 1 })
    await userInput.setValue('new')
    await userInput.trigger('keyup.enter')
    await flushPromises()
    expect(wrapper.text()).toContain('new')

    resolveOld({ events: [{ ...oneEvent, user: 'old' }], total: 99 })
    await flushPromises()
    expect(wrapper.text()).toContain('new')
    expect(wrapper.text()).not.toContain('old')
  })

  it('仅持有 Bearer token 时，审计 CSV 导出仍能通过鉴权', async () => {
    wrapper = mountView()
    await flushPromises()
    getToken.mockReturnValue('test-token')
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue({ ok: false, status: 401 })
    try {
      const exportButton = wrapper.findAll('button').find((button) => button.text().includes('导出 CSV'))
      expect(exportButton).toBeTruthy()
      await exportButton.trigger('click')
      await flushPromises()
      expect(fetchMock).toHaveBeenCalledWith(expect.stringContaining('/api/v1/audit/export'),
        expect.objectContaining({ headers: { Authorization: 'Bearer test-token' } }))
    } finally {
      fetchMock.mockRestore()
      getToken.mockReturnValue('')
    }
  })

  it('审计 CSV 返回 401 时清除登录态并通知应用', async () => {
    wrapper = mountView()
    await flushPromises()
    getToken.mockReturnValue('expired-token')
    const expired = vi.fn()
    window.addEventListener('auth-expired', expired)
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue({ ok: false, status: 401 })
    try {
      const exportButton = wrapper.findAll('button').find((button) => button.text().includes('导出 CSV'))
      await exportButton.trigger('click')
      await flushPromises()
      expect(setToken).toHaveBeenCalledWith('')
      expect(expired).toHaveBeenCalledOnce()
    } finally {
      fetchMock.mockRestore()
      window.removeEventListener('auth-expired', expired)
      getToken.mockReturnValue('')
    }
  })

  it('时间范围按毫秒时间戳进查询串', async () => {
    wrapper = mountView()
    await flushPromises()

    const from = new Date('2026-10-01T00:00:00Z')
    const to = new Date('2026-10-02T00:00:00Z')
    wrapper.vm.filters.range = [from, to]
    wrapper.vm.applyFilters()
    await flushPromises()

    expect(lastURL()).toContain(`from=${from.getTime()}`)
    expect(lastURL()).toContain(`to=${to.getTime()}`)
  })
})

// 「变更 ↔ 审计」关联的前端一半。
//
// 关联的价值在于两边能互相跳得过去，因此这里盯的是三件事：
//   ① 从资产变更历史带过来的 requestId 要真的进查询串（否则"跳过来却还在看全部"）；
//   ② 关掉它时要连地址栏一起清（否则刷新一次条件又回来，用户以为关不掉）；
//   ③ 没有关联 id 的行不给入口（点了查不到东西比没有按钮更糟）。
describe('AuditView 与资产变更的关联', () => {
  let wrapper

  const linked = { ...oneEvent, requestId: 'abc123' }
  const change = {
    id: 7, assetId: 3, typeKey: 'host', naturalKey: 'web-01', name: 'web-01',
    field: 'env', old: '', new: 'prod', source: 'manual', actor: 'admin', at: 1_800_000_000_000,
  }

  function mockData({ events = [linked], changes = [change], truncated = false } = {}) {
    http.get.mockImplementation(async (path) => {
      if (path.startsWith('/api/v1/audit/events')) return { events, total: events.length }
      if (path.startsWith('/api/v1/assets/changes')) return { requestId: 'abc123', changes, truncated }
      return {}
    })
  }

  async function clickChanges() {
    const button = wrapper.findAll('button').find((b) => b.text().includes('本次改动'))
    expect(button).toBeTruthy()
    await button.trigger('click')
    await flushPromises()
  }

  beforeEach(() => {
    vi.clearAllMocks()
    routeQuery = {}
    mockData()
  })

  afterEach(() => {
    if (wrapper) wrapper.unmount()
    wrapper = undefined
    document.body.innerHTML = ''
  })

  it('深链 ?requestId= 直接按这一次操作过滤，并显示成可关闭的条件', async () => {
    routeQuery = { requestId: 'abc123' }
    wrapper = mountView()
    await flushPromises()

    expect(lastURL()).toContain('requestId=abc123')
    expect(wrapper.text()).toContain('本次操作 abc123')
  })

  it('关掉条件时连同地址栏一起清掉', async () => {
    routeQuery = { requestId: 'abc123' }
    wrapper = mountView()
    await flushPromises()

    const tag = wrapper.findAll('.el-tag').find((t) => t.text().includes('本次操作'))
    expect(tag).toBeTruthy()
    await tag.find('.el-tag__close').trigger('click')
    await flushPromises()

    expect(routerReplace).toHaveBeenCalled()
    expect(lastURL()).not.toContain('requestId=')
  })

  it('「本次改动」按关联 id 取回这次操作动过的字段', async () => {
    wrapper = mountView()
    await flushPromises()
    await clickChanges()

    const called = http.get.mock.calls.map((c) => c[0]).find((u) => u.includes('/api/v1/assets/changes'))
    expect(called).toContain('requestId=abc123')
    // 抽屉里要把"哪个资产的哪个字段从什么变成什么"说清楚
    expect(document.body.textContent).toContain('env')
    expect(document.body.textContent).toContain('prod')
    expect(document.body.textContent).toContain('web-01')
  })

  it('改动被截断时显式说明，不让人以为"这次就动了这些"', async () => {
    mockData({ truncated: true })
    wrapper = mountView()
    await flushPromises()
    await clickChanges()

    expect(document.body.textContent).toContain('超过上限')
  })

  it('没有关联 id 的行不给入口（旧数据 / 采集上报查不到审计）', async () => {
    mockData({ events: [{ ...oneEvent }] })
    wrapper = mountView()
    await flushPromises()

    expect(wrapper.findAll('button').some((b) => b.text().includes('本次改动'))).toBe(false)
  })

  it('没有 assets:read 权限时不摆入口', async () => {
    const { principal } = useAuth()
    principal.loaded = true
    principal.permissions = ['audit:read']
    try {
      wrapper = mountView()
      await flushPromises()
      expect(wrapper.findAll('button').some((b) => b.text().includes('本次改动'))).toBe(false)
    } finally {
      principal.loaded = false
      principal.permissions = []
    }
  })
})
