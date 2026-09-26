<template>
  <div class="status-page">
    <header class="head">
      <div class="brand">{{ brand.name || '服务状态' }}</div>
      <div class="sub">
        <!-- 没有任何已发布的服务时不显示结论徽标：「状态未知」对访客没有信息量 -->
        <span v-if="items.length" class="pill" :class="overallClass">{{ overallText }}</span>
        <span class="muted">更新于 {{ fmt(updatedAt) }} · 每 30 秒自动刷新</span>
      </div>
    </header>

    <el-alert v-if="loadError" type="warning" :closable="false" show-icon :title="loadError" class="gap" />

    <div v-if="items.length" class="list">
      <div v-for="it in items" :key="it.name" class="item" :class="it.status">
        <span class="dot" />
        <span class="name">{{ it.name }}</span>
        <span class="muted type">{{ it.type }}</span>
        <span class="metrics">
          <span>{{ statusText(it.status) }}</span>
          <span class="muted">延迟 {{ it.latencyMs > 0 ? it.latencyMs + ' ms' : '—' }}</span>
          <span class="muted">24h 可用率 {{ it.uptime >= 0 ? it.uptime + '%' : '—' }}</span>
          <span class="muted">最近检查 {{ it.lastCheckAt ? fmt(it.lastCheckAt) : '—' }}</span>
        </span>
      </div>
    </div>
    <p v-else-if="!loading" class="empty">
      暂无公开的服务状态信息。
    </p>

    <!-- 文案按「外部访客」写：这是对外页面，不该出现「对外发布 / 内部地址」这类内部术语 -->
    <footer class="foot muted">
      本页展示服务的可用性概览，数据来自主动拨测。
    </footer>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import http from '../api/http'
import { useBrand } from '../composables/useBrand'

// 对外状态页（C3）：**无需登录**，因此这里不做任何权限判断，也不请求需要鉴权的接口。
// 只读 /api/v1/status（公开），并复用公开的品牌配置作为标题。
const { brand } = useBrand()

const items = ref([])
const overall = ref('unknown')
const updatedAt = ref(0)
const loading = ref(false)
const loadError = ref('')
let timer = null

const overallText = computed(() => {
  switch (overall.value) {
    case 'up':
      return '全部服务正常'
    case 'partial':
      return '部分服务异常'
    case 'down':
      return '服务异常'
    default:
      return '状态未知'
  }
})
const overallClass = computed(() => overall.value)

function statusText(s) {
  return s === 'up' ? '正常' : s === 'down' ? '异常' : '未知'
}
function fmt(ms) {
  if (!ms) return '—'
  const d = new Date(ms)
  const p = (n) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

async function load() {
  loading.value = true
  try {
    const data = await http.get('/api/v1/status')
    items.value = data.items || []
    overall.value = data.overall || 'unknown'
    updatedAt.value = data.updatedAt || 0
    loadError.value = ''
  } catch (e) {
    // 对外页面：临时取不到数据时保留上一次的内容并给出温和提示，不显示成「全部异常」
    loadError.value = '暂时无法获取最新状态，显示的是上一次结果'
    console.error('加载服务状态失败', e)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  load()
  timer = setInterval(load, 30000)
})
onUnmounted(() => {
  if (timer) clearInterval(timer)
})
</script>

<style scoped>
.status-page { max-width: 880px; margin: 0 auto; padding: 40px 20px; }
.head { margin-bottom: 18px; }
.brand { font-size: 22px; font-weight: 600; }
.sub { margin-top: 8px; display: flex; align-items: center; gap: 10px; }
.pill { padding: 2px 10px; border-radius: 999px; font-size: 13px; }
.pill.up { background: rgba(103, 194, 58, 0.15); color: #67c23a; }
.pill.partial { background: rgba(230, 162, 60, 0.15); color: #e6a23c; }
.pill.down { background: rgba(245, 108, 108, 0.15); color: #f56c6c; }
.pill.unknown { background: rgba(144, 147, 153, 0.15); color: #909399; }
.muted { color: var(--text-muted, #909399); font-size: 13px; }
.gap { margin-bottom: 12px; }
.list { display: flex; flex-direction: column; gap: 8px; }
.item { display: flex; align-items: center; gap: 12px; padding: 12px 14px; border: 1px solid var(--border-color, #e5e7eb); border-radius: 8px; }
.dot { width: 10px; height: 10px; border-radius: 50%; background: #909399; flex: 0 0 auto; }
.item.up .dot { background: #67c23a; }
.item.down .dot { background: #f56c6c; }
.item.partial .dot { background: #e6a23c; }
.name { font-weight: 500; }
.type { font-size: 12px; text-transform: uppercase; }
.metrics { margin-left: auto; display: flex; gap: 14px; font-size: 13px; flex-wrap: wrap; }
.empty { color: var(--text-muted, #909399); font-size: 14px; }
.foot { margin-top: 20px; }
@media (max-width: 720px) {
  .item { align-items: flex-start; flex-direction: column; }
  .metrics { margin-left: 0; }
}
</style>
