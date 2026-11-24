<script setup lang="ts">
import 'element-plus/dist/index.css'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElButton, ElInput, ElAlert, ElEmpty, ElPagination, ElTabs, ElTabPane, ElSkeleton, ElMessageBox } from 'element-plus'
import { api } from '@/api/reader'
import { errorText, getSession, type Model } from '@/api/client'
import BookCover from './BookCover.vue'
import { invalidateBookCover } from './coverCache'
import { bookKey, clearBookCache, db } from '@/db/chapters'
import { groupSearchResults } from './searchResults'
import { detectHorizontalSwipe, type TouchPoint } from '@/views/reader/touchNavigation'
const router = useRouter(), books = ref<Model<'BookPage'>>(), search = ref<Model<'SearchResponse'>>()
const page = ref(1), query = ref(''), keyword = ref(''), tab = ref('shelf'), busy = ref(false), searching = ref(false), importing = ref(false), adding = ref(''), error = ref(''), notice = ref('')
const addedUrls = ref<Record<string, boolean>>({})
const partialGroups = ref<Model<'SearchGroup'>[]>([])
const selectedVariants = ref<Record<string, string>>({})
const results = computed(() => groupSearchResults(search.value?.groups ?? partialGroups.value).map(result => ({
  ...result,
  current: result.variants.find(variant => variant.key === selectedVariants.value[result.key]) ?? result.variants[0]!,
})))
const removing = ref(false)
function readingChapter(book: Model<'Book'>) {
  return book.progress ? Math.min(book.progress.chapter_index + 1, book.total_chapters || Infinity) : 0
}
async function remove(book: Model<'Book'>) {
  if (removing.value) return
  const owner = getSession()?.user.id
  if (!owner) return
  removing.value = true
  error.value = ''; notice.value = ''
  try {
    try {
      await ElMessageBox.confirm('将从书架移除《' + book.name + '》及其阅读记录。', '删除书籍', { type: 'warning', confirmButtonText: '确认删除', cancelButtonText: '取消' })
    } catch { return }
    if (getSession()?.user.id !== owner) return
    await api.deleteBook(book.id)
    invalidateBookCover(owner, book.id)
    let cacheFailed = false
    try { await clearBookCache(owner, book.id); await db.jobs.delete(bookKey(owner, book.id)) }
    catch { cacheFailed = true }
    if (disposed || getSession()?.user.id !== owner) return
    if (books.value?.items.length === 1 && page.value > 1) page.value--
    if (book.active_book_url) delete addedUrls.value[book.active_book_url]
    notice.value = cacheFailed ? '书籍已删除，本机缓存清理失败' : '书籍已删除'
    await load()
  } catch (e) { if (!disposed && getSession()?.user.id === owner) error.value = errorText(e) }
  finally { removing.value = false }
}
function closeCardMenu(event: FocusEvent) {
  const menu = event.currentTarget as HTMLDetailsElement
  if (!(event.relatedTarget instanceof Node) || !menu.contains(event.relatedTarget)) menu.open = false
}
let touchStart: TouchPoint | null = null
function onTouchStart(event: TouchEvent) {
  if (event.touches.length !== 1) { touchStart = null; return }
  const t = event.touches[0]!
  touchStart = { x: t.clientX, y: t.clientY, time: Date.now() }
}
function onTouchEnd(event: TouchEvent) {
  if (!touchStart || event.changedTouches.length !== 1) { touchStart = null; return }
  const t = event.changedTouches[0]!
  const hasSelection = Boolean(window.getSelection?.()?.toString().trim())
  const dir = detectHorizontalSwipe(touchStart, { x: t.clientX, y: t.clientY, time: Date.now() }, hasSelection)
  touchStart = null
  if (dir === 'next' && tab.value === 'shelf') tab.value = 'search'
  else if (dir === 'prev' && tab.value === 'search') tab.value = 'shelf'
}
watch(query, (next, prev) => {
  if (prev.trim() !== '' && next.trim() === '') {
    page.value = 1
    void load()
  }
})
function isBookInShelf(item: Model<'SearchBook'>): boolean {
  if (addedUrls.value[item.book_url]) return true
  const list = books.value?.items ?? []
  return list.some(b => b.active_book_url === item.book_url || (b.name.trim() === item.name.trim() && (b.author || '').trim() === (item.author || '').trim()))
}
let loadSerial = 0
let loadController: AbortController | undefined
let disposed = false, pageHidden = false
function cancelLoad() { ++loadSerial; loadController?.abort(); loadController = undefined; busy.value = false; window.removeEventListener('beforeunload', cancelLoad) }
async function load() {
  if (disposed || pageHidden) return
  cancelLoad()
  const serial = ++loadSerial, controller = new AbortController()
  loadController = controller; busy.value = true; error.value = ''
  // Only pending GETs need this early teardown hook; never prompt or block leaving.
  window.addEventListener('beforeunload', cancelLoad)
  try { const result = await api.books(page.value, query.value, controller.signal); if (serial === loadSerial && !controller.signal.aborted) books.value = result }
  catch (e) { if (serial === loadSerial && !controller.signal.aborted) error.value = errorText(e) }
  finally { if (serial === loadSerial) { busy.value = false; loadController = undefined; window.removeEventListener('beforeunload', cancelLoad) } }
}
function hidePage() { pageHidden = true; cancelLoad(); cancelSearch() }
function showPage(event: PageTransitionEvent) { pageHidden = false; if (event.persisted) void load() }
async function importLocal(event: Event) {
  const input = event.target as HTMLInputElement, file = input.files?.[0]; input.value = ''; if (!file) return
  if (file.size > 100 * 1024 * 1024) { error.value = '文件不能超过 100 MiB'; return }
  importing.value = true; error.value = ''; notice.value = ''
  try { const result = await api.importBook(file); notice.value = `《${result.book.name}》已加入书架${result.warnings?.length ? ' · ' + result.warnings.join('；') : ''}`; page.value = 1; query.value = ''; await load() } catch (e) { error.value = errorText(e) } finally { importing.value = false }
}
let searchController: AbortController | undefined
function cancelSearch() {
  searchController?.abort()
  searchController = undefined
  searching.value = false
}
async function findBooks() {
  const term = keyword.value.trim()
  if (!term || disposed || pageHidden) return
  cancelSearch()
  const controller = new AbortController()
  searchController = controller
  searching.value = true; error.value = ''; search.value = undefined
  partialGroups.value = []; selectedVariants.value = {}
  try {
    const result = await api.search(term, controller.signal, group => {
      if (searchController === controller) partialGroups.value.push(group)
    })
    if (searchController === controller) search.value = result
  } catch (e) {
    if (searchController === controller) error.value = errorText(e)
  } finally {
    if (searchController === controller) cancelSearch()
  }
}

async function add(book: Model<'SearchBook'>) { adding.value = book.book_url; error.value = ''; try { const added = await api.addBook(book); addedUrls.value[book.book_url] = true; notice.value = `《${added.name}》已加入书架`; await load() } catch (e) { error.value = errorText(e) } finally { adding.value = '' } }
function read(book: Model<'Book'>) { void router.push(`/reader/${book.id}`) }
onMounted(() => { window.addEventListener('pagehide', hidePage); window.addEventListener('pageshow', showPage); void load() })
onBeforeUnmount(() => { disposed = true; cancelLoad(); cancelSearch(); window.removeEventListener('pagehide', hidePage); window.removeEventListener('pageshow', showPage) })
</script>
<template>
  <main class="library page-width" @touchstart.passive="onTouchStart" @touchend.passive="onTouchEnd"><div class="page-heading"><div><h1>我的书房<span class="heading-dot">.</span></h1><p class="muted">把想读的故事放在这里，随时继续。</p></div><label class="file-button" :class="{ disabled: importing }">{{ importing ? '正在导入…' : '＋ 导入本地书' }}<input aria-label="导入本地书" type="file" accept=".txt,.epub" :disabled="importing" @change="importLocal"></label></div>
    <ElAlert v-if="error" :title="error" type="error" :closable="false" show-icon /><ElAlert v-if="notice" :title="notice" type="success" closable @close="notice = ''" />
    <ElTabs v-model="tab"><ElTabPane label="我的书架" name="shelf"><form class="search-row" @submit.prevent="page = 1; load()"><ElInput v-model="query" aria-label="筛选书架" placeholder="按书名或作者筛选" clearable @clear="page = 1; load()" /><ElButton native-type="submit" :loading="busy">筛选</ElButton><span class="muted count">{{ books?.total ?? 0 }} 本藏书</span></form>
      <ElSkeleton v-if="busy && !books" :rows="6" animated />
      <ElEmpty v-else-if="!books?.items.length" description="书架还很安静。导入一本书，或去找书吧。" />
      <section v-else class="book-grid" aria-label="书架">
        <article v-for="book in books.items" :key="book.id" class="book-card">
          <div class="cover-frame">
            <button type="button" class="book-open" :aria-label="book.progress ? '继续阅读' : '开始阅读'" :aria-description="book.name" @click="read(book)">
              <BookCover :title="book.name" :book-id="book.id" :base="book.active_book_url" :url="book.custom_cover_url || book.cover_url" :local="book.book_type === 'local'" />
            </button>
            <details name="shelf-book-actions" class="book-menu" @focusout="closeCardMenu" @keydown.esc.stop.prevent="($event.currentTarget as HTMLDetailsElement).querySelector('summary')?.focus(); ($event.currentTarget as HTMLDetailsElement).open = false">
              <summary :aria-label="'书籍操作 ' + book.name">⋯</summary>
              <div class="book-menu-items">
                <button type="button" @click="router.push('/books/' + book.id + '/edit')">管理书籍</button>
                <button v-if="book.book_type === 'online'" type="button" @click="router.push('/books/' + book.id + '/cache')">后台缓存</button>
                <button type="button" :disabled="removing" @click="remove(book)">删除书籍</button>
              </div>
            </details>
          </div>
          <h2><RouterLink :to="'/reader/' + book.id">{{ book.name }}</RouterLink></h2>
          <p class="book-author">{{ book.author || '佚名' }}</p>
          <div class="book-meta">
            <span v-if="book.needs_source">需要重新选源</span>
            <span v-else-if="book.progress">第 {{ readingChapter(book) }} 章 / 共 {{ book.total_chapters }} 章</span>
            <span v-else>共 {{ book.total_chapters }} 章</span>
          </div>
          <progress v-if="book.total_chapters" class="shelf-progress" :max="book.total_chapters" :value="readingChapter(book)" :aria-label="book.name + ' 的阅读进度'" />
        </article>
      </section>
      <ElPagination v-if="books && books.total > 20" v-model:current-page="page" :page-size="20" :total="books.total" layout="prev, pager, next" @current-change="load" />
    </ElTabPane>
    <ElTabPane label="发现新书" name="search">
      <form class="search-row" @submit.prevent="findBooks">
        <ElInput v-model="keyword" aria-label="搜索书籍" placeholder="输入书名或作者" clearable />
        <ElButton type="primary" native-type="submit" :loading="searching">搜索</ElButton>
      </form>
      <p v-if="searching" class="muted" role="status">{{ results.length ? '已找到 ' + results.length + ' 本，继续搜索其他书源…' : '正在搜索书源…' }}</p>
      <details v-if="search?.failures?.length" class="failure-details" :open="search.failures.length <= 2">
        <summary class="error-note">{{ search.failures.length }} 个书源未返回结果（点击展开详情）</summary>
        <p v-for="failure in search.failures" :key="failure.source_id" class="error-note">部分书源搜索失败：{{ failure.message }}</p>
      </details>
      <ElEmpty v-if="search && !search.total" description="没有找到结果，试试其他关键词或书源" />
      <section v-if="results.length" class="search-group" aria-label="搜书结果">
        <article v-for="result in results" :key="result.key" class="search-result">
          <BookCover class="search-cover" :title="result.current.book.name" :base="result.current.book.book_url" :url="result.current.book.cover_url" />
          <div class="search-copy">
            <h3>{{ result.current.book.name }}</h3>
            <p class="muted">{{ result.current.book.author || '佚名' }} · {{ result.current.book.latest_chapter || '暂无更新信息' }}</p>
            <p v-if="result.current.book.intro" class="intro">{{ result.current.book.intro }}</p>
            <select v-if="result.variants.length > 1" class="variant-picker" :value="result.current.key" :aria-label="result.current.book.name + ' 的书源'" @change="selectedVariants[result.key] = ($event.target as HTMLSelectElement).value">
              <option v-for="variant in result.variants" :key="variant.key" :value="variant.key">{{ variant.source }} · {{ result.variants.length }} 个匹配</option>
            </select>
            <small v-else class="muted">{{ result.current.source }}</small>
          </div>
          <ElButton :loading="adding === result.current.book.book_url" :disabled="!!adding || isBookInShelf(result.current.book)" @click="add(result.current.book)">{{ isBookInShelf(result.current.book) ? '已放入书架' : '加入书架' }}</ElButton>
        </article>
      </section>
    </ElTabPane></ElTabs>
  </main>
</template>
<style scoped>
.library { padding-top: 18px; }
.page-heading { align-items: center; flex-wrap: nowrap; margin: 0 0 8px; gap: 10px; }
.page-heading h1 { font-size: clamp(24px, 4vw, 32px); line-height: 1.15; margin: 0 0 3px; }
.page-heading p { margin: 0; line-height: 1.35; }
.page-heading .file-button { margin-top: 0; }
.book-grid { grid-template-columns: repeat(auto-fill, minmax(min(100%, 142px), 1fr)); gap: 12px; }
.book-card { padding: 7px; }
.cover-frame { position: relative; }
.book-open { display: block; }
.book-open > .book-cover { height: auto; aspect-ratio: 3 / 4; padding: 0; position: relative; justify-content: center; gap: 4px; }
.book-open > .book-cover :deep(.book-cover-img) { position: absolute; inset: 0; width: 100%; height: 100%; object-fit: cover; }
.book-open > .book-cover :deep(span) { max-height: 68%; font-size: clamp(15px, 3vw, 23px); letter-spacing: 3px; }
.book-card h2, .book-author { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.book-card h2 { margin: 6px 0 2px; font-size: 14px; }
.book-meta { margin: 4px 0; font-size: 11px; color: #657569; }
.shelf-progress { display: block; height: 3px; width: 100%; accent-color: var(--accent, #315d4d); }
.book-menu { position: absolute; right: 4px; bottom: 4px; }
.book-menu summary { display: grid; place-items: center; width: 36px; height: 36px; border-radius: 50%; background: #223e34ee; color: white; cursor: pointer; font-size: 20px; }
.book-menu summary::-webkit-details-marker { display: none; }
.book-menu-items { position: absolute; right: 0; top: 100%; z-index: 2; display: grid; min-width: max-content; padding: 4px; border: 1px solid #dce1d8; border-radius: 6px; background: #fcfbf8; box-shadow: 0 3px 14px #17302025; }
.book-menu-items button { border: 0; background: transparent; text-align: left; padding: 8px 12px; cursor: pointer; color: inherit; }
.book-menu-items button:is(:hover, :focus-visible) { background: #edf1ec; }
.search-cover { width: 50px; height: 70px; flex: 0 0 50px; padding: 0; justify-content: center; }
.search-cover :deep(span) { font-size: 12px; text-align: center; }
.search-copy { min-width: 0; flex: 1; overflow-wrap: anywhere; }
.search-copy p { margin: 3px 0; }
.variant-picker { max-width: 100%; font-size: 16px; }
.search-row { margin: 8px 0 14px; }
.search-row :deep(.el-input) { flex: 1 1 auto; min-width: 0; width: 100%; max-width: 430px; }
.search-row :deep(.el-input__wrapper) { border-radius: 999px; }
.search-row :deep(.el-input__inner) { font-size: 16px; }
.search-row :deep(.el-button), .search-result :deep(.el-button) { flex-shrink: 0; }
:deep(.el-alert) { margin-bottom: 16px; }
@media (max-width: 650px) {
  .library { padding-top: 12px; }
  .book-grid { grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 8px; }
  .book-card { padding: 5px; }
  .search-result { gap: 8px; }
  .search-row :deep(.el-input) { flex: 1 1 0%; max-width: 100%; }
}
</style>
