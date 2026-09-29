<template>
  <div class="middleware-view">
    <!-- 页面标题 -->
    <div class="page-header">
      <div class="header-left">
        <h2 class="page-title">中间件监控</h2>
        <p class="page-desc">
          各类中间件实例监控与可视化。未部署的类型可在右侧「展示类型」中隐藏。
        </p>
      </div>
      <div class="header-actions">
        <el-button @click="viewDialog = true">展示类型</el-button>
      </div>
    </div>

    <!-- 展示类型配置 -->
    <el-dialog v-model="viewDialog" title="选择展示的中间件类型" width="480">
      <div class="view-config-tip">勾选要在中间件监控页展示的类型；全部取消勾选表示展示全部。</div>
      <el-checkbox-group v-model="viewSelection">
        <el-checkbox v-for="t in viewAvailable" :key="t" :value="t" class="view-cb">{{ viewLabel(t) }}</el-checkbox>
      </el-checkbox-group>
      <template #footer>
        <el-button @click="viewDialog = false">取消</el-button>
        <el-button @click="saveViewConfig">恢复全部</el-button>
        <el-button type="primary" :loading="savingView" @click="applyViewConfig">保存</el-button>
      </template>
    </el-dialog>

    <!-- 中间件类型 Tab -->
    <el-tabs v-model="activeTab" class="mw-tabs" type="border-card">
      <el-tab-pane label="Redis" name="redis">
        <template #label>
          <span class="tab-label">
            <img class="tab-icon" :src="redisIcon" alt="Redis" />Redis
          </span>
        </template>
        <RedisTab v-if="activeTab === 'redis'" />
      </el-tab-pane>
      <el-tab-pane label="MySQL" name="mysql">
        <template #label>
          <span class="tab-label">
            <img class="tab-icon" :src="mysqlIcon" alt="MySQL" />MySQL
          </span>
        </template>
        <MySQLTab v-if="activeTab === 'mysql'" />
      </el-tab-pane>
      <el-tab-pane label="PostgreSQL" name="postgres">
        <template #label>
          <span class="tab-label">
            <img class="tab-icon" :src="postgresIcon" alt="PostgreSQL" />PostgreSQL
          </span>
        </template>
        <PostgresTab v-if="activeTab === 'postgres'" />
      </el-tab-pane>
      <el-tab-pane label="Nginx" name="nginx">
        <template #label>
          <span class="tab-label">
            <img class="tab-icon" :src="nginxIcon" alt="Nginx" />Nginx
          </span>
        </template>
        <NginxTab v-if="activeTab === 'nginx'" />
      </el-tab-pane>
      <el-tab-pane label="Kafka" name="kafka">
        <template #label>
          <span class="tab-label">
            <img class="tab-icon" :src="kafkaIcon" alt="Kafka" />Kafka
          </span>
        </template>
        <KafkaTab v-if="activeTab === 'kafka'" />
      </el-tab-pane>
      <el-tab-pane label="Docker" name="docker">
        <template #label>
          <span class="tab-label">
            <img class="tab-icon" :src="dockerIcon" alt="Docker" />Docker
          </span>
        </template>
        <DockerTab v-if="activeTab === 'docker'" />
      </el-tab-pane>
      <el-tab-pane label="RocketMQ" name="rocketmq">
        <template #label>
          <span class="tab-label">
            <img class="tab-icon" :src="rocketmqIcon" alt="RocketMQ" />RocketMQ
          </span>
        </template>
        <RocketMQTab v-if="activeTab === 'rocketmq'" />
      </el-tab-pane>
      <el-tab-pane label="Kubernetes" name="k8s">
        <template #label>
          <span class="tab-label">
            <img class="tab-icon" :src="k8sIcon" alt="Kubernetes" />Kubernetes
          </span>
        </template>
        <K8sTab v-if="activeTab === 'k8s'" />
      </el-tab-pane>
      <el-tab-pane label="MongoDB" name="mongodb">
        <template #label>
          <span class="tab-label">
            <img class="tab-icon" :src="mongodbIcon" alt="MongoDB" />MongoDB
          </span>
        </template>
        <MongoTab v-if="activeTab === 'mongodb'" />
      </el-tab-pane>
      <el-tab-pane label="FastDFS" name="fastdfs">
        <template #label>
          <span class="tab-label">
            <img class="tab-icon" :src="fastdfsIcon" alt="FastDFS" />FastDFS
          </span>
        </template>
        <FastDFSTab v-if="activeTab === 'fastdfs'" />
      </el-tab-pane>
      <!-- 轻采集内置类型：存活 + 核心指标，共用一个 spec 驱动的通用组件 -->
      <el-tab-pane v-for="t in builtinTabList" :key="t.type" :label="t.label" :name="t.type">
        <template #label>
          <span class="tab-label">
            <img class="tab-icon" :src="t.icon" :alt="t.label" />{{ t.label }}
          </span>
        </template>
        <component :is="t.comp" v-if="activeTab === t.type" />
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<script setup>
import { ref, watch, defineAsyncComponent, h, onMounted, computed } from 'vue'
import { useRoute } from 'vue-router'
import http from '../api/http'
import './mw/mw.css'

// 各中间件 Tab 改为异步组件，拆分为独立 chunk，避免进入中间件页面时
// 一次性下载全部 Tab 代码导致首屏卡顿（仅激活的 Tab 才按需加载）
const tabLoader = (loader) => defineAsyncComponent({
  loader,
  delay: 120,
  loadingComponent: { render: () => h('div', { class: 'tab-loading' }, '加载中…') },
  errorComponent: { render: () => h('div', { class: 'tab-error' }, '页面加载失败，请刷新页面重试') },
})
const RedisTab = tabLoader(() => import('./redis/RedisTab.vue'))
const MySQLTab = tabLoader(() => import('./mysql/MySQLTab.vue'))
const PostgresTab = tabLoader(() => import('./postgres/PostgresTab.vue'))
const NginxTab = tabLoader(() => import('./nginx/NginxTab.vue'))
const KafkaTab = tabLoader(() => import('./kafka/KafkaTab.vue'))
const DockerTab = tabLoader(() => import('./docker/DockerTab.vue'))
const RocketMQTab = tabLoader(() => import('./rocketmq/RocketMQTab.vue'))
const K8sTab = tabLoader(() => import('./k8s/K8sTab.vue'))
const MongoTab = tabLoader(() => import('./mongo/MongoTab.vue'))
const FastDFSTab = tabLoader(() => import('./fastdfs/FastDFSTab.vue'))
// 轻采集内置类型（RabbitMQ/ES/ClickHouse/Nacos/ZooKeeper）：各自独立 Tab 组件
// （共用基座 mw/LightCollectorTab.vue，spec 放在各自目录，便于后续按类型扩展展示）
const RabbitMQTab = tabLoader(() => import('./rabbitmq/RabbitMQTab.vue'))
const ElasticsearchTab = tabLoader(() => import('./elasticsearch/ElasticsearchTab.vue'))
const ClickHouseTab = tabLoader(() => import('./clickhouse/ClickHouseTab.vue'))
const NacosTab = tabLoader(() => import('./nacos/NacosTab.vue'))
const ZooKeeperTab = tabLoader(() => import('./zookeeper/ZooKeeperTab.vue'))
import rabbitmqIcon from '../assets/img/rabbitmq.svg'
import elasticsearchIcon from '../assets/img/elasticsearch.svg'
import clickhouseIcon from '../assets/img/clickhouse.svg'
import nacosIcon from '../assets/img/nacos.svg'
import zookeeperIcon from '../assets/img/zookeeper.svg'
import redisIcon from '../assets/img/redis.svg'
import mysqlIcon from '../assets/img/mysql.svg'
import postgresIcon from '../assets/img/postgresql.svg'
import nginxIcon from '../assets/img/nginx.svg'
import kafkaIcon from '../assets/img/Kafka.svg'
import dockerIcon from '../assets/img/docker.svg'
import mongodbIcon from '../assets/img/mongoDB.svg'
import rocketmqIcon from '../assets/img/rocketMQ.svg'
import k8sIcon from '../assets/img/kubernetes.svg'
import fastdfsIcon from '../assets/img/fastdfs.svg'

const route = useRoute()
const BUILTIN_TABS = ['redis', 'mysql', 'postgres', 'nginx', 'kafka', 'docker', 'rocketmq', 'k8s', 'mongodb', 'fastdfs',
  'rabbitmq', 'elasticsearch', 'clickhouse', 'nacos', 'zookeeper']

// 轻采集类型的 Tab 元数据（label + logo + 组件）
const builtinTabList = [
  { type: 'rabbitmq', label: 'RabbitMQ', icon: rabbitmqIcon, comp: RabbitMQTab },
  { type: 'elasticsearch', label: 'Elasticsearch', icon: elasticsearchIcon, comp: ElasticsearchTab },
  { type: 'clickhouse', label: 'ClickHouse', icon: clickhouseIcon, comp: ClickHouseTab },
  { type: 'nacos', label: 'Nacos', icon: nacosIcon, comp: NacosTab },
  { type: 'zookeeper', label: 'ZooKeeper', icon: zookeeperIcon, comp: ZooKeeperTab },
]

// ---- 展示类型开关 ----
// enabled 为空 = 全部展示；非空 = 只展示清单内的类型
const viewDialog = ref(false)
const viewAvailable = ref([])
const enabledTypes = ref([])
const viewSelection = ref([])
const savingView = ref(false)

function viewLabel(key) {
  return builtinSpecs[key]?.label || key
}
const enabledList = computed(() => (enabledTypes.value.length ? enabledTypes.value : null))
function typeVisible(key) {
  return !enabledList.value || enabledList.value.includes(key)
}
const visibleBuiltinTabs = computed(() => BUILTIN_TABS.filter(typeVisible))
const visibleTemplateTypes = computed(() => templateTypes.value.filter((t) => typeVisible(t.type)))

async function loadViewConfig() {
  try {
    const data = await http.get('/api/v1/middleware/view-config')
    viewAvailable.value = data.available || []
    enabledTypes.value = data.enabled || []
    viewSelection.value = [...enabledTypes.value]
  } catch (e) {
    console.error('加载展示配置失败', e)
  }
}
async function saveViewConfig() {
  savingView.value = true
  try {
    const data = await http.put('/api/v1/middleware/view-config', { enabled: viewSelection.value })
    enabledTypes.value = data.enabled || []
    viewDialog.value = false
  } catch (e) {
    console.error('保存展示配置失败', e)
  } finally {
    savingView.value = false
  }
}

const validTabs = computed(() => visibleBuiltinTabs.value)
const activeTab = ref(BUILTIN_TABS.includes(route.query.tab) ? route.query.tab : 'redis')

// 支持从首页等外部链接通过 ?tab= 深链跳转到指定中间件
watch(
  () => route.query.tab,
  (t) => {
    if (validTabs.value.includes(t)) activeTab.value = t
  }
)

onMounted(async () => {
  await loadViewConfig()
  if (route.query.tab && validTabs.value.includes(route.query.tab)) activeTab.value = route.query.tab
})
</script>

<style scoped>
.middleware-view {
  padding: 4px 0 16px;
}
.page-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 16px;
}
.page-title {
  font-size: 22px;
  font-weight: 700;
  letter-spacing: -0.01em;
  background: linear-gradient(135deg, var(--text) 0%, var(--text-dim) 100%);
  -webkit-background-clip: text;
  -webkit-text-fill-color: transparent;
  background-clip: text;
}
.page-desc {
  font-size: 13px;
  color: var(--text-dim);
  margin-top: 4px;
}
.header-actions { display: flex; gap: 8px; flex-shrink: 0; }
.view-config-tip { font-size: 13px; color: var(--text-dim); margin-bottom: 12px; }
.view-cb { display: block; margin: 0 0 4px 0; }
.tab-emoji { font-size: 15px; }
.mw-tabs {
  border-radius: var(--radius);
  overflow: hidden;
}
.tab-label {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 14px;
}
.tab-label.disabled {
  color: var(--text-muted);
  cursor: not-allowed;
}
.tab-icon {
  width: 18px;
  height: 18px;
  object-fit: contain;
}
.tab-loading {
  padding: 40px 0;
  text-align: center;
  color: var(--text-dim);
  font-size: 14px;
}
.tab-error {
  padding: 40px 0;
  text-align: center;
  color: var(--danger);
  font-size: 14px;
}
.tab-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}
.tab-dot.redis { background: var(--danger); box-shadow: 0 0 8px var(--danger-dim); }
.tab-dot.mysql { background: #4479a1; }
.tab-dot.postgres { background: #336791; }
.tab-dot.nginx { background: #009639; }
.tab-dot.kafka { background: #231f20; border: 1px solid #666; }
.tab-dot.docker { background: #2496ed; }
.tab-dot.rocketmq { background: #d77429; }
.tab-dot.k8s { background: #326ce5; box-shadow: 0 0 8px rgba(50, 108, 229, 0.5); }
.tab-dot.mongo { background: #47a248; }
:deep(.el-tabs__content) {
  padding: 0 !important;
}
:deep(.el-tabs--border-card) {
  background: transparent;
  border: 1px solid var(--border);
  box-shadow: none;
}
:deep(.el-tabs__header) {
  background: rgba(255, 255, 255, 0.02);
  border-bottom: 1px solid var(--border);
}
:deep(.el-tabs__item) {
  border: none !important;
}
</style>
