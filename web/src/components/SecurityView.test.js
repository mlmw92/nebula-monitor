// 安全中心「合规矩阵导出」的用例。
//
// 为什么只测这一小块：导出按钮的行为有三种失败都不报错——无权限时按钮还显示（点下去才 403）、
// 点击后没真的发请求、失败时什么都不说（用户以为"没反应"而反复点）。
// 下载本身的机制在 api/security.test.js 里测，这里只钉住"按钮何时出现 + 点了干什么 + 失败说不说"。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import ElementPlus, { ElMessage } from 'element-plus'
import SecurityView from './SecurityView.vue'
import {
  getSecuritySummary,
  getSecurityEvents,
  getSecurityBaselines,
  getDefenseStatuses,
  exportComplianceMatrix,
} from '../api/security'

vi.mock('../api/security', () => ({
  getSecuritySummary: vi.fn(),
  getSecurityEvents: vi.fn(),
  getSecurityBaselines: vi.fn(),
  getDefenseStatuses: vi.fn(),
  postDefenseAction: vi.fn(),
  exportComplianceMatrix: vi.fn(),
}))

vi.mock('element-plus', async (importOriginal) => ({
  ...(await importOriginal()),
  ElMessage: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
}))

// 权限按用例设置：导出按钮由 security:export 门控（服务端仍然会再校验一次）。
let permissions = []
vi.mock('../composables/useAuth', () => ({
  default: () => ({ can: (perm) => permissions.includes(perm) }),
}))

if (!globalThis.ResizeObserver) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  permissions = []
  getSecuritySummary.mockResolvedValue({ score: 80, eventCount: 0, riskNodes: 0, fimChanges: 0, baselineHosts: 1 })
  getSecurityEvents.mockResolvedValue({ events: [] })
  getSecurityBaselines.mockResolvedValue({
    enabled: true,
    baselines: [{ node: 'web-01', displayName: '美国', score: 60, items: [{ key: 'k1', name: '防火墙已启用', pass: false }] }],
  })
  getDefenseStatuses.mockResolvedValue({ statuses: [] })
})

async function mountView() {
  const w = mount(SecurityView, { global: { plugins: [ElementPlus] }, attachTo: document.body })
  await flushPromises()
  return w
}

function exportButton(w) {
  return w.findAll('button').find((b) => b.text().includes('导出矩阵 CSV'))
}

describe('安全中心 · 合规矩阵导出', () => {
  it('没有 security:export 时不显示按钮（而不是点下去才 403）', async () => {
    permissions = ['security:read']
    const w = await mountView()
    expect(exportButton(w)).toBeUndefined()
  })

  it('有权限时显示按钮，点击调用导出并提示文件名', async () => {
    permissions = ['security:read', 'security:export']
    exportComplianceMatrix.mockResolvedValue('compliance-matrix-20261007-120000.csv')
    const w = await mountView()

    const btn = exportButton(w)
    expect(btn).toBeTruthy()
    await btn.trigger('click')
    await flushPromises()

    expect(exportComplianceMatrix).toHaveBeenCalledTimes(1)
    expect(ElMessage.success).toHaveBeenCalledWith(expect.stringContaining('compliance-matrix-20261007-120000.csv'))
  })

  it('导出失败时把服务端报文说出来（"没有数据"不能被静默吞掉）', async () => {
    permissions = ['security:export']
    exportComplianceMatrix.mockRejectedValue(new Error('还没有安全基线数据可导出：请确认 Agent 已上报安全基线'))
    const w = await mountView()

    await exportButton(w).trigger('click')
    await flushPromises()

    expect(ElMessage.error).toHaveBeenCalledWith(expect.stringContaining('还没有安全基线数据可导出'))
    expect(ElMessage.success).not.toHaveBeenCalled()
  })
})
