<template>
  <div class="rbac-view">
    <PageHeader
      title="角色与权限"
      desc="角色是一组权限点与默认数据范围的集合；内置角色不可删除，自定义角色的数据范围不得超过创建者自身范围"
    >
      <template #actions>
        <el-button type="primary" :icon="Plus" @click="openCreate" v-if="auth.can('roles:manage')">新建角色</el-button>
      </template>
    </PageHeader>

    <el-alert
      v-if="!auth.can('roles:manage')"
      type="info"
      :closable="false"
      title="当前账号仅有角色查看权限，编辑 / 删除操作需具备 roles:manage 权限。"
      style="margin-bottom: 12px"
    />

    <el-card class="glass table-card" shadow="never">
      <el-table :data="roles" v-loading="loading" stripe style="width: 100%">
        <el-table-column prop="name" label="角色名" min-width="130">
          <template #default="{ row }">
            <el-tag size="small" effect="dark" type="info">{{ roleLabel(row.name) }}</el-tag>
            <span v-if="row.builtin" class="builtin-tag">内置</span>
          </template>
        </el-table-column>
        <el-table-column prop="description" label="说明" min-width="220" show-overflow-tooltip />
        <el-table-column label="权限数" width="90">
          <template #default="{ row }">{{ Array.isArray(row.permissions) ? row.permissions.length : 0 }}</template>
        </el-table-column>
        <el-table-column label="默认范围" min-width="140">
          <template #default="{ row }">
            <el-tag v-if="row.scope_mode === 'restricted'" size="small" type="warning" effect="plain">受限</el-tag>
            <el-tag v-else size="small" type="success" effect="plain">全部</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" min-width="140" fixed="right" v-if="auth.canAny(['roles:manage', 'roles:read'])">
          <template #default="{ row }">
            <el-button link type="primary" size="small" @click="openEdit(row)">编辑</el-button>
            <el-popconfirm v-if="!row.builtin && auth.can('roles:manage')" title="确认删除该角色？" @confirm="removeRole(row)">
              <template #reference>
                <el-button link type="danger" size="small">删除</el-button>
              </template>
            </el-popconfirm>
            <span v-else-if="row.builtin" class="muted">内置不可删</span>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- 新建 / 编辑角色 -->
    <el-dialog v-model="formVisible" :title="editing ? '编辑角色' : '新建角色'" width="720px" top="5vh">
      <el-form :model="form" label-width="84px" @submit.prevent>
        <el-form-item label="角色名" required>
          <el-input v-model="form.name" :disabled="editing" placeholder="英文标识，如 custom_ops" />
        </el-form-item>
        <el-form-item label="说明">
          <el-input v-model="form.description" placeholder="角色用途说明" />
        </el-form-item>
        <el-form-item label="默认范围">
          <div class="scope-box">
            <el-radio-group v-model="form.scopeMode">
              <el-radio value="global">全部节点</el-radio>
              <el-radio value="restricted">限定分组</el-radio>
            </el-radio-group>
            <el-select
              v-if="form.scopeMode === 'restricted'"
              v-model="form.scopeGroups"
              multiple
              filterable
              placeholder="选择默认可见的节点分组"
              style="width: 100%; margin-top: 10px"
            >
              <el-option v-for="g in groupNames" :key="g" :label="g" :value="g" />
            </el-select>
            <p v-if="form.scopeMode === 'restricted' && !form.scopeGroups.length" class="scope-warn">
              受限模式未选任何分组时，归属此角色的用户将看不到任何节点资源。
            </p>
          </div>
        </el-form-item>
        <el-form-item label="权限点">
          <div class="perm-matrix">
            <el-alert type="warning" :closable="false" class="perm-hint">
              <template #title>
                高风险权限：system:upgrade、agent:secret:read、agent:upgrade、security:write、notify:write、users:manage、roles:manage、audit:export、assets:export
                等涉及系统变更或敏感信息，请谨慎授予。
              </template>
            </el-alert>
            <el-collapse v-model="activeDomains" class="perm-collapse">
              <el-collapse-item v-for="d in catalog.domains" :key="d.domain" :name="d.domain">
                <template #title>
                  <span class="domain-title">{{ domainLabel(d.domain) }}</span>
                  <span class="domain-count">（{{ d.items.length }} 项）</span>
                </template>
                <div class="perm-grid" v-if="d.items && d.items.length">
                  <label
                    v-for="p in d.items"
                    :key="p.key"
                    class="perm-check"
                    :class="{ checked: form.permissions.includes(p.key) }"
                    @click.prevent="togglePerm(p.key, !form.permissions.includes(p.key))"
                  >
                    <span class="check-box">
                      <span v-if="form.permissions.includes(p.key)" class="check-icon">&#10003;</span>
                    </span>
                    <span class="perm-body">
                      <span class="perm-key">{{ p.key }}</span>
                      <span class="perm-desc">{{ p.description }}</span>
                    </span>
                  </label>
                </div>
                <div v-else class="perm-empty">该域暂无权限点</div>
              </el-collapse-item>
            </el-collapse>
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="formVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="saveRole">{{ editing ? '保存' : '创建' }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, reactive, onMounted } from 'vue'
import { Plus } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import http from '../../api/http'
import PageHeader from '../common/PageHeader.vue'
import { useAuth } from '../../composables/useAuth'

const auth = useAuth()
const ROLE_LABELS = {
  super_admin: '超级管理员',
  ops_admin: '运维管理员',
  alert_admin: '告警管理员',
  security_admin: '安全管理员',
  read_only: '只读用户',
  audit: '审计用户',
}
function roleLabel(r) {
  return ROLE_LABELS[r] || r
}
const DOMAIN_LABELS = {
  dashboard: '仪表盘',
  nodes: '主机/节点',
  groups: '分组',
  middleware: '中间件',
  metrics: '指标',
  alerts: '告警',
  notify: '通知',
  report: '报告/巡检',
  security: '安全中心',
  audit: '审计',
  agent: 'Agent',
  system: '系统',
  users: '用户管理',
  roles: '角色管理',
}
function domainLabel(d) {
  return (DOMAIN_LABELS[d] ? DOMAIN_LABELS[d] + '（' + d + '）' : d)
}

const roles = ref([])
const catalog = reactive({ domains: [] })
const groupNames = ref([])
const loading = ref(false)
const saving = ref(false)
const activeDomains = ref([])

const formVisible = ref(false)
const editing = ref(false)
const form = reactive({
  name: '',
  description: '',
  permissions: [],
  scopeMode: 'global',
  scopeGroups: [],
})

async function loadAll() {
  loading.value = true
  try {
    const [r, c, g] = await Promise.all([
      http.listRoles().catch(() => ({ roles: [] })),
      http.permissionCatalog().catch(() => ({ domains: [] })),
      http.listGroups().catch(() => ({ groups: [] })),
    ])
    roles.value = (Array.isArray(r) ? r : r.roles || []).map((x) =>
      typeof x === 'string' ? { name: x, builtin: false, permissions: [] } : x
    )
    catalog.domains = (c && c.domains) || []
    const gs = Array.isArray(g) ? g : g.groups || []
    groupNames.value = gs.map((x) => (typeof x === 'string' ? x : x.name)).filter(Boolean)
    activeDomains.value = catalog.domains.map((d) => d.domain)
  } catch (e) {
    ElMessage.error(e.message || '加载角色数据失败')
  } finally {
    loading.value = false
  }
}

function resetForm() {
  form.name = ''
  form.description = ''
  form.permissions = []
  form.scopeMode = 'global'
  form.scopeGroups = []
}

function openCreate() {
  editing.value = false
  resetForm()
  formVisible.value = true
}

function openEdit(row) {
  editing.value = true
  form.name = row.name
  form.description = row.description || ''
  form.permissions = Array.isArray(row.permissions) ? [...row.permissions] : []
  form.scopeMode = row.scope_mode === 'restricted' ? 'restricted' : 'global'
  form.scopeGroups = Array.isArray(row.scope_groups) ? [...row.scope_groups] : []
  formVisible.value = true
}

function togglePerm(key, val) {
  const i = form.permissions.indexOf(key)
  if (val && i < 0) form.permissions.push(key)
  else if (!val && i >= 0) form.permissions.splice(i, 1)
}

function buildScope() {
  return form.scopeMode === 'restricted'
    ? { mode: 'restricted', groups: [...form.scopeGroups] }
    : { mode: 'global', groups: [] }
}

async function saveRole() {
  if (!form.name) {
    ElMessage.warning('请输入角色名')
    return
  }
  saving.value = true
  try {
    if (editing.value) {
      await http.updateRole(form.name, {
        description: form.description,
        permissions: [...form.permissions],
        scopeMode: form.scopeMode,
        scopeGroups: [...form.scopeGroups],
      })
      ElMessage.success('已保存')
    } else {
      await http.createRole({
        name: form.name,
        description: form.description,
        permissions: [...form.permissions],
        scopeMode: form.scopeMode,
        scopeGroups: [...form.scopeGroups],
      })
      ElMessage.success('已创建')
    }
    formVisible.value = false
    await loadAll()
  } catch (e) {
    ElMessage.error(e.message || '保存失败')
  } finally {
    saving.value = false
  }
}

async function removeRole(row) {
  try {
    await http.deleteRole(row.name)
    ElMessage.success('已删除')
    await loadAll()
  } catch (e) {
    ElMessage.error(e.message || '删除失败')
  }
}

onMounted(loadAll)
</script>

<style scoped>
.rbac-view {
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
.builtin-tag {
  margin-left: 6px;
  font-size: 13px;
  color: var(--text-muted);
}
.muted {
  color: var(--text-muted);
  font-size: 13px;
}
.scope-box {
  width: 100%;
}
.scope-warn {
  margin: 8px 0 0;
  font-size: 13px;
  color: #e6a23c;
}
.perm-matrix {
  width: 100%;
}
.perm-hint {
  margin-bottom: 10px;
}
.perm-collapse {
  border: none;
}
.perm-collapse :deep(.el-collapse-item__header) {
  background: transparent;
  color: var(--text, #e0e0e0);
  font-size: 13px;
  font-weight: 600;
  padding-left: 4px;
  border-bottom: 1px solid var(--bd);
}
.perm-collapse :deep(.el-collapse-item__wrap) {
  background: transparent;
  border-bottom: none;
}
.perm-collapse :deep(.el-collapse-item__content) {
  padding: 10px 4px 4px;
}
.domain-title {
  font-weight: 600;
}
.domain-count {
  margin-left: 6px;
  font-size: 13px;
  font-weight: 400;
  color: var(--text-dim, #999);
}
.perm-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
  gap: 6px 12px;
  max-height: 360px;
  overflow-y: auto;
  padding-right: 4px;
}
.perm-check {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 5px 8px;
  border-radius: 6px;
  cursor: pointer;
  transition: background 0.15s;
  user-select: none;
}
.perm-check:hover {
  background: var(--fill-2);
}
.check-box {
  flex-shrink: 0;
  width: 16px;
  height: 16px;
  border: 1.5px solid var(--text-muted, #666);
  border-radius: 3px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  margin-top: 2px;
  transition: all 0.15s;
}
.perm-check.checked .check-box {
  background: var(--el-color-primary, #409eff);
  border-color: var(--el-color-primary, #409eff);
}
.check-icon {
  color: #fff;
  font-size: 13px;
  line-height: 1;
}
.perm-body {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
}
.perm-key {
  font-family: var(--font-mono, 'Courier New', monospace);
  font-size: 13px;
  color: var(--text, #e0e0e0);
  word-break: break-all;
}
.perm-desc {
  font-size: 13px;
  color: var(--text-dim, #999);
  line-height: 1.3;
}
.perm-empty {
  padding: 12px 8px;
  font-size: 13px;
  color: var(--text-muted, #666);
}
.table-card {
  width: 100%;
}
</style>
