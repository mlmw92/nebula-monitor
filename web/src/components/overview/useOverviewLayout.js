import { ref } from 'vue'

const STORAGE_KEY = 'nebula-overview-layout'

// 首页区块默认配置（顺序即默认排列，span 为 12 栅格占比）
const defaultBlocks = [
  { key: 'intelligence', title: '智能运维', span: 12, visible: true },
  { key: 'health', title: '系统健康度', span: 4, visible: true },
  { key: 'kpi', title: '关键指标', span: 4, visible: true },
  { key: 'criticalAlerts', title: '紧急告警', span: 4, visible: true },
  { key: 'hostOverview', title: '主机概览', span: 12, visible: true },
  { key: 'middleware', title: '中间件概览', span: 12, visible: true },
  { key: 'recentAlerts', title: '最近告警', span: 12, visible: true },
]

// 固定置顶区块：始终在首页最上方，不可下移、不可隐藏
const PINNED_KEYS = ['intelligence']

function clone(v) {
  return JSON.parse(JSON.stringify(v))
}

function load() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return clone(defaultBlocks)
    const saved = JSON.parse(raw)
    if (!Array.isArray(saved) || saved.length === 0) return clone(defaultBlocks)
    const byKey = Object.fromEntries(saved.map((b) => [b.key, b]))
    const order = saved.map((b) => b.key)
    // 追加默认中存在但已保存布局里缺失的区块（向前兼容）
    defaultBlocks.forEach((d) => {
      if (!order.includes(d.key)) order.push(d.key)
    })
    // 固定置顶区块强制排在首页最上方（覆盖历史本地布局）
    PINNED_KEYS.forEach((k) => {
      const i = order.indexOf(k)
      if (i > 0) {
        order.splice(i, 1)
        order.unshift(k)
      }
    })
    return order
      .map((k) => ({ ...defaultBlocks.find((d) => d.key === k), ...(byKey[k] || {}) }))
      .filter((b) => defaultBlocks.some((d) => d.key === b.key))
      .map((b) => (PINNED_KEYS.includes(b.key) ? { ...b, visible: true } : b))
  } catch {
    return clone(defaultBlocks)
  }
}

export function useOverviewLayout() {
  const blocks = ref(load())
  const editing = ref(false)

  function persist() {
    try {
      localStorage.setItem(
        STORAGE_KEY,
        JSON.stringify(blocks.value.map((b) => ({ key: b.key, span: b.span, visible: b.visible })))
      )
    } catch {
      /* ignore */
    }
  }

  function moveUp(key) {
    const i = blocks.value.findIndex((b) => b.key === key)
    // 固定置顶区块位于最前，其它区块不允许越过它们（minIndex = 置顶区块数）
    const minIndex = PINNED_KEYS.length
    if (i > minIndex) {
      const arr = blocks.value
      const tmp = arr[i - 1]
      arr[i - 1] = arr[i]
      arr[i] = tmp
      blocks.value = [...arr]
      persist()
    }
  }

  function moveDown(key) {
    if (PINNED_KEYS.includes(key)) return
    const i = blocks.value.findIndex((b) => b.key === key)
    if (i >= 0 && i < blocks.value.length - 1) {
      const arr = blocks.value
      const tmp = arr[i + 1]
      arr[i + 1] = arr[i]
      arr[i] = tmp
      blocks.value = [...arr]
      persist()
    }
  }

  function toggleVisible(key) {
    if (PINNED_KEYS.includes(key)) return
    const b = blocks.value.find((x) => x.key === key)
    if (b) {
      b.visible = !b.visible
      persist()
    }
  }

  function reset() {
    blocks.value = clone(defaultBlocks)
    persist()
  }

  function setEditing(v) {
    editing.value = v
  }

  return {
    blocks,
    editing,
    pinnedKeys: PINNED_KEYS,
    moveUp,
    moveDown,
    toggleVisible,
    reset,
    setEditing,
    persist,
  }
}
