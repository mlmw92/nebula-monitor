// 审计页的时间范围与分页（批次 18 前端）。
//
// 为什么要有这个文件：审计入库后不再被 2000 条截断，"能查到多久以前"要靠时间范围与翻页才够得着。
// 两条口径错了都不会报错，只会让人看到一份**看起来完整的假列表**：
//   ① 改筛选条件若不回到第 1 页，第 3 页 + 新条件会查出空列表 → 看起来像"没有记录"；
//   ② 时间范围若不进查询串，界面选了"近 7 天"却仍在查全部 → 看起来像"这段时间没操作"。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import http from '../api/http'
import AuditView from './AuditView.vue'

vi.mock('../api/http', () => ({
  default: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() },
  getToken: vi.fn(() => ''),
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
