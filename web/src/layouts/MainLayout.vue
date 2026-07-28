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
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { Expand, Fold, Refresh, Bell, User } from '@element-plus/icons-vue'
import Sidebar from '../components/Sidebar.vue'
import http, { setToken } from '../api/http'
import { connectWS } from '../api/ws'

const router = useRouter()
const route = useRoute()
const collapsed = ref(false)
const alertCount = ref(0)
const username = ref(localStorage.getItem('nebula_user') || 'admin')
const view = ref(null)

const pageTitle = computed(() => {
  const m = { overview: '首页概览', hosts: '主机列表', node: '主机详情', alerts: '告警中心' }
  return m[route.name] || ''
})

let ws = null
let timer = null
let visible = true

async function refreshAlertCount() {
  if (!visible) return
  try {
    const d = await http.get('/api/v1/alerts?state=active')
    alertCount.value = (d.alerts || []).length
  } catch (e) {
    /* ignore */
  }
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
  ws = connectWS('alerts', null, { onMessage: () => refreshAlertCount() })
  timer = setInterval(refreshAlertCount, 30000)
  document.addEventListener('visibilitychange', onVisibility)
})
onUnmounted(() => {
  ws && ws.close()
  timer && clearInterval(timer)
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
.content {
  flex: 1;
  padding: 18px 20px;
  max-width: 1700px;
  width: 100%;
  margin: 0 auto;
}
</style>
