<template><div ref="el" class="chart"></div></template>
<script setup>
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { initChart } from '../../charts/echarts'
const props = defineProps({ baseline: { type: Object, required: true } })
const el = ref(null); let chart
function draw() {
  if (!chart || !props.baseline) return
  const points = props.baseline.points || []
  const upper = props.baseline.upper || 0, lower = props.baseline.lower || 0
  chart.setOption({ backgroundColor:'transparent', grid:{ left:44,right:18,top:28,bottom:28 }, tooltip:{ trigger:'axis' }, xAxis:{ type:'time', axisLabel:{ color:'#64748b' }, axisLine:{ lineStyle:{ color:'#334155' } } }, yAxis:{ type:'value', axisLabel:{ color:'#64748b' }, splitLine:{ lineStyle:{ color:'rgba(148,163,184,.12)' } } }, series:[{ type:'line', name:'实际值', data:points.map(p=>[p.timestamp,p.value]), showSymbol:false, smooth:true, lineStyle:{ color:'#22d3ee',width:2 }, areaStyle:{ color:'rgba(34,211,238,.08)' }, markArea:{ silent:true, itemStyle:{ color:'rgba(52,211,153,.10)' }, data:[[{ yAxis:lower },{ yAxis:upper }]] }, markLine:{ symbol:'none', label:{ color:'#94a3b8' }, lineStyle:{ color:'#34d399',type:'dashed' }, data:[{ yAxis:upper,name:'基线上界' },{ yAxis:lower,name:'基线下界' }] } }] })
}
function resize(){ chart?.resize() }
onMounted(async()=>{ await nextTick(); chart=initChart(el.value); draw(); window.addEventListener('resize',resize) }); onBeforeUnmount(()=>{window.removeEventListener('resize',resize);chart?.dispose()}); watch(()=>props.baseline,draw,{deep:true})
</script>
<style scoped>.chart{height:220px;width:100%;margin-top:12px;border:1px solid rgba(148,163,184,.11);border-radius:10px;background:rgba(11,18,32,.45)}</style>
