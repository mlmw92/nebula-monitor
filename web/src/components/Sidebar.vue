<template>
  <aside class="sidebar glass" :class="{ collapsed }">
    <nav class="nav">
      <router-link
        v-for="item in flatItems"
        :key="item.key"
        :to="item.to"
        class="nav-item"
        :class="{ active: isActiveItem(item) }"
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

      <div
        v-for="g in groups"
        :key="g.key"
        class="nav-group"
        :class="{ 'group-open': isGroupOpen(g), 'group-active': isGroupActive(g) }"
      >
        <div class="nav-group-title" @click="onGroupClick(g)">
          <el-icon :size="18"><component :is="g.icon" /></el-icon>
          <span class="label" v-show="!collapsed">{{ g.label }}</span>
          <el-icon v-show="!collapsed" class="caret"><ArrowDown v-if="isGroupOpen(g)" /><ArrowRight v-else /></el-icon>
        </div>
        <div v-show="!collapsed && isGroupOpen(g)" class="nav-group-items">
          <router-link
            v-for="sub in g.items"
            :key="sub.key"
            :to="sub.to"
            class="nav-subitem"
            :class="{ active: isActiveItem(sub) }"
          >
            <span class="sub-dot"></span>
            <span class="label">{{ sub.label }}</span>
          </router-link>
        </div>
      </div>
    </nav>

    <!-- 版本信息（统一取 Server 运行版本，不再区分 Web/Server 版本） -->
    <div class="version-info" v-show="!collapsed">
      <div class="ver-row">
        <span class="ver-label">版本</span>
        <span class="ver-val">{{ serverVersion }}</span>
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
import {
  Odometer,
  Monitor,
  Bell,
  Message,
  Connection,
  Document,
  Setting,
  ArrowDown,
  ArrowRight,
} from '@element-plus/icons-vue'
import http from '../api/http'
import { WEB_VERSION } from '../version'

const props = defineProps({
  collapsed: Boolean,
  alertCount: { type: Number, default: 0 },
})
const emit = defineEmits(['toggle', 'logout'])

const route = useRoute()

const serverVersion = ref(WEB_VERSION) // 初始用构建内嵌版本，加载后覆盖为 Server 实际运行版本

// 普通一级菜单项
const flatItems = [
  { key: 'overview', to: '/', label: '首页概览', icon: Odometer },
  { key: 'hosts', to: '/hosts', label: '主机列表', icon: Monitor },
  { key: 'middleware', to: '/middleware', label: '中间件监控', icon: Connection },
  { key: 'alerts', to: '/alerts', label: '告警中心', icon: Bell },
  { key: 'dialtest', to: '/dialtest', label: '服务拨测', icon: Connection },
  { key: 'report', to: '/report', label: '巡检报告', icon: Document },
  { key: 'notify', to: '/notify', label: '通知配置', icon: Message },
]

// 分组菜单：一级菜单 + 二级子菜单
const groups = [
  {
    key: 'system',
    label: '系统设置',
    icon: Setting,
    items: [
      { key: 'settings', to: '/system/settings', label: '站点与品牌' },
      { key: 'upgrade', to: '/system/upgrade', label: '系统升级' },
    ],
  },
]

// 用户手动展开/收起的分组状态
const openGroups = ref({})

function isActiveItem(item) {
  if (item.key === 'overview') return route.path === '/'
  return route.path.startsWith(item.to)
}

function isGroupActive(g) {
  return g.items.some(isActiveItem)
}

function isGroupOpen(g) {
  // 完全由用户手动状态控制，默认不展开
  return openGroups.value[g.key] === true
}

function onGroupClick(g) {
  if (props.collapsed) {
    // 折叠态点击分组：先展开侧边栏，再展开该分组
    emit('toggle')
    openGroups.value[g.key] = true
    return
  }
  openGroups.value[g.key] = !isGroupOpen(g)
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
/* 分组菜单 */
.nav-group {
  margin-top: 2px;
}
.nav-group-title {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 12px;
  color: var(--text-dim);
  font-size: 13px;
  border-radius: 8px;
  cursor: pointer;
  transition: all 0.15s;
  user-select: none;
}
.nav-group-title:hover {
  background: rgba(255, 255, 255, 0.04);
  color: var(--text);
}
.nav-group.group-active .nav-group-title {
  color: var(--accent);
}
.nav-group-title .caret {
  margin-left: auto;
  transition: transform 0.15s;
}
.nav-group-items {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 2px 0 2px 30px;
}
.nav-subitem {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 12px;
  color: var(--text-dim);
  font-size: 12px;
  border-radius: 8px;
  text-decoration: none;
  transition: all 0.15s;
}
.nav-subitem:hover {
  background: rgba(255, 255, 255, 0.04);
  color: var(--text);
}
.nav-subitem.active {
  background: var(--accent-dim);
  color: var(--accent);
}
.sub-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
  opacity: 0.6;
  flex-shrink: 0;
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
