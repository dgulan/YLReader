<script setup lang="ts">
import 'element-plus/dist/index.css'
import { ref, reactive, onMounted, computed } from 'vue'
import { ElMessageBox, ElButton, ElInput, ElAlert, ElCheckbox, ElPagination, ElForm, ElFormItem, ElSelect, ElOption, ElInputNumber, ElSwitch } from 'element-plus'
import { useRouter } from 'vue-router'
import { api } from '@/api/reader'
import { errorText, type Model } from '@/api/client'
import { useSourceImport } from './sourceImport'
import { buildSourceCreate, deleteSelectedSources, exportSourcesBundle } from './sourceManage'
const router = useRouter()
const sources = ref<Model<'SourcePage'>>(), page = ref(1)
const { text, sourceUrl, fileLabel, dryRun, result, busy, error, canPreview, canPreviewUrl, canCommitText, canCommitUrl, readFile, importText, importUrl } = useSourceImport(load, async report => {
  const samples = report.duplicates?.slice(0, 3).map(source => source.book_source_name).join('、')
  try {
    await ElMessageBox.confirm(
      `检测到 ${report.duplicate_count ?? report.skipped} 条重复书源${samples ? '（例如：' + samples + '）' : ''}，是否用新导入的书源覆盖已有旧书源？`,
      '检测到重复书源',
      { confirmButtonText: '覆盖旧书源', cancelButtonText: '跳过重复', type: 'warning' },
    )
    return true
  } catch { return false }
})
const creating = ref(false), exporting = ref(false), showCreate = ref(false), manageNotice = ref(''), manageError = ref('')
const draft = reactive({ url: '', name: '', group: '默认', type: 0, enabled: true, enabledExplore: true, weight: 0, rules: '{}', header: '{}' })
const filterText = ref(''), filter = ref(''), selecting = ref(false), deleting = ref(false), loading = ref(false)
const selected = ref<string[]>([]), toggling = reactive(new Set<string>())
const bulkStatus = ref(''), bulkError = ref('')
const selectable = computed(() => sources.value?.items.filter(source => source.book_source_type !== 3) ?? [])
const allSelected = computed(() => selectable.value.length > 0 && selectable.value.every(source => selected.value.includes(source.id)))
const togglingAny = computed(() => toggling.size > 0)
function isToggling(id: string) { return toggling.has(id) }
function editSource(id: string) { void router.push('/admin/sources/' + id) }
let loadVersion = 0
async function load() {
  const version = ++loadVersion
  const requestedPage = page.value
  loading.value = true
  try {
    const result = await api.sources(requestedPage, filter.value)
    if (version !== loadVersion) return
    const lastPage = Math.max(1, Math.ceil(result.total / 100))
    if (requestedPage > lastPage) {
      page.value = lastPage
      await load()
      return
    }
    sources.value = result
    const ids = new Set(selectable.value.map(source => source.id))
    selected.value = selected.value.filter(id => ids.has(id))
  } catch (e) {
    if (version === loadVersion) error.value = errorText(e)
  } finally {
    if (version === loadVersion) loading.value = false
  }
}
function applyFilter() {
  selected.value = []
  page.value = 1
  filter.value = filterText.value.trim()
  void load()
}
function changePage() { selected.value = []; void load() }
function toggleSelection() { selecting.value = !selecting.value; selected.value = [] }
function setSelected(id: string, checked: boolean) {
  selected.value = checked ? [...selected.value, id] : selected.value.filter(value => value !== id)
}
function selectAll() { selected.value = allSelected.value ? [] : selectable.value.map(source => source.id) }
function domain(value: string) {
  try { return new URL(value).hostname } catch { return '' }
}
async function toggleSource(source: Model<'Source'>, enabled: boolean) {
  if (toggling.has(source.id) || deleting.value) return
  toggling.add(source.id)
  manageError.value = ''
  try {
    const updated = await api.updateSource(source.id, { enabled, version: source.version })
    if (sources.value) sources.value.items = sources.value.items.map(row => row.id === source.id ? updated : row)
  } catch (e) {
    manageError.value = errorText(e)
    await load()
  } finally { toggling.delete(source.id) }
}
async function deleteSelection() {
  if (!selected.value.length || deleting.value) return
  deleting.value = true
  bulkError.value = ''; bulkStatus.value = ''
  try {
    const targets = selectable.value.filter(source => selected.value.includes(source.id))
    try {
      await ElMessageBox.confirm(
        '将删除选中的 ' + targets.length + ' 个书源。使用这些书源的书籍可能需要重新选源。',
        '确认删除书源',
        { confirmButtonText: '确认删除', cancelButtonText: '取消', type: 'warning' },
      )
    } catch { return }
    const { deleted, failed } = await deleteSelectedSources(targets, api.deleteSource)
    selected.value = failed.map(source => source.id)
    if (deleted) bulkStatus.value = '已删除 ' + deleted + ' 个书源'
    if (failed.length) bulkError.value = failed.length + ' 个书源删除失败，仍保持选中。'
    await load()
  } finally { deleting.value = false }
}
async function chooseFile(event: Event) { const input = event.target as HTMLInputElement, file = input.files?.[0]; input.value = ''; if (file) await readFile(file) }
async function createOne() {
  manageError.value = ''; manageNotice.value = ''
  let payload: Model<'SourceCreate'>
  try { payload = buildSourceCreate(draft) } catch (e) { manageError.value = errorText(e); return }
  creating.value = true
  try {
    const created = await api.createSource(payload)
    manageNotice.value = `已创建书源《${created.book_source_name}》`
    draft.url = ''; draft.name = ''; draft.group = '默认'; draft.rules = '{}'; draft.header = '{}'
    showCreate.value = false
    page.value = 1
    await load()
  } catch (e) { manageError.value = errorText(e) }
  finally { creating.value = false }
}
async function exportAll() {
  manageError.value = ''; manageNotice.value = ''; exporting.value = true
  try {
    const bundle = await exportSourcesBundle()
    const blob = new Blob([bundle.json], { type: 'application/json;charset=utf-8' })
    const href = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = href; link.download = bundle.filename
    document.body.append(link); link.click(); link.remove()
    URL.revokeObjectURL(href)
    manageNotice.value = `已导出 ${bundle.count} 个书源`
  } catch (e) { manageError.value = errorText(e) }
  finally { exporting.value = false }
}
onMounted(load)
</script>
<template>
  <main class="page-width admin-page"><div class="page-heading"><div><span class="eyebrow">连接故事的入口</span><h1>我的书源<span class="heading-dot">.</span></h1><p class="muted">导入你信任的 Legado 书源，开启在线阅读。</p></div></div><ElAlert v-if="error" :title="error" type="error" :closable="false" /><ElAlert v-if="manageError" :title="manageError" type="error" :closable="false" /><ElAlert v-if="manageNotice" :title="manageNotice" type="success" closable @close="manageNotice = ''" />
    <section class="surface"><div class="section-heading"><h2>导入书源</h2><label class="file-button secondary">选择 JSON 文件<input aria-label="选择书源文件" type="file" accept=".json,application/json" :disabled="busy" @change="chooseFile"></label></div>
      <div class="search-row"><ElInput v-model="sourceUrl" :disabled="busy" aria-label="在线书源网址" placeholder="粘贴在线书源网址（http/https），直接抓取导入" clearable /><ElButton :disabled="!canPreviewUrl" :loading="busy" @click="importUrl(true)">网址预检</ElButton><ElButton type="primary" :disabled="!canCommitUrl" :loading="busy" @click="importUrl(false)">导入在线书源</ElButton></div>
      <p v-if="fileLabel" role="status">已加载文件：{{ fileLabel }}，可预检后导入。</p><ElInput v-model="text" :disabled="busy" type="textarea" :rows="5" aria-label="书源 JSON" placeholder="粘贴 Legado 书源 JSON 数组或选择文件" /><div class="button-row"><ElButton :disabled="!canPreview" :loading="busy" @click="importText(true)">预检</ElButton><ElButton type="primary" :disabled="!canCommitText" @click="importText(false)">确认导入</ElButton></div>
      <p v-if="dryRun" role="status">预检：可导入 {{ dryRun.imported }}，跳过 {{ dryRun.skipped }}，失败 {{ dryRun.failed.length }}。确认后才会保存。</p>
      <p v-if="result" role="status">已导入 {{ result.imported }}，跳过 {{ result.skipped }}，失败 {{ result.failed.length }}。</p>
      <p v-if="result && (result.overwritten_count ?? result.updated) > 0" class="muted">覆盖更新 {{ result.overwritten_count ?? result.updated }} 条。</p>
      <p v-for="failure in (dryRun ?? result)?.failed" :key="failure.index" class="error-note">第 {{ failure.index + 1 }} 条：{{ failure.reason }}</p>
    </section>
    <section class="surface"><div class="section-heading"><h2>新建书源</h2><ElButton @click="showCreate = !showCreate">{{ showCreate ? '收起表单' : '新建单个书源' }}</ElButton></div>
      <ElForm v-if="showCreate" label-position="top" :disabled="creating" @submit.prevent="createOne">
        <ElFormItem label="书源地址"><ElInput v-model="draft.url" aria-label="新建书源地址" placeholder="https://example.com" /></ElFormItem>
        <ElFormItem label="书源名称"><ElInput v-model="draft.name" aria-label="新建书源名称" maxlength="128" /></ElFormItem>
        <ElFormItem label="分组"><ElInput v-model="draft.group" aria-label="新建书源分组" maxlength="64" /></ElFormItem>
        <ElFormItem label="类型"><ElSelect v-model="draft.type" aria-label="新建书源类型"><ElOption :value="0" label="文本 (0)" /><ElOption :value="1" label="音频 (1)" /><ElOption :value="2" label="图片 (2)" /></ElSelect></ElFormItem>
        <ElFormItem label="权重"><ElInputNumber v-model="draft.weight" aria-label="新建书源权重" :step="1" /></ElFormItem>
        <ElFormItem label="启用"><ElSwitch v-model="draft.enabled" aria-label="新建启用书源" /></ElFormItem>
        <ElFormItem label="解析规则 JSON"><ElInput v-model="draft.rules" aria-label="新建书源规则 JSON" type="textarea" :rows="4" /></ElFormItem>
        <ElFormItem label="请求头 JSON"><ElInput v-model="draft.header" aria-label="新建书源请求头 JSON" type="textarea" :rows="2" /></ElFormItem>
        <ElButton type="primary" native-type="submit" :loading="creating">创建书源</ElButton>
      </ElForm>
    </section>
    <section class="surface">
      <div class="section-heading">
        <h2>书源列表 <small class="muted">{{ sources?.total ?? 0 }} 个</small></h2>
        <div class="button-row source-tools">
          <ElButton :disabled="deleting" @click="toggleSelection">{{ selecting ? '退出多选' : '多选' }}</ElButton>
          <ElButton :loading="exporting" @click="exportAll">导出 JSON</ElButton>
          <ElButton :disabled="deleting" @click="load">刷新</ElButton>
        </div>
      </div>
      <form class="search-row source-filter" @submit.prevent="applyFilter">
        <ElInput v-model="filterText" aria-label="筛选书源" placeholder="按书源名称或网址筛选" maxlength="100" clearable :disabled="deleting" @clear="applyFilter" />
        <ElButton native-type="submit" :loading="loading" :disabled="deleting">筛选</ElButton>
      </form>
      <div v-if="selecting" class="button-row source-tools">
        <ElButton :disabled="!selectable.length || loading || deleting" @click="selectAll">{{ allSelected ? '取消全选' : '全选本页' }}</ElButton>
        <ElButton type="danger" :disabled="!selected.length || loading || togglingAny" :loading="deleting" @click="deleteSelection">删除所选 ({{ selected.length }})</ElButton>
      </div>
      <p v-if="!loading && !sources?.items.length" class="muted">没有匹配的书源。</p>
      <p v-if="bulkStatus" role="status">{{ bulkStatus }}</p>
      <p v-if="bulkError" class="error-note" role="alert">{{ bulkError }}</p>
      <ul class="source-list" aria-label="书源列表" :aria-busy="loading">
        <li v-for="source in sources?.items" :key="source.id" class="source-row">
          <ElCheckbox v-if="selecting" :model-value="selected.includes(source.id)" @change="setSelected(source.id, $event === true)" :aria-label="'选择 ' + source.book_source_name" :disabled="source.book_source_type === 3 || loading || deleting" />
          <div class="source-copy">
            <strong>{{ source.book_source_name }}</strong>
            <small>{{ [domain(source.book_source_url), source.book_source_group].filter(Boolean).join(' · ') || '未分组' }}</small>
          </div>
          <div class="source-actions">
            <ElSwitch :model-value="source.enabled" :aria-label="'启用 ' + source.book_source_name" :loading="isToggling(source.id)" :disabled="source.book_source_type === 3 || deleting" @change="toggleSource(source, $event === true)" />
            <ElButton size="small" :disabled="source.book_source_type === 3 || deleting" @click="editSource(source.id)">编辑</ElButton>
          </div>
        </li>
      </ul>
      <ElPagination v-if="sources && sources.total > 100" v-model:current-page="page" :disabled="deleting" :page-size="100" :total="sources.total" layout="prev, pager, next" @current-change="changePage" />
    </section>
  </main>
</template>
<style scoped>
:deep(.el-alert) { margin-bottom: 16px; }
.source-filter { margin: 12px 0; }
.source-filter :deep(.el-input__wrapper) { border-radius: 999px; }
.source-filter :deep(.el-input__inner) { font-size: 16px; }
.source-tools { gap: 8px; margin: 6px 0; }
.source-tools :deep(.el-button + .el-button) { margin-left: 0; }
.source-list { margin: 0; padding: 0; list-style: none; }
.source-row { display: flex; align-items: center; gap: 12px; min-height: 60px; padding: 6px 0; border-bottom: 1px solid #e3e5dc; }
.source-copy { display: flex; flex-direction: column; gap: 3px; flex: 1 1 0; min-width: 0; }
.source-copy strong, .source-copy small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.source-copy strong { font-size: 15px; }
.source-copy small { color: #657569; font-size: 13px; line-height: 1.35; }
.source-actions { display: flex; gap: 8px; align-items: center; }
</style>
