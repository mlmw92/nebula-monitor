// ops.js — 统一下行操作通道 API 封装。
//
// 只做「路径拼接 + 参数过滤」：权限（ops:read / ops:exec）与资源范围都在服务端校验，
// 前端隐藏按钮只是体验，不是安全边界。
import http from './http'

const withQuery = (path, params = {}) => {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== '') q.append(k, v)
  }
  const s = q.toString()
  return path + (s ? '?' + s : '')
}

// 动作目录；带 node（单节点）或 nodes（多节点）时一并返回这些节点**本机放行**的动作清单。
//
// 批量下发前必须一次拿到所有目标节点的放行情况：逐个查是 N 次请求，
// 不查就会把"本机未放行"的动作下下去，用户只会在结果里看到一堆失败。
export const listOpsActions = (node = '', nodes = []) =>
  http.get(withQuery('/api/v1/ops/actions', { node, nodes: (nodes || []).join(',') }))

// 任务列表（node / state / kind / batchId / limit）
export const listOpsTasks = (params = {}) => http.get(withQuery('/api/v1/ops/tasks', params))

export const getOpsTask = (id) => http.get('/api/v1/ops/tasks/' + encodeURIComponent(id))

// 下发一条操作任务：{ node, kind, params, reason }
// 服务端可能返回 409（节点不支持该动作 / 节点不在线）与 400（参数不合法），
// 两类错误的下一步完全不同，界面必须原样展示服务端给的说明。
export const createOpsTask = (payload) => http.post('/api/v1/ops/tasks', payload)

// 批量下发：{ nodes: [], kind, params, reason } → { batch: {batchId,total,created,failed,items} }
// 部分成功是常态（几台离线、几台没放行），因此一定要看 batch.items 的逐节点结论。
export const createOpsBatch = (payload) => http.post('/api/v1/ops/tasks/batch', payload)

// 取消任务：{ ids: [] } 或 { batchId } → { result: {cancelled,failed,items} }
// 只有仍在排队（queued）的任务能撤回；已下发的会在 items 里逐条说明"无法取消"。
export const cancelOpsTasks = (payload) => http.post('/api/v1/ops/tasks/cancel', payload)

// 删除一条**已结束**的任务记录（排队中/执行中的删不掉，服务端返回 409）
export const deleteOpsTask = (id) => http.del('/api/v1/ops/tasks/' + encodeURIComponent(id))

// ---- 文件分发（file.push）----
//
// 上传与下发是两个步骤：先拿到引用号（fileId），再用它创建任务。
// 不把内容塞进任务参数，是因为服务端的任务存储是**每次状态流转都整体重写**的一份 JSON，
// 内容进去会让它按「条数 × 文件大小」的量级膨胀（见 internal/server/ops/files.go）。
// 单文件上限 256KiB：内容要 base64 后随一轮上报响应下发。
export const uploadOpsFile = (formData, onProgress) => http.upload('/api/v1/ops/files', formData, onProgress)

// 已上传的待分发文件（供复用，不必为同一次分发重复上传）。
export const listOpsFiles = () => http.get('/api/v1/ops/files')
