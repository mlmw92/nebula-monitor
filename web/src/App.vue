<template>
  <router-view />
  <SiteFooter />
</template>

<script setup>
import SiteFooter from './components/SiteFooter.vue'
import { onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import http, { getToken, setToken } from './api/http'
import { useBrand } from './composables/useBrand'

const router = useRouter()
const { loadBrand } = useBrand()

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
  loadBrand()
  window.addEventListener('auth-expired', onAuthExpired)
})
onUnmounted(() => {
  window.removeEventListener('auth-expired', onAuthExpired)
})
</script>
