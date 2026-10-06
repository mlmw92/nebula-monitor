// 集中日志页的**容器日志（Pod 日志）**回归测试。
//
// 为什么单独一组：容器日志带的是"另一套归属"——身份来自 Agent 从 kubelet 路径解析出的
// namespace/pod/container（服务端校验后落库），资产是**那个 Pod**，而不是"某台机器上的某类来源"。
// 两套归属一旦串台，症状是"日志被标到错误的资产上"；而"只看"按钮如果没把容器过滤拼进请求，
// 症状是"点了没反应但也没报错"。两者都只能靠断言钉住。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import http from '../api/http'
import LogsView from './LogsView.vue'

vi.mock('../api/http', () => ({
  default: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() },
  getToken: vi.fn(() => ''),
}))

const routerPush = vi.fn()
// 深链用例要能逐例给 route.query，所以不能写死空对象。
// vi.mock 的工厂会提升到文件顶部，引用外层变量必须走 vi.hoisted，否则读到的是 TDZ 里的绑定。
const routeState = vi.hoisted(() => ({ query: {} }))
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: routerPush, replace: vi.fn() }),
  useRoute: () => ({ query: routeState.query }),
}))
vi.mock('../composables/useAuth', () => ({
  default: () => ({ can: () => false }),
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

function logsQueries() {
  return http.get.mock.calls.map((c) => String(c[0])).filter((p) => p.startsWith('/api/v1/logs?'))
}

beforeEach(() => {
  vi.clearAllMocks()
  wrappers = []
  routeState.query = {}
  http.get.mockImplementation(async (path) => {
    if (String(path).includes('/api/v1/logs/fields')) return { fields: {} }
    if (String(path).includes('/api/v1/logs?')) {
      return {
        lines: [
          {
            ts: Date.now(), node: 'web-01', source: 'podlog', pattern: 'err',
            text: 'error: pod boom',
            origin: { namespace: 'nebula-demo', pod: 'web-1', container: 'nginx' },
          },
          {
            ts: Date.now() - 1000, node: 'web-01', source: 'applog', pattern: 'err',
            text: 'plain file line',
          },
        ],
        truncated: false,
        // 主机来源那套映射（普通文件日志用）
        assets: {
          'applog|web-01': [{ id: 7, typeKey: 'host', typeTitle: '主机', naturalKey: 'web-01', name: 'web-01', node: 'web-01' }],
        },
        // 容器身份那套映射（容器日志用），键 `<namespace>|<pod>`
        podAssets: {
          'nebula-demo|web-1': [
            { id: 11, typeKey: 'pod', typeTitle: 'Pod', naturalKey: 'k8s/nebula-demo/pod/web-1', name: 'web-1', node: 'web-01' },
          ],
        },
      }
    }
    if (String(path).includes('/api/v1/nodes')) {
      return { nodes: [{ hostname: 'web-01', logSources: ['podlog', 'applog'] }] }
    }
    return {}
  })
})

afterEach(() => {
  for (const w of wrappers) w.unmount()
  document.body.innerHTML = ''
})

describe('LogsView 容器日志（Pod 日志）', () => {
  it('带容器身份的行展示命名空间/Pod 与容器，并标到 Pod 资产（不走主机映射）', async () => {
    const w = mountView()
    await flushPromises()

    const html = w.html()
    expect(html).toContain('nebula-demo/web-1')
    expect(html).toContain('nginx')
    // 台账里没有对应 Pod 时也要显示身份（身份来自日志本身，不依赖台账）
    expect(html).toContain('pod-line')

    // 点 Pod 资产标签 → 跳台账详情（带 id）
    const podChip = w.findAll('.asset-chip').find((c) => c.text().includes('web-1'))
    expect(podChip).toBeTruthy()
    await podChip.trigger('click')
    expect(routerPush).toHaveBeenCalledWith({ path: '/assets', query: { id: '11' } })
  })

  it('「只看」按容器身份收窄：拼出 pods 参数并可删除过滤条件', async () => {
    const w = mountView()
    await flushPromises()

    const only = w.findAll('button').filter((b) => b.text().includes('只看'))
    expect(only.length).toBeGreaterThan(0)
    await only[0].trigger('click')
    await flushPromises()

    const last = logsQueries().at(-1)
    expect(last).toContain('pods=nebula-demo%7Cweb-1')

    // 过滤条件必须看得见、删得掉（否则"日志怎么少了"会变成下一个问题）
    const chip = w.findAll('.field-tag').find((t) => t.text().includes('nebula-demo/web-1'))
    expect(chip).toBeTruthy()
    await chip.find('.el-tag__close').trigger('click')
    await flushPromises()

    // 与字段过滤同一行为：删除条件**不自动重查**（用户可能还要再删几个），
    // 因此这里显式再点一次「查询」，断言条件确实被去掉了。
    const button = w.findAll('button').find((b) => b.text().includes('查询'))
    await button.trigger('click')
    await flushPromises()

    const after = logsQueries().at(-1)
    expect(after).not.toContain('pods=')
    // 节点/来源等在「只看」时加上的条件仍然保留（删的是容器条件本身）
    expect(after).toContain('nodes=web-01')
  })

  it('普通文件日志不带容器身份，仍走主机来源映射', async () => {
    const w = mountView()
    await flushPromises()
    // 主机资产标签只应挂在 applog 那一行：容器行的资产是 Pod
    const chips = w.findAll('.asset-chip')
    expect(chips.length).toBe(2) // 一行一个（pod 行 → Pod 资产，applog 行 → 主机资产）
    expect(chips.some((c) => c.text().includes('web-01'))).toBe(true)
  })

  // 深链 `?pods=<命名空间>|<Pod>`：容器页（与 Pod 资产）跳进来时带的就是这个。
  // 认领失败的症状不是报错，而是"条件没了却照样出结果"——用户会以为这就是该 Pod 的全部日志。
  it('深链里的 pods 条件被认领：显示为可删除的容器条件并拼进查询', async () => {
    routeState.query = { pods: 'nebula-demo|web-1' }
    const w = mountView()
    await flushPromises()

    const chip = w.findAll('.field-tag').find((t) => t.text().includes('nebula-demo/web-1'))
    expect(chip).toBeTruthy()
    expect(logsQueries().at(-1)).toContain('pods=nebula-demo%7Cweb-1')
  })

  // 形态校验与服务端 parseLogPodFilters 同一条规则：不合法的项丢掉，
  // 而不是让它去撞服务端的 400——那会把**整个查询**打成失败，用户看到的是一片报错。
  it('深链里形态不合法的容器条件被丢掉，不让它把整个查询打成失败', async () => {
    routeState.query = { pods: ['nebula-demo|web-1', 'Bad Name|x', 'nopipe', '|empty', 'ns|'] }
    const w = mountView()
    await flushPromises()

    const last = logsQueries().at(-1)
    expect(last).toContain('pods=nebula-demo%7Cweb-1')
    for (const bad of ['Bad', 'nopipe', 'empty']) {
      expect(last).not.toContain(bad)
    }
    // 生效中的条件只有那一条合法的
    const podChips = w.findAll('.field-tag').filter((t) => t.text().includes('容器'))
    expect(podChips.length).toBe(1)
  })

  // 服务端 MaxLogPodFilters 是 8：多了会被 400 掉，所以超出部分先截断，保住其余条件照常生效。
  it('深链带的容器条件超过服务端上限（8）时截断', async () => {
    routeState.query = { pods: Array.from({ length: 10 }, (_, i) => `ns|pod-${i}`) }
    const w = mountView()
    await flushPromises()

    const q = logsQueries().at(-1)
    expect((q.match(/pods=/g) || []).length).toBe(8)
  })
})
