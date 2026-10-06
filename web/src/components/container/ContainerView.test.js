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
import { createOpsTask, getOpsTask, listOpsTasks, cancelOpsTasks } from '../../api/ops'
import { lookupAsset } from '../../api/asset'
import ContainerView from './ContainerView.vue'
import { resetContainerQueryCache } from './queryCache'

vi.mock('../../api/http', () => ({
  default: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() },
}))
vi.mock('../../api/ops', () => ({
  createOpsTask: vi.fn(),
  getOpsTask: vi.fn(),
  cancelOpsTasks: vi.fn(),
  listOpsTasks: vi.fn(),
}))
vi.mock('../../api/asset', () => ({
  lookupAsset: vi.fn(),
}))
// 跨页联动要用到路由：push 用于跳台账，replace 用于"只认领一次"地清掉深链参数。
const routerPush = vi.fn()
const routerReplace = vi.fn()
let routeQuery = {}
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: routerPush, replace: routerReplace }),
  useRoute: () => ({ query: routeQuery }),
}))
// 集中日志入口按 `logs:read` 门控（与 container:read 是两个权限点），
// 所以权限表要能逐例改：无权时按钮必须**不出现**，而不是点了撞 403。
const authState = vi.hoisted(() => ({ perms: ['logs:read'] }))
vi.mock('../../composables/useAuth', () => ({
  default: () => ({ can: (p) => authState.perms.includes(p) }),
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
  routeQuery = {}
  authState.perms = ['logs:read']
  http.get.mockImplementation(async (path) => {
    if (String(path).includes('/container/k8s/clusters')) return { clusters: [CLUSTER_A, CLUSTER_B] }
    return {}
  })
  createOpsTask.mockImplementation(async () => {
    seq += 1
    return { task: { id: 't' + seq, state: 'queued' } }
  })
  // 默认：服务端没有可认领的历史结果（用例要认领时自行覆盖）
  listOpsTasks.mockResolvedValue({ tasks: [] })
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

  // 结果本身持久化在服务端（ops_tasks.json 的 json 字段），前端缓存只在内存里。
  // 因此浏览器刷新（F5）之后本地空了，但服务端那份还在——先去认领它，
  // 而不是又下发一轮让用户再等一个上报周期。
  it('本地没有缓存时先认领服务端上次的回执，不下发', async () => {
    const doneAt = Date.now() - 5 * 60_000
    listOpsTasks.mockResolvedValue({
      tasks: [{
        id: 'old-1',
        node: CLUSTER_A.node,
        kind: 'container.workloads',
        state: 'succeeded',
        doneAt,
        params: { cluster: CLUSTER_A.name },
        json: JSON.stringify({ columns: ['名称'], rows: [['from-server']], total: 1, truncated: false }),
      }],
    })

    const w = mountView()
    await settle(w)

    expect(calls()).toBe(0) // 认领到了就不该再下发（不惊动 Agent）
    expect(w.text()).toContain('from-server')
    expect(w.text()).toContain('上次刷新')
    // 时间取任务的**完成**时刻，不是"现在"——否则一份 5 分钟前的快照会被说成刚查的
    expect(w.text()).toContain('5 分钟前')
  })

  it('历史结果的命名空间与当前条件不一致时不认领，退回正常下发', async () => {
    listOpsTasks.mockResolvedValue({
      tasks: [{
        id: 'old-1',
        node: CLUSTER_A.node,
        kind: 'container.workloads',
        state: 'succeeded',
        doneAt: Date.now(),
        params: { cluster: CLUSTER_A.name, namespace: 'kube-system' },
        json: JSON.stringify({ columns: ['名称'], rows: [['wrong-ns']], total: 1, truncated: false }),
      }],
    })

    const w = mountView()
    await settle(w)

    expect(calls()).toBe(1) // 条件对不上就不能顶替，"全部命名空间"的查询结果里不该混进某个命名空间的对象
    expect(w.text()).not.toContain('wrong-ns')
    expect(w.text()).toContain('row-for-t1')
  })

  // 排队中的任务可以撤回：这条路径此前引用了未定义的状态对象，
  // 点下去必抛 ReferenceError（控制台报错、按钮看着没反应），因此单独钉住。
  it('排队中的任务可撤回，且撤的是那条任务', async () => {
    getOpsTask.mockImplementation(async (id) => ({ task: { id, state: 'queued' } }))
    cancelOpsTasks.mockResolvedValue({})

    const w = mountView()
    await settle(w)

    const btn = w.findAll('button').find((b) => b.text().includes('撤回'))
    expect(btn).toBeTruthy()
    await btn.trigger('click')
    await settle(w)

    expect(cancelOpsTasks).toHaveBeenCalledWith({ ids: ['t1'] })
  })

  // Pod 日志：行上的「日志」必须下发 container.logs（带命名空间与 Pod 名），
  // 并把回执按原样展示——日志重排过就等于换了一份内容。
  it('Pod 行上的「日志」下发 container.logs 并按原样展示日志行', async () => {
    getOpsTask.mockImplementation(async (id) => {
      if (id === 't3') {
        return {
          task: {
            id,
            state: 'succeeded',
            json: JSON.stringify({
              kind: 'container.logs',
              columns: ['日志'],
              rows: [['line-1'], ['line-2']],
              total: 2,
              truncated: false,
              notice: 'Pod default/web-1 · 最近 200 行',
            }),
          },
        }
      }
      return {
        task: {
          id,
          state: 'succeeded',
          json: JSON.stringify({
            columns: ['命名空间', '名称'], rows: [['default', 'web-1']], total: 1, truncated: false,
          }),
        },
      }
    })

    const w = mountView()
    await settle(w)
    const tabs = w.findAll('.el-tabs__item')
    await tabs[1].trigger('click') // Pod
    await settle(w)

    const logBtn = w.findAll('button').find((b) => b.text() === '日志')
    expect(logBtn).toBeTruthy()
    await logBtn.trigger('click')
    await settle(w)

    const call = createOpsTask.mock.calls.at(-1)[0]
    expect(call.kind).toBe('container.logs')
    expect(call.params).toMatchObject({ cluster: CLUSTER_A.name, namespace: 'default', name: 'web-1' })

    // 抽屉走 teleport，断言落在 body 上
    expect(document.body.textContent).toContain('line-1')
    expect(document.body.textContent).toContain('line-2')
    expect(document.body.textContent).toContain('最近 200 行')
  })

  // 跨页联动：容器页的 Pod/工作负载行 → 台账资产详情。
  // 集群必须传 **apiserver 地址**（台账的自然键就是按它建的），传别名会查不到。
  it('Pod 行上的「台账」命中后跳到资产详情，未命中给明确提示', async () => {
    lookupAsset.mockResolvedValue({ ok: true, status: 200, body: { asset: { id: 42 } } })
    // Pod 列表的列序是「命名空间, 名称」（见 DETAIL_SPEC.pods）
    getOpsTask.mockImplementation(async (id) => ({
      task: {
        id,
        state: 'succeeded',
        json: JSON.stringify({
          columns: ['命名空间', '名称'], rows: [['default', 'web-1']], total: 1, truncated: false,
        }),
      },
    }))

    const w = mountView()
    await settle(w)
    await w.findAll('.el-tabs__item')[1].trigger('click') // Pod
    await settle(w)

    const btn = w.findAll('button').find((b) => b.text() === '台账')
    expect(btn).toBeTruthy()
    await btn.trigger('click')
    await settle(w)

    expect(lookupAsset).toHaveBeenCalledTimes(1)
    expect(lookupAsset.mock.calls[0][0]).toMatchObject({
      type: 'pod', cluster: CLUSTER_A.instance, namespace: 'default', name: 'web-1',
    })
    expect(routerPush).toHaveBeenCalledWith({ path: '/assets', query: { id: 42 } })

    // 未命中（404 是正常结果：清单上报有一个采集周期）→ 不跳转，只说清楚
    lookupAsset.mockResolvedValue({ ok: false, status: 404, body: { error: '资产不存在' } })
    routerPush.mockClear()
    await btn.trigger('click')
    await settle(w)
    expect(routerPush).not.toHaveBeenCalled()
    expect(document.body.textContent).toContain('尚未进入台账')
  })

  // 反向联动：从台账的容器资产点进来（?cluster=&namespace=&pod=），
  // 必须选中该集群、切到 Pod Tab、并打开那个 Pod 的日志抽屉；参数只认领一次。
  it('带 ?cluster/namespace/pod 进入时直接定位并打开日志抽屉', async () => {
    routeQuery = { cluster: CLUSTER_A.instance, namespace: 'default', pod: 'web-1' }
    const w = mountView()
    await settle(w)

    expect(routerReplace).toHaveBeenCalledWith({ path: '/container', query: {} })
    // 选中了集群（否则 currentCluster 为空，下面一切都不会发生）
    expect(w.findComponent({ name: 'ElSelect' }).props('modelValue')).toBe(keyOf(CLUSTER_A))
    // 列表与日志是两次独立下发，顺序不保证：按动作找，而不是取最后一次
    const logsCall = createOpsTask.mock.calls.map((c) => c[0]).find((c) => c.kind === 'container.logs')
    expect(logsCall).toBeTruthy()
    expect(logsCall.params).toMatchObject({ namespace: 'default', name: 'web-1' })
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

  // 集中日志入口：Pod 行上的「检索日志」跳 /logs?pods=<命名空间>|<Pod>。
  //
  // 它和「日志」是**两条数据源不同的路径**（那条是按需拉取，这条是集中日志），
  // 所以两件事都要钉住：① 参数形态与服务端 `pods=` 完全相同（前端不拼自然键、不猜节点）；
  // ② 没有 logs:read 时整个入口不出现——点了只会撞 403 的按钮不该摆在那里。
  function podsPayload() {
    return async (id) => ({
      task: {
        id,
        state: 'succeeded',
        json: JSON.stringify({
          columns: ['命名空间', '名称'], rows: [['nebula-demo', 'web-1']], total: 1, truncated: false,
        }),
      },
    })
  }

  it('Pod 行「检索日志」跳到集中日志并按容器身份收窄', async () => {
    getOpsTask.mockImplementation(podsPayload())

    const w = mountView()
    await settle(w)
    await w.findAll('.el-tabs__item')[1].trigger('click') // Pod
    await settle(w)

    const btn = w.findAll('button').find((b) => b.text().includes('检索日志'))
    expect(btn).toBeTruthy()
    await btn.trigger('click')

    // 只带 pods：节点在这里无从得知（猜错就是一个空结果），时间窗用检索页自己的默认
    expect(routerPush).toHaveBeenCalledWith({ path: '/logs', query: { pods: 'nebula-demo|web-1' } })
  })

  it('日志抽屉里也能直接跳集中日志（从台账反向进来时正看着这一屏）', async () => {
    getOpsTask.mockImplementation(podsPayload())
    routeQuery = { cluster: CLUSTER_A.instance, namespace: 'nebula-demo', pod: 'web-1' }

    const w = mountView()
    await settle(w)

    const btn = [...document.body.querySelectorAll('button')].find((b) => b.textContent.includes('在集中日志中检索'))
    expect(btn).toBeTruthy()
    btn.click()
    await settle(w)

    expect(routerPush).toHaveBeenCalledWith({ path: '/logs', query: { pods: 'nebula-demo|web-1' } })
  })

  it('没有 logs:read 时不出现集中日志入口（按需拉取仍可用）', async () => {
    authState.perms = []
    getOpsTask.mockImplementation(podsPayload())

    const w = mountView()
    await settle(w)
    await w.findAll('.el-tabs__item')[1].trigger('click') // Pod
    await settle(w)

    expect(w.text()).toContain('web-1') // Pod 列表照常
    expect(w.findAll('button').some((b) => b.text().includes('检索日志'))).toBe(false)
    // 「日志」（按需拉取，只要 container:read）不受影响
    expect(w.findAll('button').some((b) => b.text() === '日志')).toBe(true)
  })
})
