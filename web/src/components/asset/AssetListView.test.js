// 资产台账「关系图」（拓扑）回归测试。
//
// 为什么要有这个文件：关系图的三种失败都不报错、也不白屏——
// ① 点开时请求的跳数不对（用户以为看的是 2 跳，其实只有 1 跳）；
// ② 结果被上限截断却没说（让人以为"关系就这么多"，从而漏掉真实影响面）；
// ③ 点节点没有重新以它为中心（图看着能点，点了没反应）。
// 只能靠断言钉住。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import {
  listAssets,
  getAssetSummary,
  getAsset,
  getAssetHistory,
  getAssetLinks,
  getAssetTopology,
} from '../../api/asset'
import AssetListView from './AssetListView.vue'

vi.mock('../../api/asset', () => ({
  listAssets: vi.fn(),
  getAssetSummary: vi.fn(),
  getAsset: vi.fn(),
  getAssetHistory: vi.fn(),
  getAssetLinks: vi.fn(),
  getAssetTopology: vi.fn(),
  createAssetLink: vi.fn(),
  deleteAssetLink: vi.fn(),
  restoreAssetLink: vi.fn(),
  createAsset: vi.fn(),
  updateAsset: vi.fn(),
  listInspectBaselines: vi.fn(),
  setAssetBaseline: vi.fn(),
  clearAssetBaseline: vi.fn(),
  ignoreAsset: vi.fn(),
  restoreAsset: vi.fn(),
  purgeAsset: vi.fn(),
  updateAssetLabels: vi.fn(),
  batchAssets: vi.fn(),
  exportAssets: vi.fn(),
}))

vi.mock('../../composables/useAuth', () => ({
  useAuth: () => ({ can: () => true }),
}))

// 记录 echarts 实例：既要断言喂给它的数据，也要取回点击回调
let chart = null
vi.mock('echarts', () => ({
  init: vi.fn(() => {
    chart = { setOption: vi.fn(), on: vi.fn(), off: vi.fn(), dispose: vi.fn(), resize: vi.fn() }
    return chart
  }),
}))

const routerPush = vi.fn()
const routerReplace = vi.fn()
let routeQuery = {}
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: routerPush, replace: routerReplace }),
  useRoute: () => ({ query: routeQuery }),
}))

// jsdom 未实现 ResizeObserver，Element Plus 的表格/弹窗会用到它。
if (!globalThis.ResizeObserver) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

// 抽屉与弹窗改成本地渲染（默认会 teleport 到 body）：断言落在 wrapper 上更好写。
const ModelStub = {
  props: ['modelValue', 'title'],
  template: '<div v-if="modelValue" class="model-stub"><slot /></div>',
}

const HOST = {
  id: 1, typeKey: 'host', naturalKey: 'web-01', name: 'web-01', node: 'web-01',
  status: 'online', owner: '', ignored: false, values: {}, attrs: [], labels: {}, links: [],
}

const TOPOLOGY = {
  root: 'host|web-01',
  depth: 2,
  truncated: false,
  nodes: [
    { key: 'host|web-01', id: 1, typeKey: 'host', typeTitle: '主机', naturalKey: 'web-01', name: 'web-01', node: 'web-01', status: 'online', root: true, depth: 0 },
    { key: 'middleware-instance|redis:127.0.0.1:6379', id: 2, typeKey: 'middleware-instance', typeTitle: '中间件实例', naturalKey: 'redis:127.0.0.1:6379', name: 'dev-redis', node: 'web-01', status: 'missing', depth: 1 },
  ],
  edges: [
    { from: 'middleware-instance|redis:127.0.0.1:6379', to: 'host|web-01', kind: 'runs_on', source: 'discovery' },
    { from: 'host|web-01', to: 'middleware-instance|redis:127.0.0.1:6379', kind: 'depends_on', source: 'manual' },
  ],
}

let wrappers = []

function mountView() {
  const w = mount(AssetListView, {
    global: {
      plugins: [ElementPlus],
      stubs: { 'el-dialog': ModelStub, 'el-drawer': ModelStub },
    },
    attachTo: document.body,
  })
  wrappers.push(w)
  return w
}

// openLinksTab 打开详情抽屉并切到「关联关系」页签。
async function openLinksTab(w) {
  await flushPromises()
  const tab = w.findAll('.el-tabs__item').find((t) => t.text().includes('关联关系'))
  expect(tab).toBeTruthy()
  await tab.trigger('click')
  await flushPromises()
}

// openHistoryTab 打开详情抽屉并切到「变更历史」页签（变更与审计的关联入口在这里）。
async function openHistoryTab(w) {
  await flushPromises()
  const tab = w.findAll('.el-tabs__item').find((t) => t.text().includes('变更历史'))
  expect(tab).toBeTruthy()
  await tab.trigger('click')
  await flushPromises()
}

beforeEach(() => {
  vi.clearAllMocks()
  wrappers = []
  chart = null
  routeQuery = { id: '1' } // 用深链直接打开详情，省掉表格交互
  listAssets.mockResolvedValue({ assets: [HOST], total: 1 })
  getAssetSummary.mockResolvedValue({ total: 1, missing: 0, noOwner: 1, conflict: 0, changed: 0 })
  getAsset.mockResolvedValue(HOST)
  getAssetHistory.mockResolvedValue({ changes: [] })
  getAssetLinks.mockResolvedValue({ links: [], suppressed: [] })
  getAssetTopology.mockResolvedValue(TOPOLOGY)
})

afterEach(() => {
  for (const w of wrappers) w.unmount()
  document.body.innerHTML = ''
})

describe('AssetListView 关系图', () => {
  it('点「关系图」按当前跳数请求邻域，并把节点与边交给 echarts', async () => {
    const w = mountView()
    await openLinksTab(w)

    const btn = w.findAll('button').find((b) => b.text().includes('关系图'))
    expect(btn).toBeTruthy()
    await btn.trigger('click')
    await flushPromises()

    // 默认 2 跳：请求参数必须是用户看到的那一档
    expect(getAssetTopology).toHaveBeenCalledWith(1, { depth: 2 })
    expect(chart).toBeTruthy()
    const option = chart.setOption.mock.calls.at(-1)[0]
    const series = option.series[0]
    expect(series.type).toBe('graph')
    expect(series.layout).toBe('force')
    expect(series.data.length).toBe(2)
    expect(series.links.length).toBe(2)
    // 中心画得更大，一眼看出"从谁出发"
    const root = series.data.find((n) => n.id === 'host|web-01')
    const leaf = series.data.find((n) => n.id.startsWith('middleware-instance'))
    expect(root.symbolSize).toBeGreaterThan(leaf.symbolSize)
    // 失联资产要标出来（红圈），不能和"上报正常"长得一样
    expect(leaf.itemStyle.borderWidth).toBeGreaterThan(0)
    // 人工维护的边用虚线，采集发现的用实线
    const manual = series.links.find((l) => l.raw.source === 'manual')
    const discovered = series.links.find((l) => l.raw.source === 'discovery')
    expect(manual.lineStyle.type).toBe('dashed')
    expect(discovered.lineStyle.type).toBe('solid')
  })

  it('结果被截断时必须显式提示，不能让人以为关系就这么多', async () => {
    getAssetTopology.mockResolvedValue({ ...TOPOLOGY, truncated: true })
    const w = mountView()
    await openLinksTab(w)
    await w.findAll('button').find((b) => b.text().includes('关系图')).trigger('click')
    await flushPromises()

    expect(w.text()).toContain('节点数达到上限')
  })

  it('点节点即以它为中心重新展开', async () => {
    const w = mountView()
    await openLinksTab(w)
    await w.findAll('button').find((b) => b.text().includes('关系图')).trigger('click')
    await flushPromises()

    const handler = chart.on.mock.calls.find((c) => c[0] === 'click')[1]
    expect(handler).toBeTruthy()

    // 点中心自己：不该重新请求（已经是以它为中心）
    handler({ data: { raw: { id: 1, root: true } } })
    await flushPromises()
    expect(getAssetTopology).toHaveBeenCalledTimes(1)

    // 点叶子节点：以它为中心重查
    handler({ data: { raw: { id: 2, root: false } } })
    await flushPromises()
    expect(getAssetTopology).toHaveBeenCalledTimes(2)
    expect(getAssetTopology).toHaveBeenLastCalledWith(2, { depth: 2 })
  })

  it('切换跳数会按新跳数重新请求', async () => {
    const w = mountView()
    await openLinksTab(w)
    await w.findAll('button').find((b) => b.text().includes('关系图')).trigger('click')
    await flushPromises()

    const radio = w.findAll('.el-radio-button').find((r) => r.text().includes('3 跳'))
    expect(radio).toBeTruthy()
    await radio.find('input, label, span').trigger('click')
    await flushPromises()
    expect(getAssetTopology).toHaveBeenLastCalledWith(1, { depth: 3 })
  })

  it('加载失败要给出错误而不是空图', async () => {
    getAssetTopology.mockRejectedValue(new Error('查询资产关系图失败'))
    const w = mountView()
    await openLinksTab(w)
    await w.findAll('button').find((b) => b.text().includes('关系图')).trigger('click')
    await flushPromises()

    expect(w.text()).toContain('查询资产关系图失败')
  })
})

// 变更记录 → 审计的入口（「变更 ↔ 审计」关联的前向一半）。
//
// 盯两件事：有关联 id 的记录要能跳到审计页、并且把 id 带上（否则跳过去还在看全部审计）；
// 没有关联 id 的记录（采集侧写的、升级前的历史）不给入口——按钮点了查不到东西更糟。
describe('AssetListView 变更历史与审计的关联', () => {
  const linkedRecord = {
    id: 7, requestId: 'abc123', field: 'env', old: '', new: 'prod',
    source: 'manual', actor: 'admin', kind: 'update', at: 1_800_000_000_000,
  }
  const collectedRecord = {
    id: 8, field: 'cpuCores', old: '4', new: '8',
    source: 'discovery', kind: 'update', at: 1_800_000_001_000,
  }

  it('带关联 id 的记录给出「查看对应审计」，点了带上 requestId 跳审计页', async () => {
    getAssetHistory.mockResolvedValue({ records: [linkedRecord, collectedRecord] })
    const w = mountView()
    await openHistoryTab(w)

    const buttons = w.findAll('button').filter((b) => b.text().includes('查看对应审计'))
    // 两条记录里只有带 requestId 的那条有入口
    expect(buttons.length).toBe(1)
    await buttons[0].trigger('click')
    expect(routerPush).toHaveBeenCalledWith({ path: '/audit', query: { requestId: 'abc123' } })
  })

  it('采集侧的记录没有入口（它没有对应的接口调用）', async () => {
    getAssetHistory.mockResolvedValue({ records: [collectedRecord] })
    const w = mountView()
    await openHistoryTab(w)

    expect(w.findAll('button').some((b) => b.text().includes('查看对应审计'))).toBe(false)
  })
})
