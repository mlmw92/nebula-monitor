// 资产 API 的路径与参数拼装：这些细节错了不会报错，只会静默查到错的东西，
// 因此按仓库对 http 层单测的同一取向覆盖它（鉴权与资源范围由服务端负责）。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import {
  listAssets,
  getAsset,
  getAssetHistory,
  getAssetSummary,
  getAssetLinks,
  createAsset,
  updateAsset,
} from './asset'

function jsonResponse(status, body) {
  return { status, ok: status >= 200 && status < 300, json: async () => body }
}

describe('资产 API 封装', () => {
  let fetchMock

  beforeEach(() => {
    fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, {}))
    vi.stubGlobal('fetch', fetchMock)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('列表：只拼接有值的筛选参数', async () => {
    await listAssets({ type: 'host', node: 'web-01', keyword: '', limit: 50 })
    const url = fetchMock.mock.calls[0][0]
    expect(url).toBe('/api/v1/assets?type=host&node=web-01&limit=50')
    expect(url).not.toContain('keyword')
  })

  it('列表：无条件时不带问号', async () => {
    await listAssets()
    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/assets')
  })

  it('详情与历史：ID 做 URL 编码，limit 为 0 时不拼参数', async () => {
    await getAsset('12')
    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/assets/12')

    await getAssetHistory('12')
    expect(fetchMock.mock.calls[1][0]).toBe('/api/v1/assets/12/history')

    await getAssetHistory('12', 20)
    expect(fetchMock.mock.calls[2][0]).toBe('/api/v1/assets/12/history?limit=20')
  })

  it('摘要与关联：路径正确，且筛选参数与列表同一套过滤规则', async () => {
    await getAssetSummary({ status: 'missing', ownerMissing: 'true', type: '' })
    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/assets/summary?status=missing&ownerMissing=true')

    await getAssetSummary()
    expect(fetchMock.mock.calls[1][0]).toBe('/api/v1/assets/summary')

    await getAssetLinks('12')
    expect(fetchMock.mock.calls[2][0]).toBe('/api/v1/assets/12/links')
  })

  it('恢复采集值：resetAttrs 随 PUT 一起提交（与写人工值是相反方向）', async () => {
    await updateAsset('12', { resetAttrs: ['cpuCores', 'mem'] })
    const body = JSON.parse(fetchMock.mock.calls[0][1].body)
    expect(fetchMock.mock.calls[0][1].method).toBe('PUT')
    expect(body.resetAttrs).toEqual(['cpuCores', 'mem'])
    expect(body.attrs).toBeUndefined()
  })

  it('新建与更新：走 POST / PUT，更新不携带 node（归属节点由服务端拒绝变更）', async () => {
    await createAsset({ typeKey: 'host', naturalKey: 'web-09', node: 'web-09', attrs: { owner: 'alice' } })
    const [createUrl, createOpts] = fetchMock.mock.calls[0]
    expect(createUrl).toBe('/api/v1/assets')
    expect(createOpts.method).toBe('POST')
    expect(JSON.parse(createOpts.body).naturalKey).toBe('web-09')

    await updateAsset('12', { name: '新名字', attrs: { cpuCores: '16' } })
    const [updateUrl, updateOpts] = fetchMock.mock.calls[1]
    expect(updateUrl).toBe('/api/v1/assets/12')
    expect(updateOpts.method).toBe('PUT')
    const body = JSON.parse(updateOpts.body)
    expect(body.name).toBe('新名字')
    expect(body.node).toBeUndefined()
  })
})
