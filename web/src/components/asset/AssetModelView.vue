<template>
  <section class="view">
    <PageHeader
      title="配置项模型"
      desc="每个类型上会出现哪些配置项、哪些参与巡检比对；模型是「类型」的，不是「实例」的——在这里改不会动任何一台机器上已有的值"
    >
      <template #actions>
        <el-button size="small" :loading="loading" @click="reload">刷新</el-button>
      </template>
    </PageHeader>

    <el-alert
      v-if="loadError"
      type="error"
      :closable="false"
      show-icon
      title="读取配置项模型失败"
      :description="loadError"
    />

    <!-- 类型清单 -->
    <SectionCard title="类型" dense>
      <div class="type-row">
        <button
          v-for="t in types"
          :key="t.key"
          class="type-chip"
          :class="{ active: t.key === selectedKey }"
          @click="selectType(t.key)"
        >
          <span class="type-name">{{ t.title }}</span>
          <span class="muted">{{ t.assets }} 个资产</span>
          <!-- 短命对象：模型页要显示它们，但必须标出来（它们默认不计入台账健康度） -->
          <el-tag v-if="t.ephemeral" size="small" type="info" title="运行时短命对象：默认不计入台账健康度">短命对象</el-tag>
        </button>
      </div>
      <p class="muted note">
        类型与字段定义是**平台配置**（谁能进本页，看到的是同一份）；而资产数与下面的覆盖度按你的可见范围统计。
        <template v-if="current && current.baseline">
          本类型的标杆资产：<span class="mono">{{ current.baseline.assetKey }}</span>。
        </template>
      </p>
    </SectionCard>

    <!-- 字段表 -->
    <SectionCard v-if="current" :title="`字段（${current.title}）`" dense>
      <div class="toolbar">
        <span class="muted">运行态字段（不参与比对，避免探活抖动灌进差异清单）：</span>
        <el-tag v-for="k in current.schema.runtimeFields" :key="k" size="small" type="info" class="mono">{{ k }}</el-tag>
        <el-tag v-if="current.schema.runtimeFieldsDefault" size="small">内置默认</el-tag>
        <span class="spacer"></span>
        <el-button size="small" :disabled="!canWrite" @click="clearComparison">清空比对设置</el-button>
        <el-button size="small" type="primary" :disabled="!canWrite" :loading="saving" @click="save">保存</el-button>
        <span v-if="!canWrite" class="muted">缺 assets:write 权限，只能查看</span>
      </div>

      <!-- 关注字段非空时，只有被点名的字段参与比对——这点必须在页面上说清，
           否则"我明明看到这个字段有值，为什么没进差异"会变成一个谜 -->
      <el-alert
        v-if="focusCount > 0"
        class="tip"
        type="warning"
        :closable="false"
        show-icon
        :title="`当前只比对标注为「关注」的 ${focusCount} 个字段`"
        description="其余字段不参与比对（也不会被报成「缺失」）。全部改回「默认」即回到「全部字段减运行态字段」。"
      />

      <el-table :data="current.attrs" style="width: 100%">
        <el-table-column label="字段" min-width="190">
          <template #default="{ row }"><span class="mono">{{ row.key }}</span></template>
        </el-table-column>
        <el-table-column label="中文名" width="170">
          <template #default="{ row }">
            <el-input
              v-model="draft.attrMeta[row.key].title"
              size="small"
              :disabled="!canWrite"
              placeholder="未填则显示字段键"
            />
          </template>
        </el-table-column>
        <el-table-column label="单位" width="100">
          <template #default="{ row }">
            <el-input v-model="draft.attrMeta[row.key].unit" size="small" :disabled="!canWrite" placeholder="—" />
          </template>
        </el-table-column>
        <el-table-column label="备注" min-width="180">
          <template #default="{ row }">
            <el-input v-model="draft.attrMeta[row.key].note" size="small" :disabled="!canWrite" placeholder="给同事看的一句话" />
          </template>
        </el-table-column>
        <el-table-column label="来源" width="150">
          <template #default="{ row }">
            <el-tag v-if="row.discovery" size="small">采集 {{ row.discovery }}</el-tag>
            <el-tag v-if="row.manual" size="small" type="warning">人工 {{ row.manual }}</el-tag>
            <span v-if="!row.discovery && !row.manual" class="muted">—</span>
          </template>
        </el-table-column>
        <el-table-column label="覆盖度" width="160">
          <template #default="{ row }">
            <div class="cover">
              <div class="cover-track"><div class="cover-fill" :style="{ width: pct(row.coverage) }"></div></div>
              <span class="muted">{{ row.assets }}/{{ current.assets }}</span>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="比对" width="140">
          <template #default="{ row }">
            <el-select v-model="draft.mode[row.key]" size="small" :disabled="!canWrite">
              <el-option label="默认" value="default" />
              <el-option label="关注" value="focus" />
              <el-option label="排除（运行态）" value="runtime" />
            </el-select>
          </template>
        </el-table-column>
      </el-table>

      <p v-if="current.attrsTruncated" class="muted note">
        字段数超过上限，这里只按覆盖数显示前 {{ current.attrs.length }} 个——不要据此判断「这个类型只有这些字段」。
      </p>

      <div v-if="current.synonymGroups && current.synonymGroups.length" class="synonyms">
        <div class="muted">疑似同义键（只是写法不同；**不会自动合并**——合并是不可逆的，请自行确认后决定）</div>
        <div v-for="(group, i) in current.synonymGroups" :key="i" class="mono">{{ group.join('  /  ') }}</div>
      </div>

      <p class="muted note">
        这里**不显示属性值**：值里可能有连接串与口令，模型页只看模型；要看某台资产的具体值，去资产台账详情
        （那里有权限与范围约束）。
      </p>
    </SectionCard>
  </section>
</template>

<script setup>
import { computed, ref, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import PageHeader from '../common/PageHeader.vue'
import SectionCard from '../common/SectionCard.vue'
import { getAssetTypes, saveAssetTypeModel } from '../../api/asset'
import { useAuth } from '../../composables/useAuth'

const auth = useAuth()
// 前端隐藏仅为体验：服务端 assets:write 才是边界（该权限点为高风险）
const canWrite = computed(() => auth.can('assets:write'))

const types = ref([])
const selectedKey = ref('')
const loading = ref(false)
const saving = ref(false)
const loadError = ref('')
// draft 是"正在编辑的模型"：每行的比对档位与属性说明。
// 与列表数据分开，是为了"没保存就离开"不会污染下一步（保存成功后才回填）。
const draft = ref({ mode: {}, attrMeta: {} })

const current = computed(() => types.value.find((t) => t.key === selectedKey.value) || null)
// focusCount 用于提示"只比对点名字段"：非空即表示其余字段不参与比对
const focusCount = computed(() => Object.values(draft.value.mode).filter((m) => m === 'focus').length)

function pct(v) {
  return `${Math.round((Number(v) || 0) * 100)}%`
}

// buildDraft 用服务端的模型铺一次编辑态（切类型、保存成功、刷新后都走它）
function buildDraft(model) {
  const mode = {}
  const attrMeta = {}
  for (const st of model.attrs || []) {
    mode[st.key] = 'default'
    attrMeta[st.key] = { title: '', unit: '', note: '' }
  }
  for (const k of model.schema?.runtimeFields || []) {
    if (mode[k] === undefined) mode[k] = 'runtime'
    else mode[k] = 'runtime'
  }
  for (const k of model.schema?.focusFields || []) {
    mode[k] = 'focus'
  }
  for (const [k, meta] of Object.entries(model.schema?.attrMeta || {})) {
    // 说明可能落在"当前没有资产带这个键"的字段上：也要能编辑，所以按出现过的键补齐
    attrMeta[k] = { title: meta.title || '', unit: meta.unit || '', note: meta.note || '' }
    if (mode[k] === undefined) mode[k] = 'default'
  }
  draft.value = { mode, attrMeta }
}

function selectType(key) {
  selectedKey.value = key
  const model = types.value.find((t) => t.key === key)
  if (model) buildDraft(model)
}

function applyTypes(list) {
  types.value = list || []
  if (!selectedKey.value && types.value.length) {
    selectType(types.value[0].key)
    return
  }
  const model = types.value.find((t) => t.key === selectedKey.value) || types.value[0]
  if (model) selectType(model.key)
}

async function reload() {
  loading.value = true
  try {
    const data = await getAssetTypes()
    applyTypes(data.types)
    loadError.value = ''
  } catch (e) {
    loadError.value = e.message || '请稍后重试'
  } finally {
    loading.value = false
  }
}

// collect 把编辑态折成接口要的三项（判断口径与后端一致：空数组 = 用内置默认）。
//
// 有一处**必须**小心：如果用户没动过运行态字段（当前生效的就是内置默认那一份），
// 提交的 runtimeFields 要是**空数组**而不是把默认清单原样提交——
// 否则"只改了个中文名"的保存会把默认清单固化进模型，将来内置默认调整就再也影响不到它。
function collect() {
  const model = current.value
  const runtimeFields = []
  const focusFields = []
  for (const [key, mode] of Object.entries(draft.value.mode)) {
    if (mode === 'runtime') runtimeFields.push(key)
    if (mode === 'focus') focusFields.push(key)
  }
  const effective = [...(model?.schema?.runtimeFields || [])].sort()
  const picked = [...runtimeFields].sort()
  const untouchedDefault =
    model?.schema?.runtimeFieldsDefault &&
    effective.length === picked.length &&
    effective.every((k, i) => k === picked[i])
  const attrMeta = {}
  for (const [key, meta] of Object.entries(draft.value.attrMeta)) {
    const title = (meta.title || '').trim()
    const unit = (meta.unit || '').trim()
    const note = (meta.note || '').trim()
    if (title || unit || note) attrMeta[key] = { title, unit, note }
  }
  return { runtimeFields: untouchedDefault ? [] : runtimeFields, focusFields, attrMeta }
}

async function save() {
  if (!current.value) return
  saving.value = true
  try {
    const data = await saveAssetTypeModel(current.value.key, collect())
    applyTypes(data.types)
    ElMessage.success(`已保存「${current.value ? current.value.title : ''}」的模型`)
  } catch (e) {
    ElMessage.error(e.message || '保存失败')
  } finally {
    saving.value = false
  }
}

async function clearComparison() {
  try {
    await ElMessageBox.confirm(
      '清空后回到「全部字段减运行态字段」的内置默认（内置默认的运行态字段是 up / status / uptime / uptimeSeconds）。' +
        '属性说明（中文名/单位/备注）不受影响。',
      '清空比对设置',
      { type: 'warning', confirmButtonText: '清空', cancelButtonText: '取消' },
    )
  } catch (e) {
    return
  }
  const mode = {}
  for (const k of Object.keys(draft.value.mode)) mode[k] = 'default'
  draft.value = { ...draft.value, mode }
  ElMessage.info('已清空比对设置，记得点「保存」')
}

onMounted(reload)
</script>

<style scoped>
.view { padding: 0; display: flex; flex-direction: column; gap: 12px; }
.muted { color: var(--text-dim); font-size: 13px; }
.mono { font-family: var(--mono); }
.note { margin: 10px 0 0; line-height: 20px; }
.tip { margin-bottom: 10px; }
.type-row { display: flex; flex-wrap: wrap; gap: 8px; }
.type-chip {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--fill-1);
  color: var(--text);
  cursor: pointer;
  font-size: 13px;
}
.type-chip.active { border-color: var(--accent); color: var(--accent); }
.type-name { font-weight: 600; }
.toolbar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 10px; }
.spacer { flex: 1; }
.cover { display: flex; align-items: center; gap: 8px; }
.cover-track { flex: 1; height: 6px; border-radius: 3px; background: var(--fill-2); overflow: hidden; }
.cover-fill { height: 100%; background: var(--accent); }
.synonyms { margin-top: 10px; line-height: 22px; }
</style>
