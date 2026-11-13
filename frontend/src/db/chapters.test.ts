import 'fake-indexeddb/auto'
import { beforeEach, afterAll, it, expect } from 'vitest'
import { db, putChapter, getChapter, retainWindow, clearUserCache, clearBookCache } from './chapters'
import type { Model } from '@/api/client'
const content: Model<'ChapterContent'> = { chapter: { id: 1, chapter_index: 0, sort_order: 0, title: '一', normalized_title: '一', url: 'local://book', is_volume: false, is_overridden: false, word_count: 2 }, content: '正文', word_count: 2, cached: false, is_overridden: false }
beforeEach(async () => { await db.open(); await db.chapters.clear(); await db.books.clear(); await db.positions.clear() })
afterAll(async () => { await db.delete() })
it('isolates cached chapters by user and active source context', async () => {
  await putChapter('a', 'book', 0, 'source-a:1', content)
  expect((await getChapter('a', 'book', 0, 'source-a:1'))?.content).toBe('正文')
  expect(await getChapter('b', 'book', 0, 'source-a:1')).toBeUndefined()
  expect(await getChapter('a', 'book', 0, 'source-b:1')).toBeUndefined()
})
it('retains only the current chapter and two neighbors on either side', async () => {
  for (let i = 0; i < 9; i++) await putChapter('a', 'book', i, 'source:1', content)
  await retainWindow('a', 'book', 4)
  expect((await db.chapters.toArray()).map(c => c.index)).toEqual([2, 3, 4, 5, 6])
})
it('clears one users offline data without deleting another users chapters', async () => {
  await putChapter('a', 'book', 0, 'x', content); await putChapter('b', 'book', 0, 'x', content)
  await db.positions.put({ key: 'a:book', owner: 'a', base: { book_id: 'book', chapter_index: 0, char_offset: 0, chapter_ratio: 0, version: 2 }, index: 1, offset: 5, dirty: true })
  await clearUserCache('a')
  expect(await db.positions.count()).toBe(0); expect(await db.chapters.count()).toBe(1); expect((await db.chapters.toArray())[0]?.owner).toBe('b')
})

it('invalidates only the switched book and leaves other books and accounts intact', async () => {
  await putChapter('a', 'one', 0, 'old', content); await putChapter('a', 'two', 0, 'other', content); await putChapter('b', 'one', 0, 'other', content)
  await clearBookCache('a', 'one')
  expect(await getChapter('a', 'one', 0, 'old')).toBeUndefined(); expect(await db.chapters.count()).toBe(2)
})
