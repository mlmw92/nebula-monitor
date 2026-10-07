// 合规矩阵导出的客户端：这些细节错了不会报错，只会"点了没反应"或下载到一个错名字的文件。
// 服务端才是安全边界（权限点与资源范围都在那边），这里只保证请求路径、文件名与错误文案正确。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { exportComplianceMatrix } from './security'

function csvResponse(status, disposition) {
  return {
    status,
    ok: status >= 200 && status < 300,
    headers: { get: (k) => (k.toLowerCase() === 'content-disposition' ? disposition : null) },
    json: async () => ({ error: '还没有安全基线数据可导出：请确认 Agent 已上报安全基线' }),
    blob: async () => new Blob(['a,b\n1,2\n'], { type: 'text/csv' }),
  }
}

describe('合规矩阵导出', () => {
  let fetchMock

  beforeEach(() => {
    fetchMock = vi.fn().mockResolvedValue(csvResponse(200, 'attachment; filename=compliance-matrix-20261007-120000.csv'))
    vi.stubGlobal('fetch', fetchMock)
    // jsdom 没有这两个方法；不补的话下载流程会在 createObjectURL 处直接抛错。
    URL.createObjectURL = vi.fn(() => 'blob:fake')
    URL.revokeObjectURL = vi.fn()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('请求导出接口，并用服务端给的文件名下载（同一天导多份不会互相覆盖）', async () => {
    const name = await exportComplianceMatrix()
    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/security/baselines/export')
    expect(name).toBe('compliance-matrix-20261007-120000.csv')
    expect(URL.createObjectURL).toHaveBeenCalled()
    expect(URL.revokeObjectURL).toHaveBeenCalled()
  })

  it('服务端没给文件名时退回默认名', async () => {
    fetchMock.mockResolvedValue(csvResponse(200, ''))
    expect(await exportComplianceMatrix()).toBe('compliance-matrix.csv')
  })

  it('失败时抛出服务端报文（"没有数据"与"没有权限"必须能区分）', async () => {
    fetchMock.mockResolvedValue(csvResponse(400, ''))
    await expect(exportComplianceMatrix()).rejects.toThrow('还没有安全基线数据可导出')
  })
})
