<template>
  <div class="middleware-view">
    <!-- 页面标题 -->
    <div class="page-header">
      <div class="header-left">
        <h2 class="page-title">中间件监控</h2>
        <p class="page-desc">Redis / MySQL / PostgreSQL / Nginx / Kafka / Docker / RocketMQ / Kubernetes 实例监控与可视化</p>
      </div>
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
      <el-tab-pane label="MongoDB" name="mongo" disabled>
        <template #label>
          <el-tooltip content="即将支持" placement="top">
            <span class="tab-label disabled">
              <img class="tab-icon" :src="mongodbIcon" alt="MongoDB" />MongoDB
            </span>
          </el-tooltip>
        </template>
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<script setup>
import { ref } from 'vue'
import RedisTab from './redis/RedisTab.vue'
import MySQLTab from './mysql/MySQLTab.vue'
import PostgresTab from './postgres/PostgresTab.vue'
import NginxTab from './nginx/NginxTab.vue'
import KafkaTab from './kafka/KafkaTab.vue'
import DockerTab from './docker/DockerTab.vue'
import RocketMQTab from './rocketmq/RocketMQTab.vue'
import K8sTab from './k8s/K8sTab.vue'
import redisIcon from '../assets/img/redis.svg'
import mysqlIcon from '../assets/img/mysql.svg'
import postgresIcon from '../assets/img/postgresql.svg'
import nginxIcon from '../assets/img/nginx.svg'
import kafkaIcon from '../assets/img/Kafka.svg'
import dockerIcon from '../assets/img/docker.svg'
import mongodbIcon from '../assets/img/mongoDB.svg'
import rocketmqIcon from '../assets/img/rocketMQ.svg'
import k8sIcon from '../assets/img/kubernetes.svg'

const activeTab = ref('redis')
</script>

<style scoped>
.middleware-view {
  padding: 4px 0 16px;
}
.page-header {
  margin-bottom: 16px;
}
.page-title {
  font-size: 22px;
  font-weight: 700;
  letter-spacing: -0.01em;
  background: linear-gradient(135deg, #e5edf7 0%, #9fb3c8 100%);
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
.tab-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}
.tab-dot.redis { background: #dc382d; box-shadow: 0 0 8px rgba(220, 56, 45, 0.5); }
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
