<template>
  <router-view />
</template>

<script setup>
import { onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import http, { getToken, setToken } from './api/http'

const router = useRouter()

async function checkAuth() {
  try {
    const d = await http.get('/api/v1/auth-info')
    if (d.authEnabled && !getToken()) {
      router.replace('/login')
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
  window.addEventListener('auth-expired', onAuthExpired)
})
onUnmounted(() => {
  window.removeEventListener('auth-expired', onAuthExpired)
})
</script>
