<template>
  <div class="middleware-view">
    <!-- 页面标题 -->
    <div class="page-header">
      <div class="header-left">
        <h2 class="page-title">中间件监控</h2>
        <p class="page-desc">Redis / MySQL / PostgreSQL / MongoDB / Kafka 实例监控与可视化</p>
      </div>
    </div>

    <!-- 中间件类型 Tab -->
    <el-tabs v-model="activeTab" class="mw-tabs" type="border-card">
      <el-tab-pane label="Redis" name="redis">
        <template #label>
          <span class="tab-label">
            <span class="tab-dot redis"></span>Redis
          </span>
        </template>
        <RedisTab v-if="activeTab === 'redis'" />
      </el-tab-pane>
      <el-tab-pane label="MySQL" name="mysql" disabled>
        <template #label>
          <el-tooltip content="即将支持" placement="top">
            <span class="tab-label disabled">
              <span class="tab-dot mysql"></span>MySQL
            </span>
          </el-tooltip>
        </template>
      </el-tab-pane>
      <el-tab-pane label="PostgreSQL" name="postgres" disabled>
        <template #label>
          <el-tooltip content="即将支持" placement="top">
            <span class="tab-label disabled">
              <span class="tab-dot postgres"></span>PostgreSQL
            </span>
          </el-tooltip>
        </template>
      </el-tab-pane>
      <el-tab-pane label="MongoDB" name="mongo" disabled>
        <template #label>
          <el-tooltip content="即将支持" placement="top">
            <span class="tab-label disabled">
              <span class="tab-dot mongo"></span>MongoDB
            </span>
          </el-tooltip>
        </template>
      </el-tab-pane>
      <el-tab-pane label="Kafka" name="kafka" disabled>
        <template #label>
          <el-tooltip content="即将支持" placement="top">
            <span class="tab-label disabled">
              <span class="tab-dot kafka"></span>Kafka
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
.tab-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}
.tab-dot.redis { background: #dc382d; box-shadow: 0 0 8px rgba(220, 56, 45, 0.5); }
.tab-dot.mysql { background: #4479a1; }
.tab-dot.postgres { background: #336791; }
.tab-dot.mongo { background: #47a248; }
.tab-dot.kafka { background: #231f20; border: 1px solid #666; }
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
