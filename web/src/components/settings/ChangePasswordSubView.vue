<template>
  <el-card class="settings-card" shadow="never">
    <template #header>
      <div class="card-head">
        <div>
          <span class="title">修改登录密码</span>
          <span class="sub">密码以国密 SM3 加盐哈希存储，服务端不会保存明文</span>
        </div>
      </div>
    </template>

    <el-form :model="form" label-position="top" class="pwd-form" @submit.prevent>
      <el-form-item label="原密码" :error="errors.oldPassword">
        <el-input
          v-model="form.oldPassword"
          type="password"
          show-password
          placeholder="请输入当前登录密码"
          autocomplete="current-password"
        />
      </el-form-item>

      <el-form-item label="新密码" :error="errors.newPassword">
        <el-input
          v-model="form.newPassword"
          type="password"
          show-password
          placeholder="请输入新密码（建议 8 位以上，含字母与数字）"
          autocomplete="new-password"
        />
      </el-form-item>

      <el-form-item label="确认新密码" :error="errors.confirm">
        <el-input
          v-model="form.confirm"
          type="password"
          show-password
          placeholder="请再次输入新密码"
          autocomplete="new-password"
        />
      </el-form-item>

      <div class="actions">
        <el-button :loading="saving" type="primary" @click="submit">保存修改</el-button>
        <el-button :disabled="saving" @click="reset">重置</el-button>
      </div>
    </el-form>
  </el-card>
</template>

<script setup>
import { reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import http from '../../api/http'

const form = reactive({ oldPassword: '', newPassword: '', confirm: '' })
const errors = reactive({ oldPassword: '', newPassword: '', confirm: '' })
const saving = ref(false)

function reset() {
  form.oldPassword = ''
  form.newPassword = ''
  form.confirm = ''
  errors.oldPassword = ''
  errors.newPassword = ''
  errors.confirm = ''
}

function validate() {
  errors.oldPassword = ''
  errors.newPassword = ''
  errors.confirm = ''
  let ok = true
  if (!form.oldPassword) {
    errors.oldPassword = '请输入原密码'
    ok = false
  }
  if (!form.newPassword) {
    errors.newPassword = '请输入新密码'
    ok = false
  } else if (form.newPassword.length < 6) {
    errors.newPassword = '新密码至少 6 位'
    ok = false
  }
  if (form.newPassword !== form.confirm) {
    errors.confirm = '两次输入的新密码不一致'
    ok = false
  }
  return ok
}

async function submit() {
  if (!validate()) return
  saving.value = true
  try {
    await http.changePassword(form.oldPassword, form.newPassword)
    ElMessage.success('密码已修改，下次登录请使用新密码')
    reset()
  } catch (e) {
    // 原密码错误等后端校验信息已由 http 封装抛出
    ElMessage.error(e.message || '修改失败')
  } finally {
    saving.value = false
  }
}
</script>

<style scoped>
.settings-card {
  border-radius: 12px;
}
.card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.card-head .title {
  font-size: 15px;
  font-weight: 600;
  margin-right: 10px;
}
.card-head .sub {
  font-size: 12px;
  color: var(--text-muted);
}
.pwd-form {
  max-width: 460px;
}
.pwd-form :deep(.el-form-item__label) {
  font-weight: 500;
  padding-bottom: 6px;
}
.actions {
  display: flex;
  gap: 12px;
  margin-top: 8px;
}
</style>
