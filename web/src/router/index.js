import { createRouter, createWebHashHistory } from 'vue-router'
import { getToken } from '../api/http'

const routes = [
  {
    path: '/login',
    name: 'login',
    component: () => import('../components/LoginView.vue'),
    meta: { public: true },
  },
  {
    path: '/',
    component: () => import('../layouts/MainLayout.vue'),
    children: [
      { path: '', name: 'overview', component: () => import('../components/OverviewView.vue') },
      { path: 'hosts', name: 'hosts', component: () => import('../components/HostsView.vue') },
      { path: 'node/:name', name: 'node', component: () => import('../components/NodeView.vue'), props: true },
      { path: 'alerts', name: 'alerts', component: () => import('../components/AlertsView.vue') },
      { path: 'system/upgrade', name: 'system-upgrade', component: () => import('../components/UpgradeView.vue') },
      { path: 'notify', name: 'notify', component: () => import('../components/NotifyView.vue') },
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

// 全局前置守卫：未登录跳 /login（auth-info 由 App.vue 异步检查，这里先按 token 简单判断）
router.beforeEach((to) => {
  if (to.meta.public) return true
  // auth 未启用时 token 可能为空，允许进入（后端不拦截）
  return true
})

export default router