import { computed, onScopeDispose, reactive, ref, watch, type Ref } from 'vue'
import { api } from '@/api/reader'
import { ApiError, errorText, getSession, type Model } from '@/api/client'
import { bookKey, clearBookCache, db } from '@/db/chapters'
import { invalidateBookCover } from './coverCache'
export interface BookDraft { name: string; author: string; status: Model<'Book'>['status']; intro: string }
const empty = (): BookDraft => ({ name: '', author: '', status: 'unknown', intro: '' })
export function bookPatch(draft: BookDraft, version: number): Model<'BookUpdate'> {
  if (!draft.name.trim()) throw new Error('书名不能为空')
  if (!['unknown', 'serializing', 'completed', 'paused'].includes(draft.status)) throw new Error('请选择有效连载状态')
  return { version, name: draft.name.trim(), author: draft.author.trim(), status: draft.status, intro: draft.intro }
}
export function useBookEditor(bookId: Ref<string>) {
  const owner = getSession()?.user.id ?? '', book = ref<Model<'Book'>>(), draft = reactive(empty()), busy = ref(false), error = ref(''), notice = ref(''), deleted = ref(false)
  const mappings = ref<Model<'BookSourceMapping'>[]>([]), sources = ref<Model<'Source'>[]>([])
  const activeSourceName = ref('')
  const mappingBusy = ref(false), mappingError = ref(''), mappingNotice = ref('')
  const baseline = ref(JSON.stringify(draft)), dirty = computed(() => JSON.stringify(draft) !== baseline.value)
  let generation = 0
  const valid = (token: number) => Boolean(owner) && token === generation && getSession()?.user.id === owner
  function reset() {
    generation++; book.value = undefined; Object.assign(draft, empty()); baseline.value = JSON.stringify(draft)
    busy.value = false; error.value = ''; notice.value = ''; deleted.value = false
    mappings.value = []; sources.value = []; activeSourceName.value = ''; mappingBusy.value = false; mappingError.value = ''; mappingNotice.value = ''
  }
  watch(bookId, reset, { flush: 'sync' }); onScopeDispose(reset)
  function apply(value: Model<'Book'>) { book.value = value; Object.assign(draft, { name: value.name, author: value.author || '', status: value.status, intro: value.intro || '' }); baseline.value = JSON.stringify(draft) }
  async function loadMappings(token: number, id: string, activeSource?: string | null) {
    mappingBusy.value = true; mappingError.value = ''
    try {
      const results = await Promise.allSettled([
        api.mappings(id).then(async list => {
          if (!valid(token)) return
          mappings.value = list
          const active = list.find(mapping => mapping.source_id === activeSource)
          const source = activeSource && !active?.source_name ? await api.source(activeSource).catch(() => undefined) : undefined
          if (valid(token)) activeSourceName.value = active?.source_name || source?.book_source_name || ''
        }),
        api.sources(1).then(page => {
          if (valid(token)) sources.value = page.items.filter(source => source.book_source_type !== 3 && source.enabled)
        }),
      ])
      const failure = results.find(result => result.status === 'rejected')
      if (valid(token) && failure) mappingError.value = errorText(failure.reason)
    }
    finally { if (valid(token)) mappingBusy.value = false }
  }
  async function load(refreshIntro = false) {
    if (!valid(generation) || busy.value) return
    const token = generation, id = bookId.value; busy.value = true; error.value = ''; notice.value = ''
    try {
      const value = await api.book(id)
      if (!valid(token)) return
      apply(value)
      if (value.book_type === 'online') {
        const loadBindings = loadMappings(token, id, value.active_source_id)
        try {
          if (refreshIntro && !value.intro?.trim()) {
            const refreshed = await api.updateBook(id, { version: value.version, refresh_intro: true })
            if (valid(token)) apply(refreshed)
          }
        } finally {
          await loadBindings
        }
      }
    }
    catch (e) { if (valid(token)) error.value = errorText(e) }
    finally { if (valid(token)) busy.value = false }
  }
  async function addBackupMapping(sourceId: string, bookUrl: string) {
    if (!valid(generation) || mappingBusy.value || !book.value || book.value.book_type !== 'online') return false
    const token = generation, id = bookId.value, trimmedUrl = bookUrl.trim()
    mappingError.value = ''; mappingNotice.value = ''
    if (!sourceId) { mappingError.value = '请选择书源'; return false }
    if (!/^https?:\/\//i.test(trimmedUrl)) { mappingError.value = '书籍详情地址必须以 http:// 或 https:// 开头'; return false }
    mappingBusy.value = true
    try {
      await api.addMapping(id, { source_id: sourceId, book_url: trimmedUrl })
      if (!valid(token)) return false
      const list = await api.mappings(id)
      if (!valid(token)) return false
      mappings.value = list
      mappingNotice.value = '已添加备用书源绑定'
      return true
    } catch (e) { if (valid(token)) mappingError.value = errorText(e); return false }
    finally { if (valid(token)) mappingBusy.value = false }
  }
  async function removeBackupMapping(mapping: Model<'BookSourceMapping'>) {
    if (!valid(generation) || mappingBusy.value || !book.value) return false
    if (mapping.source_id === book.value.active_source_id) {
      mappingError.value = '当前激活书源不可移除，请先在阅读器中换源'
      return false
    }
    const token = generation, id = bookId.value
    mappingBusy.value = true; mappingError.value = ''; mappingNotice.value = ''
    try {
      await api.deleteMapping(id, mapping.id)
      if (!valid(token)) return false
      mappings.value = mappings.value.filter(m => m.id !== mapping.id)
      mappingNotice.value = '已移除备用书源绑定'
      return true
    } catch (e) { if (valid(token)) mappingError.value = errorText(e); return false }
    finally { if (valid(token)) mappingBusy.value = false }
  }
  async function save() {
    if (!valid(generation) || busy.value || !book.value || deleted.value) return false
    const token = generation, id = bookId.value; error.value = ''; notice.value = ''
    try {
      const patch = bookPatch(draft, book.value.version); busy.value = true
      const value = await api.updateBook(id, patch); if (!valid(token)) return false
      invalidateBookCover(owner, id, value.custom_cover_url || value.cover_url || undefined)
      apply({ ...value, progress: value.progress ?? book.value?.progress })
      try { await db.transaction('rw', db.books, db.chapters, async () => { await db.books.delete(bookKey(owner, id)); await db.chapters.where('[owner+bookId]').equals([owner, id]).delete() }); if (valid(token)) notice.value = '书籍信息已保存，重新打开章节即可更新本机缓存' }
      catch { if (valid(token)) notice.value = '书籍信息已保存，但本机缓存更新失败，请联网重新打开章节' }
      if (!valid(token)) return false
      return true
    } catch (e) { if (valid(token)) error.value = e instanceof ApiError && e.status === 409 ? '书籍版本已变化，或书名作者与已有书重复。草稿已保留，请核对后重新加载。' : errorText(e); return false }
    finally { if (valid(token)) busy.value = false }
  }
  async function remove() {
    if (!valid(generation) || busy.value || !book.value || deleted.value) return false
    const token = generation, id = bookId.value; busy.value = true; error.value = ''; notice.value = ''
    try {
      try { await api.deleteBook(id) } catch (e) { if (!(e instanceof ApiError && e.status === 404)) throw e }
      invalidateBookCover(owner, id)
      if (!valid(token)) return false
      deleted.value = true
      try { await clearBookCache(owner, id); await db.jobs.delete(bookKey(owner, id)); if (valid(token)) notice.value = '书籍已删除' }
      catch { if (valid(token)) notice.value = '书籍已从服务端删除，但本机缓存清理失败，请保持联网' }
      if (!valid(token)) return false
      return true
    } catch (e) { if (valid(token)) error.value = errorText(e); return false }
    finally { if (valid(token)) busy.value = false }
  }
  return { book, draft, dirty, busy, error, notice, deleted, mappings, sources, activeSourceName, mappingBusy, mappingError, mappingNotice, load, save, remove, addBackupMapping, removeBackupMapping }
}
