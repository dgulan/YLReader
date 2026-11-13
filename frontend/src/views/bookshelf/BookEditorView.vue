<script setup lang="ts">
import { computed, watch } from 'vue'
import { useRoute, onBeforeRouteLeave } from 'vue-router'
import { ElAlert, ElButton, ElForm, ElFormItem, ElInput, ElSelect, ElOption, ElMessageBox } from 'element-plus'
import { getSession } from '@/api/client'
import { useBookEditor } from './bookEditor'
const route = useRoute(), bookId = computed(() => String(route.params.bookId))
const { book, draft, dirty, busy, error, notice, deleted, load, save, remove } = useBookEditor(bookId)
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
  </main>
</template>
<style scoped>h2 { font-size: 18px; font-weight: 500; }.el-select { width: min(320px,100%); }.section-heading { flex-wrap: wrap; }</style>
