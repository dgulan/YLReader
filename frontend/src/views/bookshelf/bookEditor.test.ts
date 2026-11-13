import 'fake-indexeddb/auto'
import { effectScope, ref } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { api } from '@/api/reader'
import { ApiError, setSession, type Model } from '@/api/client'
import { db } from '@/db/chapters'
import { bookPatch, useBookEditor } from './bookEditor'
const scopes: ReturnType<typeof effectScope>[] = []
const book = { id: 'book', name: '原书', author: '作者', status: 'unknown', version: 3 } as Model<'Book'>
function setup() { const scope = effectScope(); scopes.push(scope); return scope.run(() => useBookEditor(ref('book')))! }
beforeEach(async () => {
  await Promise.all([db.books.clear(), db.chapters.clear(), db.positions.clear(), db.jobs.clear()])
  setSession({ access_token: 'a', refresh_token: 'a', token_type: 'Bearer', expires_in: 900, user: { id: 'a', username: 'a', role: 'user', token_version: 1, created_at: '' } })
  vi.spyOn(api, 'book').mockResolvedValue(book)
})
afterEach(() => { scopes.splice(0).forEach(s => s.stop()); setSession(null); vi.restoreAllMocks() })
it('validates names and sends only editable fields with the loaded version', () => {
  expect(() => bookPatch({ name: ' ', author: '', intro: '', status: 'unknown' }, 3)).toThrow('书名不能为空')
  expect(bookPatch({ name: ' 新书 ', author: ' 作者 ', intro: '', status: 'completed' }, 3)).toEqual({ name: '新书', author: '作者', intro: '', status: 'completed', version: 3 })
})
it('keeps a conflicting draft and does not silently adopt a new version', async () => {
  const state = setup(); await state.load(); state.draft.name = '草稿'
  const update = vi.spyOn(api, 'updateBook').mockRejectedValue(new ApiError(409, 'conflict', null))
  expect(await state.save()).toBe(false); expect(state.draft.name).toBe('草稿'); expect(state.dirty.value).toBe(true); expect(state.book.value?.version).toBe(3)
  expect(update).toHaveBeenCalledWith('book', expect.objectContaining({ version: 3 })); expect(state.error.value).toContain('草稿已保留')
})
it('invalidates only the saved book metadata and chapters while preserving positions', async () => {
  const state = setup(); await state.load()
  await db.chapters.bulkPut(['a', 'b'].map(owner => ({ key: `${owner}:book:0`, owner, bookId: 'book', index: 0, context: 'old', content: {} as Model<'ChapterContent'> })))
  const positions = vi.spyOn(db.positions, 'delete'); vi.spyOn(api, 'updateBook').mockResolvedValue({ ...book, name: '新书', version: 4 })
  state.draft.name = '新书'; expect(await state.save()).toBe(true); expect(state.dirty.value).toBe(false); expect(state.book.value?.version).toBe(4)
  expect((await db.chapters.toArray()).map(c => c.owner)).toEqual(['b']); expect(positions).not.toHaveBeenCalled()
})
it('preserves failed deletion and reconciles 404 with book-scoped local cleanup', async () => {
  const state = setup(); await state.load()
  const positions = vi.spyOn(db.positions, 'delete'), jobs = vi.spyOn(db.jobs, 'delete')
  vi.spyOn(api, 'deleteBook').mockRejectedValueOnce(new TypeError('offline')).mockRejectedValueOnce(new ApiError(404, 'not_found', null))
  expect(await state.remove()).toBe(false); expect(state.deleted.value).toBe(false); expect(positions).not.toHaveBeenCalled()
  expect(await state.remove()).toBe(true); expect(state.deleted.value).toBe(true); expect(positions).toHaveBeenCalledWith('a:book'); expect(jobs).toHaveBeenCalledWith('a:book')
})
it('ignores writes returned after account logout', async () => {
  const state = setup(); await state.load(); let finish!: (value: Model<'Book'>) => void
  vi.spyOn(api, 'updateBook').mockImplementation(() => new Promise(resolve => { finish = resolve }))
  const pending = state.save(); setSession(null); finish({ ...book, version: 4 }); expect(await pending).toBe(false); expect(state.book.value?.version).toBe(3)
})
