<template>
  <div class="settings-layout">
    <aside class="settings-sider">
      <div class="sider-head">
        <el-icon :size="18"><Setting /></el-icon>
        <span>系统设置</span>
      </div>
      <nav class="settings-nav">
        <router-link
          v-for="item in menus"
          :key="item.key"
          :to="item.to"
          class="nav-item"
          :class="{ active: route.path === item.to }"
        >
          <el-icon :size="16"><component :is="item.icon" /></el-icon>
          <span>{{ item.label }}</span>
        </router-link>
      </nav>
    </aside>

    <main class="settings-main">
      <router-view v-slot="{ Component }">
        <component :is="Component" />
      </router-view>
    </main>
  </div>
</template>

<script setup>
import { useRoute } from 'vue-router'
import { Setting, Shop } from '@element-plus/icons-vue'

const route = useRoute()

const menus = [
  { key: 'brand', to: '/system/settings/brand', label: '站点与品牌', icon: Shop },
]
</script>

<style scoped>
.settings-layout {
  display: flex;
  min-height: calc(100vh - 52px - 36px);
  gap: 20px;
}
.settings-sider {
  width: 220px;
  flex-shrink: 0;
  background: var(--card-bg);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 16px 0;
}
.sider-head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 0 18px 14px;
  margin: 0 12px 8px;
  border-bottom: 1px solid var(--border);
  font-weight: 600;
  font-size: 15px;
}
.settings-nav {
  display: flex;
  flex-direction: column;
  padding: 0 12px;
  gap: 4px;
}
.nav-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  border-radius: 8px;
  color: var(--text-dim);
  font-size: 13px;
  text-decoration: none;
  transition: all 0.15s;
}
.nav-item:hover {
  background: rgba(255, 255, 255, 0.04);
  color: var(--text);
}
.nav-item.active {
  background: var(--accent-dim);
  color: var(--accent);
}
.settings-main {
  flex: 1;
  min-width: 0;
}
</style>
