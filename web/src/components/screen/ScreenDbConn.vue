<template>
  <div class="glass panel-mini screen-dbconn">
    <div class="db-title">数据库连接</div>
    <div class="db-empty" v-if="!stats.instances">
      <span>暂无数据库实例</span>
    </div>
    <template v-else>
      <div class="db-kpi">
        <div class="dbk">
          <div class="dbk-v cyan mono">{{ stats.current }}</div>
          <div class="dbk-l">当前连接</div>
        </div>
        <div class="dbk">
          <div class="dbk-v amber mono">{{ stats.max }}</div>
          <div class="dbk-l">最大连接</div>
        </div>
        <div class="dbk">
          <div class="dbk-v mono">{{ stats.instances }}</div>
          <div class="dbk-l">实例数</div>
        </div>
      </div>
      <div class="db-usage">
        <div class="db-usage-head">
          <span>连接使用率</span>
          <b class="mono">{{ usagePct }}%</b>
        </div>
        <div class="db-usage-track">
          <span class="db-usage-fill" :class="usageTone" :style="{ width: usagePct + '%' }"></span>
        </div>
      </div>
      <div class="db-breakdown">
        <div class="db-bd" v-if="stats.mysql">
          <span class="d mysql"></span>MySQL<b class="mono">{{ stats.mysql }}</b>
        </div>
        <div class="db-bd" v-if="stats.postgres">
          <span class="d pg"></span>PostgreSQL<b class="mono">{{ stats.postgres }}</b>
        </div>
        <div class="db-bd" v-if="stats.mongo">
          <span class="d mongo"></span>MongoDB<b class="mono">{{ stats.mongo }}</b>
        </div>
      </div>
    </template>
  </div>
</template>

<script setup>
import { reactive, computed, onMounted, onUnmounted } from 'vue'
import http from '../../api/http'

const stats = reactive({ current: 0, max: 0, instances: 0, mysql: 0, postgres: 0, mongo: 0 })
let timer = null
let visible = true

const usagePct = computed(() => (stats.max ? Math.round((stats.current / stats.max) * 100) : 0))
const usageTone = computed(() => {
  if (usagePct.value >= 90) return 'danger'
  if (usagePct.value >= 70) return 'warn'
  return 'ok'
})

async function load() {
  if (!visible) return
  try {
    const [my, pg, mongo] = await Promise.all([
      http.get('/api/v1/middleware/mysql/instances').catch(() => ({ instances: [] })),
      http.get('/api/v1/middleware/postgres/instances').catch(() => ({ instances: [] })),
      http.get('/api/v1/middleware/mongodb/instances').catch(() => ({ instances: [] })),
    ])
    const myList = my.instances || []
    const pgList = pg.instances || []
    const mongoList = mongo.instances || []
    const myConn = myList.reduce((s, i) => s + (i.threadsConnected || 0), 0)
    const pgConn = pgList.reduce((s, i) => s + (i.numbackends || 0), 0)
    const mongoConn = mongoList.reduce((s, i) => s + (i.connectionsCurrent || 0), 0)
    const myMax = myList.reduce((s, i) => s + (i.maxConnections || 0), 0)
    const pgMax = pgList.reduce((s, i) => s + (i.maxConnections || 0), 0)
    const mongoMax = mongoList.reduce((s, i) => s + (i.connectionsAvailable || 0), 0)
    stats.mysql = Math.round(myConn)
    stats.postgres = Math.round(pgConn)
    stats.mongo = Math.round(mongoConn)
    stats.current = Math.round(myConn + pgConn + mongoConn)
    stats.max = Math.round(myMax + pgMax + mongoMax)
    stats.instances = myList.length + pgList.length + mongoList.length
  } catch (e) {
    /* ignore */
  }
}

function onVis() {
  visible = document.visibilityState === 'visible'
  if (visible) load()
}

onMounted(() => {
  load()
  timer = setInterval(load, 30000)
  document.addEventListener('visibilitychange', onVis)
})
onUnmounted(() => {
  timer && clearInterval(timer)
  document.removeEventListener('visibilitychange', onVis)
})
</script>

<style scoped>
.screen-dbconn {
  padding: 12px 14px;
  display: flex;
  flex-direction: column;
}
.db-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 10px;
}
.db-empty {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--text-dim);
  font-size: 12px;
}
.db-kpi {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 8px;
  margin-bottom: 12px;
}
.dbk {
  text-align: center;
  padding: 6px 2px;
  background: rgba(255, 255, 255, 0.02);
  border-radius: 7px;
}
.dbk-v {
  font-size: 18px;
  font-weight: 700;
  line-height: 1.1;
  color: var(--text);
}
.dbk-v.cyan { color: var(--info); }
.dbk-v.amber { color: var(--warn); }
.dbk-l {
  font-size: 10px;
  color: var(--text-dim);
  margin-top: 3px;
}
.db-usage-head {
  display: flex;
  justify-content: space-between;
  font-size: 12px;
  color: var(--text-dim);
  margin-bottom: 5px;
}
.db-usage-head b { color: var(--text); }
.db-usage-track {
  height: 8px;
  background: rgba(255, 255, 255, 0.06);
  border-radius: 4px;
  overflow: hidden;
}
.db-usage-fill {
  display: block;
  height: 100%;
  border-radius: 4px;
  transition: width 0.6s ease;
}
.db-usage-fill.ok { background: var(--accent); }
.db-usage-fill.warn { background: var(--warn); }
.db-usage-fill.danger { background: var(--danger); }
.db-breakdown {
  display: flex;
  gap: 14px;
  margin-top: 12px;
}
.db-bd {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--text-dim);
}
.db-bd .d {
  width: 8px;
  height: 8px;
  border-radius: 2px;
}
.db-bd .d.mysql { background: var(--info); }
.db-bd .d.pg { background: var(--violet); }
.db-bd .d.mongo { background: var(--chart-green); }
.db-bd b {
  color: var(--text);
  margin-left: 2px;
}
</style>
