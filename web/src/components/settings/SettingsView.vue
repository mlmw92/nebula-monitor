<template>
  <div class="settings-view">
    <el-tabs v-model="active" class="settings-tabs">
      <el-tab-pane label="站点与品牌" name="brand">
        <BrandSettingsSubView />
      </el-tab-pane>
      <el-tab-pane label="告警管道" name="pipeline">
        <AlertPipelineSubView />
      </el-tab-pane>
      <el-tab-pane label="系统自监控" name="selfmon">
        <SelfMonitorSubView />
      </el-tab-pane>
      <el-tab-pane label="数据保留" name="retention">
        <RetentionSubView />
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<script setup>
import { ref } from 'vue'
import { useRoute } from 'vue-router'
import BrandSettingsSubView from './BrandSettingsSubView.vue'
import AlertPipelineSubView from './AlertPipelineSubView.vue'
import SelfMonitorSubView from './SelfMonitorSubView.vue'
import RetentionSubView from './RetentionSubView.vue'

const route = useRoute()
const active = ref('brand')
</script>

<style scoped>
.settings-view {
  width: 100%;
}
.settings-tabs :deep(.el-tabs__header) {
  margin-bottom: 18px;
}
.settings-tabs :deep(.el-tabs__item) {
  font-size: 14px;
}
</style>
