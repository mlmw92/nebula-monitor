// 资产类型的**单一来源**：从 `GET /api/v1/asset-types` 取（配置项模型接口）。
//
// 为什么要有它：类型选项原先在**多处**硬编码（列表筛选、新建表单、类型显示名、巡检页两处），
// 于是"加一个类型"要改好几处前端。现在都读这里。
//
// 两条约束：
//   ① **接口不可得时退回内置类型**，而不是让下拉变空——那会让人以为"平台不认识这些类型"；
//   ② 只加载一次（模块级缓存），几个页面共用；改过模型后调 refresh() 即可。
import { reactive, readonly } from 'vue'
import { getAssetTypes } from '../api/asset'

// FALLBACK_TYPES 是内置类型的兜底（与后端 BuiltinTypes 一致）。
// 它只在接口不可用时生效——**不是**第二份"权威清单"，别在这里加新类型。
export const FALLBACK_TYPES = [
  { key: 'host', title: '主机' },
  { key: 'middleware-instance', title: '中间件实例' },
  { key: 'pod', title: '容器（Pod）' },
  { key: 'workload', title: '工作负载' },
]

const state = reactive({
  types: [...FALLBACK_TYPES],
  // fallback 为 true 表示当前用的是兜底清单（接口没拿到）
  fallback: true,
  loaded: false,
  loading: false,
  error: '',
})

let inflight = null

// loadAssetTypes 拉一次类型清单（并发调用共用同一个请求）。
export async function loadAssetTypes() {
  if (inflight) return inflight
  state.loading = true
  inflight = (async () => {
    try {
      const data = await getAssetTypes()
      const list = (data && data.types) || []
      if (list.length) {
        state.types = list.map((t) => ({
          key: t.key, title: t.title || t.key, ephemeral: !!t.ephemeral, assets: t.assets || 0,
        }))
        state.fallback = false
      }
      state.error = ''
    } catch (e) {
      // 拿不到就用内置清单（页面照常可用），但把原因留在状态里给需要的人看
      state.error = e.message || '读取资产类型失败'
    } finally {
      state.loaded = true
      state.loading = false
      inflight = null
    }
  })()
  return inflight
}

// refreshAssetTypes 强制重新拉（改过模型之后用）。
export function refreshAssetTypes() {
  inflight = null
  return loadAssetTypes()
}

// useAssetTypes 给组件用：返回清单与按类型键取显示名的方法。
export function useAssetTypes() {
  if (!state.loaded && !state.loading) loadAssetTypes()
  const labelOf = (typeKey) => {
    const hit = state.types.find((t) => t.key === typeKey)
    // 找不到就原样显示键：宁可显示 `foo`，也不要显示空白或编一个名字
    return (hit && hit.title) || typeKey
  }
  return { state: readonly(state), types: state.types, labelOf, loadAssetTypes, refreshAssetTypes }
}
