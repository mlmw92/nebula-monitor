<template>
  <section class="coverage-card" :class="{ ready: coverage?.ready }">
    <div class="coverage-head"><div><p>DATA CONFIDENCE</p><h3>分析数据就绪度</h3></div><el-tag :type="coverage?.ready ? 'success' : 'warning'" effect="dark">{{ coverage?.ready ? '结论可用' : '仍在采集' }}</el-tag></div>
    <div class="coverage-progress"><div class="progress-track"><i :style="{ width: progress + '%' }"></i></div><b>{{ progress }}%</b></div>
    <p class="coverage-message">{{ coverage?.message || '等待历史指标进入分析窗口' }}</p>
    <div v-if="coverage?.missingMetrics?.length" class="missing"><span>待补齐指标</span><el-tag v-for="metric in coverage.missingMetrics" :key="metric" size="small" effect="plain">{{ metricText(metric) }}</el-tag></div>
    <small>已获得 {{ coverage?.validSamples || 0 }} / {{ coverage?.requiredSamples || 24 }} 个小时级有效样本</small>
  </section>
</template>
<script setup>
import { computed } from 'vue'
const props = defineProps({ coverage:{ type:Object, default:()=>({}) } })
const progress = computed(()=>Math.min(100, Math.round((props.coverage?.validSamples || 0) / Math.max(props.coverage?.requiredSamples || 24, 1) * 100)))
function metricText(value) { return ({ cpu_usage:'CPU', mem_used_percent:'内存', disk_used_percent:'磁盘' })[value] || value }
</script>
<style scoped>
.coverage-card{padding:18px;border:1px solid rgba(251,191,36,.25);border-radius:15px;background:linear-gradient(125deg,rgba(251,191,36,.09),rgba(17,28,46,.86));}.coverage-card.ready{border-color:rgba(52,211,153,.25);background:linear-gradient(125deg,rgba(52,211,153,.08),rgba(17,28,46,.86));}.coverage-head,.coverage-progress,.missing{display:flex;align-items:center;justify-content:space-between;gap:12px}.coverage-head p{margin:0 0 4px;color:#fbbf24;font-size:10px;font-weight:700;letter-spacing:.14em}.ready .coverage-head p{color:#34d399}.coverage-head h3{margin:0;color:#e5edf7;font-size:16px}.coverage-progress{margin:18px 0 8px}.coverage-progress b{color:#e5edf7}.progress-track{height:7px;flex:1;overflow:hidden;border-radius:20px;background:rgba(148,163,184,.15)}.progress-track i{display:block;height:100%;border-radius:inherit;background:linear-gradient(90deg,#fbbf24,#fb923c);transition:width .45s ease}.ready .progress-track i{background:linear-gradient(90deg,#34d399,#22d3ee)}.coverage-message{margin:8px 0;color:#cbd5e1;font-size:13px}.missing{justify-content:flex-start;flex-wrap:wrap;margin:12px 0;color:#94a3b8;font-size:12px}.coverage-card>small{color:#64748b;font-size:12px}
</style>
