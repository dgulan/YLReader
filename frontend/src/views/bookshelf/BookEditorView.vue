<script setup lang="ts">
import 'element-plus/dist/index.css'
import { computed, ref, watch } from 'vue'
import { useRoute, onBeforeRouteLeave } from 'vue-router'
import { ElAlert, ElButton, ElForm, ElFormItem, ElInput, ElSelect, ElOption, ElMessageBox, ElTag } from 'element-plus'
import { getSession } from '@/api/client'
import { useBookEditor } from './bookEditor'
const route = useRoute(), bookId = computed(() => String(route.params.bookId))
const { book, draft, dirty, busy, error, notice, deleted, mappings, sources, mappingBusy, mappingError, mappingNotice, load, save, remove, addBackupMapping, removeBackupMapping } = useBookEditor(bookId)
const newMappingSourceId = ref(''), newMappingBookUrl = ref('')
async function submitMapping() { if (await addBackupMapping(newMappingSourceId.value, newMappingBookUrl.value)) newMappingBookUrl.value = '' }
async function discard() { if (!dirty.value) return true; try { await ElMessageBox.confirm('放弃尚未保存的书籍修改？', '书籍信息', { confirmButtonText: '放弃修改', cancelButtonText: '继续编辑', type: 'warning' }); return true } catch { return false } }
async function reload() { if (await discard()) await load() }
async function confirmDelete() {
  if (!book.value) return
  try { await ElMessageBox.confirm(`删除《${book.value.name}》及其阅读进度、书签和关联记录？${book.value.book_type === 'local' ? '本地书还将清理服务端文件。' : ''}删除后无法撤销。`, '删除书籍', { confirmButtonText: '确认删除书籍', cancelButtonText: '取消', type: 'warning' }) } catch { return }
  await remove()
}
watch(bookId, load, { immediate: true })
onBeforeRouteLeave(async () => !getSession() || deleted.value ? true : busy.value ? false : await discard())
</script>
<template>
  <main class="page-width"><RouterLink to="/bookshelf" class="muted">← 返回书架</RouterLink><div class="page-heading"><div><span class="eyebrow">整理你的私人书房</span><h1>管理书籍<span class="heading-dot">.</span></h1><p class="muted">编辑书籍信息；阅读进度独立保存。</p></div></div><ElAlert v-if="error" :title="error" type="error" :closable="false" /><ElAlert v-if="notice" :title="notice" type="success" :closable="false" />
    <section v-if="!deleted" class="surface"><div class="section-heading"><h2>书籍信息</h2><ElButton :disabled="busy" @click="reload">重新加载书籍</ElButton></div><ElForm label-position="top" :disabled="busy || !book" @submit.prevent="save"><ElFormItem label="书名"><ElInput v-model="draft.name" aria-label="编辑书名" /></ElFormItem><ElFormItem label="作者"><ElInput v-model="draft.author" aria-label="编辑作者" /></ElFormItem><ElFormItem label="连载状态"><ElSelect v-model="draft.status" aria-label="连载状态"><ElOption label="未知" value="unknown" /><ElOption label="连载中" value="serializing" /><ElOption label="已完结" value="completed" /><ElOption label="暂停" value="paused" /></ElSelect></ElFormItem><ElFormItem label="简介"><ElInput v-model="draft.intro" aria-label="编辑简介" type="textarea" :rows="5" /></ElFormItem><div class="button-row"><ElButton type="primary" native-type="submit" :loading="busy" :disabled="!book">保存书籍信息</ElButton><ElButton type="danger" plain :disabled="busy || !book" @click="confirmDelete">删除书籍</ElButton><span v-if="dirty" class="muted">有未保存修改</span></div></ElForm></section>
    <p v-else><RouterLink to="/bookshelf">返回书架查看</RouterLink></p>
    <section v-if="!deleted && book?.book_type === 'online'" class="surface">
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
  </main>
</template>
<style scoped>h2 { font-size: 18px; font-weight: 500; }.el-select { width: min(320px,100%); }.section-heading { flex-wrap: wrap; }.mapping-list { display: flex; flex-direction: column; gap: 12px; margin-bottom: 16px; }.mapping-row { display: flex; justify-content: space-between; align-items: center; gap: 12px; border-bottom: 1px solid #8882; padding-bottom: 10px; }.mapping-row p { margin: 4px 0 0; font-size: 12px; overflow-wrap: anywhere; }:deep(.el-alert) { margin-bottom: 16px; }
</style>
