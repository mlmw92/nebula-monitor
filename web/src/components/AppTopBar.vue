<template>
  <header class="topbar glass">
    <div class="topbar-left">
      <el-button link class="fold-btn" @click="$emit('toggle')">
        <el-icon :size="18"><Expand v-if="collapsed" /><Fold v-else /></el-icon>
      </el-button>
      <el-breadcrumb separator="/">
        <el-breadcrumb-item :to="{ path: '/' }">首页</el-breadcrumb-item>
        <template v-if="$route.name !== 'overview'">
          <el-breadcrumb-item v-for="(b, i) in breadcrumb" :key="i">{{ b }}</el-breadcrumb-item>
        </template>
      </el-breadcrumb>
    </div>

    <div class="topbar-right">
      <!-- ⌘K 入口：外观是搜索框，实际是一个按钮（真正的输入在命令面板里） -->
      <button type="button" class="cmdk-entry" @click="$emit('open-search')">
        <el-icon :size="14"><Search /></el-icon>
        <span class="cmdk-hint">搜索页面、主机…</span>
        <kbd class="cmdk-kbd">{{ shortcutLabel }}</kbd>
      </button>

      <button type="button" class="screen-btn" @click="$router.push('/screen')">
        <el-icon :size="14"><DataAnalysis /></el-icon>
        <span>数据大屏</span>
      </button>

      <el-tooltip content="刷新数据" placement="bottom">
        <el-button class="icon-btn" :icon="Refresh" circle size="small" @click="$emit('refresh')" />
      </el-tooltip>

      <el-tooltip content="告警中心" placement="bottom">
        <el-badge :value="alertCount" :hidden="!alertCount" :max="99">
          <el-button class="icon-btn" :icon="Bell" circle size="small" @click="$router.push('/alerts')" />
        </el-badge>
      </el-tooltip>

      <el-dropdown trigger="click" @command="onUserCommand">
        <button type="button" class="user-btn" :style="{ borderColor: themeColor }">
          {{ initials }}
        </button>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item disabled>{{ username || 'admin' }}</el-dropdown-item>
            <el-dropdown-item v-if="roleLabels.length" disabled>
              <span class="role-tags">
                <el-tag v-for="r in roleLabels" :key="r" size="small" type="info" effect="dark">{{ r }}</el-tag>
              </span>
            </el-dropdown-item>
            <el-dropdown-item
              v-for="t in themeOptions"
              :key="t.value"
              :command="'theme:' + t.value"
            >
              <span class="td-dot" :style="{ background: t.color }"></span>{{ t.label }}
              <el-icon v-if="theme === t.value" class="td-check"><Select /></el-icon>
            </el-dropdown-item>
            <el-dropdown-item divided command="logout">退出登录</el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
    </div>
  </header>
</template>

<script setup>
import { computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { Expand, Fold, Refresh, Bell, Search, DataAnalysis, Select } from '@element-plus/icons-vue'

const props = defineProps({
  collapsed: Boolean,
  alertCount: { type: Number, default: 0 },
  username: { type: String, default: '' },
  roleLabels: { type: Array, default: () => [] },
})
const emit = defineEmits(['toggle', 'refresh', 'logout', 'change-theme', 'open-search'])

const route = useRoute()

/* ===== 换肤（自 MainLayout 原样迁入，逻辑未改） ===== */
const THEMES = { b: '#4a9df0', a: '#00d9a3', c: '#8b5cf6' }
const theme = ref(localStorage.getItem('nebula_theme') || 'b')
const themeColor = computed(() => THEMES[theme.value] || THEMES.b)
function applyTheme(t) {
  theme.value = t
  document.body.dataset.theme = t
  localStorage.setItem('nebula_theme', t)
}
function changeTheme(t) {
  applyTheme(t)
  // 触发图表组件重新取色
  window.dispatchEvent(new CustomEvent('nebula:theme-changed', { detail: t }))
}
applyTheme(theme.value)

const THEME_LABELS = { b: '极光蓝（默认）', a: '星云青绿', c: '星河紫' }
const themeOptions = Object.keys(THEMES).map((k) => ({
  value: k,
  color: THEMES[k],
  label: THEME_LABELS[k] || k,
}))

function onUserCommand(cmd) {
  if (cmd === 'logout') {
    emit('logout')
    return
  }
  if (cmd.startsWith('theme:')) {
    const t = cmd.slice(6)
    changeTheme(t)
    emit('change-theme', t)
  }
}

/* ===== 面包屑 ===== */
const pageTitle = computed(() => {
  const m = {
    overview: '首页概览',
    hosts: '主机列表',
    node: '主机详情',
    middleware: '中间件监控',
    alerts: '告警中心',
    security: '安全中心',
    audit: '操作审计',
    dialtest: '服务拨测',
    report: '巡检报告',
    'system-upgrade': '系统升级',
    notify: '通知配置',
    'system-settings': '系统设置',
  }
  return m[route.name] || ''
})

// 面包屑：系统设置分组下显示两级（系统设置 / 子菜单）
const breadcrumb = computed(() => {
  if (route.name === 'system-upgrade') return ['系统设置', '系统升级']
  if (route.name === 'system-settings') {
    const t = route.query.tab === 'password' ? '修改密码' : '站点与品牌'
    return ['系统设置', t]
  }
  const t = pageTitle.value
  return t ? [t] : []
})

/* ===== 其他 ===== */
const isMac = /Mac|iPhone|iPad|iPod/.test((navigator.platform || '') + (navigator.userAgent || ''))
const shortcutLabel = isMac ? '⌘K' : 'Ctrl K'

const initials = computed(() => {
  const n = (props.username || 'admin').trim()
  if (!n) return 'AD'
  return /^[\x00-\x7F]+$/.test(n) ? n.slice(0, 2).toUpperCase() : n.slice(0, 1)
})
</script>

<style scoped>
.topbar {
  position: sticky;
  top: 0;
  z-index: 40;
  height: var(--topbar-h);
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 0 20px;
  border-radius: 0;
  border-left: none;
  border-right: none;
  border-top: none;
  background: var(--bg-elev);
}
.topbar-left {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
}
.fold-btn {
  color: var(--t2);
}
.topbar :deep(.el-breadcrumb__inner),
.topbar :deep(.el-breadcrumb__separator) {
  color: var(--t3);
  font-size: var(--fs-sm);
  font-weight: 400;
}
.topbar :deep(.el-breadcrumb__inner.is-link:hover) {
  color: var(--text);
}
/* 当前页用正文色，形成"你在哪"的落点 */
.topbar :deep(.el-breadcrumb__item:last-child .el-breadcrumb__inner) {
  color: var(--text);
  font-weight: 600;
}

.topbar-right {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
}

.cmdk-entry {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 240px;
  height: 32px;
  padding: 0 10px;
  border-radius: var(--r-sm);
  border: 1px solid var(--bd);
  background: var(--s0);
  color: var(--t3);
  font-family: var(--font);
  font-size: var(--fs-sm);
  cursor: pointer;
  transition: border-color var(--dur-1) var(--ease), color var(--dur-1) var(--ease);
}
.cmdk-entry:hover {
  border-color: var(--accent);
  color: var(--t2);
}
.cmdk-hint {
  flex: 1;
  text-align: left;
}
.cmdk-kbd {
  font-family: var(--mono);
  font-size: 11px;
  padding: 1px 5px;
  border-radius: var(--r-xs);
  background: var(--fill-3);
  border: 1px solid var(--bd);
  color: var(--t3);
  flex-shrink: 0;
}

/* 数据大屏入口：中性描边按钮（此前的紫渐变 + 流光 + 脉冲已全部删除） */
.screen-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 32px;
  padding: 0 12px;
  border-radius: var(--r-sm);
  border: 1px solid var(--bd-strong);
  background: transparent;
  color: var(--t2);
  font-family: var(--font);
  font-size: var(--fs-sm);
  font-weight: 600;
  cursor: pointer;
  transition: background var(--dur-1) var(--ease), color var(--dur-1) var(--ease),
    border-color var(--dur-1) var(--ease);
}
.screen-btn:hover {
  background: var(--fill-hover);
  border-color: var(--bd-hi);
  color: var(--text);
}

.icon-btn {
  color: var(--t2);
}

.user-btn {
  width: 32px;
  height: 32px;
  border-radius: 50%;
  border: 1px solid var(--accent);
  background: var(--accent-dim);
  color: var(--accent);
  font-family: var(--font);
  font-size: 12px;
  font-weight: 600;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  transition: background var(--dur-1) var(--ease);
}
.user-btn:hover {
  background: var(--fill-3);
}

.td-dot {
  display: inline-block;
  width: 10px;
  height: 10px;
  border-radius: 50%;
  margin-right: 8px;
  vertical-align: middle;
}
.td-check {
  margin-left: 8px;
  color: var(--accent);
}
.role-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  max-width: 220px;
}
</style>
