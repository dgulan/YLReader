import { computed, onScopeDispose, reactive, ref, watch, type Ref } from 'vue'
import { api } from '@/api/reader'
import { ApiError, errorText, getSession, type Model } from '@/api/client'
import { bookKey, clearBookCache, db } from '@/db/chapters'
export interface BookDraft { name: string; author: string; status: Model<'Book'>['status']; intro: string }
const empty = (): BookDraft => ({ name: '', author: '', status: 'unknown', intro: '' })
export function bookPatch(draft: BookDraft, version: number): Model<'BookUpdate'> {
  if (!draft.name.trim()) throw new Error('书名不能为空')
  if (!['unknown', 'serializing', 'completed', 'paused'].includes(draft.status)) throw new Error('请选择有效连载状态')
  return { version, name: draft.name.trim(), author: draft.author.trim(), status: draft.status, intro: draft.intro }
}
export function useBookEditor(bookId: Ref<string>) {
  const owner = getSession()!.user.id, book = ref<Model<'Book'>>(), draft = reactive(empty()), busy = ref(false), error = ref(''), notice = ref(''), deleted = ref(false)
  const baseline = ref(JSON.stringify(draft)), dirty = computed(() => JSON.stringify(draft) !== baseline.value)
  let generation = 0
  const valid = (token: number) => token === generation && getSession()?.user.id === owner
  function reset() { generation++; book.value = undefined; Object.assign(draft, empty()); baseline.value = JSON.stringify(draft); busy.value = false; error.value = ''; notice.value = ''; deleted.value = false }
  watch(bookId, reset, { flush: 'sync' }); onScopeDispose(reset)
  function apply(value: Model<'Book'>) { book.value = value; Object.assign(draft, { name: value.name, author: value.author || '', status: value.status, intro: value.intro || '' }); baseline.value = JSON.stringify(draft) }
  async function load() {
    if (busy.value) return
    const token = generation; busy.value = true; error.value = ''; notice.value = ''
    try { const value = await api.book(bookId.value); if (valid(token)) apply(value) }
    catch (e) { if (valid(token)) error.value = errorText(e) }
    finally { if (valid(token)) busy.value = false }
  }
  async function save() {
    if (busy.value || !book.value || deleted.value) return false
    const token = generation, id = bookId.value; error.value = ''; notice.value = ''
    try {
      const patch = bookPatch(draft, book.value.version); busy.value = true
      const value = await api.updateBook(id, patch); if (!valid(token)) return false
      apply(value)
      try { await db.transaction('rw', db.books, db.chapters, async () => { await db.books.delete(bookKey(owner, id)); await db.chapters.where('[owner+bookId]').equals([owner, id]).delete() }); if (valid(token)) notice.value = '书籍信息已保存，重新打开章节即可更新本机缓存' }
      catch { if (valid(token)) notice.value = '书籍信息已保存，但本机缓存更新失败，请联网重新打开章节' }
      if (!valid(token)) return false
      return true
    } catch (e) { if (valid(token)) error.value = e instanceof ApiError && e.status === 409 ? '书籍版本已变化，或书名作者与已有书重复。草稿已保留，请核对后重新加载。' : errorText(e); return false }
    finally { if (valid(token)) busy.value = false }
  }
  async function remove() {
    if (busy.value || !book.value || deleted.value) return false
    const token = generation, id = bookId.value; busy.value = true; error.value = ''; notice.value = ''
    try {
      try { await api.deleteBook(id) } catch (e) { if (!(e instanceof ApiError && e.status === 404)) throw e }
      if (!valid(token)) return false
      deleted.value = true
      try { await clearBookCache(owner, id); await db.jobs.delete(bookKey(owner, id)); if (valid(token)) notice.value = '书籍已删除' }
      catch { if (valid(token)) notice.value = '书籍已从服务端删除，但本机缓存清理失败，请保持联网' }
      if (!valid(token)) return false
      return true
    } catch (e) { if (valid(token)) error.value = errorText(e); return false }
    finally { if (valid(token)) busy.value = false }
  }
  return { book, draft, dirty, busy, error, notice, deleted, load, save, remove }
}
