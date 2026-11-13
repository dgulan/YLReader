import { effectScope, ref } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '@/api/reader'
import { ApiError, type Model } from '@/api/client'
import { bookmarkExcerpt, useBookmarks } from './bookmarks'
const scopes: ReturnType<typeof effectScope>[] = []
function setup() { const scope = effectScope(); scopes.push(scope); const book = ref('book-a'); return { book, state: scope.run(() => useBookmarks(book))! } }
afterEach(() => { scopes.splice(0).forEach(s => s.stop()); vi.restoreAllMocks() })
const mark = (id: string, index = 0, offset = 0): Model<'Bookmark'> => ({ id, book_id: 'book-a', chapter_index: index, char_offset: offset, created_at: '2026-11-12T00:00:00Z' })
describe('reader bookmarks', () => {
  it('takes an excerpt at Unicode character offsets without splitting emoji', () => {
    expect(bookmarkExcerpt('甲😀乙丙', 2)).toBe('乙丙')
    expect(Array.from(bookmarkExcerpt('😀'.repeat(200), 10))).toHaveLength(120)
  })
  it('creates at the captured position and keeps chapter/offset order', async () => {
    const { state } = setup(); vi.spyOn(api, 'bookmarks').mockResolvedValue([mark('later', 2)])
    await state.load(); const create = vi.spyOn(api, 'createBookmark').mockResolvedValue(mark('new', 1, 13))
    expect(await state.create({ chapter_index: 1, char_offset: 13, chapter_title: '第二章', note: '备注😀' })).toBe(true)
    expect(create).toHaveBeenCalledWith('book-a', { chapter_index: 1, char_offset: 13, chapter_title: '第二章', note: '备注😀' })
    expect(state.items.value.map(b => b.id)).toEqual(['new', 'later'])
  })
  it('preserves saved notes and reports duplicate positions without treating them as updates', async () => {
    const { state } = setup(); const original = { ...mark('old'), note: '原始备注' }
    vi.spyOn(api, 'bookmarks').mockResolvedValue([original]); await state.load()
    vi.spyOn(api, 'createBookmark').mockRejectedValue(new ApiError(409, 'conflict', null))
    expect(await state.create({ chapter_index: 0, char_offset: 0, chapter_title: '', note: '新草稿' })).toBe(false)
    expect(state.items.value).toEqual([original]); expect(state.error.value).toContain('备注未覆盖')
  })
  it('keeps a bookmark after a failed deletion and reconciles deletion on another device', async () => {
    const { state } = setup(); vi.spyOn(api, 'bookmarks').mockResolvedValue([mark('old')]); await state.load()
    const remove = vi.spyOn(api, 'deleteBookmark').mockRejectedValueOnce(new TypeError('offline')).mockRejectedValueOnce(new ApiError(404, 'not_found', null))
    expect(await state.remove('old')).toBe(false); expect(state.items.value).toHaveLength(1)
    expect(await state.remove('old')).toBe(true); expect(state.items.value).toEqual([]); expect(remove).toHaveBeenCalledTimes(2)
  })
  it('does not leak a late list or mutation into another book and ignores disposed responses', async () => {
    const { state, book } = setup(); let finish!: (v: Model<'Bookmark'>[]) => void
    vi.spyOn(api, 'bookmarks').mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    const request = state.load(); book.value = 'book-b'; finish([mark('old')]); await request
    expect(state.items.value).toEqual([]); expect(state.busy.value).toBe(false)
    let saved!: (v: Model<'Bookmark'>) => void
    vi.spyOn(api, 'createBookmark').mockImplementationOnce(() => new Promise(resolve => { saved = resolve }))
    const mutation = state.create({ chapter_index: 0, char_offset: 0, chapter_title: '' }); scopes[0]!.stop(); saved(mark('late'))
    expect(await mutation).toBe(false); expect(state.items.value).toEqual([])
  })
  it('reports unavailable lists and permits retry without inventing offline bookmark records', async () => {
    const { state } = setup(); vi.spyOn(api, 'bookmarks').mockRejectedValueOnce(new TypeError('offline')).mockResolvedValueOnce([mark('synced')])
    await state.load(); expect(state.error.value).toContain('请联网后重试'); expect(state.items.value).toEqual([])
    await state.load(); expect(state.error.value).toBe(''); expect(state.items.value[0]?.id).toBe('synced')
  })
})
