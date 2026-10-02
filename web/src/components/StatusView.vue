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

// 对外页强制浅色（作用域见下方 <style> 中的 html.status-light）：
// 深色主题是为长时间盯盘设计的，而这一页的读者是外部访客——要能截图转发、能打印。
// 挂在当前 html 上而不是 body，是因为 Element Plus 的暗色变量定义在 html.dark。
onMounted(() => {
  document.documentElement.classList.add('status-light')
  load()
  timer = setInterval(load, 30000)
})
onUnmounted(() => {
  document.documentElement.classList.remove('status-light')
  if (timer) clearInterval(timer)
})
</script>

<style scoped>
.status-page { max-width: 880px; margin: 0 auto; padding: 40px 20px; }
.head { margin-bottom: 18px; }
.brand { font-size: 22px; font-weight: 600; }
.sub { margin-top: 8px; display: flex; align-items: center; gap: 10px; }
/* 状态徽标：底色 8% + 1px 同色描边，白底上比"15% 实心 + 亮色字"更稳，
   且打印时不会糊成一团 */
.pill { padding: 2px 10px; border-radius: 999px; font-size: 13px; border: 1px solid currentColor; }
.pill.up { background: rgba(22, 163, 74, 0.08); color: #15803d; }
.pill.partial { background: rgba(217, 119, 6, 0.08); color: #b45309; }
.pill.down { background: rgba(220, 38, 38, 0.08); color: #b91c1c; }
.pill.unknown { background: rgba(100, 116, 139, 0.08); color: #64748b; }
.muted { color: var(--text-muted, #64748b); font-size: 13px; }
.gap { margin-bottom: 12px; }
.list { display: flex; flex-direction: column; gap: 8px; }
.item { display: flex; align-items: center; gap: 12px; padding: 12px 14px; border: 1px solid var(--border-color, #d7dfe8); border-radius: 8px; background: var(--bg-card, #fff); }
/* 状态点：异常态加一圈淡光晕，让"有问题"在扫视时先跳出来 */
.dot { width: 10px; height: 10px; border-radius: 50%; background: #94a3b8; flex: 0 0 auto; }
.item.up .dot { background: #16a34a; }
.item.down .dot { background: #dc2626; box-shadow: 0 0 0 3px rgba(220, 38, 38, 0.16); }
.item.partial .dot { background: #d97706; box-shadow: 0 0 0 3px rgba(217, 119, 6, 0.16); }
.name { font-weight: 500; }
.type { font-size: 12px; text-transform: uppercase; }
.metrics { margin-left: auto; display: flex; gap: 14px; font-size: 13px; flex-wrap: wrap; }
.empty { color: var(--text-muted, #64748b); font-size: 14px; }
.foot { margin-top: 20px; }
@media (max-width: 720px) {
  .item { align-items: flex-start; flex-direction: column; }
  .metrics { margin-left: 0; }
}
</style>

<!--
  对外状态页的浅色作用域（非 scoped）。

  为什么整页换色而不是改几个颜色：这是免登录的对外页面，读者是客户和访客，
  不是值班运维。深色主题的价值是长时间盯盘不刺眼，代价是截图转发即失真、
  打印直接失效、阳光下不可读——这三点恰好是状态页的核心使用场景。

  为什么作用域挂在 html 而不是 body：Element Plus 的暗色变量定义在 html.dark
  （特异性 0-1-1），只有同级的 html.status-light 配合后置加载才能盖回去。
  本页是异步路由，CSS 天然晚于主 chunk 加载，顺序成立。

  为什么用令牌而不是写死颜色：页面上所有组件（含 el-alert）都读令牌，
  一次性反转即可全量生效，后续改主题不需要再动这一页。
-->
<style>
html.status-light,
html.status-light body {
  --s0: #ffffff;
  --s1: #ffffff;
  --s2: #f8fafc;
  --s3: #f1f5f9;
  --bg: #f6f8fb;
  --bg-elev: #ffffff;
  --bg-card: #ffffff;
  --bg-primary: #ffffff;
  --panel-bg: #ffffff;

  --bd: #d7dfe8;
  --bd-strong: #b6c2d1;
  --bd-hi: #94a3b8;
  --border: #d7dfe8;
  --border-color: #d7dfe8;
  --border-strong: #b6c2d1;

  --t1: #0f172a;
  --t2: #475569;
  --t3: #64748b;
  --text: #0f172a;
  --text-main: #0f172a;
  --text-dim: #475569;
  --label: #1e293b;
  --text-muted: #64748b;

  --accent: #2563eb;
  --accent-dim: rgba(37, 99, 235, 0.10);
  --accent-glow: rgba(37, 99, 235, 0.14);

  --ok: #16a34a;
  --ok-dim: rgba(22, 163, 74, 0.10);
  --ok-bd: #86c9a2;
  --warn: #d97706;
  --warn-dim: rgba(217, 119, 6, 0.10);
  --warn-bd: #e3b07c;
  --danger: #dc2626;
  --danger-dim: rgba(220, 38, 38, 0.09);
  --danger-bd: #e69b9b;
  --info: #0284c7;
  --info-dim: rgba(2, 132, 199, 0.09);
  --info-bd: #93c5e0;

  --fill-1: #f8fafc;
  --fill-2: #f1f5f9;
  --fill-3: #e8eef6;
  --fill-hover: #f4f8fc;
  --grid-line: rgba(15, 23, 42, 0.06);

  --el-bg-color: #ffffff;
  --el-bg-color-page: #f6f8fb;
  --el-bg-color-overlay: #ffffff;
  --el-text-color-primary: #0f172a;
  --el-text-color-regular: #475569;
  --el-text-color-secondary: #64748b;
  --el-text-color-placeholder: #94a3b8;
  --el-text-color-disabled: #b6c2d1;
  --el-border-color: #d7dfe8;
  --el-border-color-light: #e1e8f0;
  --el-border-color-lighter: #eaf0f6;
  --el-border-color-extra-light: #f1f5fa;
  --el-border-color-dark: #b6c2d1;
  --el-border-color-darker: #94a3b8;
  --el-fill-color: #f1f5f9;
  --el-fill-color-light: #f6f9fc;
  --el-fill-color-lighter: #fafcfd;
  --el-fill-color-blank: #ffffff;
  --el-fill-color-dark: #e2e8f0;
  --el-fill-color-darker: #cbd5e1;
  --el-mask-color: rgba(15, 23, 42, 0.4);
  --el-color-primary: #2563eb;
  --el-color-primary-light-3: #6b96f2;
  --el-color-primary-light-5: #93b3f6;
  --el-color-primary-light-7: #bed1fa;
  --el-color-primary-light-8: #d3e0fc;
  --el-color-primary-light-9: #e7effe;
  --el-color-success: #16a34a;
  --el-color-success-light-9: #e7f6ed;
  --el-color-warning: #d97706;
  --el-color-warning-light-9: #fdf3e3;
  --el-color-danger: #dc2626;
  --el-color-danger-light-9: #fde9e9;
  --el-color-error: #dc2626;
  --el-color-error-light-9: #fde9e9;
  --el-color-info: #0284c7;
  --el-color-info-light-9: #e5f3fb;
}

html.status-light body {
  background: #f6f8fb;
  color: #0f172a;
}
</style>
