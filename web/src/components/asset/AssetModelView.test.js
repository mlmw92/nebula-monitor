// 配置项模型页的回归测试。
//
// 这张页面上"错了也不报错"的地方有三处，只能靠断言钉住：
//   ① 保存时把**内置默认的运行态字段**原样提交上去——模型就此被固化，将来默认调整再也影响不到它；
//   ② 关注字段非空时（只有点名字段参与比对）页面没说明——"我明明看到这个字段有值，为什么没进差异"；
//   ③ 短命对象（Pod/工作负载）在模型页里没有标记——它们默认不计入健康度，不标会让人以为台账漏了。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import { getAssetTypes, saveAssetTypeModel } from '../../api/asset'
import AssetModelView from './AssetModelView.vue'

vi.mock('../../api/asset', () => ({
  getAssetTypes: vi.fn(),
  saveAssetTypeModel: vi.fn(),
}))

let canWrite = true
vi.mock('../../composables/useAuth', () => ({
  useAuth: () => ({ can: () => canWrite }),
}))

if (!globalThis.ResizeObserver) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

const DEFAULT_RUNTIME = ['up', 'status', 'uptime', 'uptimeSeconds']

const TYPES = [
  {
    key: 'host',
    title: '主机',
    builtin: true,
    ephemeral: false,
    assets: 2,
    schema: {
      runtimeFields: [...DEFAULT_RUNTIME],
      runtimeFieldsDefault: true,
      focusFields: [],
      attrMeta: { cpuCores: { title: 'CPU 核数', unit: '核' } },
    },
    baseline: { assetId: 3, assetKey: 'web-01' },
    attrs: [
      { key: 'cpuCores', assets: 2, coverage: 1, discovery: 2, manual: 1 },
      { key: 'env', assets: 1, coverage: 0.5, discovery: 1, manual: 0 },
    ],
    synonymGroups: [['cpu_cores', 'cpu-cores', 'cpuCores']],
  },
  {
    key: 'pod',
    title: '容器（Pod）',
    builtin: true,
    ephemeral: true,
    assets: 1,
    schema: { runtimeFields: [...DEFAULT_RUNTIME], runtimeFieldsDefault: true, focusFields: [] },
    attrs: [{ key: 'phase', assets: 1, coverage: 1, discovery: 1, manual: 0 }],
  },
]

let wrappers = []

function mountView() {
  const w = mount(AssetModelView, { global: { plugins: [ElementPlus] }, attachTo: document.body })
  wrappers.push(w)
  return w
}

beforeEach(() => {
  vi.clearAllMocks()
  canWrite = true
  wrappers = []
  getAssetTypes.mockResolvedValue({ types: TYPES })
  saveAssetTypeModel.mockImplementation(async () => ({ types: TYPES }))
})

afterEach(() => {
  wrappers.forEach((w) => w.unmount())
  document.body.innerHTML = ''
})

function findButton(w, text) {
  return w.findAll('button').find((b) => b.text().includes(text))
}

describe('AssetModelView', () => {
  it('列出类型（含短命对象标记）与选中类型的字段画像', async () => {
    const w = mountView()
    await flushPromises()

    expect(w.text()).toContain('主机')
    expect(w.text()).toContain('容器（Pod）')
    // 短命对象必须被标出来（它们默认不计入台账健康度）
    expect(w.text()).toContain('短命对象')
    // 字段画像：键名、覆盖度、来源分布、中文名（来自模型）
    expect(w.text()).toContain('cpuCores')
    expect(w.text()).toContain('1/2')
    expect(w.text()).toContain('采集 2')
    expect(w.text()).toContain('人工 1')
    // 疑似同义键只提示，不自动合并
    expect(w.text()).toContain('疑似同义键')
    // 标杆资产
    expect(w.text()).toContain('web-01')
  })

  it('只改中文名时，运行态字段提交空数组（不把内置默认固化进模型）', async () => {
    const w = mountView()
    await flushPromises()

    w.vm.draft.attrMeta.cpuCores.title = 'CPU 核数'
    await findButton(w, '保存').trigger('click')
    await flushPromises()

    expect(saveAssetTypeModel).toHaveBeenCalled()
    const [typeKey, payload] = saveAssetTypeModel.mock.calls.at(-1)
    expect(typeKey).toBe('host')
    // 关键：没动过运行态字段 → 提交空数组（= 继续用内置默认）
    expect(payload.runtimeFields).toEqual([])
    expect(payload.focusFields).toEqual([])
    expect(payload.attrMeta.cpuCores.title).toBe('CPU 核数')
  })

  it('选成「关注」后：提交的是关注字段，并在页面上说明"只比对这几个"', async () => {
    const w = mountView()
    await flushPromises()

    w.vm.draft.mode.env = 'focus'
    await flushPromises()
    expect(w.text()).toContain('只比对标注为「关注」的 1 个字段')

    await findButton(w, '保存').trigger('click')
    await flushPromises()
    const [, payload] = saveAssetTypeModel.mock.calls.at(-1)
    expect(payload.focusFields).toEqual(['env'])
  })

  it('改动了运行态字段时，提交的是显式清单', async () => {
    const w = mountView()
    await flushPromises()

    w.vm.draft.mode.cpuCores = 'runtime'
    await findButton(w, '保存').trigger('click')
    await flushPromises()

    const [, payload] = saveAssetTypeModel.mock.calls.at(-1)
    expect(payload.runtimeFields).toContain('cpuCores')
    expect(payload.runtimeFields).toContain('up')
  })

  it('没有 assets:write 时只能查看', async () => {
    canWrite = false
    const w = mountView()
    await flushPromises()

    expect(findButton(w, '保存').attributes('disabled')).toBeDefined()
    expect(findButton(w, '清空比对设置').attributes('disabled')).toBeDefined()
    expect(w.text()).toContain('缺 assets:write 权限')
  })

  it('取不到模型时给出错误，而不是显示一个空页面', async () => {
    getAssetTypes.mockRejectedValue(new Error('资产台账未启用'))
    const w = mountView()
    await flushPromises()

    expect(w.text()).toContain('读取配置项模型失败')
    expect(w.text()).toContain('资产台账未启用')
  })
})
