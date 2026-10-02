<template>
  <div class="profile-view">
    <PageHeader
      title="个人中心"
      desc="查看当前账号的身份信息，修改昵称与登录密码；角色与数据范围由管理员分配，个人不可更改"
    />

    <el-tabs v-model="activeTab" class="profile-tabs">
      <el-tab-pane label="基本信息" name="basic">
        <el-card class="glass info-card" shadow="never">
          <el-form label-position="top" class="profile-form">
            <el-form-item label="用户名">
              <el-input :model-value="principal.username" disabled />
            </el-form-item>
            <el-form-item label="昵称">
              <el-input v-model="nickname" maxlength="64" show-word-limit placeholder="展示用的昵称" />
            </el-form-item>
            <el-form-item label="我的角色">
              <div class="role-tags">
                <el-tag v-for="r in roleLabels" :key="r" size="small" type="info" effect="dark">{{ r }}</el-tag>
                <span v-if="!roleLabels.length" class="muted">—</span>
              </div>
            </el-form-item>
            <el-form-item label="数据范围">
              <el-tag v-if="principal.scope.mode === 'restricted'" size="small" type="warning" effect="plain">限定分组</el-tag>
              <el-tag v-else size="small" type="success" effect="plain">全部节点</el-tag>
              <span v-if="principal.scope.mode === 'restricted'" class="scope-groups">
                <el-tag v-for="g in principal.scope.groups" :key="g" size="small" class="grp-tag">{{ g }}</el-tag>
                <span v-if="!principal.scope.groups.length" class="muted">（无分组，无可见资源）</span>
              </span>
            </el-form-item>
            <div class="actions">
              <el-button type="primary" :loading="savingNick" @click="saveNickname">保存昵称</el-button>
            </div>
          </el-form>
        </el-card>
      </el-tab-pane>

      <el-tab-pane label="修改密码" name="password">
        <ChangePasswordSubView />
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<script setup>
import { ref, computed } from 'vue'
import { ElMessage } from 'element-plus'
import http from '../../api/http'
import { useAuth } from '../../composables/useAuth'
import ChangePasswordSubView from '../settings/ChangePasswordSubView.vue'
import PageHeader from '../common/PageHeader.vue'

const auth = useAuth()
const principal = auth.principal
const activeTab = ref('basic')

const ROLE_LABELS = {
  super_admin: '超级管理员',
  ops_admin: '运维管理员',
  alert_admin: '告警管理员',
  security_admin: '安全管理员',
  read_only: '只读用户',
  audit: '审计用户',
}
const roleLabels = computed(() => principal.roles.map((r) => ROLE_LABELS[r] || r))

const nickname = ref(principal.displayName || '')
const savingNick = ref(false)

async function saveNickname() {
  savingNick.value = true
  try {
    await http.updateMe({ displayName: nickname.value })
    await auth.loadMe()
    ElMessage.success('昵称已保存')
  } catch (e) {
    ElMessage.error(e.message || '保存失败')
  } finally {
    savingNick.value = false
  }
}
</script>

<style scoped>
.profile-view {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.page-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}
.page-title {
  font-size: 20px;
  font-weight: 700;
  margin: 0;
}
.page-sub {
  margin: 6px 0 0;
  font-size: 13px;
  color: var(--text-dim);
}
.card-title {
  font-size: 15px;
  font-weight: 600;
}
.info-card {
  width: 100%;
  margin-top: 14px;
}
.profile-tabs {
  margin-top: 4px;
}
.profile-form {
  max-width: 480px;
}
.role-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}
.grp-tag {
  margin: 0 4px 2px 0;
}
.scope-groups {
  margin-left: 6px;
}
.muted {
  color: var(--text-muted);
}
.actions {
  margin-top: 4px;
}
</style>
