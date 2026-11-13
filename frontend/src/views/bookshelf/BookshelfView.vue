<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElButton, ElInput, ElAlert, ElEmpty, ElPagination, ElTabs, ElTabPane, ElSkeleton } from 'element-plus'
import { api } from '@/api/reader'
import { errorText, type Model } from '@/api/client'
const router = useRouter(), books = ref<Model<'BookPage'>>(), search = ref<Model<'SearchResponse'>>()
const page = ref(1), query = ref(''), keyword = ref(''), tab = ref('shelf'), busy = ref(false), searching = ref(false), importing = ref(false), adding = ref(''), error = ref(''), notice = ref('')
let loadSerial = 0
async function load() { const serial = ++loadSerial; busy.value = true; error.value = ''; try { const result = await api.books(page.value, query.value); if (serial === loadSerial) books.value = result } catch (e) { if (serial === loadSerial) error.value = errorText(e) } finally { if (serial === loadSerial) busy.value = false } }
async function importLocal(event: Event) {
  const input = event.target as HTMLInputElement, file = input.files?.[0]; input.value = ''; if (!file) return
  if (file.size > 100 * 1024 * 1024) { error.value = '文件不能超过 100 MiB'; return }
  importing.value = true; error.value = ''; notice.value = ''
  try { const result = await api.importBook(file); notice.value = `《${result.book.name}》已加入书架${result.warnings?.length ? ' · ' + result.warnings.join('；') : ''}`; page.value = 1; query.value = ''; await load() } catch (e) { error.value = errorText(e) } finally { importing.value = false }
}
async function findBooks() { if (!keyword.value.trim()) return; searching.value = true; error.value = ''; search.value = undefined; try { search.value = await api.search(keyword.value.trim()) } catch (e) { error.value = errorText(e) } finally { searching.value = false } }
async function add(book: Model<'SearchBook'>) { adding.value = book.book_url; error.value = ''; try { const added = await api.addBook(book); notice.value = `《${added.name}》已加入书架`; await load() } catch (e) { error.value = errorText(e) } finally { adding.value = '' } }
function read(book: Model<'Book'>) { void router.push(`/reader/${book.id}`) }
onMounted(load)
</script>
<template>
  <main class="library page-width"><div class="page-heading"><div><span class="eyebrow">每一页，都值得停留</span><h1>我的书房<span class="heading-dot">.</span></h1><p class="muted">把想读的故事放在这里，随时继续。</p></div><label class="file-button" :class="{ disabled: importing }">{{ importing ? '正在导入…' : '＋ 导入本地书' }}<input aria-label="导入本地书" type="file" accept=".txt,.epub" :disabled="importing" @change="importLocal"></label></div>
    <ElAlert v-if="error" :title="error" type="error" :closable="false" show-icon /><ElAlert v-if="notice" :title="notice" type="success" closable @close="notice = ''" />
    <ElTabs v-model="tab"><ElTabPane label="我的书架" name="shelf"><form class="search-row" @submit.prevent="page = 1; load()"><ElInput v-model="query" aria-label="筛选书架" placeholder="按书名或作者筛选" clearable /><ElButton native-type="submit" :loading="busy">筛选</ElButton><span class="muted count">{{ books?.total ?? 0 }} 本藏书</span></form>
      <ElSkeleton v-if="busy && !books" :rows="6" animated />
      <ElEmpty v-else-if="!books?.items.length" description="书架还很安静。导入一本书，或去找书吧。" />
      <section v-else class="book-grid" aria-label="书架"><article v-for="book in books.items" :key="book.id" class="book-card"><button class="book-open" @click="read(book)"><span class="book-cover" :class="{ local: book.book_type === 'local' }"><span>{{ book.name.slice(0, 6) }}</span><small>{{ book.book_type === 'local' ? '私人藏书' : '在线阅读' }}</small></span><h2>{{ book.name }}</h2><p>{{ book.author || '佚名' }}</p></button><div class="book-meta"><span v-if="book.needs_source">需要重新选源</span><span v-else-if="book.progress">读至第 {{ book.progress.chapter_index + 1 }} 章</span><span v-else>尚未开始</span><span>{{ book.total_chapters }} 章</span></div><ElButton class="read-button" @click="read(book)">{{ book.progress ? '继续阅读' : '开始阅读' }}</ElButton><ElButton class="cache-button" @click="router.push(`/books/${book.id}/edit`)">管理书籍</ElButton><ElButton v-if="book.book_type === 'online'" class="cache-button" @click="router.push(`/books/${book.id}/cache`)">后台缓存</ElButton></article></section>
      <ElPagination v-if="books && books.total > 20" v-model:current-page="page" :page-size="20" :total="books.total" layout="prev, pager, next" @current-change="load" />
    </ElTabPane><ElTabPane label="发现新书" name="search"><form class="search-row" @submit.prevent="findBooks"><ElInput v-model="keyword" aria-label="搜索书籍" placeholder="输入书名或作者，搜索启用的书源" /><ElButton type="primary" native-type="submit" :loading="searching">搜索</ElButton></form><p class="muted">先在书源页面导入 Legado 书源，再开始搜索。</p><p v-for="failure in search?.failures" :key="failure.source_id" class="error-note">部分书源搜索失败：{{ failure.message }}</p><ElEmpty v-if="search && !search.total" description="没有找到结果，试试其他关键词或书源" /><section v-for="group in search?.groups" :key="group.source_id" class="search-group"><h2>{{ group.source_name }}</h2><article v-for="book in group.items" :key="book.book_url" class="search-result"><div><h3>{{ book.name }}</h3><p class="muted">{{ book.author || '佚名' }} · {{ book.latest_chapter || '暂无更新信息' }}</p><p class="intro">{{ book.intro }}</p></div><ElButton :loading="adding === book.book_url" :disabled="!!adding" @click="add(book)">加入书架</ElButton></article></section></ElTabPane></ElTabs>
  </main>
</template>
<style scoped>.cache-button { width: 100%; margin: 8px 0 0; }</style>
