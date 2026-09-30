<template>
  <aside class="sidebar glass" :class="{ collapsed }">
      <div class="brand">
        <img v-if="brand.logo" :src="brand.logo" alt="logo" style="width:36px;height:36px;border-radius:10px;object-fit:contain;background:rgba(255,255,255,0.05);box-shadow:0 0 16px rgba(64,158,255,0.35)" />
        <div class="brand-text" v-show="!collapsed">
          <h1 :title="brand.name">{{ brand.name }}</h1>
          <p v-if="brand.subtitle">{{ brand.subtitle }}</p>
        </div>
      </div>

    <nav class="nav">
      <router-link
        to="/"
        class="nav-item"
        :class="{ active: route.path === '/' }"
      >
        <el-icon :size="18"><Odometer /></el-icon>
        <span class="label" v-show="!collapsed">首页概览</span>
      </router-link>

      <div
        v-for="g in visibleGroups"
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
          <template v-for="sub in g.items" :key="sub.key">
            <router-link
              v-if="isItemVisible(sub)"
              :to="sub.to"
              class="nav-subitem"
              :class="{ active: isActiveItem(sub) }"
            >
              <span class="sub-dot"></span>
              <span class="label">{{ sub.label }}</span>
              <el-badge
                v-if="sub.key === 'alerts' && alertCount > 0"
                :value="alertCount"
                :max="99"
                class="nav-badge"
              />
            </router-link>
          </template>
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
import { ref, computed, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import {
  Odometer,
  Monitor,
  Bell,
  Message,
  Connection,
  Document,
  Files,
  Setting,
  ArrowDown,
  ArrowRight,
  DataLine,
  Histogram,
  Grid,
  Lock,
  List,
  Aim,
  View,
} from '@element-plus/icons-vue'
import http from '../api/http'
import { useBrand } from '../composables/useBrand'
import { useAuth } from '../composables/useAuth'
import { WEB_VERSION } from '../version'

const props = defineProps({
  collapsed: Boolean,
  alertCount: { type: Number, default: 0 },
})
const emit = defineEmits(['toggle', 'logout'])

const route = useRoute()
const { brand } = useBrand()
const auth = useAuth()

const serverVersion = ref(WEB_VERSION) // 初始用构建内嵌版本，加载后覆盖为 Server 实际运行版本

// 分组菜单：一级分组 + 二级子菜单
const groups = [
  {
    key: 'monitoring',
    label: '观测监控',
    icon: DataLine,
    items: [
      { key: 'hosts', to: '/hosts', label: '主机列表', icon: Monitor, perm: 'nodes:read' },
      { key: 'middleware', to: '/middleware', label: '中间件监控', icon: Connection, perm: 'middleware:read' },
      { key: 'logs', to: '/logs', label: '集中日志', icon: List, perm: 'logs:read' },
      { key: 'dialtest', to: '/dialtest', label: '服务拨测', icon: Aim, perm: 'probe:read' },
      // 对外状态页入口（页面本身免登录，菜单项属管理侧快捷入口）
      { key: 'status', to: '/status', label: '对外状态页', icon: View, perm: 'probe:read' },
      { key: 'metrics-explore', to: '/metrics/explore', label: '指标浏览', icon: DataLine, perm: 'nodes:read' },
      { key: 'dashboards', to: '/system/dashboards', label: '自定义仪表盘', icon: Grid, perm: 'dashboard:read' },
    ],
  },
  {
    key: 'assets',
    label: '资产与配置',
    icon: Files,
    items: [
      // 读权限即可进入；维护按钮另行按 assets:write 门控（该权限点为高风险，需二次确认）
      { key: 'assets', to: '/assets', label: '资产台账', icon: Files, perm: 'assets:read' },
    ],
  },
  {
    key: 'alerting',
    label: '告警运维',
    icon: Bell,
    items: [
      { key: 'alerts', to: '/alerts', label: '告警中心', icon: Bell, perm: 'alerts:read' },
      { key: 'intelligence', to: '/intelligence', label: '智能分析', icon: Histogram, perm: 'nodes:read' },
      { key: 'notify', to: '/notify', label: '通知配置', icon: Message, perm: 'notify:read' },
      { key: 'report', to: '/report', label: '巡检报告', icon: Document, perm: 'report:read' },
    ],
  },
  {
    key: 'security',
    label: '安全治理',
    icon: Lock,
    items: [
      { key: 'security', to: '/security', label: '安全中心', icon: Lock, perm: 'security:read' },
      { key: 'audit', to: '/audit', label: '操作审计', icon: List, perm: 'audit:read' },
    ],
  },
  {
    key: 'system',
    label: '系统设置',
    icon: Setting,
    items: [
      { key: 'settings', to: '/system/settings', label: '站点与品牌' },
      { key: 'profile', to: '/system/profile', label: '个人中心' },
      { key: 'upgrade', to: '/system/upgrade', label: '系统升级', perm: 'system:upgrade' },
      { key: 'users', to: '/system/users', label: '用户管理', perm: 'users:manage' },
      { key: 'roles', to: '/system/roles', label: '角色与权限', perm: 'roles:read' },
    ],
  },
]

// 按当前用户权限过滤可见的菜单项（前端隐藏仅为体验，真正鉴权以服务端为准）。
// principal 未加载（单管理员/未启用 RBAC）时 auth.can 恒为 true，不隐藏。
function isItemVisible(sub) {
  if (!sub.perm) return true
  return auth.can(sub.perm)
}

// 仅含可见子项的菜单分组（无可见子项的分组标题不显示）
const visibleGroups = computed(() => groups.filter((g) => g.items.some(isItemVisible)))

// 用户手动展开/收起的分组状态（默认展开当前路由所在分组）
const openGroups = ref({})

function isActiveItem(item) {
  if (item.key === 'overview') return route.path === '/'
  return route.path.startsWith(item.to)
}

function isGroupActive(g) {
  return g.items.some(isActiveItem)
}

function isGroupOpen(g) {
  // 默认展开当前路由所在分组；用户可手动收起/展开
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

onMounted(() => {
  loadVersion()
  // 默认展开当前路由所在的分组
  const active = groups.find(isGroupActive)
  if (active) openGroups.value[active.key] = true
})
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
.brand-text {
  min-width: 0;
  overflow: hidden;
}
.brand-text h1 {
  font-size: 17px;
  font-weight: 700;
  letter-spacing: 0.03em;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.brand-text p {
  font-size: 13px;
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
  font-size: 15px;
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
  font-size: 15px;
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
  font-size: 14px;
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
  font-size: 13px;
}
.ver-label {
  color: var(--text-muted);
}
.ver-val {
  color: var(--text-dim);
  font-family: var(--mono);
  font-size: 13px;
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
