<script setup lang="ts">
import '@/reader-overlays.css'
import { computed, ref, watch, nextTick, onBeforeUnmount } from 'vue'
import { useRoute, useRouter, onBeforeRouteLeave, onBeforeRouteUpdate } from 'vue-router'
import { api } from '@/api/reader'
import { getSession, errorText, ApiError, type Model } from '@/api/client'
import { db, bookKey, chapterContext, getChapter, putChapter, deleteChapter, retainWindow, clearBookCache } from '@/db/chapters'
import { useReaderConfig } from '@/stores/readerConfig'
import VirtualList from '@/components/VirtualList.vue'
import SourceSwitcher from './SourceSwitcher.vue'
import BookmarksPanel from './BookmarksPanel.vue'
import ChapterOverrideDialog from './ChapterOverrideDialog.vue'
import { bookmarkExcerpt } from './bookmarks'
import { visibleOffset, utf16Offset, codePointLength, chapterFraction } from './position'
import { reconcileProgress, remoteProgressState } from './progress'
import { loadChapter, preloadChapters, type ChapterRequest } from './chapterLoading'
import { loadBookBootstrap } from './bookBootstrap'
import { singleFlight, watchPageCheckpoint } from './checkpoint'
import { withDeadline, RequestDeadlineError } from './deadline'
import { detectHorizontalSwipe, type TouchPoint } from './touchNavigation'
const route = useRoute(), router = useRouter(), config = useReaderConfig()
const owner = getSession()?.user.id ?? ''
const book = ref<Model<'Book'>>(), toc = ref<Model<'ChapterList'>>(), content = ref<Model<'ChapterContent'>>()
const baseProgress = ref<Model<'Progress'>>(), conflict = ref<Model<'Progress'>>()
const index = ref(0), offset = ref(0), dirty = ref(false), loading = ref(true), saving = ref(false), offline = ref(false), error = ref(''), saveError = ref('')
const viewport = ref<HTMLElement>(), body = ref<HTMLElement>(), drawer = ref<HTMLDialogElement>()
const restoring = ref(false)
const immersive = ref(false)
const localFault = ref(false)
let touchStart: TouchPoint | null = null
function onTouchStart(event: TouchEvent) {
  if (event.touches.length !== 1) { touchStart = null; return }
  const t = event.touches[0]!
  touchStart = { x: t.clientX, y: t.clientY, time: Date.now() }
}
function onTouchEnd(event: TouchEvent) {
  if (!touchStart || event.changedTouches.length !== 1 || loading.value || restoring.value || conflict.value) { touchStart = null; return }
  const t = event.changedTouches[0]!
  const hasSelection = Boolean(window.getSelection?.()?.toString().trim())
  const dir = detectHorizontalSwipe(touchStart, { x: t.clientX, y: t.clientY, time: Date.now() }, hasSelection)
  touchStart = null
  if (dir === 'next' && content.value?.next_chapter_index != null) void changeChapter(content.value.next_chapter_index)
  else if (dir === 'prev' && content.value?.prev_chapter_index != null) void changeChapter(content.value.prev_chapter_index)
}
const uncertain = ref(false), reviewing = ref(false)
const directory = ref<InstanceType<typeof VirtualList>>()
const bookmarkTrigger = ref<HTMLButtonElement>(), directoryTrigger = ref<HTMLButtonElement>()
let lastDirectoryTrigger: HTMLElement | null = null
function openDirectory() {
  const active = document.activeElement instanceof HTMLElement && document.activeElement !== document.body ? document.activeElement : null
  lastDirectoryTrigger = directoryTrigger.value ?? active
  drawer.value?.showModal(); directory.value?.revealSelected()
}
function onDirectoryClose() {
  const target = lastDirectoryTrigger
  lastDirectoryTrigger = null
  if (target && target.isConnected && !target.hasAttribute('disabled')) target.focus()
}
let generation = 0, revision = 0, saveEpoch = 0, disposed = false, timer: ReturnType<typeof setTimeout> | undefined, controller = new AbortController()
let bookController = new AbortController()
const switcher = ref<InstanceType<typeof SourceSwitcher>>()
const bookmarks = ref<InstanceType<typeof BookmarksPanel>>()
const overrideDialog = ref<InstanceType<typeof ChapterOverrideDialog>>()
async function openBookmarks() {
  clearTimeout(timer); capture(); void flush()
  const at = content.value && !error.value ? { chapter_index: index.value, char_offset: offset.value, chapter_title: title.value, excerpt: bookmarkExcerpt(content.value.content, offset.value) } : undefined
  await bookmarks.value?.open(at, bookmarkTrigger.value)
}
async function visitBookmark(mark: Model<'Bookmark'>) { await changeChapter(mark.chapter_index, mark.char_offset) }
async function openOverride() { clearTimeout(timer); capture(); void flush(); await overrideDialog.value?.open() }
async function overrideUpdated(targetIndex: number) {
  try { await deleteChapter(owner, bookId.value, targetIndex) } catch { /* Best-effort cache invalidation. */ }
  if (index.value === targetIndex) await openChapter(targetIndex, offset.value)
}
async function openSwitcher() { clearTimeout(timer); capture(); if (!await flush()) { saveError.value = '请先同步阅读进度或解决冲突，再切换书源'; return }; await switcher.value?.open() }
async function switched() { clearTimeout(timer); controller.abort(); let cacheFailed = false; try { await clearBookCache(owner, bookId.value) } catch { cacheFailed = true }; await boot(); if (cacheFailed) saveError.value = '已切换书源，但本机缓存清理失败，请保持联网' }
const bookId = computed(() => String(route.params.bookId))
const entries = computed(() => (toc.value?.items ?? []).map(ch => ({ id: ch.is_volume ? `v:${ch.id}` : ch.chapter_index!, label: ch.title, disabled: ch.is_volume })))
const title = computed(() => content.value?.chapter.title ?? '正在打开…')
const status = computed(() => conflict.value ? '进度有冲突' : reviewing.value ? '正在核对进度…' : saving.value ? '正在保存…' : uncertain.value ? '进度保存结果待确认' : dirty.value ? '进度等待同步' : localFault.value ? '进度已同步，本机记录待更新' : '进度已同步')
async function persistLocal() {
  if (!baseProgress.value || getSession()?.user.id !== owner || disposed) return
  const book = bookId.value, epoch = saveEpoch
  const current = () => !disposed && epoch === saveEpoch && book === bookId.value && getSession()?.user.id === owner
  try {
    await db.positions.put({ key: bookKey(owner, book), owner, base: { ...baseProgress.value }, index: index.value, offset: offset.value, dirty: dirty.value })
    if (current()) localFault.value = false
  } catch (e) { if (current()) localFault.value = true; throw e }
}
function capture() {
  if (error.value || loading.value || restoring.value || !body.value || !viewport.value) return
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
const saveProgressTask = singleFlight(async (): Promise<boolean> => {
  if (reviewing.value) return false
  if (disposed || getSession()?.user.id !== owner || !baseProgress.value || !book.value) return true
  const requestedBook = bookId.value, epoch = saveEpoch
  const current = () => !disposed && epoch === saveEpoch && requestedBook === bookId.value && getSession()?.user.id === owner
  try {
    try { await persistLocal() } catch { if (current()) saveError.value = '本机记录未保存，请保持页面打开并重试保存' }
    if (!current()) return false
    if (conflict.value) return false
    if (!dirty.value) { saveError.value = localFault.value ? '进度已同步，但本机记录更新失败，请保持页面打开并重试保存' : ''; return !localFault.value }
    while (dirty.value) {
      if (!current()) return false
      const sentRevision = revision, sentIndex = index.value, sentOffset = offset.value
      saving.value = true
      let next: Model<'Progress'>
      try {
        const chapter = toc.value?.items.find(c => c.chapter_index === sentIndex)
        const version = baseProgress.value!.version
        const data = { chapter_index: sentIndex, char_offset: sentOffset, chapter_title: chapter?.title ?? '', normalized_title: chapter?.normalized_title ?? '', chapter_ratio: chapterFraction(content.value?.content ?? '', sentOffset), client_updated_at: new Date().toISOString() }
        next = await withDeadline(signal => { if (!current()) throw new DOMException('Aborted', 'AbortError'); return api.saveProgress(requestedBook, data, version, signal) })
      } catch (e) {
        if (!current()) return false
        if (e instanceof ApiError && e.status === 409) { conflict.value = conflictProgress(e); uncertain.value = !conflict.value }
        else uncertain.value = !(e instanceof ApiError && e.status >= 400 && e.status < 500)
        const localMessage = localFault.value ? '本机记录也未保存，请保持页面打开' : '阅读位置已保存在本机'
        saveError.value = uncertain.value ? (e instanceof RequestDeadlineError ? '保存等待超时，结果尚未确认；' : '连接或响应异常，保存结果尚未确认；') + localMessage : errorText(e) + (localFault.value ? '；本机记录也未保存，请保持页面打开并重试' : '')
        return false
      }
      if (!current()) return false
      baseProgress.value = next; dirty.value = revision !== sentRevision; saveError.value = ''; offline.value = false; uncertain.value = false
      // The server has acknowledged this version. A local failure cannot undo it.
      try { await persistLocal() }
      catch { if (current()) saveError.value = dirty.value ? '上次进度已同步，但新位置及本机记录仍待保存，请保持页面打开并重试' : '进度已同步，但本机记录更新失败，请保持页面打开并重试保存'; return false }
      if (!current()) return false
    }
    return true
  } finally { if (current()) saving.value = false }
})
function flush(retryUncertain = false): Promise<boolean> {
  if (!uncertain.value || retryUncertain) return saveProgressTask()
  // Scroll, hide and leave may checkpoint locally, but must not replay an unknown PUT.
  const epoch = saveEpoch
  return persistLocal().then(() => false, () => {
    if (!disposed && epoch === saveEpoch) saveError.value = '保存结果待确认，且本机记录未保存，请保持页面打开'
    return false
  })
}
async function reviewProgress() {
  if (!uncertain.value || reviewing.value || saving.value || loading.value || restoring.value || !baseProgress.value) return
  const requestedBook = bookId.value, epoch = saveEpoch
  const current = () => !disposed && epoch === saveEpoch && requestedBook === bookId.value && getSession()?.user.id === owner
  clearTimeout(timer); capture(); reviewing.value = true
  try {
    try { await persistLocal() } catch { /* The explicit status below retains the local failure. */ }
    if (!current()) return
    const remote = await withDeadline(signal => api.progress(requestedBook, signal))
    if (!current()) return
    const state = remoteProgressState(baseProgress.value!, remote)
    if (state === 'advanced') { conflict.value = remote; saveError.value = '服务器进度版本已变化，请选择要保留的位置' }
    else saveError.value = state === 'unchanged' ? '尚未观察到新的服务器版本，原保存结果仍待确认；可重试保存或稍后再次核对' : '服务器进度数据异常，请重新打开书籍核对'
    if (localFault.value) saveError.value += '；本机记录也未保存，请保持页面打开'
  } catch (e) { if (current()) saveError.value = e instanceof RequestDeadlineError ? '核对等待超时，保存结果仍待确认' : '暂时无法核对服务器进度，保存结果仍待确认' }
  finally { if (current()) reviewing.value = false }
}
async function restorePosition(current: () => boolean = () => true) {
  await nextTick()
  if (!current()) return
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
function fetchChapter(request: ChapterRequest) {
  return loadChapter(request, { fetch: api.content, read: getChapter, write: putChapter,
    cacheError: () => { saveError.value = '章节缓存不可用，离线阅读暂不可用' },
  })
}
async function openChapter(i: number, at = 0, onCommitted?: () => void) {
  if (!book.value) return false
  controller.abort(); controller = new AbortController(); const signal = controller.signal, serial = ++generation
  const request = { owner, bookId: bookId.value, context: chapterContext(book.value), index: i, signal }, total = book.value.total_chapters
  const current = () => !signal.aborted && serial === generation
  loading.value = true; restoring.value = true; error.value = ''
  try {
    const loaded = await fetchChapter(request)
    if (!current()) return false
    content.value = loaded.result; index.value = i; offset.value = Math.min(Math.max(0, at), codePointLength(loaded.result.content)); offline.value = loaded.cached
    onCommitted?.()
    loading.value = false; await restorePosition(current)
    await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
    if (!current()) return false
    restoring.value = false
    if (!loaded.cached) {
      const neighbors = [i + 1, i - 1, i + 2, i - 2].filter(n => n >= 0 && n < total)
      void preloadChapters(neighbors, signal, index => fetchChapter({ ...request, index })).then(() => { if (current()) return retainWindow(owner, request.bookId, i) }).catch(() => undefined)
    }
    return true
  } catch (e) { if (current() && !(e instanceof DOMException && e.name === 'AbortError')) error.value = errorText(e); return false }
  finally { if (current()) { loading.value = false; restoring.value = false } }
}
async function boot() {
  saveEpoch++; localFault.value = false; saving.value = false; uncertain.value = false; reviewing.value = false
  bookController.abort(); bookController = new AbortController(); controller.abort()
  const signal = bookController.signal, requestedBook = bookId.value
  function check() { signal.throwIfAborted(); if (!owner || getSession()?.user.id !== owner) throw new DOMException('Aborted', 'AbortError') }
  loading.value = true; restoring.value = false; offline.value = false; content.value = undefined; error.value = ''; conflict.value = undefined; dirty.value = false; saveError.value = ''
  book.value = undefined; toc.value = undefined; baseProgress.value = undefined
  try {
    check()
    await config.initialize(owner)
    check()
    const local = await db.positions.get(bookKey(owner, requestedBook)).catch(() => undefined)
    check()
    const bootstrap = await loadBookBootstrap(
      { owner, bookId: requestedBook, local, signal },
      {
        fetchBook: (id, sig) => api.book(id, sig),
        fetchChapters: (id, sig) => api.chapters(id, false, undefined, sig),
        fetchProgress: (id, sig) => api.progress(id, sig),
        readCachedBook: (userId, id) => db.books.get(bookKey(userId, id)),
        writeCachedBook: entry => db.books.put(entry),
        chapterContext,
        bookKey,
      },
    )
    check()
    book.value = bootstrap.book; toc.value = bootstrap.toc; offline.value = bootstrap.offline
    const current = bootstrap.progress
    if (!toc.value.total) throw new Error('目录中没有可读章节，请检查书源或导入内容')
    const restored = reconcileProgress(current, local)
    baseProgress.value = restored.base; dirty.value = restored.dirty; conflict.value = restored.conflict
    let earlyPersist: Promise<void> | undefined
    if (!await openChapter(Math.min(restored.index, Math.max(0, book.value!.total_chapters - 1)), restored.offset, () => {
      earlyPersist = persistLocal().catch(() => undefined)
    })) return
    check()
    if (earlyPersist) await earlyPersist
    if (!restored.dirty && !offline.value && current.char_offset === 0 && current.chapter_ratio > 0) {
      const adjusted = await api.progress(requestedBook, signal)
      check()
      if (adjusted.version !== baseProgress.value.version) { baseProgress.value = adjusted; await openChapter(Math.min(adjusted.chapter_index, Math.max(0, book.value!.total_chapters - 1)), adjusted.char_offset) }
    }
    check()
    await persistLocal().catch(() => { if (!signal.aborted) saveError.value = '本机缓存不可用' }); if (!signal.aborted && dirty.value && !conflict.value) void flush()
  } catch (e) { if (!signal.aborted && !(e instanceof DOMException && e.name === 'AbortError')) { error.value = errorText(e); loading.value = false } }
}
async function changeChapter(i: string | number, at = 0) {
  drawer.value?.close(); capture(); await flush(); if (conflict.value) return
  const target = Number(i); if (!Number.isInteger(target) || target < 0 || target >= (book.value?.total_chapters ?? 0)) return
  if (!await openChapter(target, at)) return
  dirty.value = true; revision++; await flush()
}
async function keepMine() { if (!conflict.value || reviewing.value || saving.value || loading.value || restoring.value) return; baseProgress.value = conflict.value; conflict.value = undefined; uncertain.value = false; dirty.value = true; revision++; await flush() }
async function useRemote() { if (!conflict.value || reviewing.value || saving.value || loading.value || restoring.value) return; const remote = conflict.value; conflict.value = undefined; baseProgress.value = remote; dirty.value = false; uncertain.value = false; await openChapter(remote.chapter_index, remote.char_offset); await persistLocal() }
async function leave() {
  clearTimeout(timer); capture(); bookController.abort(); controller.abort()
  try { await persistLocal() }
  catch { if (dirty.value || uncertain.value) { saveError.value = '本机记录未保存，请先重试保存后再离开'; return false } }
  // Capture a bounded save task before the view is invalidated, without blocking navigation.
  void flush()
  await Promise.resolve()
}
function online() {
  const epoch = saveEpoch, requestedBook = bookId.value
  void saveProgressTask.afterPending(() => {
    if (disposed || epoch !== saveEpoch || requestedBook !== bookId.value || getSession()?.user.id !== owner || !navigator.onLine) return Promise.resolve(false)
    return flush(true)
  })
}
function checkpoint() {
  clearTimeout(timer); capture()
  const epoch = saveEpoch
  // A slow in-flight PUT must not delay the newest local checkpoint.
  void persistLocal().catch(() => { if (!disposed && epoch === saveEpoch) saveError.value = '本机记录未保存，请保持页面打开并重试保存' })
  void flush()
}
const stopCheckpoint = watchPageCheckpoint(checkpoint)
window.addEventListener('online', online)
onBeforeRouteLeave(leave)
onBeforeRouteUpdate(leave)
watch(bookId, boot, { immediate: true })
onBeforeUnmount(() => { disposed = true; saveEpoch++; stopCheckpoint(); clearTimeout(timer); bookController.abort(); controller.abort(); window.removeEventListener('online', online) })
</script>
<template>
  <main class="reader" :data-theme="config.theme" :style="{ '--reader-size': config.fontSize + 'px' }">
    <header v-show="!immersive" class="reader-header"><RouterLink to="/bookshelf" aria-label="返回书架">← 书架</RouterLink><span>{{ book?.name ?? '阅读器' }}</span><button v-if="book?.book_type === 'online'" :disabled="loading || saving" @click="openSwitcher">换源</button><button v-if="book?.book_type === 'online'" :disabled="loading || restoring || !content" @click="openOverride">{{ content?.is_overridden ? '单章覆盖中' : '单章换源' }}</button><button ref="bookmarkTrigger" :disabled="loading || restoring || !book" @click="openBookmarks">书签</button><button ref="directoryTrigger" aria-haspopup="dialog" aria-controls="reader-directory" @click="openDirectory" :disabled="!toc">目录</button><button type="button" :aria-pressed="immersive" @click="immersive = true">沉浸</button></header>
    <div v-if="conflict" class="reader-conflict" role="alert"><p>服务器当前位于第 {{ conflict.chapter_index + 1 }} 章。请选择要保留的位置。</p><button :disabled="reviewing || saving || loading || restoring" @click="keepMine">保留本机位置</button><button :disabled="reviewing || saving || loading || restoring" @click="useRemote">使用另一端位置</button></div>
    <p v-if="uncertain && !conflict" class="reader-notice" role="status">保存结果待确认 <button :disabled="reviewing || saving || loading || restoring" @click="reviewProgress">{{ reviewing ? '正在核对…' : '核对服务器进度' }}</button></p>
    <p v-if="saveError" class="reader-notice" role="status">{{ saveError }}</p>
    <p v-if="config.error" class="reader-notice" role="status">{{ config.error }} <button :disabled="config.loading || config.saving" @click="config.retry">重试设置同步</button></p>
    <section ref="viewport" class="reader-viewport" :aria-busy="loading || restoring" aria-label="章节阅读区" tabindex="0" @scroll="scroll" @touchstart.passive="onTouchStart" @touchend.passive="onTouchEnd">
      <div v-if="loading" class="reader-state" role="status">正在翻开这一页…</div><div v-else-if="error" class="reader-state" role="alert"><p>{{ error }}</p><button @click="boot">重新加载</button><RouterLink to="/bookshelf">返回书架</RouterLink></div>
      <article v-show="!loading && !error" class="reader-article"><span class="reader-kicker">{{ offline ? '离线缓存' : '正文' }} · 第 {{ index + 1 }} 章<template v-if="content?.is_overridden"> · 已单章换源</template></span><h1>{{ title }}</h1><p ref="body" class="chapter-body">{{ content?.content }}</p><p class="chapter-end">本章完</p></article>
    </section>
    <footer v-show="!immersive" class="reader-footer"><div class="reader-options"><label>主题<select :value="config.theme" :disabled="config.loading || loading || restoring" aria-label="阅读主题" @change="changeAppearance(() => config.setTheme(($event.target as HTMLSelectElement).value))"><option value="day">日间</option><option value="night">夜间</option><option value="paper">羊皮纸</option><option value="green">护眼</option></select></label><button aria-label="缩小字号" :disabled="config.loading || loading || restoring || config.fontSize <= 14" @click="changeAppearance(() => config.changeFont(-1))">A−</button><button aria-label="放大字号" :disabled="config.loading || loading || restoring || config.fontSize >= 32" @click="changeAppearance(() => config.changeFont(1))">A＋</button></div><div class="reader-nav"><button :disabled="loading || restoring || !content || content.prev_chapter_index == null" @click="changeChapter(content!.prev_chapter_index!)">上一章</button><button :disabled="loading || restoring || !content || content.next_chapter_index == null" @click="changeChapter(content!.next_chapter_index!)">下一章</button><button :disabled="loading || restoring || saving || reviewing" @click="capture(); flush(true)">保存进度</button></div><span class="reader-status" role="status">{{ status }}</span></footer>
    <button v-if="immersive" type="button" class="immersive-restore" @click="immersive = false">显示工具栏</button>
    <SourceSwitcher ref="switcher" :book-id="bookId" :active-source="book?.active_source_id" @switched="switched" />
    <BookmarksPanel ref="bookmarks" :book-id="bookId" :total="book?.total_chapters ?? 0" @navigate="visitBookmark" />
    <ChapterOverrideDialog ref="overrideDialog" :book-id="bookId" :chapter-index="index" :chapter-title="title" :is-overridden="Boolean(content?.is_overridden)" @updated="overrideUpdated" />
    <dialog id="reader-directory" ref="drawer" class="toc-dialog" aria-labelledby="toc-title" @close="onDirectoryClose"><div class="toc-heading"><h2 id="toc-title">章节目录</h2><button type="button" autofocus @click="drawer?.close()">关闭</button></div><VirtualList ref="directory" :items="entries" :selected="index" label="章节目录列表" @select="changeChapter" /></dialog>
  </main>
</template>
<style scoped>
.reader { --reader-bg: #f2ead7; --reader-ink: #423c30; --reader-muted: #8b816d; background: var(--reader-bg); color: var(--reader-ink); height: 100dvh; display: flex; flex-direction: column; overflow: hidden; }
.reader[data-theme=day] { --reader-bg: #fbfbf7; --reader-ink: #303930; --reader-muted: #7c897c; }.reader[data-theme=night] { --reader-bg: #1d2422; --reader-ink: #c6cfc7; --reader-muted: #849187; }.reader[data-theme=green] { --reader-bg: #dce8d8; --reader-ink: #334831; --reader-muted: #778d72; }
.reader button,.reader select { background: transparent; color: inherit; border: 1px solid #8884; border-radius: 5px; padding: 7px 12px; cursor: pointer; font-size: 12px; }.reader button:disabled { opacity: .35; cursor: default; }.reader-header { display: flex; justify-content: space-between; align-items: center; gap: 20px; height: 64px; flex-shrink: 0; padding: 0 32px; border-bottom: 1px solid #8882; font-size: 13px; }.reader-header button,.reader-header a { flex-shrink: 0; white-space: nowrap; }.reader-header span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }.reader-viewport { overflow-y: auto; flex: 1; min-height: 0; overscroll-behavior: contain; -webkit-overflow-scrolling: touch; touch-action: pan-y; scroll-behavior: auto; }
.reader { overscroll-behavior-y: contain; }
.immersive-restore { position: fixed; right: max(16px, env(safe-area-inset-right)); bottom: max(16px, env(safe-area-inset-bottom)); z-index: 20; background: var(--reader-bg); opacity: .9; }.reader-article { width: min(100%, 800px); padding: 54px 42px 70px; margin: auto; }.reader-kicker { color: var(--reader-muted); font-size: 11px; letter-spacing: 2px; }.reader h1 { font-size: 29px; line-height: 1.5; margin: 20px 0 42px; }.chapter-body { font-family: 'Noto Serif SC','Songti SC',SimSun,serif; font-size: var(--reader-size); line-height: 2; white-space: pre-wrap; overflow-wrap: anywhere; word-break: normal; margin: 0; }.chapter-end { text-align: center; font-size: 12px; color: var(--reader-muted); padding: 40px; }.reader-footer { display: flex; flex-wrap: wrap; align-items: center; gap: 16px; padding: 14px 28px; border-top: 1px solid #8882; flex-shrink: 0; font-size: 10px; }.reader-options,.reader-nav { display: flex; align-items: center; gap: 8px; }.reader-options label { display: flex; align-items: center; gap: 8px; }.reader-nav { margin: auto; }.reader-status { color: var(--reader-muted); }.reader-state { text-align: center; padding: 90px 24px; }.reader-state a { display: block; margin: 20px; }.reader-notice,.reader-conflict { padding: 12px 24px; margin: 0; font-size: 12px; background: #b9842720; }.reader-conflict p { margin: 0 0 8px; }.reader-conflict button { margin-right: 12px; }.toc-dialog { width: min(520px,95vw); border: 1px solid #8884; border-radius: 10px; color: var(--reader-ink); background: var(--reader-bg); padding: 18px; }.toc-dialog::backdrop { background: #10282077; }.toc-heading { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }.toc-heading h2 { font-size: 18px; font-weight: 500; }
@media(max-width:650px) { .reader-header { height: 54px; padding: 0 18px; }.reader-article { padding: 32px 22px 50px; }.reader-footer { padding: 10px 14px max(10px,env(safe-area-inset-bottom)); gap: 10px; }.reader-options { margin: auto; }.reader-nav { margin: auto; }.reader-status { width: 100%; text-align: center; }.reader h1 { font-size: 25px; margin-bottom: 28px; } }
.reader { color-scheme: light; }
.reader[data-theme=night] { color-scheme: dark; }
.reader :is(button, a, select):focus-visible { outline: 2px solid var(--reader-ink); outline-offset: 3px; }
.reader-header { min-height: 64px; height: auto; padding-block: max(10px, env(safe-area-inset-top)) 10px; padding-inline: max(18px, env(safe-area-inset-left)) max(18px, env(safe-area-inset-right)); }
.reader-header span { min-width: 0; flex: 1; }
.reader-article { padding-inline: max(22px, env(safe-area-inset-left)) max(22px, env(safe-area-inset-right)); }
.reader-footer { padding-inline: max(14px, env(safe-area-inset-left)) max(14px, env(safe-area-inset-right)); padding-bottom: max(14px, env(safe-area-inset-bottom)); }
.toc-dialog { max-height: calc(100dvh - 32px); overflow: auto; }
@media (min-width: 651px) { .reader-article { padding-inline: max(42px, env(safe-area-inset-left)) max(42px, env(safe-area-inset-right)); } }
@media (max-width: 650px) {
  .reader-header { flex-wrap: wrap; gap: 8px; }
  .reader :is(button, select), .reader-header a { min-height: 44px; }
  .reader-header a { display: inline-flex; align-items: center; }
}
@media (max-width: 360px) { .reader-header span { display: none; } }
</style>
