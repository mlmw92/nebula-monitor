// 角色页的**业务范围**回归测试。
//
// 为什么单钉这一块：业务范围是授权的第二个维度，前端拼错字段名（或拼成 `scope` 而不是
// 平铺的 assetMode/assetLabels）**不会报错**——服务端把缺字段当成"不限制"，
// 于是"我明明配了限定，结果全都能看"。这类失败方向是权限静默放大，只能靠断言钉住。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import http from '../../api/http'
import RolesView from './RolesView.vue'

vi.mock('../../api/http', () => ({
  default: {
    listRoles: vi.fn(),
    permissionCatalog: vi.fn(),
    listGroups: vi.fn(),
    get: vi.fn(),
    createRole: vi.fn(),
    updateRole: vi.fn(),
    deleteRole: vi.fn(),
  },
}))
vi.mock('../../composables/useAuth', () => ({
  useAuth: () => ({
    can: () => true,
    canAny: () => true,
    principal: { assetScopeLabelKey: 'biz' },
  }),
}))

if (!globalThis.ResizeObserver) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

const limitedRole = {
  name: 'biz_pay',
  description: '支付线只读',
  builtin: false,
  permissions: ['assets:read'],
  scope_mode: 'restricted',
  scope_groups: ['g1'],
  asset_mode: 'limited',
  asset_labels: [{ key: 'biz', value: 'pay' }],
}

let wrappers = []

function mountView() {
  const w = mount(RolesView, { global: { plugins: [ElementPlus] }, attachTo: document.body })
  wrappers.push(w)
  return w
}

beforeEach(() => {
  vi.clearAllMocks()
  wrappers = []
  http.listRoles.mockResolvedValue({ roles: [limitedRole] })
  http.permissionCatalog.mockResolvedValue({ domains: [], assetScopeLabelKey: 'biz' })
  http.listGroups.mockResolvedValue({ groups: ['g1'] })
  http.get.mockResolvedValue({ key: 'biz', values: ['pay', 'risk'] })
  http.updateRole.mockResolvedValue({ ok: 'true' })
  http.createRole.mockResolvedValue({ ok: 'true' })
})

afterEach(() => {
  for (const w of wrappers) w.unmount()
  document.body.innerHTML = ''
})

describe('RolesView 业务范围（资产标签维度）', () => {
  it('表格把业务范围显示出来，并区分「限定了却没有取值」', async () => {
    const w = mountView()
    await flushPromises()

    // 配了 biz=pay：必须显示成 `biz=pay`，否则运维看不出这个角色还限了业务
    expect(w.text()).toContain('biz=pay')

    // 限定了却没有取值 = 无可见资产：必须显式说出来（不是"不限"）
    http.listRoles.mockResolvedValue({
      roles: [{ ...limitedRole, asset_labels: [] }],
    })
    const w2 = mountView()
    await flushPromises()
    expect(w2.text()).toContain('业务范围为空')
  })

  it('编辑时回填、保存时平铺成 assetMode/assetLabels（不是 scope 对象）', async () => {
    const w = mountView()
    await flushPromises()

    const edit = w.findAll('button').find((b) => b.text() === '编辑')
    expect(edit).toBeTruthy()
    await edit.trigger('click')
    await flushPromises()

    // 弹窗里有多个下拉（角色 / 节点分组 / 业务取值），按 placeholder 精确取到业务取值那个：
    // 取错实例会让用例"通过"却什么也没验证。
    const assetSelect = w
      .findAllComponents({ name: 'ElSelect' })
      .find((s) => String(s.props('placeholder') || '').includes('标签值'))
    expect(assetSelect).toBeTruthy()

    // 打开时已经回填了 limited（否则一保存就把业务范围抹成不限）
    expect(assetSelect.exists()).toBe(true)

    // 改成两个取值（模拟在界面上选了 pay 与 risk）
    assetSelect.vm.$emit('update:modelValue', ['pay', 'risk'])
    await flushPromises()

    const save = w.findAll('button').find((b) => b.text().includes('保存'))
    expect(save).toBeTruthy()
    await save.trigger('click')
    await flushPromises()

    expect(http.updateRole).toHaveBeenCalledTimes(1)
    const [name, payload] = http.updateRole.mock.calls[0]
    expect(name).toBe('biz_pay')
    expect(payload.assetMode).toBe('limited')
    expect(payload.assetLabels).toEqual([
      { key: 'biz', value: 'pay' },
      { key: 'biz', value: 'risk' },
    ])
  })

  it('切成「不限」时显式给 all + 空选择器（服务端不接受 all 带着选择器）', async () => {
    const w = mountView()
    await flushPromises()
    await w.findAll('button').find((b) => b.text() === '编辑').trigger('click')
    await flushPromises()

    // 同样按内容定位：业务维度的单选组才有「限定取值」这一项
    const group = w
      .findAllComponents({ name: 'ElRadioGroup' })
      .find((g) => g.text().includes('限定取值'))
    expect(group).toBeTruthy()
    group.vm.$emit('update:modelValue', 'all')
    await flushPromises()

    await w.findAll('button').find((b) => b.text().includes('保存')).trigger('click')
    await flushPromises()

    const payload = http.updateRole.mock.calls[0][1]
    expect(payload.assetMode).toBe('all')
    expect(payload.assetLabels).toEqual([])
  })
})
