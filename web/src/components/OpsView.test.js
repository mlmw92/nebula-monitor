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
  createOpsTask: vi.fn(),
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

  // 抽屉里的时间线只能用真实字段（创建 / 下发 / 完成）。
  // 编造中间步骤与耗时，会让"这条指令到底发出去没有"变成一个新的谜题。
  it('抽屉的执行时间线按真实字段分步，排队中明确说"等节点上报"', async () => {
    const base = Date.now() - 60_000
    listOpsTasks.mockResolvedValue({
      tasks: [succeededTask({ createdAt: base, deliveredAt: base + 3_000, doneAt: base + 3_081 })],
    })
    wrapper = mountView()
    await flushPromises()
    await clickButton('查看结果')

    const text = document.body.textContent
    expect(text).toContain('任务已创建并入队')
    expect(text).toContain('已下发至节点')
    expect(text).toContain('执行完成并回传结果')
    expect(text, '两个时间点之间的间隔应算出来（+3.00s）').toContain('+3.00s')
    expect(text, '基本信息里应有触发人与原因').toContain('排查 CPU 抖动')
  })

  it('排队中的任务在时间线里说明要等节点上报，而不是画一个假的下发时间', async () => {
    listOpsTasks.mockResolvedValue({ tasks: [succeededTask({ state: 'queued', data: {}, message: '' })] })
    wrapper = mountView()
    await flushPromises()
    await clickButton('查看结果')

    const text = document.body.textContent
    expect(text).toContain('等待节点下一次上报')
    expect(text, '没有下发时间就不该出现"已下发"这一步').not.toContain('已下发至节点')
  })

  // 指标卡的口径必须经得起追问：成功率的分母是**终态**，
  // 把"进行中"算进去（或算成失败）都会让人误判这批操作的质量。
  it('指标卡：成功率按终态算、平均耗时只算已结束且有时长的任务', async () => {
    listOpsTasks.mockResolvedValue({
      tasks: [
        succeededTask({ id: 'ops-1', state: 'succeeded', durationMs: 128 }),
        succeededTask({ id: 'ops-2', state: 'failed', durationMs: 200, data: {} }),
        succeededTask({ id: 'ops-3', state: 'running', durationMs: 0, data: {} }),
      ],
    })
    wrapper = mountView()
    await flushPromises()

    const text = wrapper.text()
    expect(text, '1 成功 / 1 失败 → 50%').toContain('50%')
    expect(text, '平均耗时 = (128+200)/2 = 164 ms').toContain('164 ms')
    expect(text, '进行中要单独说清楚，不能混进成功率').toContain('进行中 1')
    expect(text, '口径写在 hint 里，避免用户拿它当"全部历史"').toContain('基于 2 条已结束任务')
  })

  // 批量动作的第一道坎是"选中之后按钮才出现"——选中态与按钮必须同源，
  // 否则会出现"按钮在但没有选中项"或反过来"选中了却找不到入口"。
  it('勾选记录后出现批量条，重新执行会按原样逐条下发', async () => {
    listOpsTasks.mockResolvedValue({ tasks: [succeededTask()] })
    wrapper = mountView()
    await flushPromises()

    expect(wrapper.text(), '没选中时不该出现批量条').not.toContain('已选中')

    const ck = wrapper.find('.el-table__body input[type="checkbox"]')
    expect(ck.exists(), '表格应有多选列').toBe(true)
    await ck.setValue(true)
    await flushPromises()

    expect(wrapper.text()).toContain('已选中')
    expect(wrapper.text()).toContain('批量删除')

    const rerun = wrapper.findAll('button').find((b) => b.text().includes('重新执行'))
    await rerun.trigger('click')
    await flushPromises()

    const box = document.querySelector('.el-message-box')
    expect(box, '重新执行要先确认').toBeTruthy()
    expect(box.textContent).toContain('按原样重新下发 1 条任务')

    const confirm = [...box.querySelectorAll('button')].find((b) => b.textContent.includes('重新执行'))
    confirm.click()
    await flushPromises()

    const { createOpsTask } = await import('../api/ops')
    expect(createOpsTask).toHaveBeenCalledWith(
      expect.objectContaining({ node: 'web-01', kind: 'node.diagnostics' }),
    )
  })
})
