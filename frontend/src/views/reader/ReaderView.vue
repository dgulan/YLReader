<script setup lang="ts">
import { computed, ref, watch, nextTick, onBeforeUnmount } from 'vue'
import { useRoute, useRouter, onBeforeRouteLeave, onBeforeRouteUpdate } from 'vue-router'
import { api } from '@/api/reader'
import { getSession, errorText, ApiError, type Model } from '@/api/client'
import { db, bookKey, chapterContext, getChapter, putChapter, retainWindow, clearBookCache } from '@/db/chapters'
import { useReaderConfig } from '@/stores/readerConfig'
import VirtualList from '@/components/VirtualList.vue'
import SourceSwitcher from './SourceSwitcher.vue'
import BookmarksPanel from './BookmarksPanel.vue'
import { bookmarkExcerpt } from './bookmarks'
import { visibleOffset, utf16Offset, codePointLength, chapterFraction } from './position'
import { reconcileProgress } from './progress'
const route = useRoute(), router = useRouter(), config = useReaderConfig()
const owner = getSession()!.user.id
const book = ref<Model<'Book'>>(), toc = ref<Model<'ChapterList'>>(), content = ref<Model<'ChapterContent'>>()
const baseProgress = ref<Model<'Progress'>>(), conflict = ref<Model<'Progress'>>()
const index = ref(0), offset = ref(0), dirty = ref(false), loading = ref(true), saving = ref(false), offline = ref(false), error = ref(''), saveError = ref('')
const viewport = ref<HTMLElement>(), body = ref<HTMLElement>(), drawer = ref<HTMLDialogElement>()
const restoring = ref(false)
let generation = 0, revision = 0, timer: ReturnType<typeof setTimeout> | undefined, controller = new AbortController(), saveTask: Promise<boolean> | null = null
const switcher = ref<InstanceType<typeof SourceSwitcher>>()
const bookmarks = ref<InstanceType<typeof BookmarksPanel>>()
async function openBookmarks() {
  clearTimeout(timer); capture(); void flush()
  const at = content.value && !error.value ? { chapter_index: index.value, char_offset: offset.value, chapter_title: title.value, excerpt: bookmarkExcerpt(content.value.content, offset.value) } : undefined
  await bookmarks.value?.open(at)
}
async function visitBookmark(mark: Model<'Bookmark'>) { await changeChapter(mark.chapter_index, mark.char_offset) }
async function openSwitcher() { clearTimeout(timer); capture(); if (!await flush()) { saveError.value = '请先同步阅读进度或解决冲突，再切换书源'; return }; await switcher.value?.open() }
async function switched() { clearTimeout(timer); controller.abort(); let cacheFailed = false; try { await clearBookCache(owner, bookId.value) } catch { cacheFailed = true }; await boot(); if (cacheFailed) saveError.value = '已切换书源，但本机缓存清理失败，请保持联网' }
const bookId = computed(() => String(route.params.bookId))
const entries = computed(() => (toc.value?.items ?? []).map(ch => ({ id: ch.is_volume ? `v:${ch.id}` : ch.chapter_index!, label: ch.title, disabled: ch.is_volume })))
const title = computed(() => content.value?.chapter.title ?? '正在打开…')
const status = computed(() => conflict.value ? '进度有冲突' : saving.value ? '正在保存…' : dirty.value ? '进度保存在本机，等待同步' : '进度已同步')
async function persistLocal() {
  if (baseProgress.value) await db.positions.put({ key: bookKey(owner, bookId.value), owner, base: { ...baseProgress.value }, index: index.value, offset: offset.value, dirty: dirty.value })
}
function capture() {
  if (loading.value || restoring.value || !body.value || !viewport.value) return
  const node = body.value.firstChild
  if (!(node instanceof Text)) return
  const next = visibleOffset(node, viewport.value)
  if (offset.value !== next) { offset.value = next; dirty.value = true; revision++ }
}
function scroll() { if (restoring.value || loading.value) return; clearTimeout(timer); timer = setTimeout(() => { capture(); void flush() }, 600) }
function conflictProgress(e: ApiError) {
  const data = e.data as { error?: { details?: { current?: Model<'Progress'> } } } | null
  const current = data?.error?.details?.current
  return current && current.version > 0 ? current : undefined
}
async function flush(): Promise<boolean> {
  if (saveTask) { await saveTask; return dirty.value && !conflict.value ? flush() : !dirty.value }
  if (getSession()?.user.id !== owner || !baseProgress.value || !book.value) return true
  saveTask = (async () => {
    try { await persistLocal() } catch { saveError.value = '本机缓存不可用，请保持页面打开并手动保存' }
    if (conflict.value) return false
    while (dirty.value) {
      const sentRevision = revision, sentIndex = index.value, sentOffset = offset.value
      saving.value = true
      try {
        const chapter = toc.value?.items.find(c => c.chapter_index === sentIndex)
        const next = await api.saveProgress(bookId.value, { chapter_index: sentIndex, char_offset: sentOffset, chapter_title: chapter?.title ?? '', normalized_title: chapter?.normalized_title ?? '', chapter_ratio: chapterFraction(content.value?.content ?? '', sentOffset), client_updated_at: new Date().toISOString() }, baseProgress.value!.version)
        baseProgress.value = next; dirty.value = revision !== sentRevision; saveError.value = ''; offline.value = false
        await persistLocal()
      } catch (e) {
        if (e instanceof ApiError && e.status === 409) conflict.value = conflictProgress(e)
        saveError.value = e instanceof TypeError ? '当前离线，阅读位置已保存在本机' : errorText(e)
        return false
      } finally { saving.value = false }
    }
    return true
  })().finally(() => { saveTask = null })
  return saveTask
}
async function restorePosition() {
  await nextTick()
  const node = body.value?.firstChild
  if (!(node instanceof Text) || !viewport.value) return
  const range = document.createRange(); range.setStart(node, utf16Offset(node.data, offset.value)); range.collapse(true)
  viewport.value.scrollTop = 0
  if (offset.value) viewport.value.scrollTop += range.getBoundingClientRect().top - viewport.value.getBoundingClientRect().top - 24
}
async function changeAppearance(action: () => void) {
  if (loading.value || restoring.value || config.loading) return
  clearTimeout(timer); capture(); restoring.value = true
  try { action(); await restorePosition(); await new Promise<void>(resolve => requestAnimationFrame(() => resolve())) }
  finally { restoring.value = false }
  void flush()
}
async function fetchChapter(i: number, signal: AbortSignal) {
  const context = chapterContext(book.value!)
  try {
    const result = await api.content(bookId.value, i, signal)
    if (signal.aborted) throw new DOMException('Aborted', 'AbortError')
    await putChapter(owner, bookId.value, i, context, result).catch(() => { saveError.value = '章节缓存不可用，离线阅读暂不可用' })
    return { result, cached: false }
  } catch (e) {
    if (!(e instanceof TypeError)) throw e
    const cached = await getChapter(owner, bookId.value, i, context)
    if (!cached) throw new Error('当前章节未缓存，请联网后重试')
    return { result: cached, cached: true }
  }
}
async function openChapter(i: number, at = 0) {
  if (!book.value) return
  controller.abort(); controller = new AbortController(); const signal = controller.signal, serial = ++generation
  loading.value = true; restoring.value = true; error.value = ''
  try {
    const loaded = await fetchChapter(i, signal)
    if (serial !== generation) return
    content.value = loaded.result; index.value = i; offset.value = Math.min(Math.max(0, at), codePointLength(loaded.result.content)); offline.value = loaded.cached
    loading.value = false; await restorePosition()
    await new Promise<void>(resolve => requestAnimationFrame(() => resolve())); restoring.value = false
    if (!loaded.cached) {
      const neighbors = [i - 2, i - 1, i + 1, i + 2].filter(n => n >= 0 && n < book.value!.total_chapters)
      void Promise.allSettled(neighbors.map(n => fetchChapter(n, signal))).then(() => { if (!signal.aborted) return retainWindow(owner, bookId.value, i) }).catch(() => undefined)
    }
  } catch (e) { if (serial === generation && !(e instanceof DOMException && e.name === 'AbortError')) error.value = errorText(e) }
  finally { if (serial === generation) { loading.value = false; restoring.value = false } }
}
async function boot() {
  loading.value = true; content.value = undefined; error.value = ''; conflict.value = undefined; dirty.value = false; saveError.value = ''
  try {
    await config.initialize(owner)
    const local = await db.positions.get(bookKey(owner, bookId.value)).catch(() => undefined)
    let current: Model<'Progress'>, liveBook: Model<'Book'> | undefined
    try {
      const b = await api.book(bookId.value); liveBook = b; book.value = b
      const [t, p] = await Promise.all([api.chapters(bookId.value), api.progress(bookId.value)])
      b.total_chapters = t.total; book.value = b; toc.value = t; current = p
      await db.books.put({ key: bookKey(owner, bookId.value), owner, book: b, toc: t }).catch(() => undefined)
    } catch (e) {
      if (!(e instanceof TypeError)) throw e
      const cached = await db.books.get(bookKey(owner, bookId.value))
      if (!cached || !local) throw new Error('尚无离线记录，请联网打开这本书')
      if (liveBook && chapterContext(liveBook) !== chapterContext(cached.book)) throw new Error('书源或书籍已变化，尚无新版本缓存，请联网重试')
      book.value = cached.book; toc.value = cached.toc; current = local.base; offline.value = true
    }
    if (!toc.value.total) throw new Error('目录中没有可读章节，请检查书源或导入内容')
    const restored = reconcileProgress(current, local)
    baseProgress.value = restored.base; dirty.value = restored.dirty; conflict.value = restored.conflict
    await openChapter(Math.min(restored.index, Math.max(0, book.value!.total_chapters - 1)), restored.offset)
    if (!restored.dirty && !offline.value && current.char_offset === 0 && current.chapter_ratio > 0) {
      const adjusted = await api.progress(bookId.value)
      if (adjusted.version !== baseProgress.value.version) { baseProgress.value = adjusted; await openChapter(Math.min(adjusted.chapter_index, Math.max(0, book.value!.total_chapters - 1)), adjusted.char_offset) }
    }
    await persistLocal().catch(() => { saveError.value = '本机缓存不可用' }); if (dirty.value && !conflict.value) void flush()
  } catch (e) { error.value = errorText(e); loading.value = false }
}
async function changeChapter(i: string | number, at = 0) {
  drawer.value?.close(); capture(); await flush(); if (conflict.value) return
  const target = Number(i); if (!Number.isInteger(target) || target < 0 || target >= (book.value?.total_chapters ?? 0)) return
  await openChapter(target, at); if (error.value) return
  dirty.value = true; revision++; await flush()
}
async function keepMine() { if (!conflict.value) return; baseProgress.value = conflict.value; conflict.value = undefined; dirty.value = true; revision++; await flush() }
async function useRemote() { if (!conflict.value) return; const remote = conflict.value; conflict.value = undefined; baseProgress.value = remote; dirty.value = false; await openChapter(remote.chapter_index, remote.char_offset); await persistLocal() }
async function leave() { clearTimeout(timer); capture(); await flush(); controller.abort() }
function online() { void flush() }
window.addEventListener('online', online)
onBeforeRouteLeave(leave)
onBeforeRouteUpdate(leave)
watch(bookId, boot, { immediate: true })
onBeforeUnmount(() => { clearTimeout(timer); controller.abort(); window.removeEventListener('online', online) })
</script>
<template>
  <main class="reader" :data-theme="config.theme" :style="{ '--reader-size': config.fontSize + 'px' }">
    <header class="reader-header"><RouterLink to="/bookshelf" aria-label="返回书架">← 书架</RouterLink><span>{{ book?.name ?? '阅读器' }}</span><button v-if="book?.book_type === 'online'" :disabled="loading || saving" @click="openSwitcher">换源</button><button :disabled="loading || restoring || !book" @click="openBookmarks">书签</button><button @click="drawer?.showModal()" :disabled="!toc">目录</button></header>
    <div v-if="conflict" class="reader-conflict" role="alert"><p>另一端已更新至第 {{ conflict.chapter_index + 1 }} 章。请选择要保留的位置。</p><button @click="keepMine">保留本机位置</button><button @click="useRemote">使用另一端位置</button></div>
    <p v-if="saveError" class="reader-notice" role="status">{{ saveError }}</p>
    <p v-if="config.error" class="reader-notice" role="status">{{ config.error }} <button :disabled="config.loading || config.saving" @click="config.retry">重试设置同步</button></p>
    <section ref="viewport" class="reader-viewport" :aria-busy="loading || restoring" aria-label="章节阅读区" tabindex="0" @scroll="scroll">
      <div v-if="loading" class="reader-state" role="status">正在翻开这一页…</div><div v-else-if="error" class="reader-state" role="alert"><p>{{ error }}</p><button @click="boot">重新加载</button><RouterLink to="/bookshelf">返回书架</RouterLink></div>
      <article v-show="!loading && !error" class="reader-article"><span class="reader-kicker">{{ offline ? '离线缓存' : '正文' }} · 第 {{ index + 1 }} 章</span><h1>{{ title }}</h1><p ref="body" class="chapter-body">{{ content?.content }}</p><p class="chapter-end">本章完</p></article>
    </section>
    <footer class="reader-footer"><div class="reader-options"><label>主题<select :value="config.theme" :disabled="config.loading || loading || restoring" aria-label="阅读主题" @change="changeAppearance(() => config.setTheme(($event.target as HTMLSelectElement).value))"><option value="day">日间</option><option value="night">夜间</option><option value="paper">羊皮纸</option><option value="green">护眼</option></select></label><button aria-label="缩小字号" :disabled="config.loading || loading || restoring || config.fontSize <= 14" @click="changeAppearance(() => config.changeFont(-1))">A−</button><button aria-label="放大字号" :disabled="config.loading || loading || restoring || config.fontSize >= 32" @click="changeAppearance(() => config.changeFont(1))">A＋</button></div><div class="reader-nav"><button :disabled="loading || restoring || !content || content.prev_chapter_index == null" @click="changeChapter(content!.prev_chapter_index!)">上一章</button><button :disabled="loading || restoring || !content || content.next_chapter_index == null" @click="changeChapter(content!.next_chapter_index!)">下一章</button><button :disabled="loading || restoring || saving" @click="capture(); flush()">保存进度</button></div><span class="reader-status" role="status">{{ status }}</span></footer>
    <SourceSwitcher ref="switcher" :book-id="bookId" :active-source="book?.active_source_id" @switched="switched" />
    <BookmarksPanel ref="bookmarks" :book-id="bookId" :total="book?.total_chapters ?? 0" @navigate="visitBookmark" />
    <dialog ref="drawer" class="toc-dialog" aria-labelledby="toc-title"><div class="toc-heading"><h2 id="toc-title">章节目录</h2><button @click="drawer?.close()">关闭</button></div><VirtualList :items="entries" :selected="index" @select="changeChapter" /></dialog>
  </main>
</template>
<style scoped>
.reader { --reader-bg: #f2ead7; --reader-ink: #423c30; --reader-muted: #8b816d; background: var(--reader-bg); color: var(--reader-ink); height: 100dvh; display: flex; flex-direction: column; overflow: hidden; }
.reader[data-theme=day] { --reader-bg: #fbfbf7; --reader-ink: #303930; --reader-muted: #7c897c; }.reader[data-theme=night] { --reader-bg: #1d2422; --reader-ink: #c6cfc7; --reader-muted: #849187; }.reader[data-theme=green] { --reader-bg: #dce8d8; --reader-ink: #334831; --reader-muted: #778d72; }
.reader button,.reader select { background: transparent; color: inherit; border: 1px solid #8884; border-radius: 5px; padding: 7px 12px; cursor: pointer; font-size: 12px; }.reader button:disabled { opacity: .35; cursor: default; }.reader-header { display: flex; justify-content: space-between; align-items: center; gap: 20px; height: 64px; flex-shrink: 0; padding: 0 32px; border-bottom: 1px solid #8882; font-size: 13px; }.reader-header button,.reader-header a { flex-shrink: 0; white-space: nowrap; }.reader-header span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }.reader-viewport { overflow-y: auto; flex: 1; min-height: 0; overscroll-behavior: contain; scroll-behavior: auto; }.reader-article { width: min(100%, 800px); padding: 54px 42px 70px; margin: auto; }.reader-kicker { color: var(--reader-muted); font-size: 11px; letter-spacing: 2px; }.reader h1 { font-size: 29px; line-height: 1.5; margin: 20px 0 42px; }.chapter-body { font-family: 'Noto Serif SC','Songti SC',SimSun,serif; font-size: var(--reader-size); line-height: 2; white-space: pre-wrap; overflow-wrap: anywhere; word-break: normal; margin: 0; }.chapter-end { text-align: center; font-size: 12px; color: var(--reader-muted); padding: 40px; }.reader-footer { display: flex; flex-wrap: wrap; align-items: center; gap: 16px; padding: 14px 28px; border-top: 1px solid #8882; flex-shrink: 0; font-size: 10px; }.reader-options,.reader-nav { display: flex; align-items: center; gap: 8px; }.reader-options label { display: flex; align-items: center; gap: 8px; }.reader-nav { margin: auto; }.reader-status { color: var(--reader-muted); }.reader-state { text-align: center; padding: 90px 24px; }.reader-state a { display: block; margin: 20px; }.reader-notice,.reader-conflict { padding: 12px 24px; margin: 0; font-size: 12px; background: #b9842720; }.reader-conflict p { margin: 0 0 8px; }.reader-conflict button { margin-right: 12px; }.toc-dialog { width: min(520px,95vw); border: 1px solid #8884; border-radius: 10px; color: var(--reader-ink); background: var(--reader-bg); padding: 18px; }.toc-dialog::backdrop { background: #10282077; }.toc-heading { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }.toc-heading h2 { font-size: 18px; font-weight: 500; }
@media(max-width:650px) { .reader-header { height: 54px; padding: 0 18px; }.reader-article { padding: 32px 22px 50px; }.reader-footer { padding: 10px 14px max(10px,env(safe-area-inset-bottom)); gap: 10px; }.reader-options { margin: auto; }.reader-nav { margin: auto; }.reader-status { width: 100%; text-align: center; }.reader h1 { font-size: 25px; margin-bottom: 28px; } }
</style>
