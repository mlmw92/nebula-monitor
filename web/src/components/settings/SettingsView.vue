<template>
  <div class="settings-view">
    <PageHeader title="系统设置" desc="站点品牌、告警管道、自监控与数据保留策略" />

    <!-- 二级导航改为纵向：
         原来用横向 el-tabs，而子页内部还会横向分块，两层都是横的，层级分不清；
         纵向之后左列是「分类」、右列是「内容」，一眼可分。 -->
    <div class="settings-layout">
      <aside class="settings-nav">
        <div class="sn-title">设置分类</div>
        <div
          v-for="p in panes"
          :key="p.name"
          class="sn-item"
          :class="{ on: active === p.name }"
          @click="active = p.name"
        >
          {{ p.label }}
        </div>
        <div class="sn-tip">
          图标、名称与主题色的改动会同步到登录页、浏览器标签与对外状态页
        </div>
      </aside>

      <div class="settings-body">
        <keep-alive>
          <component :is="currentComp" />
        </keep-alive>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed } from 'vue'
import { useRoute } from 'vue-router'
import PageHeader from '../common/PageHeader.vue'
import BrandSettingsSubView from './BrandSettingsSubView.vue'
import AlertPipelineSubView from './AlertPipelineSubView.vue'
import SelfMonitorSubView from './SelfMonitorSubView.vue'
import RetentionSubView from './RetentionSubView.vue'

const route = useRoute()

const panes = [
  { name: 'brand', label: '站点与品牌', comp: BrandSettingsSubView },
  { name: 'pipeline', label: '告警管道', comp: AlertPipelineSubView },
  { name: 'selfmon', label: '系统自监控', comp: SelfMonitorSubView },
  { name: 'retention', label: '数据保留', comp: RetentionSubView },
]

// 支持 /system/settings?tab=pipeline 直达某个分类
const active = ref(panes.some((p) => p.name === route.query.tab) ? route.query.tab : 'brand')

// keep-alive 保留子页实例，切回来时未保存的表单状态不丢
const currentComp = computed(() => (panes.find((p) => p.name === active.value) || panes[0]).comp)
</script>

<style scoped>
.settings-layout {
  display: grid;
  grid-template-columns: 200px minmax(0, 1fr);
  gap: var(--sp-5);
  align-items: start;
}
.settings-nav {
  display: flex;
  flex-direction: column;
  gap: 2px;
  position: sticky;
  top: 0;
}
.sn-title {
  font-size: var(--fs-xs);
  color: var(--t3);
  padding: 0 10px;
  margin-bottom: 6px;
}
.sn-item {
  font-size: var(--fs-sm);
  color: var(--t3);
  padding: 7px 10px;
  border-radius: var(--r-sm);
  border-left: 2px solid transparent;
  cursor: pointer;
  transition: color var(--dur-1) var(--ease), background var(--dur-1) var(--ease);
  user-select: none;
}
.sn-item:hover {
  color: var(--t2);
  background: var(--fill-2);
}
.sn-item.on {
  color: var(--accent);
  background: var(--accent-dim);
  border-left-color: var(--accent);
  font-weight: 600;
}
.sn-tip {
  margin-top: var(--sp-5);
  padding: 10px;
  border: 1px dashed var(--bd-strong);
  border-radius: var(--r-md);
  font-size: var(--fs-xs);
  color: var(--t3);
  line-height: 1.6;
}
.settings-body {
  min-width: 0;
}
@media (max-width: 1100px) {
  .settings-layout {
    grid-template-columns: 1fr;
  }
  .settings-nav {
    position: static;
    flex-direction: row;
    flex-wrap: wrap;
  }
  .sn-tip {
    display: none;
  }
}
</style>
