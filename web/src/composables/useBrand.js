// useBrand —— 系统品牌（名称/Logo）全局状态。
// 单例 reactive：所有组件共享；启动时由 App 调用 loadBrand 拉取，
// 未登录时 GET /api/v1/ui/settings 由后端放行，登录页也能展示自定义品牌。
import { reactive } from 'vue'
import http from '../api/http'

const STORAGE_KEY = 'nebula_brand'

function defaults() {
  return { name: 'NebulaEye', logo: '' }
}

function loadCache() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw) {
      const d = JSON.parse(raw)
      if (d && typeof d.name === 'string') return { name: d.name, logo: d.logo || '' }
    }
  } catch (e) {
    /* 损坏缓存忽略 */
  }
  return defaults()
}

// 模块级单例，跨组件共享同一份品牌状态
const brand = reactive(loadCache())
let loadPromise = null

function persist() {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ name: brand.name, logo: brand.logo }))
  } catch (e) {
    /* 忽略容量错误 */
  }
}

function applyTitle() {
  document.title = brand.name ? `${brand.name} · 服务器监控` : '服务器监控系统'
}

export function useBrand() {
  // 拉取最新品牌配置；并发只发一次请求
  function loadBrand(force = false) {
    if (loadPromise && !force) return loadPromise
    loadPromise = (async () => {
      try {
        const d = await http.get('/api/v1/ui/settings')
        if (d && d.name) {
          brand.name = d.name
          brand.logo = d.logo || ''
          persist()
        }
      } catch (e) {
        // 接口不可达：保留缓存/默认值
      } finally {
        applyTitle()
      }
    })()
    return loadPromise
  }

  // 保存品牌配置（需登录），成功后更新内存与缓存
  async function saveBrand(name, logo) {
    const d = await http.put('/api/v1/ui/settings', { name, logo })
    if (d && d.config) {
      brand.name = d.config.name || name
      brand.logo = d.config.logo || ''
      persist()
      applyTitle()
    }
    return d
  }

  return { brand, loadBrand, saveBrand, applyTitle }
}

export default useBrand
