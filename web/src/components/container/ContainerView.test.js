// 「容器与工作负载」页的**查询时机**回归测试。
//
// 为什么要有这个文件：这一页的数据来自**异步下行任务**——下发指令 → 等 Agent 下一轮上报 → 回执，
// 通常 15 秒以上。因此"什么时候会下发"直接决定体验。用户报过：
// 「容器与工作负载不应该每次点击都刷新，应该记住上次刷新的状态以及刷新时间，需要刷新时手动刷新即可」。
//
// 这类缺陷不报错、不白屏、控制台也干净（只是悄悄多发了一次任务、让人白等 15 秒），
// 只能靠用例钉住——顺带把"上次刷新时间"这个**用户唯一能判断数据新鲜度的依据**钉住。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import http from '../../api/http'
import { createOpsTask, getOpsTask } from '../../api/ops'
import ContainerView from './ContainerView.vue'
import { resetContainerQueryCache } from './queryCache'

vi.mock('../../api/http', () => ({
  default: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() },
}))
vi.mock('../../api/ops', () => ({
  createOpsTask: vi.fn(),
  getOpsTask: vi.fn(),
  cancelOpsTasks: vi.fn(),
}))

// jsdom 未实现 ResizeObserver，Element Plus 的表格会用到它。
if (!globalThis.ResizeObserver) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

const CLUSTER_A = { node: 'web-01', instance: 'https://10.0.0.9:6443', name: 'k3s-dev', up: true }
const CLUSTER_B = { node: 'web-02', instance: 'https://10.0.0.10:6443', name: 'k8s-2', up: true }

const keyOf = (c) => c.node + '|' + c.instance

// 每次下发的任务 id 递增，用来在断言里区分"这是第几次下发的结果"。
let seq = 0
let wrappers = []

function mountView() {
  const w = mount(ContainerView, { global: { plugins: [ElementPlus] }, attachTo: document.body })
  wrappers.push(w)
  return w
}

// settle 等两拍：loadClusters → autoRun → createOpsTask → 立即轮询一次到终态。
async function settle(w) {
  await flushPromises()
  await flushPromises()
  await flushPromises()
}

const calls = () => createOpsTask.mock.calls.length

beforeEach(() => {
  vi.clearAllMocks()
  resetContainerQueryCache()
  seq = 0
  wrappers = []
  http.get.mockImplementation(async (path) => {
    if (String(path).includes('/container/k8s/clusters')) return { clusters: [CLUSTER_A, CLUSTER_B] }
    return {}
  })
  createOpsTask.mockImplementation(async () => {
    seq += 1
    return { task: { id: 't' + seq, state: 'queued' } }
  })
  // 轮询第一次就返回终态：用例关心的是"下发了几次"，不是轮询节奏。
  getOpsTask.mockImplementation(async (id) => ({
    task: {
      id,
      state: 'succeeded',
      json: JSON.stringify({
        columns: ['名称', '命名空间'],
        rows: [['row-for-' + id, 'default']],
        total: 1,
        truncated: false,
      }),
    },
  }))
})

afterEach(() => {
  for (const w of wrappers) w.unmount()
})

describe('ContainerView 容器与工作负载页', () => {
  it('首次进入自动下发一次，并显示上次刷新时间', async () => {
    const w = mountView()
    await settle(w)

    expect(calls()).toBe(1)
    expect(w.text()).toContain('row-for-t1')
    // 没有时间戳，用户就无法判断看到的是 5 秒前还是 5 小时前的数据
    expect(w.text()).toContain('上次刷新')
  })

  it('离开再回来沿用上次结果与时间，不重新下发', async () => {
    const first = mountView()
    await settle(first)
    expect(calls()).toBe(1)
    first.unmount()

    // 重新进入这一页（等价于路由切走再切回）：结果是异步任务等来的，
    // 不该因为"点进来"就再下发一轮、让人再等 15 秒。
    const again = mountView()
    await settle(again)

    expect(calls()).toBe(1)
    expect(again.text()).toContain('row-for-t1')
    expect(again.text()).toContain('上次刷新')
  })

  it('切 Tab：没有结果的 Tab 查一次，已有结果的 Tab 不重查', async () => {
    const w = mountView()
    await settle(w)
    expect(calls()).toBe(1)

    const tabs = w.findAll('.el-tabs__item')
    await tabs[1].trigger('click') // Pod
    await settle(w)
    expect(calls()).toBe(2)
    expect(w.text()).toContain('row-for-t2')

    await tabs[0].trigger('click') // 切回工作负载：已有结果，不该再下发
    await settle(w)
    expect(calls()).toBe(2)
    expect(w.text()).toContain('row-for-t1')
  })

  it('换集群：目标集群没有缓存才下发，切回来直接显示缓存', async () => {
    const w = mountView()
    await settle(w)
    expect(calls()).toBe(1)

    const select = w.findComponent({ name: 'ElSelect' })
    select.vm.$emit('update:modelValue', keyOf(CLUSTER_B))
    select.vm.$emit('change', keyOf(CLUSTER_B))
    await settle(w)
    expect(calls()).toBe(2)
    expect(w.text()).toContain('row-for-t2')

    select.vm.$emit('update:modelValue', keyOf(CLUSTER_A))
    select.vm.$emit('change', keyOf(CLUSTER_A))
    await settle(w)
    // 换集群是换视图、不是刷新：切回来应该直接看到上次的结果与时间
    expect(calls()).toBe(2)
    expect(w.text()).toContain('row-for-t1')
  })

  it('点「查询」= 手动刷新：会重新下发并更新时间', async () => {
    const w = mountView()
    await settle(w)
    expect(calls()).toBe(1)

    const button = w.findAll('button').find((b) => b.text().includes('查询'))
    expect(button).toBeTruthy()
    await button.trigger('click')
    await settle(w)

    expect(calls()).toBe(2)
    expect(w.text()).toContain('row-for-t2')
    expect(w.text()).toContain('上次刷新')
  })
})
