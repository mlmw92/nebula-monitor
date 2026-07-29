<template>
  <div class="notify-view">
    <div class="page-head">
      <div>
        <h2>通知配置</h2>
        <p class="sub">配置告警通知渠道，保存后立即生效（热加载，无需重启）。配置独立存储，不修改 server.yaml。</p>
      </div>
      <el-button type="primary" :loading="saving" :disabled="loading" @click="save">保存并生效</el-button>
    </div>

    <el-form v-loading="loading" :model="notify" label-position="top" class="cards">
      <!-- 邮件 -->
      <el-card shadow="never" class="ch">
        <template #header>
          <div class="ch-head">
            <span class="ch-title">邮件 (SMTP)</span>
            <el-switch v-model="notify.email.enabled" />
          </div>
        </template>
        <el-row :gutter="16">
          <el-col :span="12"><el-form-item label="SMTP 主机"><el-input v-model="notify.email.smtpHost" placeholder="smtp.example.com" /></el-form-item></el-col>
          <el-col :span="12"><el-form-item label="端口"><el-input-number v-model="notify.email.smtpPort" :min="1" :max="65535" controls-position="right" /></el-form-item></el-col>
          <el-col :span="12"><el-form-item label="用户名"><el-input v-model="notify.email.username" /></el-form-item></el-col>
          <el-col :span="12"><el-form-item label="发件人"><el-input v-model="notify.email.from" placeholder="monitor@example.com" /></el-form-item></el-col>
          <el-col :span="12"><el-form-item label="密码"><el-input v-model="notify.email.password" type="password" show-password placeholder="不修改请留空" /></el-form-item></el-col>
          <el-col :span="12">
            <el-form-item label="加密方式">
              <el-radio-group v-model="emailEncryption">
                <el-radio value="none">无</el-radio>
                <el-radio value="starttls">STARTTLS</el-radio>
                <el-radio value="tls">SSL/TLS</el-radio>
              </el-radio-group>
            </el-form-item>
          </el-col>
        </el-row>
        <el-form-item label="收件人（多人每行一个）">
          <StringListInput v-model="notify.email.to" placeholder="ops@example.com" />
        </el-form-item>
      </el-card>

      <!-- Webhook -->
      <el-card shadow="never" class="ch">
        <template #header>
          <div class="ch-head">
            <span class="ch-title">Webhook</span>
            <el-switch v-model="notify.webhook.enabled" />
          </div>
        </template>
        <el-form-item label="Webhook 地址（多个地址每行一个）">
          <StringListInput v-model="notify.webhook.urls" placeholder="https://example.com/webhook" />
        </el-form-item>
      </el-card>

      <!-- 钉钉 -->
      <el-card shadow="never" class="ch">
        <template #header>
          <div class="ch-head">
            <span class="ch-title">钉钉机器人</span>
            <el-switch v-model="notify.dingtalk.enabled" />
          </div>
        </template>
        <el-form-item label="机器人 Webhook（多个群每行一个）">
          <StringListInput v-model="notify.dingtalk.urls" placeholder="https://oapi.dingtalk.com/robot/send?access_token=xxx" />
        </el-form-item>
        <el-form-item label="加签密钥（可选）"><el-input v-model="notify.dingtalk.secret" type="password" show-password placeholder="不修改请留空" /></el-form-item>
        <el-form-item label="@ 手机号（可选，多人每行一个）">
          <StringListInput v-model="notify.dingtalk.atMobiles" placeholder="13800000000" />
        </el-form-item>
      </el-card>

      <!-- 飞书 -->
      <el-card shadow="never" class="ch">
        <template #header>
          <div class="ch-head">
            <span class="ch-title">飞书机器人</span>
            <el-switch v-model="notify.feishu.enabled" />
          </div>
        </template>
        <el-form-item label="机器人 Webhook（多个群每行一个）">
          <StringListInput v-model="notify.feishu.urls" placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/xxx" />
        </el-form-item>
        <el-form-item label="签名密钥（可选）"><el-input v-model="notify.feishu.secret" type="password" show-password placeholder="不修改请留空" /></el-form-item>
      </el-card>

      <!-- 企业微信 -->
      <el-card shadow="never" class="ch">
        <template #header>
          <div class="ch-head">
            <span class="ch-title">企业微信机器人</span>
            <el-switch v-model="notify.wecom.enabled" />
          </div>
        </template>
        <el-form-item label="机器人 Webhook（多个群每行一个）">
          <StringListInput v-model="notify.wecom.urls" placeholder="https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxx" />
        </el-form-item>
        <el-form-item label="@ 成员（可选，userid 或 @all，多人每行一个）">
          <StringListInput v-model="notify.wecom.mentionedList" placeholder="@all" />
        </el-form-item>
      </el-card>
    </el-form>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import http from '../api/http'
import StringListInput from './StringListInput.vue'

const loading = ref(false)
const saving = ref(false)
const notify = ref(defaultConfig())

// emailEncryption 将加密方式映射到 useTLS / useStartTLS 两个布尔字段。
const emailEncryption = computed({
  get() {
    if (notify.value.email.useStartTLS) return 'starttls'
    if (notify.value.email.useTLS) return 'tls'
    return 'none'
  },
  set(v) {
    notify.value.email.useTLS = v === 'tls'
    notify.value.email.useStartTLS = v === 'starttls'
  },
})

function defaultConfig() {
  return {
    email: { enabled: false, smtpHost: '', smtpPort: 587, username: '', password: '', from: '', to: [], useTLS: true, useStartTLS: false },
    webhook: { enabled: false, urls: [] },
    dingtalk: { enabled: false, urls: [], secret: '', atMobiles: [] },
    feishu: { enabled: false, urls: [], secret: '' },
    wecom: { enabled: false, urls: [], mentionedList: [] },
  }
}

function merge(dst, src) {
  if (!src) return dst
  for (const k of Object.keys(dst)) {
    if (src[k] && typeof src[k] === 'object' && !Array.isArray(src[k])) {
      merge(dst[k], src[k])
    } else if (src[k] !== undefined) {
      dst[k] = src[k]
    }
  }
  return dst
}

async function load() {
  loading.value = true
  try {
    const c = await http.get('/api/v1/notify')
    notify.value = merge(defaultConfig(), c)
  } catch (e) {
    ElMessage.error('加载通知配置失败：' + e.message)
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  try {
    await http.put('/api/v1/notify', notify.value)
    ElMessage.success('已保存并生效')
  } catch (e) {
    ElMessage.error('保存失败：' + e.message)
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.notify-view {
  max-width: 920px;
}
.page-head {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 16px;
  margin-bottom: 18px;
}
.page-head h2 {
  margin: 0 0 4px;
  font-size: 22px;
  font-weight: 600;
}
.sub {
  margin: 0;
  color: var(--text-dim);
  font-size: 13px;
  max-width: 640px;
}
.cards {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.ch {
  border: 1px solid var(--border);
  background: var(--card);
}
.ch-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.ch-title {
  font-weight: 600;
  font-size: 15px;
}
</style>
