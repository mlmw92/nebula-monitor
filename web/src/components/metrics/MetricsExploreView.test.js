// MetricsExploreView 跨节点指标浏览 / 导出的契约回归。
// 受限用户（节点分组资源范围）下，指标趋势查询与 CSV 导出都不得携带 node= 参数，
// 否则可能通过参数形式访问范围外节点。安全边界在服务端（服务端独立校验并拒绝），
// 本测试只锁定前端的请求形态与 403 提示呈现，不把前端判定当作授权依据。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount } from '@vue/test-utils'

// vi.mock 工厂会被提升到 import 之前，故用 vi.hoisted 共享这些 mock 句柄。
const { httpGet, exportMetricCSV, metricCatalog, metricActive } = vi.hoisted(() => ({
  httpGet: vi.fn(),
  exportMetricCSV: vi.fn(),
  metricCatalog: vi.fn(),
  metricActive: vi.fn(),
}))

// 组件同时使用默认导出 http（http.exportMetricCSV）与具名导出 get / metricCatalog / metricActive。
vi.mock('../../api/http', () => ({
  default: { get: httpGet, exportMetricCSV },
  get: httpGet,
  metricCatalog,
  metricActive,
}))

// 图表库与仪表盘 composable 在本用例范围内无关，静默 stub 掉，避免真实 ECharts 初始化。
vi.mock('../../charts/echarts', () => ({
  initChart: vi.fn(() => ({
    clear: vi.fn(),
    setOption: vi.fn(),
    resize: vi.fn(),
    dispose: vi.fn(),
    isDisposed: () => false,
  })),
  monitorOption: vi.fn(() => ({})),
  COLORS: { cyan: '#22d3ee' },
}))

vi.mock('../../composables/useDashboards', () => {
  const dash = () => ({ state: {}, load: vi.fn(async () => []), create: vi.fn(), update: vi.fn(), remove: vi.fn() })
  return { useDashboards: dash, default: dash }
})

vi.mock('element-plus', () => ({
  ElMessage: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
}))

import { ElMessage } from 'element-plus'
import MetricsExploreView from './MetricsExploreView.vue'

const flush = () => new Promise((resolve) => setTimeout(resolve, 0))

// el-tree 需要能手动派发 node-click；el-button 需渲染默认插槽才能按文案定位「导出 CSV」。
const TreeStub = { name: 'ElTree', props: ['data'], template: '<div class="tree-stub" />' }
const ButtonStub = { name: 'ElButton', template: '<button class="el-button"><slot /></button>' }
const InputStub = { name: 'ElInput', template: '<div class="kw-input" />' }

const stubs = { 'el-tree': TreeStub, 'el-button': ButtonStub, 'el-input': InputStub }

const CATALOG = {
  catalog: { system: [{ name: 'cpu_usage', title: 'CPU 使用率', category: 'system', unit: '%', chart: 'line' }] },
  categories: ['system'],
}

const CPU_NODE = {
  key: 'm:cpu_usage',
  label: 'CPU 使用率 (cpu_usage)',
  meta: { name: 'cpu_usage', title: 'CPU 使用率', category: 'system', unit: '%', chart: 'line', active: false },
}

function mountView() {
  return mount(MetricsExploreView, { global: { stubs } })
}

// 触发指标树叶子节点点击（等价于用户点击 CPU 使用率），并等待渲染期的微任务结算。
async function clickCpuNode(wrapper) {
  wrapper.findComponent({ name: 'ElTree' }).vm.$emit('node-click', CPU_NODE)
  await flush()
}

describe('MetricsExploreView 跨节点指标浏览与导出（契约）', () => {
  let wrapper

  beforeEach(() => {
    httpGet.mockReset()
    exportMetricCSV.mockReset()
    metricCatalog.mockReset()
    metricActive.mockReset()
    ElMessage.error.mockReset()
    ElMessage.warning.mockReset()
    ElMessage.success.mockReset()
    metricCatalog.mockResolvedValue(CATALOG)
    metricActive.mockResolvedValue({ items: [] })
    httpGet.mockResolvedValue({ series: [] })
    exportMetricCSV.mockResolvedValue({})
  })

  afterEach(() => {
    if (wrapper) wrapper.unmount()
    wrapper = undefined
  })

  it('点击指标节点后以 metric 查询趋势，且不携带 node 参数', async () => {
    wrapper = mountView()
    await flush() // 等待 onMounted 的 metricCatalog / metricActive 完成
    await clickCpuNode(wrapper)

    expect(httpGet).toHaveBeenCalledTimes(1)
    expect(httpGet).toHaveBeenCalledWith(expect.stringMatching(/^\/api\/v1\/query\/range\?metric=cpu_usage/))
    expect(httpGet.mock.calls[0][0]).not.toContain('node=')
  })

  it('导出 CSV 携带 metric 且不携带 node 参数', async () => {
    wrapper = mountView()
    await flush()
    await clickCpuNode(wrapper)

    const exportBtn = wrapper.findAll('button').find((b) => b.text().includes('导出 CSV'))
    expect(exportBtn).toBeTruthy()
    await exportBtn.trigger('click')
    await flush()

    expect(exportMetricCSV).toHaveBeenCalledTimes(1)
    const args = exportMetricCSV.mock.calls[0][0]
    expect(args).toEqual(expect.objectContaining({ metric: 'cpu_usage' }))
    expect(Object.keys(args)).not.toContain('node')
    expect(JSON.stringify(args)).not.toContain('node=')
  })

  it('查询被拒绝（范围外节点分组）时呈现「查询失败」提示', async () => {
    httpGet.mockRejectedValue(new Error('无权访问该节点分组'))

    wrapper = mountView()
    await flush()
    await clickCpuNode(wrapper)

    expect(ElMessage.error).toHaveBeenCalledWith(expect.stringContaining('查询失败'))
  })
})
