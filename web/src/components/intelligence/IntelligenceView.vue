<template>
  <div class="intelligence-page">
    <header class="hero">
      <div><p>INTELLIGENCE LAYER · 7D ROBUST ANALYSIS</p><h2>智能分析</h2><span>将历史指标转化为基线异常、容量趋势与可解释风险信号。</span></div>
      <el-button :icon="Refresh" :loading="loading" @click="load(true)">刷新分析</el-button>
    </header>
    <div class="summary-grid">
      <div v-for="card in cards" :key="card.label" class="summary-card" :class="card.tone"><span>{{ card.label }}</span><strong>{{ card.value }}</strong><small>{{ card.desc }}</small></div>
    </div>
    <section class="panel">
      <div class="panel-head"><div><h3>风险优先级队列</h3><p>按异常强度、容量紧迫度与主机可用性综合排序；不会触发自动处置。</p></div><el-select v-model="severityFilter" clearable placeholder="全部风险" style="width:130px"><el-option label="紧急" value="critical" /><el-option label="关注" value="warning" /><el-option label="稳定" value="info" /></el-select></div>
      <el-table :data="filteredHosts" row-key="node" class="risk-table" @row-click="openHost">
        <el-table-column label="资源" min-width="170"><template #default="{ row }"><strong>{{ row.node }}</strong><small>{{ row.group || '未分组' }}</small></template></el-table-column>
        <el-table-column label="风险" width="100"><template #default="{ row }"><el-tag :type="tagType(row.severity)" effect="dark">{{ severityText(row.severity) }}</el-tag></template></el-table-column>
        <el-table-column label="评分" width="88"><template #default="{ row }"><b :class="['score', row.severity]">{{ row.score }}</b></template></el-table-column>
        <el-table-column label="关键证据" min-width="320"><template #default="{ row }"><span v-if="row.evidence?.length">{{ row.evidence[0].title }}</span><span v-else class="muted">历史数据处于稳定范围</span></template></el-table-column>
        <el-table-column label="容量预测" min-width="170"><template #default="{ row }"><span v-if="urgentForecast(row)">{{ forecastText(urgentForecast(row)) }}</span><span v-else class="muted">暂无紧迫预测</span></template></el-table-column>
      </el-table>
      <el-empty v-if="!loading && !filteredHosts.length" description="尚无可展示的分析结果" />
    </section>
    <el-drawer v-model="drawer" :title="selected?.node || '分析详情'" size="min(720px, 92vw)">
      <template v-if="selected"><div class="drawer-score"><span>综合风险评分</span><strong :class="selected.severity">{{ selected.score }}</strong><el-tag :type="tagType(selected.severity)">{{ severityText(selected.severity) }}</el-tag></div>
        <section class="drawer-section"><h4>风险依据</h4><div v-for="item in selected.evidence" :key="item.title" class="evidence"><el-tag size="small" :type="tagType(item.severity)">{{ severityText(item.severity) }}</el-tag><div><b>{{ item.title }}</b><p>{{ item.detail }}</p><small>{{ item.suggestion }}</small></div></div><el-empty v-if="!selected.evidence?.length" description="未发现持续偏离或紧迫容量风险" :image-size="55" /></section>
        <section class="drawer-section"><h4>动态基线</h4><div v-for="item in selected.baselines" :key="item.metric + JSON.stringify(item.labels)" class="baseline"><div><b>{{ metricText(item.metric) }}</b><span>{{ item.status === 'insufficient_data' ? '样本不足' : `基线 ${one(item.lower)}–${one(item.upper)}` }}</span></div><strong :class="{ alert: item.isAnomalous }">{{ one(item.latest) }}{{ item.metric === 'cpu_usage' || item.metric.includes('percent') ? '%' : '' }}</strong></div></section>
        <section class="drawer-section"><h4>趋势对照</h4><BaselineTrendChart v-for="item in selected.baselines" :key="'chart-' + item.metric + JSON.stringify(item.labels)" :baseline="item" /></section>
        <section class="drawer-section"><h4>容量预测</h4><div v-for="item in selected.forecasts" :key="JSON.stringify(item.labels)" class="forecast"><b>{{ item.labels?.mount || item.labels?.device || '磁盘分区' }}</b><span v-if="item.status === 'unknown'">{{ item.reason }}</span><span v-else>{{ forecastText(item) }} · 当前 {{ one(item.latest) }}% · 拟合度 {{ one(item.rSquared) }}</span></div></section>
      </template>
    </el-drawer>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import http from '../../api/http'
import BaselineTrendChart from './BaselineTrendChart.vue'
const summary = ref(null), loading = ref(false), severityFilter = ref(''), drawer = ref(false), selected = ref(null)
const cards = computed(() => [{ label:'需关注资源', value:summary.value?.riskCount || 0, desc:'具有可解释风险证据', tone:'orange' }, { label:'持续异常', value:summary.value?.anomalyCount || 0, desc:'连续偏离动态基线', tone:'yellow' }, { label:'容量紧迫', value:summary.value?.urgentCapacityCount || 0, desc:'预计 7 天内耗尽', tone:'red' }, { label:'分析覆盖', value:summary.value?.nodeCount || 0, desc:'主机 · 近 7 天窗口', tone:'cyan' }])
const filteredHosts = computed(() => (summary.value?.hosts || []).filter((item) => !severityFilter.value || item.severity === severityFilter.value))
function tagType(value) { return ({ critical:'danger', warning:'warning', info:'success' })[value] || 'info' }; function severityText(value) { return ({ critical:'紧急', warning:'关注', info:'稳定' })[value] || '未知' }; function metricText(value) { return ({ cpu_usage:'CPU 使用率', mem_used_percent:'内存使用率', disk_used_percent:'磁盘使用率' })[value] || value }; function one(value) { return Number(value || 0).toFixed(1) }
function urgentForecast(host) { return (host.forecasts || []).find((item) => item.status === 'urgent' || item.status === 'warning') }
function forecastText(item) { return item.daysRemaining < 1 ? '不足 1 天耗尽' : `约 ${Math.ceil(item.daysRemaining)} 天后耗尽` }
function openHost(row) { selected.value = row; drawer.value = true }
async function load(refresh = false) { loading.value = true; try { summary.value = await http.analysisSummary(refresh) } catch (error) { console.error('加载智能分析失败', error) } finally { loading.value = false } }
onMounted(() => load())
</script>

<style scoped>
.intelligence-page{padding:4px;max-width:1440px}.hero{display:flex;align-items:center;justify-content:space-between;gap:24px;padding:26px 28px;border:1px solid rgba(99,102,241,.3);border-radius:18px;background:radial-gradient(circle at 82% 15%,rgba(34,211,238,.16),transparent 28%),linear-gradient(115deg,#111c2e,#0b1220);}.hero p{margin:0 0 7px;color:#22d3ee;font:700 10px/1.2 PingFang SC;letter-spacing:.15em}.hero h2{margin:0;color:#e5edf7;font-size:26px}.hero span,.panel-head p{color:#94a3b8;font-size:13px}.summary-grid{display:grid;grid-template-columns:repeat(4,1fr);gap:14px;margin:16px 0}.summary-card{padding:18px;border:1px solid rgba(148,163,184,.13);border-radius:14px;background:#111c2e}.summary-card span,.summary-card small{display:block;color:#94a3b8;font-size:12px}.summary-card strong{display:block;margin:9px 0 5px;color:#e5edf7;font-size:30px}.summary-card.orange strong{color:#fb923c}.summary-card.yellow strong{color:#fbbf24}.summary-card.red strong{color:#f87171}.summary-card.cyan strong{color:#22d3ee}.panel{padding:20px;border:1px solid rgba(148,163,184,.12);border-radius:16px;background:#111c2e}.panel-head{display:flex;justify-content:space-between;gap:16px;align-items:center;margin-bottom:14px}.panel-head h3,.drawer-section h4{margin:0;color:#e5edf7;font-size:16px}.panel-head p{margin:6px 0 0}.risk-table{--el-table-bg-color:transparent;--el-table-tr-bg-color:transparent;--el-table-header-bg-color:rgba(255,255,255,.03);--el-table-border-color:rgba(148,163,184,.12);--el-table-text-color:#cbd5e1;--el-table-header-text-color:#94a3b8}.risk-table small{display:block;color:#64748b;margin-top:3px}.score{font-size:18px}.score.critical{color:#f87171}.score.warning{color:#fbbf24}.score.info{color:#34d399}.muted{color:#64748b}.drawer-score{display:flex;align-items:center;gap:13px;padding:16px;border-radius:12px;background:linear-gradient(110deg,rgba(99,102,241,.17),rgba(34,211,238,.06))}.drawer-score span{color:#94a3b8}.drawer-score strong{font-size:34px}.drawer-score strong.critical{color:#f87171}.drawer-score strong.warning{color:#fbbf24}.drawer-score strong.info{color:#34d399}.drawer-section{margin-top:26px}.evidence{display:flex;gap:10px;padding:13px 0;border-bottom:1px solid rgba(148,163,184,.12)}.evidence b,.baseline b,.forecast b{color:#e5edf7}.evidence p{margin:5px 0;color:#cbd5e1;font-size:13px}.evidence small,.baseline span,.forecast span{color:#94a3b8;font-size:12px}.baseline,.forecast{display:flex;justify-content:space-between;gap:12px;padding:12px 0;border-bottom:1px solid rgba(148,163,184,.12)}.baseline span{display:block;margin-top:4px}.baseline strong{color:#34d399}.baseline strong.alert{color:#f87171}.forecast{display:block}.forecast span{display:block;margin-top:5px}@media(max-width:850px){.hero,.panel-head{align-items:flex-start;flex-direction:column}.summary-grid{grid-template-columns:repeat(2,1fr)}.risk-table{font-size:12px}}
</style>
