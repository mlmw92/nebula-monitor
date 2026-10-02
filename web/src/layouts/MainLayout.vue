<template>
  <div class="layout">
    <Sidebar
      :alert-count="alertCount"
      :collapsed="collapsed"
      :username="username"
      :role-labels="roleLabels"
      @toggle="collapsed = !collapsed"
      @logout="logout"
    />

    <div class="main-wrap" :class="{ collapsed }">
      <AppTopBar
        :collapsed="collapsed"
        :alert-count="alertCount"
        :username="username"
        :role-labels="roleLabels"
        @toggle="collapsed = !collapsed"
        @refresh="refresh"
        @logout="logout"
        @open-search="openSearch"
      />

      <main class="content">
        <router-view v-slot="{ Component, route }">
          <keep-alive :include="['OverviewView', 'HostsView', 'AlertsView']">
            <component :is="Component" :key="route.fullPath" ref="view" />
          </keep-alive>
        </router-view>
      </main>

      <SiteFooter />
    </div>

    <CommandPalette ref="palette" />
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import Sidebar from '../components/Sidebar.vue'
import AppTopBar from '../components/AppTopBar.vue'
import CommandPalette from '../components/CommandPalette.vue'
import SiteFooter from '../components/SiteFooter.vue'
import http, { setToken } from '../api/http'
import { connectWS } from '../api/ws'
import { useAuth } from '../composables/useAuth'

const router = useRouter()
const auth = useAuth()
const collapsed = ref(false)
const alertCount = ref(0)
const username = ref(localStorage.getItem('nebula_user') || 'admin')
const view = ref(null)
const palette = ref(null)

function openSearch() {
  palette.value && palette.value.open()
}

// 角色名 → 中文展示标签
const ROLE_LABELS = {
  super_admin: '超级管理员',
  ops_admin: '运维管理员',
  alert_admin: '告警管理员',
  security_admin: '安全管理员',
  read_only: '只读用户',
  audit: '审计用户',
}
const roleLabels = computed(() =>
  auth.principal.roles.map((r) => ROLE_LABELS[r] || r)
)

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
  auth.clear()
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
.content {
  flex: 1;
  padding: 18px 20px;
  width: 100%;
}
</style>
