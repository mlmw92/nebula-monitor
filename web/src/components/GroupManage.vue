<template>
  <el-dialog v-model="visible" title="分组管理" width="560px" @close="$emit('close')">
    <!-- 新建分组 -->
    <div class="add-row">
      <el-input
        v-model="newName"
        placeholder="分组名称"
        size="small"
        style="flex: 1"
        @keyup.enter="createGroup"
      />
      <el-input
        v-model="newDesc"
        placeholder="描述（可选）"
        size="small"
        style="flex: 1"
      />
      <el-button type="primary" size="small" :icon="Plus" @click="createGroup">添加</el-button>
    </div>

    <!-- 分组列表 -->
    <el-table :data="groups" stripe size="small" style="margin-top: 14px" empty-text="暂无分组">
      <el-table-column prop="name" label="分组名称" min-width="120" />
      <el-table-column prop="description" label="描述" min-width="140" show-overflow-tooltip>
        <template #default="{ row }">{{ row.description || '-' }}</template>
      </el-table-column>
      <el-table-column label="节点数" width="80">
        <template #default="{ row }">{{ nodeCount(row.name) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="80">
        <template #default="{ row }">
          <el-button link type="danger" size="small" @click="del(row)" :disabled="row.name === 'default'">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <template #footer>
      <el-button @click="$emit('close')">关闭</el-button>
    </template>
  </el-dialog>
</template>

<script setup>
import { ref, computed } from 'vue'
import { Plus } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import http from '../api/http'

const props = defineProps({
  groups: { type: Array, default: () => [] },
  nodes: { type: Array, default: () => [] },
})
const emit = defineEmits(['close', 'changed'])

const visible = ref(true)
const newName = ref('')
const newDesc = ref('')

function nodeCount(name) {
  return props.nodes.filter((n) => (n.group || 'default') === name).length
}

async function createGroup() {
  if (!newName.value.trim()) {
    ElMessage.warning('请输入分组名称')
    return
  }
  try {
    await http.post('/api/v1/groups', { name: newName.value.trim(), description: newDesc.value.trim() })
    ElMessage.success('分组已创建')
    newName.value = ''
    newDesc.value = ''
    emit('changed')
  } catch (e) {
    ElMessage.error('创建失败：' + (e.message || '未知错误'))
  }
}

async function del(row) {
  try {
    await ElMessageBox.confirm(
      `确认删除分组「${row.name}」？该分组下的节点会归入 default。`,
      '删除分组',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' }
    )
    await http.del('/api/v1/groups/' + encodeURIComponent(row.name))
    ElMessage.success('已删除')
    emit('changed')
  } catch (e) {
    /* 取消 */
  }
}
</script>

<style scoped>
.add-row {
  display: flex;
  gap: 8px;
}
</style>
