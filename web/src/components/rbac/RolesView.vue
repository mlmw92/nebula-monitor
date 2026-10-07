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
        <el-table-column label="默认范围" min-width="190">
          <template #default="{ row }">
            <el-tag v-if="row.scope_mode === 'restricted'" size="small" type="warning" effect="plain">受限</el-tag>
            <el-tag v-else size="small" type="success" effect="plain">全部</el-tag>
            <!-- 业务范围是第二个维度，与节点范围取交集。没配就不显示：否则「全部」容易被读成
                 「业务上也不限」，而实际语义是「该维度不生效」。 -->
            <template v-if="row.asset_mode === 'limited'">
              <el-tag
                v-for="l in (row.asset_labels || [])"
                :key="l.key + '=' + l.value"
                size="small"
                type="info"
                effect="plain"
                class="grp-tag"
              >{{ l.key }}={{ l.value }}</el-tag>
              <el-tag v-if="!(row.asset_labels || []).length" size="small" type="danger" effect="plain" class="grp-tag">
                业务范围为空 → 无可见资产
              </el-tag>
            </template>
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
            <!-- 业务维度：与节点维度是两个独立维度，**取交集**（都放行才看得见）。
                 默认「不限」= 该维度不生效，因此存量角色的行为不会因为这次上线而变化。 -->
            <div class="scope-sub">
              <div class="scope-sub-title">
                业务范围（按资产标签 <code>{{ assetKey }}</code> 划）
              </div>
              <el-radio-group v-model="form.assetMode">
                <el-radio value="all">不限</el-radio>
                <el-radio value="limited">限定取值</el-radio>
              </el-radio-group>
              <el-select
                v-if="form.assetMode === 'limited'"
                v-model="form.assetValues"
                multiple
                filterable
                allow-create
                default-first-option
                :reserve-keyword="false"
                placeholder="选择或直接输入标签值（如 pay）"
                style="width: 100%; margin-top: 10px"
              >
                <el-option v-for="v in assetValueOptions" :key="v" :label="v" :value="v" />
              </el-select>
              <p class="scope-note">
                与上面的节点范围是<b>且</b>的关系：两个范围都放行才看得见该资产。
                标签值要先打在资产上（台账 → 资产 → 标签/批量维护）；<b>没打标签的资产不属于任何业务范围</b>。
              </p>
              <p v-if="form.assetMode === 'limited' && !form.assetValues.length" class="scope-warn">
                限定了业务范围却没有取值时，归属此角色的人将看不到任何资产（这是"无权限"，不是"不限"）。
              </p>
            </div>
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
import { ref, reactive, computed, onMounted } from 'vue'
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
const catalog = reactive({ domains: [], assetScopeLabelKey: '' })
const groupNames = ref([])
const loading = ref(false)
const saving = ref(false)
const activeDomains = ref([])

// 业务范围的约定标签键由服务端给出（可配）：硬编码在前端的话，部署方改过键之后
// 表单会静默地去查一个不存在的键，看起来像"没有可选值"。
const assetKey = computed(
  () => auth.principal.assetScopeLabelKey || catalog.assetScopeLabelKey || 'biz'
)
// 候选值（可选）：拿不到就留空，表单仍允许直接输入——不因为"没有候选项"就把能力藏起来。
const assetValueOptions = ref([])

const formVisible = ref(false)
const editing = ref(false)
const form = reactive({
  name: '',
  description: '',
  permissions: [],
  scopeMode: 'global',
  scopeGroups: [],
  // 业务维度：默认「不限」（= 该维度不生效），与后端的空模式一致。
  assetMode: 'all',
  assetValues: [],
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
    catalog.assetScopeLabelKey = (c && c.assetScopeLabelKey) || ''
    const gs = Array.isArray(g) ? g : g.groups || []
    groupNames.value = gs.map((x) => (typeof x === 'string' ? x : x.name)).filter(Boolean)
    activeDomains.value = catalog.domains.map((d) => d.domain)
    await loadAssetValues()
  } catch (e) {
    ElMessage.error(e.message || '加载角色数据失败')
  } finally {
    loading.value = false
  }
}

// loadAssetValues 取该标签键下的候选取值（服务端按调用者的可见节点收窄）。
// 失败（例如没有 assets:read）只是没有候选，不影响填写——`allow-create` 仍可直接输入。
async function loadAssetValues() {
  try {
    const d = await http.get('/api/v1/assets/label-values?key=' + encodeURIComponent(assetKey.value))
    assetValueOptions.value = (d && d.values) || []
  } catch (e) {
    assetValueOptions.value = []
  }
}

function resetForm() {
  form.name = ''
  form.description = ''
  form.permissions = []
  form.scopeMode = 'global'
  form.scopeGroups = []
  form.assetMode = 'all'
  form.assetValues = []
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
  form.assetMode = row.asset_mode === 'limited' ? 'limited' : 'all'
  form.assetValues = Array.isArray(row.asset_labels) ? row.asset_labels.map((x) => x.value) : []
  formVisible.value = true
}

function togglePerm(key, val) {
  const i = form.permissions.indexOf(key)
  if (val && i < 0) form.permissions.push(key)
  else if (!val && i >= 0) form.permissions.splice(i, 1)
}

// buildAssetLabels 拼业务范围的选择器。
//
// 不限时返回**空数组**而不是省略该字段：服务端不允许"声明 all 却带着选择器"，
// 显式给空数组才表达"这次就是把业务维度关掉"，与"没传这个字段（不改）"区分开。
function buildAssetLabels() {
  return form.assetMode === 'limited'
    ? form.assetValues.map((v) => ({ key: assetKey.value, value: v }))
    : []
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
        assetMode: form.assetMode,
        assetLabels: buildAssetLabels(),
      })
      ElMessage.success('已保存')
    } else {
      await http.createRole({
        name: form.name,
        description: form.description,
        permissions: [...form.permissions],
        scopeMode: form.scopeMode,
        scopeGroups: [...form.scopeGroups],
        assetMode: form.assetMode,
        assetLabels: buildAssetLabels(),
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
/* 业务范围是第二个维度，与节点范围是「且」的关系：视觉上也要能看出这是两块，
   否则很容易被读成"选一个就行"。 */
.scope-sub {
  margin-top: 14px;
  padding-top: 12px;
  border-top: 1px dashed var(--border-color, rgba(128, 128, 128, 0.25));
}
.scope-sub-title {
  margin-bottom: 8px;
  font-size: 13px;
  color: var(--text-muted);
}
.scope-note {
  margin: 8px 0 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--text-muted);
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
