<template>
  <div class="settings-page">
    <el-card class="box-card" shadow="never">
      <template #header>
        <div class="card-head">
          <span class="title">系统设置</span>
          <span class="sub">自定义系统名称与 Logo，保存后全局生效</span>
        </div>
      </template>

      <el-form label-width="96px" class="form">
        <el-form-item label="系统名称">
          <el-input
            v-model="form.name"
            maxlength="64"
            show-word-limit
            placeholder="如 星云监控"
            style="max-width: 360px"
          />
        </el-form-item>

        <el-form-item label="系统 Logo">
          <div class="logo-row">
            <div class="logo-preview">
              <img v-if="form.logo" :src="form.logo" alt="logo" />
              <div v-else class="logo-empty">未设置（使用默认徽标）</div>
            </div>
            <div class="logo-actions">
              <input ref="fileInput" type="file" accept="image/*" hidden @change="onFile" />
              <div class="btn-line">
                <el-button size="small" @click="pickFile">选择图片</el-button>
                <el-button size="small" text type="danger" :disabled="!form.logo" @click="clearLogo">清除 Logo</el-button>
              </div>
              <div class="hint">建议 PNG/SVG，≤ 3MB；以内嵌方式存储，升级不会丢失</div>
            </div>
          </div>
        </el-form-item>

        <el-form-item>
          <el-button type="primary" :loading="saving" @click="save">保存</el-button>
          <el-button :disabled="saving" @click="resetDefault">恢复默认</el-button>
        </el-form-item>
      </el-form>
    </el-card>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { useBrand } from '../composables/useBrand'

const { brand, saveBrand } = useBrand()
const form = ref({ name: 'NebulaEye', logo: '' })
const fileInput = ref(null)
const saving = ref(false)

onMounted(() => {
  form.value.name = brand.name || 'NebulaEye'
  form.value.logo = brand.logo || ''
})

function pickFile() {
  fileInput.value && fileInput.value.click()
}

function onFile(e) {
  const f = e.target.files && e.target.files[0]
  if (!f) return
  if (!f.type.startsWith('image/')) {
    ElMessage.error('请选择图片文件')
    return
  }
  if (f.size > 3 * 1024 * 1024) {
    ElMessage.error('Logo 过大，请控制在 3MB 以内')
    return
  }
  const reader = new FileReader()
  reader.onload = () => {
    form.value.logo = reader.result
  }
  reader.readAsDataURL(f)
  e.target.value = ''
}

function clearLogo() {
  form.value.logo = ''
}

function resetDefault() {
  form.value.name = 'NebulaEye'
  form.value.logo = ''
}

async function save() {
  if (!form.value.name || !form.value.name.trim()) {
    ElMessage.error('系统名称不能为空')
    return
  }
  saving.value = true
  try {
    await saveBrand(form.value.name.trim(), form.value.logo)
    ElMessage.success('已保存，全局即时生效')
  } catch (e) {
    ElMessage.error(e.message || '保存失败')
  } finally {
    saving.value = false
  }
}
</script>

<style scoped>
.settings-page {
  max-width: 760px;
  margin: 0 auto;
}
.card-head {
  display: flex;
  align-items: baseline;
  gap: 12px;
}
.card-head .title {
  font-size: 15px;
  font-weight: 600;
}
.card-head .sub {
  font-size: 12px;
  color: var(--text-muted);
}
.logo-row {
  display: flex;
  gap: 20px;
  align-items: flex-start;
}
.logo-preview {
  width: 96px;
  height: 96px;
  border-radius: 14px;
  border: 1px dashed var(--border);
  display: flex;
  align-items: center;
  justify-content: center;
  overflow: hidden;
  background: var(--bg-soft, rgba(255, 255, 255, 0.04));
  flex-shrink: 0;
}
.logo-preview img {
  max-width: 100%;
  max-height: 100%;
  object-fit: contain;
}
.logo-empty {
  font-size: 11px;
  color: var(--text-muted);
  padding: 8px;
  text-align: center;
}
.logo-actions {
  display: flex;
  flex-direction: column;
  gap: 8px;
  align-items: flex-start;
}
.btn-line {
  display: flex;
  gap: 8px;
}
.logo-actions .hint {
  font-size: 11px;
  color: var(--text-muted);
  max-width: 240px;
  line-height: 1.5;
}
</style>
