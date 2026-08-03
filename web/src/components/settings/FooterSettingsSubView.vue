<template>
  <el-card class="settings-card" shadow="never">
    <template #header>
      <div class="card-head">
        <div>
          <span class="title">页脚</span>
          <span class="sub">配置显示在页面底部的页脚文本</span>
        </div>
        <div class="head-actions">
          <el-button :disabled="saving" @click="resetDefault">恢复默认</el-button>
          <el-button type="primary" :loading="saving" @click="save">保存</el-button>
        </div>
      </div>
    </template>

    <el-form label-position="top" class="footer-form">
      <el-form-item label="页脚文本">
        <el-input
          v-model="form.footer"
          type="textarea"
          :rows="4"
          maxlength="512"
          show-word-limit
          placeholder="如 © 2025 您的公司。保留所有权利。"
        />
        <div class="field-hint">留空则不在页面底部显示页脚；支持纯文本，保存后全局即时生效</div>
      </el-form-item>

      <el-form-item label="预览">
        <div class="footer-preview">
          <span v-if="form.footer" class="preview-text">{{ form.footer }}</span>
          <span v-else class="preview-empty">当前未设置页脚文本</span>
        </div>
      </el-form-item>
    </el-form>
  </el-card>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { useBrand } from '../../composables/useBrand'

const { brand, saveBrand } = useBrand()
const form = ref({ footer: '' })
const saving = ref(false)

onMounted(() => {
  form.value.footer = brand.footer || ''
})

function resetDefault() {
  form.value.footer = ''
}

async function save() {
  saving.value = true
  try {
    await saveBrand(brand.name, brand.logo, form.value.footer)
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
.footer-form :deep(.el-form-item__label) {
  font-weight: 500;
  padding-bottom: 6px;
}
.field-hint {
  font-size: 12px;
  color: var(--text-muted);
  line-height: 1.5;
  margin-top: 6px;
}
.footer-preview {
  width: 100%;
  padding: 16px 20px;
  border-radius: 8px;
  border: 1px dashed var(--border);
  background: var(--bg-soft, rgba(255, 255, 255, 0.04));
  text-align: center;
  color: var(--text-dim);
  font-size: 13px;
}
.preview-text {
  color: var(--text);
  white-space: pre-wrap;
}
.preview-empty {
  color: var(--text-muted);
}
</style>
