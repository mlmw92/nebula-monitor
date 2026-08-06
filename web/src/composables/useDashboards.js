// useDashboards —— 自定义仪表盘全局状态（单例 reactive）。
// 仪表盘配置服务端共享（YAML 落盘），此 composable 仅做内存缓存 + localStorage 兜底，
// 每次进入页面从服务端拉取最新。
import { reactive } from 'vue'
import http from '../api/http'

const STORAGE_KEY = 'nebula_dashboards'

function loadCache() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw) {
      const arr = JSON.parse(raw)
      if (Array.isArray(arr)) return arr
    }
  } catch (e) {
    /* 忽略损坏缓存 */
  }
  return []
}

const state = reactive({ dashboards: loadCache(), loaded: false })

function persist() {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(state.dashboards))
  } catch (e) {
    /* 忽略容量错误 */
  }
}

export function useDashboards() {
  async function load(force = false) {
    if (state.loaded && !force) return state.dashboards
    try {
      const d = await http.listDashboards()
      if (d && Array.isArray(d.dashboards)) {
        state.dashboards = d.dashboards
        state.loaded = true
        persist()
      }
    } catch (e) {
      // 接口不可达：保留缓存
    }
    return state.dashboards
  }

  async function create(name, panels) {
    const d = await http.createDashboard(name, panels)
    await load(true)
    return d
  }

  async function update(id, name, panels) {
    const d = await http.updateDashboard(id, name, panels)
    await load(true)
    return d
  }

  async function remove(id) {
    const d = await http.deleteDashboard(id)
    await load(true)
    return d
  }

  return { state, load, create, update, remove }
}

export default useDashboards
