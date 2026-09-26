<template>
  <div class="middleware-view">
    <!-- 页面标题 -->
    <div class="page-header">
      <div class="header-left">
        <h2 class="page-title">中间件监控</h2>
        <p class="page-desc">
          Redis / MySQL / PostgreSQL / Nginx / Kafka / Docker / RocketMQ / Kubernetes / MongoDB / FastDFS
          实例监控与可视化，以及由「采集项模板」派生的自定义类型
        </p>
      </div>
      <el-button @click="$router.push('/templates')">采集项模板</el-button>
    </div>

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
      <!-- 采集项模板派生的类型：由 Server 侧类型注册表动态提供，无需为每类中间件写前端代码 -->
      <el-tab-pane v-for="t in templateTypes" :key="t.type" :label="t.label" :name="t.type">
        <template #label>
          <span class="tab-label">
            {{ t.label }}
            <span
              v-if="t.total === 0"
              class="tab-hint-dot"
              title="已配置该模板，但还没有采集到实例"
            ></span>
          </span>
        </template>
        <TemplateTab v-if="activeTab === t.type" :type="t.type" :label="t.label" />
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
// 模板派生类型共用一个通用 Tab（实例表 + 摘要指标），因此新增一类中间件不必再写前端组件
const TemplateTab = tabLoader(() => import('./mw/TemplateTab.vue'))
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
const BUILTIN_TABS = ['redis', 'mysql', 'postgres', 'nginx', 'kafka', 'docker', 'rocketmq', 'k8s', 'mongodb', 'fastdfs']

// 模板派生类型来自 Server 侧类型注册表（总览接口把 kind=template 的类型一并返回）
const templateTypes = ref([])
async function loadTemplateTypes() {
  try {
    const data = await http.get('/api/v1/middleware/overview')
    templateTypes.value = (data.types || []).filter((t) => t.kind === 'template')
  } catch (e) {
    console.error('加载模板派生类型失败', e)
  }
}

const validTabs = computed(() => BUILTIN_TABS.concat(templateTypes.value.map((t) => t.type)))
const activeTab = ref(BUILTIN_TABS.includes(route.query.tab) ? route.query.tab : 'redis')

// 支持从首页等外部链接通过 ?tab= 深链跳转到指定中间件
watch(
  () => route.query.tab,
  (t) => {
    if (validTabs.value.includes(t)) activeTab.value = t
  }
)

// 模板类型要等类型列表返回后才存在，因此加载完成后再解析一次深链
onMounted(async () => {
  await loadTemplateTypes()
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
/* 模板类型暂无数据时的提示点（与 Tab 内的空状态文案配合） */
.tab-hint-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--warning, #e6a23c);
  display: inline-block;
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
