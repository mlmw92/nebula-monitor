<template>
  <Teleport to="body">
    <div v-if="visible" class="cmdk-mask" @click.self="close">
      <div class="cmdk" role="dialog" aria-modal="true">
        <div class="cmdk-in">
          <el-icon :size="15" class="cmdk-ic"><Search /></el-icon>
          <input
            ref="inputEl"
            v-model="query"
            class="cmdk-input"
            type="text"
            placeholder="搜索页面、主机…"
            autocomplete="off"
            spellcheck="false"
          />
          <kbd class="cmdk-kbd">ESC 关闭</kbd>
        </div>

        <div ref="listEl" class="cmdk-list">
          <template v-if="total > 0">
            <template v-for="sec in sections" :key="sec.name">
              <div class="cmdk-sec">{{ sec.name }}</div>
              <div
                v-for="it in sec.items"
                :key="it.id"
                class="cmdk-row"
                :class="{ on: it.idx === activeIdx }"
                @mouseenter="activeIdx = it.idx"
                @click="choose(it.idx)"
              >
                <el-icon :size="14" class="cmdk-row-ic"><component :is="it.icon" /></el-icon>
                <span class="cmdk-row-label">{{ it.label }}</span>
                <span class="cmdk-row-rt">{{ it.hint }}</span>
              </div>
            </template>
          </template>
          <EmptyState
            v-else
            :icon="Search"
            title="没有匹配的页面或主机"
            :hints="[
              '换个关键词试试，或直接点左侧菜单进入',
              query ? `当前关键词：${query}` : '支持搜索页面名称与主机名',
            ]"
          />
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import {
  Odometer, Monitor, Bell, Message, Connection, Document, Files, Setting,
  DataLine, Histogram, Grid, Lock, List, Aim, Tools, Search, Share,
} from '@element-plus/icons-vue'
import http from '../api/http'
import { useAuth } from '../composables/useAuth'
import EmptyState from './common/EmptyState.vue'

/* =========================================================
 * 数据源：**只从 router 派生**（不手写第二份路由清单，否则必然漂移）。
 * 这里只提供「路由 name → 中文标题 / 图标 / 所属分组」的展示映射。
 * 新增页面时补一条即可；漏补时回退到路由 name，不会凭空消失。
 * ========================================================= */
const TITLES = {
  overview: '首页概览',
  hosts: '主机列表',
  middleware: '中间件监控',
  container: '容器与工作负载',
  logs: '集中日志',
  dialtest: '服务拨测',
  'metrics-explore': '指标浏览',
  'system-dashboards': '自定义仪表盘',
  assets: '资产台账',
  'assets-topology': '关系视图',
  inspect: '配置巡检',
  ops: '节点操作',
  alerts: '告警中心',
  intelligence: '智能分析',
  notify: '通知配置',
  report: '巡检报告',
  security: '安全中心',
  audit: '操作审计',
  'system-settings': '站点与品牌',
  'system-profile': '个人中心',
  'system-upgrade': '系统升级',
  'system-users': '用户管理',
  'system-roles': '角色与权限',
}

const ICONS = {
  overview: Odometer,
  hosts: Monitor,
  middleware: Connection,
  container: Grid,
  logs: List,
  dialtest: Aim,
  'metrics-explore': DataLine,
  'system-dashboards': Grid,
  assets: Files,
  'assets-topology': Share,
  inspect: Aim,
  ops: Tools,
  alerts: Bell,
  intelligence: Histogram,
  notify: Message,
  report: Document,
  security: Lock,
  audit: List,
  'system-settings': Setting,
  'system-profile': Setting,
  'system-upgrade': Setting,
  'system-users': Setting,
  'system-roles': Setting,
}

const GROUP_ORDER = ['概览', '观测监控', '资产与配置', '运维操作', '告警运维', '安全治理', '系统设置']
const GROUP_BY_PREFIX = [
  ['hosts', '观测监控'],
  ['middleware', '观测监控'],
  ['container', '观测监控'],
  ['logs', '观测监控'],
  ['dialtest', '观测监控'],
  ['metrics/', '观测监控'],
  ['system/dashboards', '观测监控'],
  ['assets', '资产与配置'],
  ['inspect', '资产与配置'],
  ['ops', '运维操作'],
  ['alerts', '告警运维'],
  ['intelligence', '告警运维'],
  ['notify', '告警运维'],
  ['report', '告警运维'],
  ['security', '安全治理'],
  ['audit', '安全治理'],
  ['system/', '系统设置'],
]

function groupOf(path) {
  if (path === '') return '概览'
  const hit = GROUP_BY_PREFIX.find(([p]) => path.startsWith(p))
  return hit ? hit[1] : '系统设置'
}

const props = defineProps({
  // 主机直达的开关：需要 nodes:read，避免无权限账号看到一个必然 403 的分组
  enableHosts: { type: Boolean, default: true },
})

const router = useRouter()
const auth = useAuth()

const visible = ref(false)
const query = ref('')
const activeIdx = ref(0)
const inputEl = ref(null)
const listEl = ref(null)

const routeItems = computed(() => {
  const root = (router.options.routes || []).find((r) => r.path === '/')
  const children = (root && root.children) || []
  const out = []
  for (const c of children) {
    if (!c.name) continue
    // 需要路径参数的路由（如 node/:name）不能从面板直达
    if (c.path.includes(':')) continue
    if (c.meta && c.meta.perm && !auth.can(c.meta.perm)) continue
    const path = '/' + c.path
    out.push({
      id: 'page:' + c.name,
      label: TITLES[c.name] || c.name,
      hint: groupOf(c.path),
      icon: ICONS[c.name] || Grid,
      to: path,
    })
  }
  return out
})

// 主机直达：只在打开面板时拉一次并缓存（不新增接口）
const hostItems = ref([])
let hostsLoaded = false
async function loadHosts() {
  if (hostsLoaded || !props.enableHosts || !auth.can('nodes:read')) return
  hostsLoaded = true
  try {
    const d = await http.get('/api/v1/nodes')
    hostItems.value = (d.nodes || [])
      .filter((n) => n.hostname)
      .map((n) => ({
        id: 'host:' + n.hostname,
        label: n.displayName || n.hostname,
        hint: n.group || '未分组',
        icon: Monitor,
        to: '/node/' + n.hostname,
      }))
  } catch (e) {
    hostItems.value = []
  }
}

const allGroups = computed(() => {
  const map = new Map()
  for (const it of routeItems.value) {
    if (!map.has(it.hint)) map.set(it.hint, [])
    map.get(it.hint).push(it)
  }
  const groups = [...map.entries()]
    .map(([name, items]) => ({ name, items }))
    .sort((a, b) => GROUP_ORDER.indexOf(a.name) - GROUP_ORDER.indexOf(b.name))
  if (hostItems.value.length) groups.push({ name: '主机', items: hostItems.value })
  return groups
})

const sections = computed(() => {
  const q = query.value.trim().toLowerCase()
  const out = []
  let idx = 0
  for (const g of allGroups.value) {
    const items = g.items.filter(
      (it) => !q || it.label.toLowerCase().includes(q) || it.hint.toLowerCase().includes(q)
    )
    if (!items.length) continue
    out.push({ name: g.name, items: items.map((it) => ({ ...it, idx: idx++ })) })
  }
  return out
})

// idx 是按渲染顺序连续分配的，所以条目总数就是各分组条目数之和
const total = computed(() => sections.value.reduce((n, s) => n + s.items.length, 0))

watch(query, () => {
  activeIdx.value = 0
})

watch(activeIdx, async () => {
  await nextTick()
  const el = listEl.value && listEl.value.querySelector('.cmdk-row.on')
  if (el && el.scrollIntoView) el.scrollIntoView({ block: 'nearest' })
})

function open() {
  visible.value = true
  query.value = ''
  activeIdx.value = 0
  loadHosts()
  nextTick(() => {
    inputEl.value && inputEl.value.focus()
  })
}

function close() {
  visible.value = false
}

function move(delta) {
  if (!total.value) return
  activeIdx.value = (activeIdx.value + delta + total.value) % total.value
}

function choose(i) {
  for (const sec of sections.value) {
    for (const it of sec.items) {
      if (it.idx === i) {
        close()
        router.push(it.to)
        return
      }
    }
  }
}

function onKeydown(e) {
  const openHotkey = (e.metaKey || e.ctrlKey) && (e.key === 'k' || e.key === 'K')
  if (openHotkey) {
    e.preventDefault()
    visible.value ? close() : open()
    return
  }
  if (!visible.value) return
  if (e.key === 'Escape') {
    e.preventDefault()
    close()
  } else if (e.key === 'ArrowDown') {
    e.preventDefault()
    move(1)
  } else if (e.key === 'ArrowUp') {
    e.preventDefault()
    move(-1)
  } else if (e.key === 'Enter') {
    e.preventDefault()
    choose(activeIdx.value)
  }
}

onMounted(() => {
  window.addEventListener('keydown', onKeydown)
})
onUnmounted(() => {
  window.removeEventListener('keydown', onKeydown)
})

defineExpose({ open, close, toggle: () => (visible.value ? close() : open()) })
</script>

<style scoped>
.cmdk-mask {
  position: fixed;
  inset: 0;
  z-index: 2000;
  background: rgba(0, 0, 0, 0.55);
  display: flex;
  justify-content: center;
  align-items: flex-start;
  padding-top: 15vh;
}
.cmdk {
  width: 560px;
  max-width: calc(100vw - 32px);
  background: var(--s3);
  border: 1px solid var(--bd-strong);
  border-radius: var(--r-lg);
  box-shadow: var(--sh-3);
  overflow: hidden;
}
.cmdk-in {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--bd);
}
.cmdk-ic {
  color: var(--t3);
  flex-shrink: 0;
}
.cmdk-input {
  flex: 1;
  min-width: 0;
  background: transparent;
  border: none;
  outline: none;
  padding: 0;
  color: var(--text);
  font-family: var(--font);
  font-size: var(--fs-md);
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
.cmdk-list {
  max-height: 320px;
  overflow-y: auto;
  padding: 6px;
}
.cmdk-sec {
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.08em;
  color: var(--t3);
  padding: 8px 10px 4px;
}
.cmdk-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border-radius: var(--r-sm);
  font-size: var(--fs-base);
  color: var(--t2);
  cursor: pointer;
}
.cmdk-row.on {
  background: var(--accent-dim);
  color: var(--accent);
}
.cmdk-row-ic {
  flex-shrink: 0;
  opacity: 0.9;
}
.cmdk-row-label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.cmdk-row-rt {
  margin-left: auto;
  font-size: var(--fs-xs);
  color: var(--t3);
  flex-shrink: 0;
}
</style>
