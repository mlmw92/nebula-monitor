// asset.js — 资产台账 API 封装，复用 http 的 get/post/put。
//
// 与 security.js 同一形态：只做「路径拼接 + 参数过滤」，鉴权与错误文案交给 http.js。
// 服务端才是安全边界（资源范围、权限点都在服务端校验），这里不做任何权限判断。
import http, { getToken } from './http'

// withQuery 拼接查询串，空值一律不拼：
// `?type=` 与「不传 type」在服务端语义不同（前者是"筛选类型为空"，后者是"全部类型"），
// 传空串会把筛选条件写坏。
const withQuery = (path, params = {}) => {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== '') q.append(k, v)
  }
  const s = q.toString()
  return path + (s ? '?' + s : '')
}

// 资产列表：type / node / keyword 过滤 + limit / offset 分页。
export const listAssets = (params = {}) => http.get(withQuery('/api/v1/assets', params))

// 资产详情（属性含采集值与人工值两个来源，values 为生效值）
export const getAsset = (id) => http.get('/api/v1/assets/' + encodeURIComponent(id))

// 按「类型 + 身份」精确查一条资产，供跨页联动使用（容器页的 Pod/工作负载 → 台账资产）。
//
// 参数两种形态：`{ type, key }`（已知自然键）或 `{ type, cluster, namespace, name[, kind] }`
// （容器类资产给身份，由服务端拼键——前端不拼，拼错只会命中另一条资产）。
//
// 刻意**不用 http.get**：404 在这里是**正常结果**（"这个对象还没进台账"，
// 清单上报有一个采集周期），而通用封装会把它抛成异常，调用方只能靠文案猜。
export async function lookupAsset(params = {}) {
  const token = getToken()
  const res = await fetch(withQuery('/api/v1/assets/lookup', params), {
    headers: token ? { Authorization: 'Bearer ' + token } : {},
  })
  let body = {}
  try {
    body = await res.json()
  } catch (e) {
    /* 非 JSON 响应体：交给调用方按状态码兜底 */
  }
  return { ok: res.ok, status: res.status, body }
}

// 字段级变更历史；limit 为 0 时用服务端默认上限。
export const getAssetHistory = (id, limit = 0) =>
  http.get('/api/v1/assets/' + encodeURIComponent(id) + '/history' + (limit ? '?limit=' + limit : ''))

// 手工新建资产：以人工来源建档（服务端对已存在的类型 + 自然键返回 409）
export const createAsset = (payload) => http.post('/api/v1/assets', payload)

// 维护人工值（名称与属性）：只写 manual 来源，采集值不受影响。
// 注意服务端不接受 node 变更（归属节点是资源范围锚点），因此这里也不传。
// payload.resetAttrs 列出要「恢复采集值」的字段（删掉这些字段的人工值），与 attrs 是两个方向。
export const updateAsset = (id, payload) => http.put('/api/v1/assets/' + encodeURIComponent(id), payload)

// 台账健康度摘要（总数 / 失联 / 无责任人 / 冲突 / 近 7 天变更）。
// 与列表共用同一套筛选参数：顶部数字点进去必须看到同一个集合。
export const getAssetSummary = (params = {}) => http.get(withQuery('/api/v1/assets/summary', params))

// 资产的直接关联（出边 + 入边）；范围外的对端由服务端剔除，前端拿到的都是可见资产。
// 返回 { links, suppressed }：suppressed 是被人工隐藏（逻辑删除）的边 ——
// 它必须能看见且能恢复，否则「删掉的关系就永远回不来」。
export const getAssetLinks = (id) => http.get('/api/v1/assets/' + encodeURIComponent(id) + '/links')

// 关系图：以某资产为中心、N 跳以内的邻域（depth 由服务端夹紧到 1..3）。
// 与 getAssetLinks 的分工：那个给直接关系（表格逐条看），这个给邻域（图上找影响面）；
// 两者共用同一套资源范围裁剪，因此"表里看不到、图里看得到"不会发生。
export const getAssetTopology = (id, params = {}) =>
  http.get(withQuery('/api/v1/assets/' + encodeURIComponent(id) + '/topology', params))

// ---- 关系的人工维护 ----
// 三种动作共用一套寻址：URL 里的资产是基准，给 {toType,toKey,kind,direction}。
// direction 用读接口返回的原值即可（out=基准 → 对端，in=对端 → 基准），前端不必自己算方向。

// 添加关系：幂等；若这条边此前由采集建立，会把它升级为「人工认领」，之后不再被采集覆盖。
export const createAssetLink = (id, payload) =>
  http.post('/api/v1/assets/' + encodeURIComponent(id) + '/links', payload)

// 解除关系 = 逻辑删除：服务端会落一条抑制记录，采集不会再把它建回来。
// **寻址走查询串**：DELETE 的请求体在 HTTP 语义里没有定义，中间设备丢弃它是合法行为，
// 因此服务端只从查询串读（见 internal/server/api/asset_link_api.go 文件头）。
export const deleteAssetLink = (id, payload) =>
  http.del(withQuery('/api/v1/assets/' + encodeURIComponent(id) + '/links', payload))

// 取消抑制：把这条关系交还给采集（逻辑删除的出口）。
export const restoreAssetLink = (id, payload) =>
  http.post('/api/v1/assets/' + encodeURIComponent(id) + '/links/restore', payload)

// ---- 配置巡检（inspect）----
// 巡检只给结论、不改配置；「跑」与「改」在服务端是两个权限点（inspect:run / assets:write）。

// 触发一次巡检：范围复用台账筛选（type / node / keyword），fields 为关注字段（留空=服务端默认）。
export const runInspect = (payload = {}) => http.post('/api/v1/inspect/runs', payload)

// 巡检记录（时间倒序）。
export const listInspectRuns = (limit = 0) => http.get(withQuery('/api/v1/inspect/runs', { limit }))

// 某次巡检的差异项（严重级别高的排前面）。
export const listInspectFindings = (runId, limit = 0) =>
  http.get(withQuery('/api/v1/inspect/runs/' + encodeURIComponent(runId) + '/findings', { limit }))

// 各资产类型当前的期望值来源（标杆资产）。
export const listInspectBaselines = () => http.get('/api/v1/inspect/baselines')

// 把某资产的当前配置设为该资产类型的期望值（标杆）；服务端按类型只保留一个。
export const setAssetBaseline = (id) => http.post('/api/v1/assets/' + encodeURIComponent(id) + '/baseline', {})

// 清除该资产所属类型的标杆（幂等）。
export const clearAssetBaseline = (id) => http.del('/api/v1/assets/' + encodeURIComponent(id) + '/baseline')

// 配置快照（巡检基线；只在首次或字段真变化时新增）
export const listAssetSnapshots = (id, limit = 0) =>
  http.get(withQuery('/api/v1/assets/' + encodeURIComponent(id) + '/snapshots', { limit }))

// ---- 生命周期与标签 ----
// 忽略 = 从台账隐藏（可恢复、不停止采集）；彻底删除**仅限纯人工建档资产**
// （采集资产会被下一轮上报重建，服务端会返回 409 并提示改用忽略）。

export const ignoreAsset = (id, reason = '') =>
  http.post('/api/v1/assets/' + encodeURIComponent(id) + '/ignore', { reason })

export const restoreAsset = (id) => http.post('/api/v1/assets/' + encodeURIComponent(id) + '/restore', {})

export const purgeAsset = (id) => http.post('/api/v1/assets/' + encodeURIComponent(id) + '/purge', {})

// 标签维护：labels 写入/覆盖，remove 删除；两者键冲突时服务端返回 400。
export const updateAssetLabels = (id, labels, remove = []) =>
  http.put('/api/v1/assets/' + encodeURIComponent(id) + '/labels', { labels, remove })

// ---- 批量维护与清单导出 ----

// 批量维护：转派责任人 / 清除责任人 / 打标签 / 忽略 / 恢复（{ ids, op, ... }）。
//
// 刻意**不用 http.post**：一条都没成功时服务端返回 409，而响应体里带着**逐条原因**，
// 通用封装只抛 error.message，会把最有用的那份信息丢掉——用户只能看到
// "没有任何一条资产被更新"，却看不到"为什么"。因此这里把状态码与 body 一起交回调用方。
export async function batchAssets(payload) {
  const token = getToken()
  const res = await fetch('/api/v1/assets/batch', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: 'Bearer ' + token } : {}),
    },
    body: JSON.stringify(payload),
  })
  let body = {}
  try {
    body = await res.json()
  } catch (e) {
    /* 非 JSON 响应体：交给调用方按状态码兜底 */
  }
  return { ok: res.ok, status: res.status, body }
}

// 导出资产清单 CSV：与列表**同一套筛选参数**（服务端不分页，只导资源范围内的资产）。
// 文件名优先用服务端给的（自带导出时间），避免同一天导出多份互相覆盖。
export async function exportAssets(params = {}, fallbackName = 'assets.csv') {
  const token = getToken()
  const res = await fetch(withQuery('/api/v1/assets/export', params), {
    headers: token ? { Authorization: 'Bearer ' + token } : {},
  })
  if (!res.ok) {
    let msg = 'HTTP ' + res.status
    try {
      const body = await res.json()
      if (body && body.error) msg = body.error
    } catch (e) {
      /* 保留状态码文案 */
    }
    throw new Error(msg)
  }
  const disposition = res.headers.get('Content-Disposition') || ''
  const hit = /filename=([^;]+)/.exec(disposition)
  const name = hit ? hit[1].trim() : fallbackName
  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
  return name
}
