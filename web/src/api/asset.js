// asset.js — 资产台账 API 封装，复用 http 的 get/post/put。
//
// 与 security.js 同一形态：只做「路径拼接 + 参数过滤」，鉴权与错误文案交给 http.js。
// 服务端才是安全边界（资源范围、权限点都在服务端校验），这里不做任何权限判断。
import http from './http'

// 资产列表：type / node / keyword 过滤 + limit / offset 分页。
// 空值不拼进查询串：`?type=` 与「不传 type」在服务端语义不同，传空串会把筛选条件写坏。
export const listAssets = (params = {}) => {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== '') {
      q.append(k, v)
    }
  }
  const s = q.toString()
  return http.get('/api/v1/assets' + (s ? '?' + s : ''))
}

// 资产详情（属性含采集值与人工值两个来源，values 为生效值）
export const getAsset = (id) => http.get('/api/v1/assets/' + encodeURIComponent(id))

// 字段级变更历史；limit 为 0 时用服务端默认上限。
export const getAssetHistory = (id, limit = 0) =>
  http.get('/api/v1/assets/' + encodeURIComponent(id) + '/history' + (limit ? '?limit=' + limit : ''))

// 手工新建资产：以人工来源建档（服务端对已存在的类型 + 自然键返回 409）
export const createAsset = (payload) => http.post('/api/v1/assets', payload)

// 维护人工值（名称与属性）：只写 manual 来源，采集值不受影响。
// 注意服务端不接受 node 变更（归属节点是资源范围锚点），因此这里也不传。
export const updateAsset = (id, payload) => http.put('/api/v1/assets/' + encodeURIComponent(id), payload)
