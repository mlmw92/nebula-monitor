<template>
  <div class="settings-view">
    <el-tabs v-model="active" class="settings-tabs">
      <el-tab-pane label="站点与品牌" name="brand">
        <BrandSettingsSubView />
      </el-tab-pane>
      <el-tab-pane label="修改密码" name="password">
        <ChangePasswordSubView />
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<script setup>
import { ref } from 'vue'
import { useRoute } from 'vue-router'
import BrandSettingsSubView from './BrandSettingsSubView.vue'
import ChangePasswordSubView from './ChangePasswordSubView.vue'

const route = useRoute()
const active = ref(route.query.tab === 'password' ? 'password' : 'brand')
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
