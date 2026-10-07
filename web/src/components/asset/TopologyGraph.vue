<!--
  资产关系图（N 跳邻域）。力导向布局，点节点即以它为中心重新展开。

  抽成独立组件的原因：这张图有三个入口——台账详情（看某个资产的关系）、
  告警影响面（看这条告警会影响谁）、全库关系视图（看符合筛选的那批边）。
  三处必须**完全同一套**范围裁剪、节点着色与交互，否则同一个资产在不同入口
  会画出不同的图，而这种不一致没有报错、只在现场被当成"数据不对"。

  两种工作模式（由 reCenter 区分）：
  - **邻域模式**（默认）：组件自己按 assetId 取数，点节点换中心；
  - **数据模式**（传了 data）：数据由调用方给（例如全库筛选子图），没有"中心"这个概念，
    因此不显示跳数、点节点改为向外抛 node-click（由调用方决定去哪里）。
-->
<template>
  <div class="topo">
    <div class="topo-bar">
      <!-- 跳数切换必须显式忽略事件参数：el-radio-group 的 change 会把新值当第一个实参传进来，
           而 load 的第一个参数是资产 id——直接绑 load 会把"3 跳"当成"资产 3"。 -->
      <el-radio-group v-if="reCenter" v-model="depth" size="small" @change="() => load()">
        <el-radio-button :value="1">1 跳</el-radio-button>
        <el-radio-button :value="2">2 跳</el-radio-button>
        <el-radio-button :value="3">3 跳</el-radio-button>
      </el-radio-group>
      <span v-if="reCenter" class="muted">点节点可以「以它为中心」重新展开</span>
      <span v-else class="muted">点节点打开该资产的详情</span>
      <span class="muted topo-legend">实线 = 采集发现 · 虚线 = 人工维护 · 红圈 = 失联</span>
    </div>
    <el-alert
      v-if="truncated"
      type="warning"
      :closable="false"
      show-icon
      :title="truncatedTitle"
      :description="truncatedHint"
      class="alert-gap"
    />
    <el-alert v-if="error" type="error" :closable="false" show-icon :title="error" class="alert-gap" />
    <div v-show="!error" ref="chartRef" class="topo-chart" :style="{ height: height + 'px' }"></div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onBeforeUnmount, nextTick, watch } from 'vue'
import * as echarts from 'echarts'
import { getAssetTopology } from '../../api/asset'

const props = defineProps({
  // 邻域模式必传（以谁为中心）；数据模式下不需要。
  assetId: { type: [Number, String], default: null },
  // 画布高度：抽屉里要矮一些，独立弹窗里可以高一些。
  height: { type: Number, default: 460 },
  // data 非空即进入**数据模式**：不自己取数、没有中心、点节点只抛事件。
  // 形状与邻域载荷一致（nodes/edges/truncated），因此两处画出来的是同一种图。
  data: { type: Object, default: null },
  // 是否允许"点节点换中心"。全库视图里没有中心可言，置 false。
  reCenter: { type: Boolean, default: true },
})

const emit = defineEmits(['node-click'])

const depth = ref(2)
const error = ref('')
const topo = ref(null)
const chartRef = ref(null)
let chart = null

const truncated = computed(() => !!(topo.value && topo.value.truncated))

// 截断提示要跟着模式走：邻域模式的边界是**节点数**（能缩跳数），数据模式常常是**边数**
// （只能缩筛选）。说错是哪一种，用户就不知道该往哪调。
const truncatedTitle = computed(() => (props.reCenter
  ? '节点数达到上限，图只展开了部分关系'
  : '命中结果超过上限，图只画出了前一部分'))
const truncatedHint = computed(() => (props.reCenter
  ? '缩小跳数，或直接以目标资产为中心查看。静默省略会让人以为关系就这么多。'
  : '当前筛选命中的关系超过上限，缩小筛选范围可以看到全部。静默省略会让人以为关系就这么多。'))

// 类型 → 颜色。与图例共用同一份定义，避免"图上是蓝的、图例说是绿的"。
const TOPO_COLORS = {
  host: '#4a9df0',
  'middleware-instance': '#00d9a3',
  pod: '#e6a23c',
  workload: '#8b5cf6',
}

// 状态标签与台账列表共用一套措辞（"上报正常/失联"而不是"在线/离线"：
// 后者是采集侧探活的结果，两者同词会让人觉得自相矛盾）。
const STATUS_LABELS = { online: '上报正常', missing: '失联', archived: '归档' }
const statusLabel = (s) => STATUS_LABELS[s] || s || '—'

// load 取邻域并重绘。id 省略时以当前资产为中心（点节点会显式传入新中心）。
async function load(id) {
  const target = id === undefined ? props.assetId : id
  error.value = ''
  try {
    topo.value = await getAssetTopology(target, { depth: depth.value })
  } catch (e) {
    topo.value = null
    error.value = e.message || '加载关系图失败'
  }
  await nextTick()
  render()
}

// render 把邻域画成力导向图：节点大小按跳数（中心最大）、颜色按类型、
// 红圈表示失联；边用虚线区分人工维护。
function render() {
  const data = topo.value
  if (!data || !chartRef.value) return
  if (!chart) chart = echarts.init(chartRef.value)
  const nodes = (data.nodes || []).map((n) => ({
    id: n.key,
    name: n.name || n.naturalKey,
    symbolSize: n.root ? 52 : n.depth === 1 ? 38 : 28,
    itemStyle: {
      color: TOPO_COLORS[n.typeKey] || '#909399',
      // 已从台账隐藏的资产画成半透明：它仍在关系里，但已不是"要治理的对象"
      opacity: n.ignored ? 0.45 : 1,
      borderColor: n.status === 'online' ? 'transparent' : '#f56c6c',
      borderWidth: n.status === 'online' ? 0 : 2,
    },
    // 原始节点挂在 data 上：tooltip 与点击回调都要用
    raw: n,
    label: { show: true, fontSize: 11 },
  }))
  const links = (data.edges || []).map((e) => ({
    source: e.from,
    target: e.to,
    raw: e,
    lineStyle: { type: e.source === 'manual' ? 'dashed' : 'solid', width: 1.4, color: '#9aa4b2' },
    label: { show: true, formatter: e.kind, fontSize: 10, color: '#8a94a6' },
  }))

  chart.setOption(
    {
      tooltip: {
        formatter: (p) => {
          if (p.dataType === 'edge') {
            const e = p.data.raw || {}
            return `${e.kind}<br/>${e.from}<br/>↓<br/>${e.to}<br/>来源：${e.source === 'manual' ? '人工维护' : '采集发现'}`
          }
          const n = (p.data && p.data.raw) || {}
          // 数据模式没有中心，"距中心 N 跳"会是一句假话（那里所有节点的 depth 都是 0）
          const where = props.reCenter ? (n.depth ? `距中心 ${n.depth} 跳` : '（中心）') : ''
          const head = `${n.typeTitle || ''}：${n.name || n.naturalKey}`
          const tail = where ? `<br/>${where}` : ''
          return `${head}<br/>归属节点：${n.node || '—'}<br/>状态：${statusLabel(n.status)}${tail}`
        },
      },
      series: [
        {
          type: 'graph',
          layout: 'force',
          roam: true,
          draggable: true,
          data: nodes,
          links,
          // 力导向参数刻意收敛：关系图节点数不多，斥力太大只会散成一团看不清
          force: { repulsion: 320, edgeLength: 130, gravity: 0.08 },
          emphasis: { focus: 'adjacency' },
          edgeSymbol: ['none', 'arrow'],
          edgeSymbolSize: 7,
        },
      ],
    },
    true
  )
  chart.off('click')
  chart.on('click', (p) => {
    const n = p && p.data && p.data.raw
    if (!n) return
    if (!props.reCenter) {
      // 数据模式没有中心，交给调用方决定去哪里（全库视图拿它打开资产详情）
      emit('node-click', n)
      return
    }
    // 点中心自己不必重查（已经是以它为中心）
    if (n.root) return
    load(n.id)
  })
}

function dispose() {
  if (chart) {
    chart.dispose()
    chart = null
  }
}

// 换资产（同一个组件被复用时）必须重新拉取并重建图表：
// 否则会画出上一个资产的图，而界面上没有任何提示。
watch(() => props.assetId, () => {
  if (props.data) return
  dispose()
  load()
})

// 数据模式：数据由调用方给（筛选变了就换一份），换数据必须重建图表。
watch(() => props.data, (d) => {
  if (!d) return
  dispose()
  topo.value = d
  render()
})

onMounted(() => {
  if (props.data) {
    topo.value = props.data
    render()
    return
  }
  load()
})
onBeforeUnmount(dispose)
</script>

<style scoped>
.topo-bar {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 10px;
}
.topo-legend {
  margin-left: auto;
}
.topo-chart {
  width: 100%;
}
</style>
