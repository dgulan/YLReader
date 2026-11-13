import { onScopeDispose, ref, watch, type Ref } from 'vue'
import { api } from '@/api/reader'
import { ApiError, errorText, type Model } from '@/api/client'
const bookmarkError = (error: unknown) => error instanceof TypeError ? '当前无法连接，书签未同步，请联网后重试' : errorText(error)

export function bookmarkExcerpt(text: string, offset: number) {
  return Array.from(text).slice(Math.max(0, offset), Math.max(0, offset) + 120).join('')
}

export function useBookmarks(bookId: Ref<string>) {
  const items = ref<Model<'Bookmark'>[]>([]), busy = ref(false), error = ref(''), message = ref('')
  let generation = 0
  function reset() { generation++; items.value = []; busy.value = false; error.value = ''; message.value = '' }
  watch(bookId, reset, { flush: 'sync' })
  onScopeDispose(reset)
  async function load() {
    if (busy.value) return
    const token = ++generation, book = bookId.value
    busy.value = true; error.value = ''; message.value = ''; items.value = []
    try { const list = await api.bookmarks(book); if (token === generation) items.value = list }
    catch (e) { if (token === generation) error.value = bookmarkError(e) }
    finally { if (token === generation) busy.value = false }
  }
  async function create(position: Model<'BookmarkCreate'>) {
    if (busy.value) return false
    const token = generation, book = bookId.value
    busy.value = true; error.value = ''; message.value = ''
    try {
      const saved = await api.createBookmark(book, position)
      if (token !== generation) return false
      items.value = [...items.value.filter(b => b.id !== saved.id), saved].sort((a, b) => a.chapter_index - b.chapter_index || a.char_offset - b.char_offset)
      message.value = '书签已保存'; return true
    } catch (e) {
      if (token === generation) error.value = e instanceof ApiError && e.status === 409 ? '此位置已有书签，备注未覆盖，请查看已有书签' : bookmarkError(e)
      return false
    } finally { if (token === generation) busy.value = false }
  }
  async function remove(id: string) {
    if (busy.value) return false
    const token = generation
    busy.value = true; error.value = ''; message.value = ''
    try {
      try { await api.deleteBookmark(id) } catch (e) { if (!(e instanceof ApiError && e.status === 404)) throw e }
      if (token !== generation) return false
      items.value = items.value.filter(b => b.id !== id); message.value = '书签已删除'; return true
    } catch (e) { if (token === generation) error.value = bookmarkError(e); return false }
    finally { if (token === generation) busy.value = false }
  }
  return { items, busy, error, message, load, create, remove }
}
