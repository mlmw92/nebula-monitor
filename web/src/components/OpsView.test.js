// 「节点操作」页的交互回归测试。
//
// 为什么要有这个文件：这一页是"下发指令 → 等回执 → 看输出"的闭环，其中「查看结果」是
// **唯一能看到 Agent 到底输出了什么**的入口。它曾经点不动——弹窗里的输出用了 v-for
// 作用域外的变量，组件一渲染就抛错，表现是"点了没反应"（连报错都只在控制台里）。
// 那种缺陷不会让任何测试变红、也不会让页面白屏，只能靠"真的点一下"发现。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import http from '../api/http'
import { listOpsActions, listOpsTasks } from '../api/ops'
import OpsView from './OpsView.vue'

vi.mock('../api/http', () => ({
  default: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() },
}))
vi.mock('../api/ops', () => ({
  listOpsTasks: vi.fn(),
  listOpsActions: vi.fn(),
  createOpsBatch: vi.fn(),
  cancelOpsTasks: vi.fn(),
  deleteOpsTask: vi.fn(),
}))

// jsdom 未实现 ResizeObserver，Element Plus 的表格与弹窗会用到它。
if (!globalThis.ResizeObserver) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

// 一条"已经跑完、有输出"的任务：这是用户会去点「查看结果」的场景。
function succeededTask(overrides = {}) {
  return {
    id: 'ops-1',
    node: 'web-01',
    kind: 'node.diagnostics',
    title: '节点诊断包',
    state: 'succeeded',
    message: '已采集完成',
    durationMs: 128,
    operator: 'admin',
    reason: '排查 CPU 抖动',
    createdAt: Date.now() - 60_000,
    data: {
      '系统信息': 'Linux web-01 5.15.0-88-generic x86_64',
      '磁盘占用': '/dev/vda1  40G  12G  26G  32% /',
    },
    ...overrides,
  }
}

function mountView() {
  return mount(OpsView, {
    global: { plugins: [ElementPlus] },
    attachTo: document.body,
  })
}

describe('OpsView 节点操作页', () => {
  let wrapper

  beforeEach(() => {
    vi.clearAllMocks()
    listOpsTasks.mockResolvedValue({ tasks: [succeededTask()] })
    listOpsActions.mockResolvedValue({ actions: [] })
    http.get.mockImplementation(async (path) => {
      if (path === '/api/v1/nodes') return { nodes: [] }
      if (path === '/api/v1/groups') return { groups: [] }
      throw new Error(`未知路径: ${path}`)
    })
  })

  afterEach(() => {
    if (wrapper) wrapper.unmount()
    wrapper = undefined
    document.body.innerHTML = ''
  })

  async function clickButton(text) {
    const btn = wrapper.findAll('button').find((b) => b.text().includes(text))
    expect(btn, `页面上应有「${text}」按钮`).toBeTruthy()
    await btn.trigger('click')
    await flushPromises()
  }

  it('点击「查看结果」应弹出结果弹窗并展示 Agent 输出', async () => {
    wrapper = mountView()
    await flushPromises()

    await clickButton('查看结果')

    // 弹窗挂在 body 上（Teleport），因此查 body 而不是 wrapper
    const text = document.body.textContent
    expect(text, '弹窗应带上动作名作为标题').toContain('执行结果 · 节点诊断包')
    expect(text, '应展示回执 message').toContain('已采集完成')
    expect(text, '应展示输出分节名').toContain('系统信息')
    expect(text, '应展示输出原文——这正是"点开结果"的唯一用处').toContain('Linux web-01 5.15.0-88-generic x86_64')
    expect(text, '应展示第二段输出').toContain('/dev/vda1  40G  12G  26G  32% /')
  })

  it('任务还没有输出时给出说明而不是空白弹窗', async () => {
    listOpsTasks.mockResolvedValue({ tasks: [succeededTask({ state: 'queued', data: {}, message: '' })] })
    wrapper = mountView()
    await flushPromises()

    await clickButton('查看结果')

    expect(document.body.textContent).toContain('暂无输出')
  })

  it('排队中的任务可取消、终态任务可删除，已下发的两者都不显示', async () => {
    listOpsTasks.mockResolvedValue({
      tasks: [
        succeededTask({ id: 'ops-1', state: 'queued' }),
        succeededTask({ id: 'ops-2', state: 'succeeded' }),
        succeededTask({ id: 'ops-3', state: 'delivered' }),
      ],
    })
    wrapper = mountView()
    await flushPromises()

    const buttons = wrapper.findAll('button').map((b) => b.text())
    // 排队中：可取消；已下发：撤不回来，不能给「取消」按钮（给了会让人以为撤得回来）
    expect(buttons.filter((t) => t.includes('取消')).length).toBe(1)
    // 终态（成功）可删除，排队中与已下发的不可
    expect(buttons.filter((t) => t.includes('删除')).length).toBe(1)
  })
})
