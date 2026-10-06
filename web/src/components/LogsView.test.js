// 集中日志页的**结构化字段检索**回归测试。
//
// 为什么要有这个文件：字段过滤是本页唯一的"精确"筛法（关键词会在别的字段的值里误命中），
// 而它的三种失败都不报错：参数没拼进请求、条件形态不对被服务端 400、
// 候选字段列的是"可能存在的名字"（用户按它筛永远查不到）。只能靠断言钉住。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import http from '../api/http'
import LogsView from './LogsView.vue'

vi.mock('../api/http', () => ({
  default: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() },
  getToken: vi.fn(() => ''),
}))

const routerReplace = vi.fn()
let routeQuery = {}
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn(), replace: routerReplace }),
  useRoute: () => ({ query: routeQuery }),
}))

if (!globalThis.ResizeObserver) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

let wrappers = []

function mountView() {
  const w = mount(LogsView, { global: { plugins: [ElementPlus] }, attachTo: document.body })
  wrappers.push(w)
  return w
}

// logsCalls 返回对 /api/v1/logs 的查询串（排除字段候选接口）。
function logsQueries() {
  return http.get.mock.calls
    .map((c) => String(c[0]))
    .filter((p) => p.startsWith('/api/v1/logs?'))
}

beforeEach(() => {
  vi.clearAllMocks()
  wrappers = []
  routeQuery = {}
  http.get.mockImplementation(async (path) => {
    if (String(path).includes('/api/v1/logs/fields')) {
      return { fields: { applog: ['level', 'status'] } }
    }
    if (String(path).includes('/api/v1/logs?')) {
      return {
        lines: [{ ts: Date.now(), node: 'web-01', source: 'applog', text: '{"level":"error"}', fields: { level: 'error', status: '500' } }],
        truncated: false,
      }
    }
    if (String(path).includes('/api/v1/nodes')) {
      return { nodes: [{ hostname: 'web-01', logSources: ['applog'] }] }
    }
    return {}
  })
})

afterEach(() => {
  for (const w of wrappers) w.unmount()
  document.body.innerHTML = ''
})

describe('LogsView 结构化字段检索', () => {
  it('字段条件会拼进查询，形态不对则拒绝并提示', async () => {
    const w = mountView()
    await flushPromises()

    const input = w.findAll('input').find((i) => i.element.placeholder.includes('字段过滤'))
    expect(input).toBeTruthy()

    // 形态不对：不加入条件（否则服务端 400，用户只看到一个报错）
    await input.setValue('status500')
    await input.trigger('keyup.enter')
    await flushPromises()
    expect(logsQueries().some((q) => q.includes('field='))).toBe(false)

    // 正确形态：加入条件，下一次查询带上 field=status:500
    await input.setValue('status:500')
    await input.trigger('keyup.enter')
    await flushPromises()
    const button = w.findAll('button').find((b) => b.text().includes('查询'))
    await button.trigger('click')
    await flushPromises()

    const last = logsQueries().at(-1)
    expect(last).toContain('field=status%3A500')
  })

  it('同一个字段只允许一个值：第二次输入被拒绝', async () => {
    const w = mountView()
    await flushPromises()
    const input = w.findAll('input').find((i) => i.element.placeholder.includes('字段过滤'))

    await input.setValue('status:500')
    await input.trigger('keyup.enter')
    await input.setValue('status:404')
    await input.trigger('keyup.enter')
    await flushPromises()

    const tags = w.findAll('.field-tag')
    expect(tags.length).toBe(1)
    expect(tags[0].text()).toBe('status:500')
  })

  it('候选字段只列服务端见过的名字，点击后填入输入框', async () => {
    const w = mountView()
    await flushPromises()

    expect(w.text()).toContain('已知字段')
    const pick = w.findAll('button').find((b) => b.text() === 'status')
    expect(pick).toBeTruthy()
    await pick.trigger('click')
    await flushPromises()

    const input = w.findAll('input').find((i) => i.element.placeholder.includes('字段过滤'))
    expect(input.element.value).toBe('status:')
  })

  it('结果里展示结构化字段，且深链带来的 field 条件被采纳', async () => {
    routeQuery = { field: ['level:error', 'status:500'] }
    const w = mountView()
    await flushPromises()

    expect(w.findAll('.field-tag').length).toBe(2)
    const last = logsQueries().at(-1)
    expect(last).toContain('field=level%3Aerror')
    expect(last).toContain('field=status%3A500')
    // 字段贴在日志原文上方
    expect(w.findAll('.field-chip').length).toBe(2)
  })
})
