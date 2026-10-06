// 主机列表页两个页签的口径隔离测试。
//
// 为什么要有这个文件：「普通主机」与「网闸代理」共用一个 PageHeader 和一条工具栏，
// 页头计数、状态筛选（全部/在线/离线/异常）、分组下拉、关键词搜索、列设置、
// 分组管理/批量升级 全是**主机维度**的东西。它们曾经在两个页签下都渲染，
// 于是在网闸代理页签上会拿主机的台数去描述代理节点（"共 4 台 · 在线 4"），
// 而这张表里压根没有主机。这种缺陷不会抛错、不会白屏，只能靠断言"界面上不该有什么"来防回归。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import http from '../api/http'
import HostsView from './HostsView.vue'

vi.mock('../api/http', () => ({
  default: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() },
  getToken: vi.fn(() => ''),
}))

// useRoute 供「从告警详情跳主机」的深链消费（?node=）；用例默认无 query。
const routerReplace = vi.fn()
let routeQuery = {}
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn(), replace: routerReplace }),
  useRoute: () => ({ query: routeQuery }),
}))

// jsdom 未实现 ResizeObserver，Element Plus 的表格会用到它。
if (!globalThis.ResizeObserver) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

// 主机与代理节点用不同的节点名，便于断言"代理页签不会串味到主机数据"
const hostNode = { hostname: 'web-01', ip: '10.0.0.1', status: 'online', group: 'default', mode: 'agent', version: '1.0.0' }
const proxyNode = { node: 'hub-proxy', mode: 'hub', online: true, connActive: 2, forwardTotal: 10, reconnectTotal: 0 }

let proxyItems = []
let hostNodes = []

function mountView() {
  return mount(HostsView, {
    global: { plugins: [ElementPlus] },
    attachTo: document.body,
  })
}

describe('HostsView 页签口径隔离', () => {
  let wrapper

  beforeEach(() => {
    vi.clearAllMocks()
    hostNodes = [hostNode]
    proxyItems = [proxyNode]
    routeQuery = {}
    http.get.mockImplementation(async (path) => {
      if (path === '/api/v1/nodes') return { nodes: hostNodes }
      if (path === '/api/v1/nodes/latest') return { metrics: {} }
      if (path === '/api/v1/groups') return { groups: [{ id: 1, name: 'default' }] }
      if (path === '/api/v1/proxy/status') return { items: proxyItems }
      if (path === '/api/v1/version') return { server: '1.0.0' }
      return {}
    })
  })

  afterEach(() => {
    if (wrapper) wrapper.unmount()
    wrapper = undefined
    document.body.innerHTML = ''
  })

  // 告警详情等处的「跳主机」入口会带 ?node= 过来。此前这个参数没人消费，
  // 点了等于只是切到主机页——看着跳过去了，实际没有落到那台机器上。
  it('带 ?node= 进入时精确落到那一台，且参数只认领一次', async () => {
    routeQuery = { node: 'web-01' }
    // 故意放一台前缀相同的机器：关键词是模糊匹配，直接回填原文会同时筛出两台
    hostNodes = [hostNode, { ...hostNode, hostname: 'web-011', ip: '10.0.0.11' }]
    wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('全部 (1)')
    // 按行断言：只应剩精确命中的那一台（前缀相同的 web-011 必须被排除）。
    // 不能用"文本里不含 web-011"来判断——行内主机名与 IP 是相邻单元格，
    // 拼接后 "web-01" + "10.0.0.1" 天然含 "web-011" 这个子串。
    const rows = wrapper.findAll('.el-table__row')
    expect(rows.length).toBe(1)
    expect(rows[0].text()).toContain('web-01')
    expect(routerReplace).toHaveBeenCalledWith({ path: '/hosts', query: {} })
  })

  it('普通主机页签显示主机计数与筛选工具栏', async () => {
    wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('.ph-desc').text()).toContain('共 1 台')
    expect(wrapper.find('.toolbar').exists()).toBe(true)
    expect(wrapper.text()).toContain('全部 (1)')
    expect(wrapper.text()).toContain('分组管理')
  })

  it('网闸代理页签不显示主机计数、筛选工具栏与主机操作按钮', async () => {
    wrapper = mountView()
    await flushPromises()

    await wrapper.findAll('.subnav-item')[1].trigger('click')
    await flushPromises()

    // 页头描述换成代理口径：台数、在线/离线都来自代理状态上报
    expect(wrapper.find('.ph-desc').text()).toBe('共 1 个代理节点 · 在线 1 · 离线 0')
    // 主机维度的筛选与操作一律不出现
    expect(wrapper.find('.toolbar').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('全部 (')
    expect(wrapper.text()).not.toContain('分组管理')
    expect(wrapper.text()).not.toContain('批量升级')
    // 代理自己的表格照常展示
    expect(wrapper.text()).toContain('网闸代理状态')
    expect(wrapper.text()).toContain('转发请求')
    expect(wrapper.text()).toContain('hub-proxy')
  })

  it('无代理上报时页头描述不残留主机计数', async () => {
    proxyItems = []
    wrapper = mountView()
    await flushPromises()

    await wrapper.findAll('.subnav-item')[1].trigger('click')
    await flushPromises()

    expect(wrapper.find('.ph-desc').text()).toBe('暂无代理节点上报')
    expect(wrapper.text()).not.toContain('台')
  })
})
