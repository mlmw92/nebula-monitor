<template>
  <aside class="sidebar glass" :class="{ collapsed }">
    <div class="brand">
      <div class="logo-mark"></div>
      <div class="brand-text" v-show="!collapsed">
        <h1>NebulaEye</h1>
        <p>监控中心</p>
      </div>
    </div>

    <nav class="nav">
      <router-link
        v-for="item in items"
        :key="item.key"
        :to="item.to"
        class="nav-item"
        :class="{ active: isActive(item) }"
      >
        <el-icon :size="18"><component :is="item.icon" /></el-icon>
        <span class="label" v-show="!collapsed">{{ item.label }}</span>
        <el-badge
          v-if="item.key === 'alerts' && alertCount > 0"
          :value="alertCount"
          :max="99"
          class="nav-badge"
        />
      </router-link>
    </nav>

    <!-- 版本信息 -->
    <div class="version-info" v-show="!collapsed">
      <div class="ver-row">
        <span class="ver-label">Web</span>
        <span class="ver-val">{{ webVersion }}</span>
      </div>
      <div class="ver-row">
        <span class="ver-label">Server</span>
        <span class="ver-val" :class="serverVersion === '...' ? 'loading' : ''">{{ serverVersion }}</span>
      </div>
    </div>

    <div class="sidebar-footer">
      <el-button link class="toggle-btn" @click="$emit('toggle')">
        <el-icon :size="18"><Fold v-if="!collapsed" /><Expand v-else /></el-icon>
        <span v-show="!collapsed" class="label">收起</span>
      </el-button>
    </div>
  </aside>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { Odometer, Monitor, Bell } from '@element-plus/icons-vue'
import http from '../api/http'
import { WEB_VERSION } from '../version'

defineProps({
  collapsed: Boolean,
  alertCount: { type: Number, default: 0 },
})
defineEmits(['toggle', 'logout'])

const route = useRoute()

const webVersion = WEB_VERSION
const serverVersion = ref('...')

const items = [
  { key: 'overview', to: '/', label: '首页概览', icon: Odometer },
  { key: 'hosts', to: '/hosts', label: '主机列表', icon: Monitor },
  { key: 'alerts', to: '/alerts', label: '告警中心', icon: Bell },
]

function isActive(item) {
  if (item.key === 'overview') return route.path === '/'
  return route.path.startsWith(item.to)
}

async function loadVersion() {
  try {
    const ver = await http.get('/api/v1/version')
    serverVersion.value = ver.server || '-'
  } catch (e) {
    serverVersion.value = '-'
  }
}

onMounted(loadVersion)
</script>

<style scoped>
.sidebar {
  position: fixed;
  left: 0;
  top: 0;
  bottom: 0;
  width: var(--sidebar-w);
  display: flex;
  flex-direction: column;
  padding: 16px 12px;
  border-radius: 0;
  border-right: 1px solid var(--border);
  border-left: none;
  border-top: none;
  border-bottom: none;
  z-index: 50;
  transition: width 0.2s;
  overflow: hidden;
}
.sidebar.collapsed {
  width: 64px;
}
.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 4px 6px 16px;
  border-bottom: 1px solid var(--border);
  margin-bottom: 12px;
  height: 52px;
}
.logo-mark {
  flex-shrink: 0;
  width: 36px;
  height: 36px;
  border-radius: 10px;
  background: linear-gradient(135deg, var(--accent), #00a37a);
  box-shadow: 0 0 16px var(--accent-glow);
  position: relative;
}
.logo-mark::after {
  content: '';
  position: absolute;
  inset: 9px;
  border: 2px solid #002b22;
  border-radius: 3px;
  border-top-color: transparent;
  border-right-color: transparent;
  transform: rotate(-45deg);
}
.brand-text h1 {
  font-size: 15px;
  font-weight: 700;
  letter-spacing: 0.03em;
  white-space: nowrap;
}
.brand-text p {
  font-size: 10px;
  color: var(--text-dim);
}
.nav {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.nav-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 12px;
  color: var(--text-dim);
  font-size: 13px;
  border-radius: 8px;
  text-decoration: none;
  transition: all 0.15s;
  position: relative;
}
.nav-item:hover {
  background: rgba(255, 255, 255, 0.04);
  color: var(--text);
}
.nav-item.active {
  background: var(--accent-dim);
  color: var(--accent);
}
.nav-item.active::before {
  content: '';
  position: absolute;
  left: -12px;
  top: 50%;
  transform: translateY(-50%);
  width: 3px;
  height: 18px;
  background: var(--accent);
  border-radius: 0 2px 2px 0;
}
.label {
  white-space: nowrap;
}
.nav-badge {
  margin-left: auto;
}
/* 版本信息 */
.version-info {
  padding: 8px 12px;
  border-top: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.ver-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 11px;
}
.ver-label {
  color: var(--text-muted);
}
.ver-val {
  color: var(--text-dim);
  font-family: var(--mono);
  font-size: 10px;
}
.ver-val.loading {
  opacity: 0.5;
}
.sidebar-footer {
  padding-top: 10px;
  border-top: 1px solid var(--border);
}
.toggle-btn {
  width: 100%;
  justify-content: flex-start;
  gap: 12px;
  color: var(--text-dim);
  height: 36px;
  padding: 0 12px;
}
.sidebar.collapsed .toggle-btn {
  justify-content: center;
}
</style>
