// 用户页**业务范围**回填与提交的回归测试。
//
// 为什么必须有这一组：服务端返回的 scope 是 snake_case（`asset_mode` / `asset_labels`，
// 与同一对象里的 display_name / token_version 一致）。前端按 camelCase 读它**不会报错**，
// 只会静默拿到 undefined —— 症状是"打开编辑框显示不限，点保存就把这个人的业务范围抹掉了"，
// 即权限被静默放大。这条路径只能靠"打开 → 不改动 → 保存 → 断言请求体"钉住。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import http from '../../api/http'
import UsersView from './UsersView.vue'

vi.mock('../../api/http', () => ({
  default: {
    listUsers: vi.fn(),
    listRoles: vi.fn(),
    listGroups: vi.fn(),
    get: vi.fn(),
    createUser: vi.fn(),
    updateUser: vi.fn(),
  },
}))
vi.mock('../../composables/useAuth', () => ({
  useAuth: () => ({
    principal: { assetScopeLabelKey: 'biz' },
    can: () => true,
  }),
}))

if (!globalThis.ResizeObserver) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

// 服务端真实形状：scope 里是 snnake_case 的 asset_mode / asset_labels
const scopedUser = {
  username: 'ops_pay',
  display_name: '支付线运维',
  roles: ['biz_pay'],
  status: 'enabled',
  scope: {
    mode: 'restricted',
    groups: ['国内'],
    asset_mode: 'limited',
    asset_labels: [{ key: 'biz', value: 'pay' }],
  },
}

let wrappers = []

function mountView() {
  const w = mount(UsersView, { global: { plugins: [ElementPlus] }, attachTo: document.body })
  wrappers.push(w)
  return w
}

beforeEach(() => {
  vi.clearAllMocks()
  wrappers = []
  http.listUsers.mockResolvedValue({ users: [scopedUser] })
  http.listRoles.mockResolvedValue({ roles: ['biz_pay'] })
  http.listGroups.mockResolvedValue({ groups: ['国内'] })
  http.get.mockResolvedValue({ key: 'biz', values: ['pay', 'risk'] })
  http.updateUser.mockResolvedValue({ ok: 'true' })
  http.createUser.mockResolvedValue({ ok: 'true' })
})

afterEach(() => {
  for (const w of wrappers) w.unmount()
  document.body.innerHTML = ''
})

describe('UsersView 业务范围（资产标签维度）', () => {
  it('表格把用户的业务范围显示出来', async () => {
    const w = mountView()
    await flushPromises()
    expect(w.text()).toContain('biz=pay')
  })

  it('编辑时按服务端的 snake_case 回填，且不改动直接保存不会抹掉业务范围', async () => {
    const w = mountView()
    await flushPromises()

    await w.findAll('button').find((b) => b.text() === '编辑').trigger('click')
    await flushPromises()

    // ① 回填：单选组应停在「限定取值」（读成 camelCase 的话这里会是 all）
    const group = w
      .findAllComponents({ name: 'ElRadioGroup' })
      .find((g) => g.text().includes('限定取值'))
    expect(group).toBeTruthy()
    expect(group.props('modelValue')).toBe('limited')

    // ② 不改动任何东西直接保存：请求体里必须原样带着 biz=pay
    await w.findAll('button').find((b) => b.text().includes('保存')).trigger('click')
    await flushPromises()

    expect(http.updateUser).toHaveBeenCalledTimes(1)
    const [name, payload] = http.updateUser.mock.calls[0]
    expect(name).toBe('ops_pay')
    expect(payload.scope.asset_mode).toBe('limited')
    expect(payload.scope.asset_labels).toEqual([{ key: 'biz', value: 'pay' }])
  })
})
