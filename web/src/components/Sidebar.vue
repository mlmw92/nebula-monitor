<template>
  <aside class="sidebar glass" :class="{ collapsed }">
      <div class="brand">
        <img v-if="brand.logo" :src="brand.logo" alt="logo" class="brand-logo" />
        <div class="brand-text" v-show="!collapsed">
          <h1 :title="brand.name">{{ brand.name }}</h1>
          <p v-if="brand.subtitle">{{ brand.subtitle }}</p>
        </div>
      </div>

    <nav class="nav">
      <el-tooltip content="首页概览" placement="right" :disabled="!collapsed">
        <router-link
          to="/"
          class="nav-item"
          :class="{ active: route.path === '/' }"
        >
          <el-icon :size="18"><Odometer /></el-icon>
          <span class="label" v-show="!collapsed">首页概览</span>
        </router-link>
      </el-tooltip>

      <div
        v-for="g in visibleGroups"
        :key="g.key"
        class="nav-group"
        :class="{ 'group-open': isGroupOpen(g), 'group-active': isGroupActive(g) }"
      >
        <el-tooltip :content="g.label" placement="right" :disabled="!collapsed">
          <div class="nav-group-title" @click="onGroupClick(g)">
            <el-icon :size="15"><component :is="g.icon" /></el-icon>
            <span class="label" v-show="!collapsed">{{ g.label }}</span>
            <el-icon v-show="!collapsed" class="caret"><ArrowDown v-if="isGroupOpen(g)" /><ArrowRight v-else /></el-icon>
          </div>
        </el-tooltip>
        <div v-show="!collapsed && isGroupOpen(g)" class="nav-group-items">
          <template v-for="sub in g.items" :key="sub.key">
            <!-- 免登录的独立页（对外状态页）：用新标签页打开，避免把管理台顶掉。
                 用原生 a + hash 链接：hash 路由下 " #/status " 就是它的地址，中间键/Ctrl+点击也照常工作。 -->
            <a
              v-if="isItemVisible(sub) && sub.newTab"
              :href="'#' + sub.to"
              target="_blank"
              rel="noopener"
              class="nav-subitem"
            >
              <span class="sub-dot"></span>
              <span class="label">{{ sub.label }}</span>
              <el-icon class="ext-icon" :size="12"><TopRight /></el-icon>
            </a>
            <router-link
              v-else-if="isItemVisible(sub)"
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

    <!-- 用户卡：头像 + 用户名 + 角色 -->
    <div class="nav-foot">
      <el-tooltip :content="username || 'admin'" placement="right" :disabled="!collapsed">
        <div class="avatar">{{ initials }}</div>
      </el-tooltip>
      <div v-show="!collapsed" class="u-meta">
        <div class="u-name">{{ username || 'admin' }}</div>
        <div class="u-sub">{{ roleText }}</div>
      </div>
    </div>

    <!-- 版本信息（统一取 Server 运行版本，不再区分 Web/Server 版本） -->
    <div class="version-info" v-show="!collapsed">
      <div class="ver-row">
        <span class="ver-label">版本</span>
        <span class="ver-val">{{ serverVersion }}</span>
      </div>
    </div>

    <div class="sidebar-footer">
      <el-tooltip :content="collapsed ? '展开侧栏' : '收起侧栏'" placement="right">
        <el-button link class="toggle-btn" @click="$emit('toggle')">
          <el-icon :size="18"><Fold v-if="!collapsed" /><Expand v-else /></el-icon>
          <span v-show="!collapsed" class="label">收起</span>
        </el-button>
      </el-tooltip>
    </div>
  </aside>
</template>

<script setup>
import { ref, computed, onMounted, watch } from 'vue'
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
  TopRight,
  Tools,
} from '@element-plus/icons-vue'
import http from '../api/http'
import { useBrand } from '../composables/useBrand'
import { useAuth } from '../composables/useAuth'
import { WEB_VERSION } from '../version'

const props = defineProps({
  collapsed: Boolean,
  alertCount: { type: Number, default: 0 },
  username: { type: String, default: '' },
  roleLabels: { type: Array, default: () => [] },
})
const emit = defineEmits(['toggle', 'logout'])

const route = useRoute()
const { brand } = useBrand()
const auth = useAuth()

const serverVersion = ref(WEB_VERSION) // 初始用构建内嵌版本，加载后覆盖为 Server 实际运行版本

// 头像首字母：英文取前两位，中文取首字
const initials = computed(() => {
  const n = (props.username || '').trim()
  if (!n) return 'AD'
  return /^[\x00-\x7F]+$/.test(n) ? n.slice(0, 2).toUpperCase() : n.slice(0, 1)
})

const roleText = computed(() => {
  const labels = props.roleLabels || []
  return labels.length ? labels.join(' · ') : '用户'
})

// 分组菜单：一级分组 + 二级子菜单
const groups = [
  {
    key: 'monitoring',
    label: '观测监控',
    icon: DataLine,
    items: [
      { key: 'hosts', to: '/hosts', label: '主机列表', icon: Monitor, perm: 'nodes:read' },
      { key: 'middleware', to: '/middleware', label: '中间件监控', icon: Connection, perm: 'middleware:read' },
      // 容器只读管理面：读权限是 container:read（与 middleware:read 分开，见路由注释）
      { key: 'container', to: '/container', label: '容器与工作负载', icon: Grid, perm: 'container:read' },
      { key: 'logs', to: '/logs', label: '集中日志', icon: List, perm: 'logs:read' },
      { key: 'dialtest', to: '/dialtest', label: '服务拨测', icon: Aim, perm: 'probe:read' },
      // 对外状态页入口：页面本身免登录、且在管理壳之外（/status 与 /screen 同级），
      // 所以在管理台里点它应当新开标签页——否则看完外部视角就回不到原来的页面了。
      { key: 'status', to: '/status', label: '对外状态页', icon: View, perm: 'probe:read', newTab: true },
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
      // 配置巡检：读看记录与差异；触发巡检另有 inspect:run（页面内门控）
      { key: 'inspect', to: '/inspect', label: '配置巡检', icon: Aim, perm: 'inspect:read' },
    ],
  },
  {
    key: 'ops',
    label: '运维操作',
    icon: Tools,
    items: [
      // 下行操作：读看任务与动作目录需 ops:read；**下发**另有 ops:exec（高风险，页面内门控）。
      // 能不能真的执行还取决于目标机器自己的 guards.ops——权限只是四道护栏之一。
      { key: 'ops', to: '/ops', label: '节点操作', icon: Tools, perm: 'ops:read' },
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

// 分组的显式展开/收起状态。
// 默认**全部展开**——此前只展开当前路由所在分组，导致 22 个页面全靠逐层点开；
// 这里只记录"用户手动收起过"的分组（false），未记录的一律视为展开。
const openGroups = ref({})

function isActiveItem(item) {
  if (item.key === 'overview') return route.path === '/'
  return route.path.startsWith(item.to)
}

function isGroupActive(g) {
  return g.items.some(isActiveItem)
}

function isGroupOpen(g) {
  return openGroups.value[g.key] !== false
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

// 路由切换时确保目标分组是展开的（用户此前手动收起过也要重新展开，
// 否则会出现"点了命令面板跳转过来，却看不到自己在哪"的情况）。
watch(
  () => route.path,
  () => {
    const active = groups.find(isGroupActive)
    if (active && openGroups.value[active.key] === false) {
      openGroups.value[active.key] = true
    }
  }
)

onMounted(() => {
  loadVersion()
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
  padding: 4px 6px 14px;
  border-bottom: 1px solid var(--border);
  margin-bottom: 10px;
  height: 52px;
}
.brand-logo {
  width: 34px;
  height: 34px;
  border-radius: var(--r-lg);
  object-fit: contain;
  flex-shrink: 0;
  background: var(--fill-2);
  box-shadow: 0 0 16px var(--accent-glow);
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
  /* 菜单超出视口时必须能滚动：此前这里没有 overflow，而 .sidebar 是 overflow: hidden，
     于是多展开几个分组后底部（系统设置里的升级/用户/角色）直接被裁掉且无法滚动到。
     min-height: 0 是必须的——flex 子项默认 min-height:auto 不会收缩，加了 overflow 也不生效。 */
  min-height: 0;
  overflow-y: auto;
  overflow-x: hidden;
}
.nav-item {
  display: flex;
  align-items: center;
  gap: 10px;
  height: 36px;
  padding: 0 10px;
  color: var(--text-dim);
  font-size: var(--fs-base);
  border-radius: var(--r-sm);
  text-decoration: none;
  transition: background var(--dur-1) var(--ease), color var(--dur-1) var(--ease);
  position: relative;
}
.nav-item:hover {
  background: var(--fill-1);
  color: var(--text);
}
.nav-item.active {
  background: var(--accent-dim);
  color: var(--accent);
  font-weight: 600;
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
/* 分组菜单：分组标题是"章节标签"，刻意做小、做淡，与子项拉开层级差 */
.nav-group {
  margin-top: 6px;
}
.nav-group-title {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 6px 10px;
  color: var(--t3);
  font-size: 11.5px;
  font-weight: 600;
  letter-spacing: 0.1em;
  border-radius: var(--r-sm);
  cursor: pointer;
  transition: background var(--dur-1) var(--ease), color var(--dur-1) var(--ease);
  user-select: none;
}
.nav-group-title:hover {
  background: var(--fill-1);
  color: var(--text-dim);
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
  padding: 2px 0 2px 24px;
  position: relative;
}
.nav-subitem {
  position: relative;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 7px 10px;
  color: var(--text-dim);
  font-size: var(--fs-base);
  border-radius: var(--r-sm);
  text-decoration: none;
  transition: background var(--dur-1) var(--ease), color var(--dur-1) var(--ease);
}
.nav-subitem:hover {
  background: var(--fill-1);
  color: var(--text);
}
.nav-subitem.active {
  background: var(--accent-dim);
  color: var(--accent);
  font-weight: 600;
}
/* 活动项左侧光条：与 .nav-item.active::before 落在同一条竖线上。
   子项位于 padding-left:24px 的容器内，所以偏移量取 -24px 才能回到侧栏内缘。 */
.nav-subitem.active::before {
  content: '';
  position: absolute;
  left: -24px;
  top: 50%;
  transform: translateY(-50%);
  width: 3px;
  height: 18px;
  background: var(--accent);
  border-radius: 0 2px 2px 0;
}
.sub-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
  opacity: 0.6;
  flex-shrink: 0;
}
/* 新标签页打开的入口（对外状态页）：给一个 ↖ 提示，
   避免"点了怎么多出一个页签"的困惑；同时区分于普通菜单项 */
.ext-icon {
  margin-left: auto;
  color: var(--text-muted);
}
/* 用户卡：头像 + 用户名 + 角色 · 版本 */
.nav-foot {
  margin-top: 8px;
  padding: 10px 4px 2px;
  border-top: 1px solid var(--bd);
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}
.avatar {
  width: 30px;
  height: 30px;
  border-radius: 50%;
  flex-shrink: 0;
  background: var(--accent-dim);
  border: 1px solid var(--accent);
  color: var(--accent);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.02em;
}
.u-meta {
  min-width: 0;
}
.u-name {
  font-size: var(--fs-sm);
  font-weight: 600;
  color: var(--text);
  line-height: 1.3;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.u-sub {
  font-size: 11px;
  color: var(--t3);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.sidebar.collapsed .nav-foot {
  justify-content: center;
  padding-left: 0;
  padding-right: 0;
}
/* 版本信息（保持与改造前一致的呈现：左侧「版本」标签 + 右侧等宽字体版本号） */
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
.sidebar-footer {
  padding-top: 6px;
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
