<template>
  <router-view />
</template>

<script setup>
import { onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import http, { getToken, setToken } from './api/http'
import { useBrand } from './composables/useBrand'
import { useAuth } from './composables/useAuth'

const router = useRouter()
const { loadBrand } = useBrand()
const auth = useAuth()

async function checkAuth() {
  try {
    const d = await http.get('/api/v1/auth-info')
    if (d.authEnabled && !getToken()) {
      router.replace('/login')
    } else if (getToken()) {
      // 已登录：拉取当前用户授权信息（角色/权限/范围），供菜单与按钮隐藏使用
      try { await auth.loadMe() } catch (e) { /* 授权信息非阻塞，前端放行 */ }
    }
  } catch {
    /* 接口不可达，放行 */
  }
}

function onAuthExpired() {
  setToken('')
  router.replace('/login')
}

onMounted(() => {
  checkAuth()
  loadBrand()
  window.addEventListener('auth-expired', onAuthExpired)
})
onUnmounted(() => {
  window.removeEventListener('auth-expired', onAuthExpired)
})
</script>
