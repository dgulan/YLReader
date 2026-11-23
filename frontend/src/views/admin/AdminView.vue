<script setup lang="ts">
import 'element-plus/dist/index.css'
import { ref, reactive, onMounted, h } from 'vue'
import { ElMessageBox, ElButton, ElInput, ElAlert, ElTableV2, ElAutoResizer, ElPagination, ElForm, ElFormItem, ElSelect, ElOption, ElInputNumber, ElSwitch } from 'element-plus'
import { useRouter } from 'vue-router'
import { api } from '@/api/reader'
import { errorText, type Model } from '@/api/client'
import { useSourceImport } from './sourceImport'
import { buildSourceCreate, exportSourcesBundle } from './sourceManage'
const router = useRouter()
const sources = ref<Model<'SourcePage'>>(), page = ref(1)
const { text, sourceUrl, fileLabel, dryRun, result, busy, error, canPreview, canPreviewUrl, canCommitText, canCommitUrl, readFile, importText, importUrl } = useSourceImport(load, async report => {
  const samples = report.duplicates?.slice(0, 3).map(source => source.book_source_name).join('、')
  try {
    await ElMessageBox.confirm(
      `已导入 ${report.imported_count ?? report.imported} 条新书源，检测到 ${report.duplicate_count} 条重复书源${samples ? '（例如：' + samples + '）' : ''}。是否覆盖已有书源？`,
      '检测到重复书源',
      { confirmButtonText: '覆盖旧书源', cancelButtonText: '跳过重复', type: 'warning' },
    )
    return true
  } catch { return false }
})
const creating = ref(false), exporting = ref(false), showCreate = ref(false), manageNotice = ref(''), manageError = ref('')
const draft = reactive({ url: '', name: '', group: '默认', type: 0, enabled: true, enabledExplore: true, weight: 0, rules: '{}', header: '{}' })
const columns = [{ key: 'actions', title: '操作', width: 140, cellRenderer: ({ rowData }: { rowData: Model<'Source'> }) => h(ElButton, { disabled: rowData.book_source_type === 3, onClick: () => router.push(`/admin/sources/${rowData.id}`) }, () => '编辑与调试') }, { key: 'book_source_name', dataKey: 'book_source_name', title: '书源名称', width: 260 }, { key: 'book_source_group', dataKey: 'book_source_group', title: '分组', width: 160 }, { key: 'book_source_url', dataKey: 'book_source_url', title: '地址', width: 360 }, { key: 'enabled', dataKey: 'enabled', title: '状态', width: 100, cellRenderer: ({ cellData }: { cellData: unknown }) => h('span', cellData ? '启用' : '停用') }]
async function load() { try { sources.value = await api.sources(page.value) } catch (e) { error.value = errorText(e) } }
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
      <p v-if="fileLabel" role="status">已加载文件：{{ fileLabel }}，可预检后导入。</p><ElInput v-model="text" :disabled="busy" type="textarea" :rows="5" aria-label="书源 JSON" placeholder="粘贴 Legado 书源 JSON 数组或选择文件"  /><div class="button-row"><ElButton :disabled="!canPreview" :loading="busy" @click="importText(true)">预检</ElButton><ElButton type="primary" :disabled="!canCommitText" @click="importText(false)">确认导入</ElButton><span class="muted">重复书源默认跳过</span></div><p v-if="dryRun" role="status">预检：可导入 {{ dryRun.imported }}，跳过 {{ dryRun.skipped }}，失败 {{ dryRun.failed.length }}。确认后才会保存。</p><p v-if="result" role="status">已导入 {{ result.imported }}<template v-if="(result.overwritten_count ?? result.updated) > 0">，覆盖更新 {{ result.overwritten_count ?? result.updated }} 条</template>，跳过 {{ result.skipped }}，失败 {{ result.failed.length }}。</p><p v-for="failure in (dryRun ?? result)?.failed" :key="failure.index" class="error-note">第 {{ failure.index + 1 }} 条：{{ failure.reason }}</p></section>
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
    <section class="surface"><div class="section-heading"><h2>书源列表 <small class="muted">{{ sources?.total ?? 0 }} 个</small></h2><div class="button-row"><ElButton :loading="exporting" @click="exportAll">导出 JSON</ElButton><ElButton @click="load">刷新</ElButton></div></div><div style="height: 400px"><ElAutoResizer><template #default="{ height, width }"><ElTableV2 :columns="columns" :data="sources?.items ?? []" :width="width" :height="height" row-key="id" /></template></ElAutoResizer></div><ElPagination v-if="sources && sources.total > 100" v-model:current-page="page" :page-size="100" :total="sources.total" layout="prev, pager, next" @current-change="load" /></section>
  </main>
</template>
<style scoped>:deep(.el-alert) { margin-bottom: 16px; }</style>
