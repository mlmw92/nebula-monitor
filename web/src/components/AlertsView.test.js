// AlertsView 的「测试事件」按钮可见性由 notify:write 决定。
// 该按钮会真实写事件并向通知渠道发消息，前端隐藏只是体验层（服务端独立审计拒绝），
// 但写反「未加载即放行」或漏判权限点，都会让受限用户看到无权执行的按钮。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import useAuth from '../composables/useAuth'

// 组件挂载时会发一串请求（/alerts、/rules、/alerts/stats、/maintenance…），
// 统一返回安全的空结构，避免测试触发真实网络或渲染期取值崩溃。
vi.mock('../api/http', () => ({
  default: {
    get: vi.fn(async () => ({
      alerts: [],
      rules: [],
      groups: [],
      templates: [],
      acks: {},
      inhibits: [],
      firing: 0,
      suppressed: 0,
      total: 0,
      bySeverity: { critical: 0, warning: 0, info: 0 },
    })),
    post: vi.fn(async () => ({ ok: true })),
    put: vi.fn(async () => ({})),
    del: vi.fn(async () => ({})),
  },
  getToken: vi.fn(() => ''),
}))

vi.mock('echarts', () => ({
  init: vi.fn(() => ({ setOption: vi.fn(), resize: vi.fn(), dispose: vi.fn(), on: vi.fn() })),
  graphic: { LinearGradient: vi.fn() },
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  // 组件读 route.query 处理「从日志页建规则」的深链，缺这个导出会在挂载期直接抛
  useRoute: () => ({ query: {} }),
}))

// 组件按需 import 的图标必须在这里都有同名导出，否则会在加载期直接抛
// "No \"Xxx\" export is defined on the \"@element-plus/icons-vue\" mock"
// 而让整个文件的用例全部失败（新增图标时记得同步这里）。
vi.mock('@element-plus/icons-vue', () => ({
  Plus: { name: 'Plus', template: '<i />' },
  Printer: { name: 'Printer', template: '<i />' },
}))

vi.mock('element-plus', () => ({
  ElMessage: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  ElMessageBox: { confirm: vi.fn(async () => true) },
}))

vi.mock('./RuleModal.vue', () => ({ default: { name: 'RuleModal', template: '<div />' } }))

import AlertsView from './AlertsView.vue'

// el-button 需渲染默认插槽，才能断言按钮文案；其余 Element Plus 组件一律静默，
// 避免表格列的作用域插槽在 stub 下取值崩溃。
const ButtonStub = { name: 'ElButton', template: '<button class="el-button"><slot /></button>' }
const NoopStub = { name: 'Noop', template: '<div />' }

const stubs = {
  'el-button': ButtonStub,
  'el-alert': NoopStub,
  'el-date-picker': NoopStub,
  'el-dialog': NoopStub,
  'el-divider': NoopStub,
  'el-drawer': NoopStub,
  'el-dropdown': NoopStub,
  'el-dropdown-item': NoopStub,
  'el-dropdown-menu': NoopStub,
  'el-input': NoopStub,
  'el-input-number': NoopStub,
  'el-link': NoopStub,
  'el-option': NoopStub,
  'el-pagination': NoopStub,
  'el-radio': NoopStub,
  'el-radio-button': NoopStub,
  'el-radio-group': NoopStub,
  'el-select': NoopStub,
  'el-switch': NoopStub,
  'el-table': NoopStub,
  'el-table-column': NoopStub,
  'el-tag': NoopStub,
}

const { principal, clear } = useAuth()
const flush = () => new Promise((resolve) => setTimeout(resolve, 0))

function mountView() {
  return mount(AlertsView, {
    global: {
      stubs,
      directives: { loading: {} },
    },
  })
}

describe('AlertsView 测试事件按钮的权限可见性', () => {
  let wrapper

  beforeEach(() => {
    clear() // → loaded=false、permissions=[]，避免跨用例状态泄漏
  })

  afterEach(() => {
    if (wrapper) wrapper.unmount()
    wrapper = undefined
    clear()
  })

  it('已加载授权且只有 alerts:read 时不显示测试事件按钮', async () => {
    principal.loaded = true
    principal.permissions = ['alerts:read']

    wrapper = mountView()
    await flush()

    expect(wrapper.text()).not.toContain('测试事件')
  })

  it('已加载授权且有 notify:write 时显示测试事件按钮', async () => {
    principal.loaded = true
    principal.permissions = ['alerts:read', 'notify:write']

    wrapper = mountView()
    await flush()

    expect(wrapper.text()).toContain('测试事件')
  })

  it('未加载授权（单管理员 / 未启用 RBAC 兼容）时显示测试事件按钮', async () => {
    principal.loaded = false
    principal.permissions = []

    wrapper = mountView()
    await flush()

    expect(wrapper.text()).toContain('测试事件')
  })
})
