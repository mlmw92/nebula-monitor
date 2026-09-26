import { createRouter, createWebHashHistory } from 'vue-router'
import { getToken } from '../api/http'
import { useAuth } from '../composables/useAuth'

const routes = [
  {
    path: '/login',
    name: 'login',
    component: () => import('../components/LoginView.vue'),
    meta: { public: true },
  },
  {
    path: '/screen',
    name: 'screen',
    component: () => import('../components/screen/ScreenView.vue'),
  },
  {
    path: '/',
    component: () => import('../layouts/MainLayout.vue'),
    children: [
      { path: '', name: 'overview', component: () => import('../components/OverviewView.vue') },
      { path: 'hosts', name: 'hosts', component: () => import('../components/HostsView.vue'), meta: { perm: 'nodes:read' } },
      { path: 'node/:name', name: 'node', component: () => import('../components/NodeView.vue'), props: true, meta: { perm: 'nodes:read' } },
      { path: 'middleware', name: 'middleware', component: () => import('../components/MiddlewareView.vue'), meta: { perm: 'middleware:read' } },
      { path: 'alerts', name: 'alerts', component: () => import('../components/AlertsView.vue') },
      { path: 'intelligence', name: 'intelligence', component: () => import('../components/intelligence/IntelligenceView.vue') },
      { path: 'dialtest', name: 'dialtest', component: () => import('../components/DialTestView.vue') },
      { path: 'report', name: 'report', component: () => import('../components/ReportView.vue') },
      { path: 'system/upgrade', name: 'system-upgrade', component: () => import('../components/UpgradeView.vue') },
      { path: 'notify', name: 'notify', component: () => import('../components/NotifyView.vue') },
      { path: 'system/settings', name: 'system-settings', component: () => import('../components/settings/SettingsView.vue') },
      { path: 'metrics/explore', name: 'metrics-explore', component: () => import('../components/metrics/MetricsExploreView.vue'), meta: { perm: 'nodes:read' } },
      { path: 'system/dashboards', name: 'system-dashboards', component: () => import('../components/dashboard/DashboardView.vue') },
      { path: 'security', name: 'security', component: () => import('../components/SecurityView.vue') },
      { path: 'audit', name: 'audit', component: () => import('../components/AuditView.vue') },
      { path: 'system/users', name: 'system-users', component: () => import('../components/rbac/UsersView.vue'), meta: { perm: 'users:manage' } },
      { path: 'system/roles', name: 'system-roles', component: () => import('../components/rbac/RolesView.vue'), meta: { perm: 'roles:read' } },
      { path: 'system/profile', name: 'system-profile', component: () => import('../components/profile/ProfileView.vue') },
    ],
  },
]

const router = createRouter({
  history: createWebHashHistory(),
  routes,
  scrollBehavior() {
    return { top: 0 }
  },
})

// 全局前置守卫：未登录跳 /login；带权限要求的路由做二次校验（菜单已隐藏时再兜底）。
// 注意：前端隐藏仅为体验，真正鉴权以服务端为准；principal 未加载（单管理员/未启用 RBAC）
// 时放行，避免误拦。
const auth = useAuth()
router.beforeEach((to) => {
  if (to.meta.public) return true
  if (to.meta.perm) {
    // 已加载授权且明确无权限：拦截到首页
    if (auth.principal.loaded && !auth.can(to.meta.perm)) return { path: '/' }
  }
  return true
})

export default router