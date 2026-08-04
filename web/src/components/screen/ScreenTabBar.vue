<template>
  <nav class="screen-tab-bar" ref="barRef">
    <div class="tab-inner">
      <button
        v-for="tab in tabs"
        :key="tab.key"
        class="tab-item"
        :class="{ active: active === tab.key }"
        @click="emit('update:active', tab.key)"
      >
        <span class="tab-icon" v-html="tab.icon"></span>
        <span class="tab-label">{{ tab.label }}</span>
        <span class="tab-badge" v-if="tab.badge != null && tab.badge > 0">{{ tab.badge > 99 ? '99+' : tab.badge }}</span>
      </button>
    </div>
    <!-- 滑动指示器 -->
    <div class="tab-indicator" :style="indicatorStyle"></div>
  </nav>
</template>

<script setup>
import { ref, computed, watch, nextTick } from 'vue'

const props = defineProps({
  active: { type: String, default: 'overview' },
  tabs: { type: Array, default: () => [] },
  firingCount: { type: Number, default: 0 },
})
const emit = defineEmits(['update:active'])

const barRef = ref(null)
const indicatorStyle = ref({ width: 0, transform: 'translateX(0)' })

// 默认 Tab 定义
const defaultTabs = [
  {
    key: 'overview',
    label: '概览',
    icon: '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="7" height="7"/><rect x="14" y="3" width="7" height="7"/><rect x="14" y="14" width="7" height="7"/><rect x="3" y="14" width="7" height="7"/></svg>',
  },
  {
    key: 'hosts',
    label: '主机',
    icon: '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="20" height="14" rx="2" ry="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/></svg>',
  },
  {
    key: 'middleware',
    label: '中间件',
    icon: '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M21 12c0 1.66-4 3-9 3s-9-1.34-9-3"/><path d="M3 5v14c0 1.66 4 3 9 3s9-1.34 9-3V5"/></svg>',
  },
  {
    key: 'network',
    label: '网络',
    icon: '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="2" y1="12" x2="22" y2="12"/><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/></svg>',
  },
  {
    key: 'alerts',
    label: '告警',
    icon: '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9"/><path d="M13.73 21a2 2 0 0 1-3.46 0"/></svg>',
    badge: props.firingCount,
  },
]

const tabs = computed(() => {
  const list = props.tabs.length ? props.tabs : defaultTabs
  // 更新告警 Tab 的徽章
  return list.map((t) =>
    t.key === 'alerts' ? { ...t, badge: props.firingCount } : t
  )
})

function updateIndicator() {
  if (!barRef.value) return
  const activeEl = barRef.value.querySelector('.tab-item.active')
  if (!activeEl) return
  const barRect = barRef.value.getBoundingClientRect()
  const elRect = activeEl.getBoundingClientRect()
  indicatorStyle.value = {
    width: elRect.width * 0.7 + 'px',
    transform: `translateX(${elRect.left - barRect.left + elRect.width * 0.15}px)`,
  }
}

watch(() => props.active, () => {
  nextTick(updateIndicator)
})

// 初始更新
import { onMounted } from 'vue'
onMounted(() => {
  nextTick(updateIndicator)
})
</script>

<style scoped>
.screen-tab-bar {
  position: relative;
  display: flex;
  align-items: center;
  height: 44px;
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  z-index: 2;
  overflow: visible;
}

.tab-inner {
  display: flex;
  align-items: center;
  gap: 2px;
  padding: 0 4px;
  width: 100%;
  height: 100%;
}

.tab-item {
  position: relative;
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 0 18px;
  height: 100%;
  background: transparent;
  border: none;
  color: var(--text-dim);
  font-size: 13px;
  font-family: var(--font);
  letter-spacing: 0.06em;
  cursor: pointer;
  transition: color 0.2s, background 0.2s;
  white-space: nowrap;
  user-select: none;
  border-radius: 8px;
  z-index: 1;
}

.tab-item:hover {
  color: var(--text);
  background: rgba(255, 255, 255, 0.04);
}

.tab-item.active {
  color: var(--accent);
}

.tab-icon {
  display: flex;
  align-items: center;
  justify-content: center;
  opacity: 0.7;
}

.tab-item.active .tab-icon {
  opacity: 1;
}

.tab-icon :deep(svg) {
  width: 16px;
  height: 16px;
}

.tab-badge {
  position: absolute;
  top: 6px;
  right: 6px;
  min-width: 16px;
  height: 16px;
  padding: 0 5px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 8px;
  background: var(--danger);
  color: #fff;
  font-size: 10px;
  font-weight: 700;
  font-family: var(--mono);
  line-height: 1;
  box-shadow: 0 0 8px var(--danger);
}

.tab-indicator {
  position: absolute;
  bottom: 0;
  height: 2px;
  background: var(--accent);
  border-radius: 1px;
  box-shadow: 0 0 8px var(--accent-glow), 0 0 16px var(--accent-glow);
  transition: transform 0.3s cubic-bezier(0.4, 0, 0.2, 1), width 0.3s cubic-bezier(0.4, 0, 0.2, 1);
  z-index: 2;
}

/* 4K 适配 */
@media (min-width: 2400px) {
  .screen-tab-bar {
    height: 56px;
  }
  .tab-item {
    font-size: 16px;
    padding: 0 24px;
    gap: 8px;
  }
  .tab-icon :deep(svg) {
    width: 20px;
    height: 20px;
  }
  .tab-badge {
    top: 8px;
    right: 8px;
    min-width: 20px;
    height: 20px;
    font-size: 12px;
  }
}

@media (min-width: 3440px) {
  .screen-tab-bar {
    height: 68px;
  }
  .tab-item {
    font-size: 20px;
    padding: 0 32px;
    gap: 10px;
  }
  .tab-icon :deep(svg) {
    width: 24px;
    height: 24px;
  }
  .tab-badge {
    top: 10px;
    right: 10px;
    min-width: 24px;
    height: 24px;
    font-size: 14px;
  }
}
</style>