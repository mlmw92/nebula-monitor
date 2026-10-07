// useAuth —— 当前登录用户的授权信息（角色 / 权限 / 资源范围）全局状态。
// 单例 reactive：登录或 App 启动时由 loadMe 拉取 /api/v1/auth/me。
// 仅用于前端菜单/按钮的体验级隐藏；真正的鉴权以服务端为准。
import { reactive } from 'vue'
import http from '../api/http'

const STORAGE_KEY = 'nebula_principal'

function defaults() {
  return {
    loaded: false, // 是否已从 /auth/me 取得有效授权（false 表示单管理员/未启用 RBAC，前端放行）
    username: '',
    displayName: '',
    roles: [], // 角色名数组
    permissions: [], // 权限点数组
    scope: { mode: 'global', groups: [] }, // 资源范围：节点分组 + 业务标签两个维度
    // 业务范围约定的资产标签键（由服务端给出）：表单要显示"正在按哪个标签划范围"，
    // 硬编码在前端的话，部署方改过键之后表单会静默地去查一个不存在的键。
    assetScopeLabelKey: ''
  }
}

function loadCache() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw) {
      const d = JSON.parse(raw)
      if (d && typeof d.username === 'string') {
        return {
          loaded: false, // 缓存仅用于展示用户名/角色，loaded 由 loadMe 真正置位
          username: d.username || '',
          displayName: d.displayName || '',
          roles: Array.isArray(d.roles) ? d.roles : [],
          permissions: [],
          scope: d.scope && d.scope.mode ? { mode: d.scope.mode, groups: Array.isArray(d.scope.groups) ? d.scope.groups : [] } : { mode: 'global', groups: [] }
        }
      }
    }
  } catch (e) {
    /* 损坏缓存忽略 */
  }
  return defaults()
}

// 模块级单例，跨组件共享同一份授权状态
const principal = reactive(loadCache())
let loadPromise = null

function persist() {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({
      username: principal.username,
      displayName: principal.displayName,
      roles: principal.roles,
      scope: principal.scope
    }))
  } catch (e) {
    /* 忽略容量错误 */
  }
}

export function useAuth() {
  // 拉取当前用户授权信息；并发只发一次请求
  function loadMe(force = false) {
    if (loadPromise && !force) return loadPromise
    loadPromise = (async () => {
      try {
        const d = await http.get('/api/v1/auth/me')
        if (d) {
          principal.username = d.username || ''
          principal.displayName = d.displayName || ''
          principal.roles = Array.isArray(d.roles) ? d.roles : []
          principal.permissions = Array.isArray(d.permissions) ? d.permissions : []
          principal.scope = d.scope && d.scope.mode
            ? {
                mode: d.scope.mode,
                groups: Array.isArray(d.scope.groups) ? d.scope.groups : [],
                // 业务维度原样带上：表单要用它回显与拼请求（不用就丢掉，前端少一份隐式状态）
                assetMode: d.scope.assetMode || '',
                assetLabels: Array.isArray(d.scope.assetLabels) ? d.scope.assetLabels : []
              }
            : { mode: 'global', groups: [], assetMode: '', assetLabels: [] }
          principal.assetScopeLabelKey = d.assetScopeLabelKey || ''
          principal.loaded = true
          persist()
        }
      } catch (e) {
        // 503（未启用 RBAC / 单管理员模式）或网络错误：前端视为完全权限
        principal.loaded = false
      } finally {
        loadPromise = null
      }
    })()
    return loadPromise
  }

  // 是否拥有某权限点。未加载授权（单管理员/未启用）时放行，后端兜底。
  function can(perm) {
    if (!principal.loaded) return true
    return principal.permissions.includes(perm)
  }

  // 是否拥有其中任一权限点。
  function canAny(perms) {
    if (!principal.loaded) return true
    return perms.some((p) => principal.permissions.includes(p))
  }

  // 是否能访问某节点分组（资源范围）。未加载或 global 模式放行；restricted 仅放行 scope.groups 内分组。
  function canAccessGroup(group) {
    if (!principal.loaded) return true
    if (principal.scope.mode === 'global') return true
    return principal.scope.groups.includes(group)
  }

  const isSuperAdmin = () => principal.roles.includes('super_admin')
  const manageUsers = () => can('users:manage')
  const manageRoles = () => can('roles:manage')
  const readRoles = () => can('roles:read')

  function clear() {
    principal.username = ''
    principal.displayName = ''
    principal.roles = []
    principal.permissions = []
    principal.scope = { mode: 'global', groups: [] }
    principal.loaded = false
    try { localStorage.removeItem(STORAGE_KEY) } catch (e) { /* ignore */ }
  }

  return { principal, loadMe, can, canAny, canAccessGroup, isSuperAdmin, manageUsers, manageRoles, readRoles, clear }
}

export default useAuth
