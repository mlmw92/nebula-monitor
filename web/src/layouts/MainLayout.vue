<template>
  <div class="layout">
    <Sidebar :alert-count="alertCount" :collapsed="collapsed" @toggle="collapsed = !collapsed" @logout="logout" />

    <div class="main-wrap" :class="{ collapsed }">
      <header class="topbar glass">
        <div class="topbar-left">
          <el-button link @click="collapsed = !collapsed">
            <el-icon :size="18"><Expand v-if="collapsed" /><Fold v-else /></el-icon>
          </el-button>
          <el-breadcrumb separator="/">
            <el-breadcrumb-item :to="{ path: '/' }">首页</el-breadcrumb-item>
            <el-breadcrumb-item v-if="$route.name !== 'overview'">{{ pageTitle }}</el-breadcrumb-item>
          </el-breadcrumb>
        </div>
        <div class="topbar-right">
          <div class="screen-entry" @click="$router.push('/screen')">
            <span class="screen-icon"><el-icon :size="16"><DataAnalysis /></el-icon></span>
            <span class="screen-text">数据大屏</span>
          </div>
          <el-tooltip content="切换配色主题" placement="bottom">
            <el-dropdown trigger="click" @command="changeTheme">
              <el-button circle size="small">
                <span class="theme-dot" :style="{ background: themeColor }"></span>
              </el-button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item command="b"><span class="td-dot" style="background:#4a9df0"></span>极光蓝（默认）</el-dropdown-item>
                  <el-dropdown-item command="a"><span class="td-dot" style="background:#00d9a3"></span>星云青绿</el-dropdown-item>
                  <el-dropdown-item command="c"><span class="td-dot" style="background:#8b5cf6"></span>星河紫</el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
          </el-tooltip>
          <el-tooltip content="刷新数据" placement="bottom">
            <el-button :icon="Refresh" circle size="small" @click="refresh" />
          </el-tooltip>
          <el-badge :value="alertCount" :hidden="!alertCount" :max="99">
            <el-button :icon="Bell" circle size="small" @click="$router.push('/alerts')" />
          </el-badge>
          <el-dropdown trigger="click">
            <el-button circle size="small">
              <el-icon><User /></el-icon>
            </el-button>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item>{{ username }}</el-dropdown-item>
                <el-dropdown-item divided @click="logout">退出登录</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </header>

      <main class="content">
        <router-view v-slot="{ Component, route }">
          <keep-alive :include="['OverviewView', 'HostsView', 'AlertsView']">
            <component :is="Component" :key="route.fullPath" ref="view" />
          </keep-alive>
        </router-view>
      </main>

      <footer v-if="brand.footer" class="global-footer">
        {{ brand.footer }}
      </footer>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { Expand, Fold, Refresh, Bell, User, DataAnalysis } from '@element-plus/icons-vue'
import Sidebar from '../components/Sidebar.vue'
import http, { setToken } from '../api/http'
import { connectWS } from '../api/ws'
import { useBrand } from '../composables/useBrand'

const router = useRouter()
const route = useRoute()
const { brand } = useBrand()
const collapsed = ref(false)
const alertCount = ref(0)
const username = ref(localStorage.getItem('nebula_user') || 'admin')
const view = ref(null)

/* ===== 换肤 ===== */
const THEMES = { b: '#4a9df0', a: '#00d9a3', c: '#8b5cf6' }
const theme = ref(localStorage.getItem('nebula_theme') || 'b')
const themeColor = computed(() => THEMES[theme.value] || THEMES.b)
function applyTheme(t) {
  theme.value = t
  document.body.dataset.theme = t
  localStorage.setItem('nebula_theme', t)
}
function changeTheme(t) {
  applyTheme(t)
  // 触发图表组件重新取色
  window.dispatchEvent(new CustomEvent('nebula:theme-changed', { detail: t }))
}
applyTheme(theme.value)

const pageTitle = computed(() => {
  const m = {
    overview: '首页概览',
    hosts: '主机列表',
    node: '主机详情',
    middleware: '中间件监控',
    alerts: '告警中心',
    dialtest: '服务拨测',
    report: '巡检报告',
    'system-upgrade': '系统升级',
    notify: '通知配置',
    'system-settings-brand': '系统设置',
    'system-settings-footer': '系统设置',
  }
  return m[route.name] || ''
})

let ws = null
let timer = null
let visible = true
// 告警广播可能连续推送，防抖合并 HTTP 请求，避免连锁刷新
let alertDebounceTimer = null

async function refreshAlertCount() {
  if (!visible) return
  try {
    const d = await http.get('/api/v1/alerts?state=active')
    alertCount.value = (d.alerts || []).length
  } catch (e) {
    /* ignore */
  }
}

function onAlertPushed() {
  if (alertDebounceTimer) return
  alertDebounceTimer = setTimeout(() => {
    alertDebounceTimer = null
    refreshAlertCount()
  }, 3000)
}

function refresh() {
  view.value && view.value.reload && view.value.reload()
  refreshAlertCount()
}

function logout() {
  setToken('')
  localStorage.removeItem('nebula_user')
  ws && ws.close()
  router.replace('/login')
}

function onVisibility() {
  visible = document.visibilityState === 'visible'
  if (visible) {
    refreshAlertCount()
    if (!timer) timer = setInterval(refreshAlertCount, 30000)
  } else {
    if (timer) {
      clearInterval(timer)
      timer = null
    }
  }
}

onMounted(() => {
  refreshAlertCount()
  ws = connectWS('alerts', null, { onMessage: onAlertPushed })
  timer = setInterval(refreshAlertCount, 30000)
  document.addEventListener('visibilitychange', onVisibility)
})
onUnmounted(() => {
  ws && ws.close()
  timer && clearInterval(timer)
  if (alertDebounceTimer) clearTimeout(alertDebounceTimer)
  document.removeEventListener('visibilitychange', onVisibility)
})
</script>

<style scoped>
.layout {
  display: flex;
  min-height: 100vh;
}
.main-wrap {
  flex: 1;
  margin-left: var(--sidebar-w);
  transition: margin-left 0.2s;
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.main-wrap.collapsed {
  margin-left: 64px;
}
.topbar {
  position: sticky;
  top: 0;
  z-index: 40;
  height: 52px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 16px;
  border-radius: 0;
  border-left: none;
  border-right: none;
  border-top: none;
}
.topbar-left {
  display: flex;
  align-items: center;
  gap: 12px;
}
.topbar-right {
  display: flex;
  align-items: center;
  gap: 8px;
}
.theme-dot {
  width: 12px;
  height: 12px;
  border-radius: 50%;
  display: inline-block;
  box-shadow: 0 0 6px var(--accent-glow);
}
.td-dot {
  display: inline-block;
  width: 10px;
  height: 10px;
  border-radius: 50%;
  margin-right: 8px;
  vertical-align: middle;
}
/* 数据大屏入口：带呼吸光效的芯片按钮 */
.screen-entry {
  position: relative;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 6px 14px;
  border-radius: 20px;
  cursor: pointer;
  font-size: 13px;
  font-weight: 600;
  letter-spacing: 0.02em;
  color: #fff;
  background: linear-gradient(135deg, #7c3aed 0%, #6366f1 100%);
  border: 1px solid rgba(124, 58, 237, 0.5);
  box-shadow: 0 0 16px rgba(124, 58, 237, 0.35), inset 0 1px 0 rgba(255,255,255,0.1);
  user-select: none;
  transition: transform 0.2s, box-shadow 0.2s;
  overflow: hidden;
}
.screen-entry::before {
  content: '';
  position: absolute;
  inset: 0;
  border-radius: 20px;
  background: linear-gradient(135deg, rgba(255,255,255,0) 0%, rgba(255,255,255,0.08) 50%, rgba(255,255,255,0) 100%);
  animation: screen-shimmer 2.4s ease-in-out infinite;
}
.screen-entry::after {
  content: '';
  position: absolute;
  inset: -2px;
  border-radius: 22px;
  background: linear-gradient(135deg, #7c3aed, #06b6d4, #8b5cf6);
  z-index: -1;
  opacity: 0;
  transition: opacity 0.3s;
}
.screen-entry:hover {
  transform: translateY(-1px);
  box-shadow: 0 0 24px rgba(124, 58, 237, 0.55), 0 4px 12px rgba(0,0,0,0.25);
}
.screen-entry:hover::after {
  opacity: 0.5;
  animation: screen-pulse 1.5s ease-in-out infinite;
}
.screen-entry:active {
  transform: translateY(0);
}
.screen-icon {
  display: flex;
  align-items: center;
  opacity: 0.9;
}
.screen-text {
  white-space: nowrap;
  text-shadow: 0 1px 2px rgba(0,0,0,0.3);
}
.screen-badge {
  font-size: 9px;
  font-weight: 700;
  letter-spacing: 0.12em;
  color: #7c3aed;
  background: #fff;
  border-radius: 4px;
  padding: 1px 5px;
  line-height: 1;
  animation: screen-badge-blink 2s step-end infinite;
}
@keyframes screen-shimmer {
  0%, 100% { transform: translateX(-100%); }
  50% { transform: translateX(100%); }
}
@keyframes screen-pulse {
  0%, 100% { opacity: 0.3; }
  50% { opacity: 0.7; }
}
@keyframes screen-badge-blink {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.6; }
}
.content {
  flex: 1;
  padding: 18px 20px;
  max-width: 1700px;
  width: 100%;
  margin: 0 auto;
}
.global-footer {
  padding: 14px 20px;
  text-align: center;
  font-size: 12px;
  color: var(--text-muted);
  border-top: 1px solid var(--border);
  background: var(--card-bg);
}
</style>
