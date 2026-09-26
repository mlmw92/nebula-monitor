// useAuth 决定前端菜单/按钮的显隐。它只是体验层（真正鉴权在服务端），
// 但「未加载即放行」这条兼容语义一旦写反，会导致单管理员部署整站菜单消失。
import { describe, it, expect, beforeEach } from 'vitest'
import useAuth from './useAuth'

const { principal, can, canAny, canAccessGroup, isSuperAdmin, clear } = useAuth()

describe('useAuth 前端权限判定', () => {
  beforeEach(() => {
    clear() // → loaded=false、permissions=[]、scope=global
  })

  it('未加载授权时一律放行（单管理员 / 未启用 RBAC 的兼容语义）', () => {
    expect(principal.loaded).toBe(false)
    expect(can('users:manage')).toBe(true)
    expect(canAny(['whatever'])).toBe(true)
    expect(canAccessGroup('any-group')).toBe(true)
  })

  it('已加载时按权限点精确判定', () => {
    principal.loaded = true
    principal.permissions = ['nodes:read']

    expect(can('nodes:read')).toBe(true)
    expect(can('nodes:write')).toBe(false)
    expect(canAny(['nodes:write', 'nodes:read'])).toBe(true)
    expect(canAny(['nodes:write', 'agent:upgrade'])).toBe(false)
  })

  it('资源范围：global 全放行，restricted 仅放行 scope.groups 内分组', () => {
    principal.loaded = true

    principal.scope = { mode: 'global', groups: [] }
    expect(canAccessGroup('g2')).toBe(true)

    principal.scope = { mode: 'restricted', groups: ['g1'] }
    expect(canAccessGroup('g1')).toBe(true)
    expect(canAccessGroup('g2')).toBe(false)
    // restricted 但分组为空 = 无资源权限，不得放大为全部
    principal.scope = { mode: 'restricted', groups: [] }
    expect(canAccessGroup('g1')).toBe(false)
  })

  it('超管判定基于角色名', () => {
    principal.loaded = true
    principal.roles = ['super_admin']
    expect(isSuperAdmin()).toBe(true)
    principal.roles = ['ops_admin']
    expect(isSuperAdmin()).toBe(false)
  })

  it('clear 复位全部状态并清理本地缓存', () => {
    principal.loaded = true
    principal.username = 'alice'
    principal.permissions = ['nodes:read']
    principal.scope = { mode: 'restricted', groups: ['g1'] }
    localStorage.setItem('nebula_principal', JSON.stringify({ username: 'alice' }))

    clear()

    expect(principal.loaded).toBe(false)
    expect(principal.username).toBe('')
    expect(principal.roles).toEqual([])
    expect(principal.permissions).toEqual([])
    expect(principal.scope).toEqual({ mode: 'global', groups: [] })
    expect(localStorage.getItem('nebula_principal')).toBeNull()
  })
})
