<template>
  <el-card class="settings-card" shadow="never">
    <template #header>
      <div class="card-head">
        <div>
          <span class="title">站点与品牌</span>
          <span class="sub">自定义系统名称与 Logo，保存后全局生效</span>
        </div>
        <div class="head-actions">
          <el-button :disabled="saving" @click="resetDefault">恢复默认</el-button>
          <el-button type="primary" :loading="saving" @click="save">保存</el-button>
        </div>
      </div>
    </template>

    <el-form label-position="top" class="two-col-form">
      <el-row :gutter="28">
        <el-col :xs="24" :md="12">
          <el-form-item label="系统名称">
            <el-input
              v-model="form.name"
              maxlength="64"
              show-word-limit
              placeholder="如 星云监控"
            />
            <div class="field-hint">在整个应用程序中显示的名称</div>
          </el-form-item>
        </el-col>

        <el-col :xs="24" :md="12">
          <el-form-item label="系统 Logo">
            <div class="logo-row">
              <div class="logo-preview">
                <img v-if="form.logo" :src="form.logo" alt="logo" />
                <div v-else class="logo-empty">未设置</div>
              </div>
              <div class="logo-actions">
                <input ref="fileInput" type="file" accept="image/*" hidden @change="onFile" />
                <div class="btn-line">
                  <el-button size="small" @click="pickFile">选择图片</el-button>
                  <el-button size="small" text type="danger" :disabled="!form.logo" @click="clearLogo">清除</el-button>
                </div>
                <div class="field-hint">建议 PNG/SVG，≤ 3MB；以内嵌方式存储</div>
              </div>
            </div>
          </el-form-item>
        </el-col>
      </el-row>
    </el-form>
  </el-card>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { useBrand } from '../../composables/useBrand'

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
.head-actions {
  display: flex;
  gap: 10px;
}
.two-col-form :deep(.el-form-item__label) {
  font-weight: 500;
  padding-bottom: 6px;
}
.field-hint {
  font-size: 12px;
  color: var(--text-muted);
  line-height: 1.5;
  margin-top: 6px;
}
.logo-row {
  display: flex;
  gap: 16px;
  align-items: flex-start;
}
.logo-preview {
  width: 80px;
  height: 80px;
  border-radius: 12px;
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
</style>
