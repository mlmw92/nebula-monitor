<template>
  <div class="login-page">
    <div class="bg-grid"></div>
    <div class="bg-glow"></div>

    <div class="login-card glass">
      <div class="brand">
        <img v-if="brand.logo" :src="brand.logo" class="logo-img" alt="logo" />
        <div v-else class="logo-mark"></div>
        <div class="brand-text">
          <h1>{{ brand.name || 'NebulaEye' }}</h1>
          <p>服务器监控系统</p>
        </div>
      </div>

      <el-form class="login-form" @submit.prevent="doLogin">
        <el-form-item>
          <el-input
            v-model="username"
            size="large"
            placeholder="用户名"
            :prefix-icon="User"
            autofocus
          />
        </el-form-item>
        <el-form-item>
          <el-input
            v-model="password"
            size="large"
            type="password"
            placeholder="密码"
            :prefix-icon="Lock"
            show-password
            @keyup.enter="doLogin"
          />
        </el-form-item>
        <el-alert
          v-if="error"
          :title="error"
          type="error"
          show-icon
          :closable="false"
          style="margin-bottom: 16px"
        />
        <el-button
          type="primary"
          size="large"
          class="login-btn"
          :loading="loading"
          @click="doLogin"
        >
          登 录
        </el-button>
      </el-form>

      <p class="hint">账号与口令由部署时设定（安装脚本默认生成强口令，请查阅安装日志 / server.yaml）</p>
    </div>

    <footer v-if="brand.footer" class="login-footer">
      {{ brand.footer }}
    </footer>
  </div>
</template>

<script setup>
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { User, Lock } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import http, { setToken } from '../api/http'
import { useBrand } from '../composables/useBrand'

const router = useRouter()
const { brand, loadBrand } = useBrand()
// 拉取最新品牌配置（含系统名称、Logo、页脚），确保登录页即时反映设置
loadBrand()

const username = ref('admin')
const password = ref('')
const error = ref('')
const loading = ref(false)

async function doLogin() {
  error.value = ''
  loading.value = true
  try {
    const d = await http.post('/api/v1/login', {
      username: username.value,
      password: password.value,
    })
    if (d.token) {
      setToken(d.token)
      ElMessage.success('登录成功')
      router.replace('/')
    } else if (d.authEnabled === false) {
      setToken('')
      router.replace('/')
    }
  } catch (e) {
    error.value = e.message || '登录失败'
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.login-page {
  position: fixed;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--bg);
  overflow: hidden;
}
.bg-grid {
  position: absolute;
  inset: 0;
  background-image: linear-gradient(var(--grid-line) 1px, transparent 1px),
    linear-gradient(90deg, var(--grid-line) 1px, transparent 1px);
  background-size: 48px 48px;
  mask-image: radial-gradient(circle at 50% 50%, #000 0%, transparent 70%);
}
.bg-glow {
  position: absolute;
  width: 600px;
  height: 600px;
  border-radius: 50%;
  background: radial-gradient(circle, var(--glow-acc) 0%, transparent 70%);
  top: 50%;
  left: 50%;
  transform: translate(-50%, -50%);
  filter: blur(40px);
}
.login-card {
  position: relative;
  width: 380px;
  padding: 36px 32px 28px;
  border-radius: 16px;
  z-index: 1;
  box-shadow: 0 20px 60px rgba(0, 0, 0, 0.5), 0 0 0 1px var(--border);
}
.brand {
  display: flex;
  align-items: center;
  gap: 14px;
  margin-bottom: 30px;
}
.logo-mark {
  width: 44px;
  height: 44px;
  border-radius: 12px;
  background: var(--brand-grad);
  position: relative;
  box-shadow: 0 0 24px var(--accent-glow);
}
.logo-mark::after {
  content: '';
  position: absolute;
  inset: 10px;
  border: 2px solid var(--brand-ink);
  border-radius: 4px;
  border-top-color: transparent;
  border-right-color: transparent;
  transform: rotate(-45deg);
}
.logo-img {
  width: 44px;
  height: 44px;
  border-radius: 12px;
  object-fit: contain;
  background: var(--card-bg);
  box-shadow: 0 0 24px var(--accent-glow);
}
.brand-text h1 {
  font-size: 20px;
  font-weight: 700;
  letter-spacing: 0.04em;
  color: var(--text);
}
.brand-text p {
  font-size: 12px;
  color: var(--text-dim);
  margin-top: 2px;
}
.login-form {
  display: flex;
  flex-direction: column;
}
.login-btn {
  width: 100%;
  letter-spacing: 0.1em;
  font-weight: 600;
}
.hint {
  text-align: center;
  font-size: 11px;
  color: var(--text-muted);
  margin-top: 20px;
}
.login-footer {
  position: absolute;
  bottom: 20px;
  left: 0;
  right: 0;
  text-align: center;
  font-size: 12px;
  color: var(--text-muted);
  z-index: 1;
}
</style>
