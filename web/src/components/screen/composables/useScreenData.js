// useScreenData.js — 大屏数据获取 composable（从 ScreenView.vue 提取）
import { ref, computed } from 'vue'
import http from '../../../api/http'
import { rateShort } from '../../../charts/echarts'

const MW_TYPES = ['redis', 'mysql', 'postgres', 'nginx', 'kafka', 'docker', 'rocketmq', 'k8s']
const MW_LABELS = {
  redis: 'Redis', mysql: 'MySQL', postgres: 'PG', nginx: 'Nginx',
  kafka: 'Kafka', docker: 'Docker', rocketmq: 'MQ', k8s: 'K8s',
}

export function useScreenData() {
  const nodes = ref([])
  const metrics = ref({})
  const alerts = ref([])
  const mwInstances = ref([])
  const nginxSummary = ref(null)

  const activeAlerts = computed(() => alerts.value.filter((a) => a.state === 'firing'))
  const firingCount = computed(() => activeAlerts.value.length)

  const nodeCards = computed(() =>
    nodes.value.map((n) => ({
      name: n.hostname,
      ip: n.ip || '-',
      online: n.status === 'online',
      cpu: metrics.value[n.hostname]?.cpu || 0,
      mem: metrics.value[n.hostname]?.mem || 0,
      disk: metrics.value[n.hostname]?.disk || 0,
      load1: metrics.value[n.hostname]?.load1 || 0,
      load: metrics.value[n.hostname]?.load1 || 0,
      netIn: metrics.value[n.hostname]?.netIn || 0,
      netOut: metrics.value[n.hostname]?.netOut || 0,
      memTotal: metrics.value[n.hostname]?.memTotal || 0,
      procCount: metrics.value[n.hostname]?.procCount || 0,
    }))
  )

  async function loadBase() {
    if (!document.visibilityState || document.visibilityState === 'visible') {
      // 可见性由 ScreenView 管理，这里只做数据加载
    }
    try {
      const [nd, ad, md] = await Promise.all([
        http.get('/api/v1/nodes').catch(() => ({ nodes: [] })),
        http.get('/api/v1/alerts?state=active').catch(() => ({ alerts: [] })),
        http.get('/api/v1/nodes/latest').catch(() => ({ metrics: {} })),
      ])
      nodes.value = nd.nodes || []
      alerts.value = ad.alerts || []
      metrics.value = md.metrics || {}
    } catch (e) {
      /* ignore */
    }
  }

  async function loadMiddleware() {
    const results = await Promise.all(
      MW_TYPES.map((t) =>
        t === 'docker'
          ? http.get('/api/v1/middleware/docker/containers').catch(() => ({ containers: [] }))
          : http.get(`/api/v1/middleware/${t}/instances`).catch(() => ({ instances: [] }))
      )
    )
    const list = []
    results.forEach((res, i) => {
      const type = MW_TYPES[i]
      const items = type === 'docker' ? res?.containers || [] : res?.instances || []
      items.forEach((it) => {
        list.push({
          type: MW_LABELS[type] || type,
          name: it.name || it.container || it.ip || it.instance || '-',
          node: it.node || '-',
          status: it.up || it.online ? '在线' : '离线',
        })
      })
    })
    mwInstances.value = list
  }

  async function loadNginx() {
    try {
      nginxSummary.value = await http.get('/api/v1/middleware/nginx/access/summary')
    } catch (e) {
      nginxSummary.value = null
    }
  }

  async function refreshAll() {
    await Promise.all([loadBase(), loadMiddleware(), loadNginx()])
  }

  return {
    nodes,
    metrics,
    alerts,
    mwInstances,
    nginxSummary,
    nodeCards,
    activeAlerts,
    firingCount,
    loadBase,
    loadMiddleware,
    loadNginx,
    refreshAll,
  }
}