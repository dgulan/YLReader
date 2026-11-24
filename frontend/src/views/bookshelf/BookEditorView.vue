<script setup lang="ts">
import 'element-plus/dist/index.css'
import { computed, ref, watch } from 'vue'
import { useRoute, onBeforeRouteLeave } from 'vue-router'
import { ElAlert, ElButton, ElForm, ElFormItem, ElInput, ElSelect, ElOption, ElMessageBox, ElTag, ElTabs, ElTabPane } from 'element-plus'
import { getSession } from '@/api/client'
import { useBookEditor } from './bookEditor'
import BookCover from './BookCover.vue'
const props = defineProps<{ bookId?: string }>()
const route = useRoute(), bookId = computed(() => props.bookId ?? String(route.params.bookId))
const { book, draft, dirty, busy, error, notice, deleted, mappings, sources, activeSourceName, mappingBusy, mappingError, mappingNotice, load, save, remove, addBackupMapping, removeBackupMapping } = useBookEditor(bookId)
const activeTab = ref(props.bookId ? 'overview' : 'edit')
const statusLabels = { unknown: '状态未知', serializing: '连载中', completed: '已完结', paused: '暂停' }
const total = computed(() => book.value?.total_chapters ?? 0)
const progress = computed(() => book.value?.progress)
const hasRead = computed(() => Boolean(book.value?.last_read_at || progress.value?.chapter_title || progress.value?.chapter_index
  || progress.value?.char_offset || progress.value?.chapter_ratio))
const chapter = computed(() => total.value && hasRead.value ? Math.min(total.value, (progress.value?.chapter_index ?? 0) + 1) : 0)
const percent = computed(() => total.value
  ? Math.min(100, Math.round(((progress.value?.chapter_index ?? 0) + (progress.value?.chapter_ratio ?? 0)) / total.value * 1000) / 10)
  : 0)
const remaining = computed(() => total.value - chapter.value)
const latestChapter = computed(() => book.value?.latest_chapter_title
  || mappings.value.find(mapping => mapping.source_id === book.value?.active_source_id)?.latest_chapter_title)
const newMappingSourceId = ref(''), newMappingBookUrl = ref('')
async function submitMapping() { if (await addBackupMapping(newMappingSourceId.value, newMappingBookUrl.value)) newMappingBookUrl.value = '' }
async function discard() { if (!dirty.value) return true; try { await ElMessageBox.confirm('放弃尚未保存的书籍修改？', '书籍信息', { confirmButtonText: '放弃修改', cancelButtonText: '继续编辑', type: 'warning' }); return true } catch { return false } }
async function canClose() { return !getSession() || deleted.value || (!busy.value && await discard()) }
async function reload() { if (await discard()) await load(true) }
async function confirmDelete() {
  if (!book.value) return
  try { await ElMessageBox.confirm(`删除《${book.value.name}》及其阅读进度、书签和关联记录？${book.value.book_type === 'local' ? '本地书还将清理服务端文件。' : ''}删除后无法撤销。`, '删除书籍', { confirmButtonText: '确认删除书籍', cancelButtonText: '取消', type: 'warning' }) } catch { return }
  await remove()
}
watch(bookId, () => { void load() }, { immediate: true })
onBeforeRouteLeave(canClose)
defineExpose({ canClose })
</script>
<template>
  <component :is="props.bookId ? 'div' : 'main'" :class="{ 'page-width': !props.bookId }"><template v-if="!props.bookId"><RouterLink to="/bookshelf" class="muted">← 返回书架</RouterLink><div class="page-heading"><div><span class="eyebrow">整理你的私人书房</span><h1>管理书籍<span class="heading-dot">.</span></h1><p class="muted">编辑书籍信息；阅读进度独立保存。</p></div></div></template><ElAlert v-if="error" :title="error" type="error" :closable="false" /><ElAlert v-if="notice" :title="notice" type="success" :closable="false" />
    <ElTabs v-if="!deleted" v-model="activeTab" class="book-editor-tabs">
      <ElTabPane label="概览" name="overview">
        <div v-if="book" class="book-overview">
          <div class="book-overview-hero">
            <BookCover class="overview-cover" :title="book.name" :book-id="book.id" :base="book.active_book_url" :url="book.custom_cover_url || book.cover_url" :local="book.book_type === 'local'" />
            <div class="overview-metadata">
              <h2>{{ book.name }}</h2>
              <p>{{ book.author || '佚名' }} · {{ book.book_type === 'local' ? '本地书籍' : activeSourceName || '书源名称未知' }}</p>
              <p class="overview-status"><ElTag size="small" effect="plain">{{ statusLabels[book.status] }}</ElTag> 共 {{ total }} 章</p>
              <p>最新章节 · {{ latestChapter || '暂无记录' }}</p>
              <ElButton type="primary" @click="$router.push({ name: 'reader', params: { bookId: book.id } })">继续阅读</ElButton>
            </div>
          </div>
          <section class="overview-progress" aria-label="阅读进度">
            <div class="overview-progress-heading">
              <div>
                <h3>{{ chapter ? `当前读至：第 ${chapter} 章` : '尚无阅读进度' }}</h3>
                <p v-if="progress?.chapter_title">{{ progress.chapter_title }}</p>
              </div>
              <strong>{{ percent }}%</strong>
            </div>
            <progress :value="percent" max="100" aria-label="全书阅读百分比" />
            <p class="muted">剩余 {{ remaining }} 章</p>
          </section>
          <section class="overview-intro" aria-labelledby="overview-intro-title">
            <div class="overview-intro-heading">
              <h3 id="overview-intro-title">作品简介</h3>
              <ElButton :loading="busy" @click="reload">重新加载书籍</ElButton>
            </div>
            <p class="overview-intro-text">{{ book.intro?.trim() || '暂无作品简介' }}</p>
          </section>
        </div>
        <p v-else-if="busy" role="status">正在加载书籍…</p>
        <ElButton v-else @click="reload">重新加载书籍</ElButton>
      </ElTabPane>
      <ElTabPane label="编辑与绑定" name="edit" lazy>
        <section class="surface"><div class="section-heading"><h2>书籍信息</h2><ElButton :disabled="busy" @click="reload">重新加载书籍</ElButton></div><ElForm label-position="top" :disabled="busy || !book" @submit.prevent="save"><ElFormItem label="书名"><ElInput v-model="draft.name" aria-label="编辑书名" /></ElFormItem><ElFormItem label="作者"><ElInput v-model="draft.author" aria-label="编辑作者" /></ElFormItem><ElFormItem label="连载状态"><ElSelect v-model="draft.status" aria-label="连载状态"><ElOption label="未知" value="unknown" /><ElOption label="连载中" value="serializing" /><ElOption label="已完结" value="completed" /><ElOption label="暂停" value="paused" /></ElSelect></ElFormItem><ElFormItem label="简介"><ElInput v-model="draft.intro" aria-label="编辑简介" type="textarea" :rows="5" /></ElFormItem><div class="button-row"><ElButton type="primary" native-type="submit" :loading="busy" :disabled="!book">保存书籍信息</ElButton><ElButton type="danger" plain :disabled="busy || !book" @click="confirmDelete">删除书籍</ElButton><span v-if="dirty" class="muted">有未保存修改</span></div></ElForm></section>
    <section v-if="book?.book_type === 'online'" class="surface">
      <div class="section-heading"><h2>备用书源绑定</h2></div>
      <ElAlert v-if="mappingError" :title="mappingError" type="error" :closable="false" />
      <ElAlert v-if="mappingNotice" :title="mappingNotice" type="success" closable @close="mappingNotice = ''" />
      <div v-if="mappings.length" class="mapping-list">
        <div v-for="m in mappings" :key="m.id" class="mapping-row">
          <div>
            <strong>{{ m.source_name || m.source_id }}</strong>
            <ElTag v-if="m.source_id === book.active_source_id" size="small">当前激活</ElTag>
            <p class="muted">{{ m.book_url }} · 状态：{{ m.sync_status }}</p>
          </div>
          <ElButton type="danger" plain size="small" :disabled="mappingBusy || m.source_id === book.active_source_id" @click="removeBackupMapping(m)">移除</ElButton>
        </div>
      </div>
      <p v-else class="muted">暂无备用书源绑定。</p>
      <ElForm label-position="top" :disabled="mappingBusy" @submit.prevent="submitMapping">
        <ElFormItem label="选择备用书源"><ElSelect v-model="newMappingSourceId" aria-label="选择备用书源" placeholder="请选择启用的在线书源"><ElOption v-for="s in sources" :key="s.id" :value="s.id" :label="s.book_source_name" /></ElSelect></ElFormItem>
        <ElFormItem label="书籍详情 URL"><ElInput v-model="newMappingBookUrl" aria-label="备用书源书籍地址" placeholder="https://example.com/book/1" /></ElFormItem>
        <ElButton type="primary" native-type="submit" :loading="mappingBusy">追加备用源</ElButton>
      </ElForm>
    </section>
      </ElTabPane>
    </ElTabs>
    <p v-else-if="!props.bookId"><RouterLink to="/bookshelf">返回书架查看</RouterLink></p>
  </component>
</template>
<style scoped>h2 { font-size: 18px; font-weight: 500; }:deep(.el-select) { width: min(320px,100%); }.section-heading { flex-wrap: wrap; }.mapping-list { display: flex; flex-direction: column; gap: 12px; margin-bottom: 16px; }.mapping-row { display: flex; justify-content: space-between; align-items: center; gap: 12px; border-bottom: 1px solid #8882; padding-bottom: 10px; }.mapping-row p { margin: 4px 0 0; font-size: 12px; overflow-wrap: anywhere; }:deep(.el-alert) { margin-bottom: 16px; }
.book-overview { display: grid; gap: 20px; }
.book-overview-hero { display: grid; grid-template-columns: clamp(70px, 20%, 110px) minmax(0, 1fr); align-items: start; gap: clamp(12px, 4vw, 20px); }
.overview-cover { height: auto; aspect-ratio: 3 / 4; padding: 8px 3px; gap: 6px; justify-content: center; }
.overview-cover.has-image { padding: 0; }
.overview-cover :deep(span) { font-size: 16px; letter-spacing: 1px; }
.overview-metadata { overflow-wrap: anywhere; }
.overview-metadata h2 { margin: 0; font-size: clamp(18px, 4vw, 24px); font-weight: 650; color: var(--el-text-color-primary); }
.overview-metadata p { margin: 6px 0; }
.overview-metadata :deep(.el-button) { margin-top: 6px; border-radius: 999px; }
.overview-status { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.overview-progress, .overview-intro { border-top: 1px solid var(--el-border-color-light); padding-top: 16px; }
.overview-progress-heading, .overview-intro-heading { display: flex; flex-wrap: wrap; align-items: baseline; justify-content: space-between; gap: 8px; }
.overview-progress h3, .overview-intro h3 { margin: 0; color: var(--el-text-color-primary); font-size: 16px; }
.overview-progress-heading p { margin: 6px 0 0; overflow-wrap: anywhere; }
.overview-progress-heading strong { color: var(--accent, #315d4d); font-size: 20px; }
.overview-progress progress { width: 100%; height: 8px; margin-top: 10px; accent-color: var(--accent, #315d4d); }
.overview-progress > p { margin: 5px 0 0; }
.overview-intro-text { margin: 12px 0 0; white-space: pre-wrap; overflow-wrap: anywhere; line-height: 1.8; }
</style>
