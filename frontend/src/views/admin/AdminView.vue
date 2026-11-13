<script setup lang="ts">
import { ref, onMounted, h } from 'vue'
import { ElButton, ElInput, ElAlert, ElTableV2, ElAutoResizer, ElPagination } from 'element-plus'
import { useRouter } from 'vue-router'
import { api } from '@/api/reader'
import { errorText, type Model } from '@/api/client'
const router = useRouter()
const sources = ref<Model<'SourcePage'>>(), text = ref(''), dryRun = ref<Model<'ImportResult'>>(), result = ref<Model<'ImportResult'>>()
const busy = ref(false), error = ref(''), page = ref(1)
const columns = [{ key: 'actions', title: '操作', width: 140, cellRenderer: ({ rowData }: { rowData: Model<'Source'> }) => h(ElButton, { disabled: rowData.book_source_type === 3, onClick: () => router.push(`/admin/sources/${rowData.id}`) }, () => '编辑与调试') }, { key: 'book_source_name', dataKey: 'book_source_name', title: '书源名称', width: 260 }, { key: 'book_source_group', dataKey: 'book_source_group', title: '分组', width: 160 }, { key: 'book_source_url', dataKey: 'book_source_url', title: '地址', width: 360 }, { key: 'enabled', dataKey: 'enabled', title: '状态', width: 100, cellRenderer: ({ cellData }: { cellData: unknown }) => h('span', cellData ? '启用' : '停用') }]
async function load() { try { sources.value = await api.sources(page.value) } catch (e) { error.value = errorText(e) } }
async function importText(preview: boolean) { busy.value = true; error.value = ''; try { JSON.parse(text.value); const report = await api.importSources(text.value, preview); if (preview) dryRun.value = report; else { result.value = report; dryRun.value = undefined; await load() } } catch (e) { error.value = e instanceof SyntaxError ? 'JSON 格式不正确，请检查文件内容' : errorText(e) } finally { busy.value = false } }
async function readFile(event: Event) { const input = event.target as HTMLInputElement, file = input.files?.[0]; input.value = ''; if (!file) return; if (file.size > 32 * 1024 * 1024) { error.value = '书源文件不能超过 32 MiB'; return }; text.value = await file.text(); dryRun.value = undefined }
onMounted(load)
</script>
<template>
  <main class="page-width admin-page"><div class="page-heading"><div><span class="eyebrow">连接故事的入口</span><h1>我的书源<span class="heading-dot">.</span></h1><p class="muted">导入你信任的 Legado 书源，开启在线阅读。</p></div></div><ElAlert v-if="error" :title="error" type="error" :closable="false" />
    <section class="surface"><div class="section-heading"><h2>导入书源</h2><label class="file-button secondary">选择 JSON 文件<input aria-label="选择书源文件" type="file" accept=".json,application/json" @change="readFile"></label></div><ElInput v-model="text" type="textarea" :rows="5" aria-label="书源 JSON" placeholder="粘贴 Legado 书源 JSON 数组或选择文件" @input="dryRun = undefined" /><div class="button-row"><ElButton :disabled="!text.trim()" :loading="busy" @click="importText(true)">预检</ElButton><ElButton type="primary" :disabled="!dryRun || busy" @click="importText(false)">确认导入</ElButton><span class="muted">重复书源默认跳过</span></div><p v-if="dryRun" role="status">预检：可导入 {{ dryRun.imported }}，跳过 {{ dryRun.skipped }}，失败 {{ dryRun.failed.length }}。确认后才会保存。</p><p v-if="result" role="status">已导入 {{ result.imported }}，跳过 {{ result.skipped }}，失败 {{ result.failed.length }}。</p><p v-for="failure in (dryRun ?? result)?.failed" :key="failure.index" class="error-note">第 {{ failure.index + 1 }} 条：{{ failure.reason }}</p></section>
    <section class="surface"><div class="section-heading"><h2>书源列表 <small class="muted">{{ sources?.total ?? 0 }} 个</small></h2><ElButton @click="load">刷新</ElButton></div><div style="height: 400px"><ElAutoResizer><template #default="{ height, width }"><ElTableV2 :columns="columns" :data="sources?.items ?? []" :width="width" :height="height" row-key="id" /></template></ElAutoResizer></div><ElPagination v-if="sources && sources.total > 100" v-model:current-page="page" :page-size="100" :total="sources.total" layout="prev, pager, next" @current-change="load" /></section>
  </main>
</template>
