import Dexie, { type Table } from 'dexie'
import type { Model } from '@/api/client'
import type { ReaderPreferences, PreferencePatch } from '@/stores/readerPreferences'
export interface CachedPreferences { owner: string; values: ReaderPreferences; pending: PreferencePatch }
export interface CachedJob { key: string; owner: string; bookId: string; job: Model<'Job'> }
export interface CachedChapter { key: string; owner: string; bookId: string; index: number; context: string; content: Model<'ChapterContent'> }
export interface CachedBook { key: string; owner: string; book: Model<'Book'>; toc: Model<'ChapterList'> }
export interface LocalPosition { key: string; owner: string; base: Model<'Progress'>; index: number; offset: number; dirty: boolean }
class ReaderDB extends Dexie {
  chapters!: Table<CachedChapter, string>
  books!: Table<CachedBook, string>
  positions!: Table<LocalPosition, string>
  preferences!: Table<CachedPreferences, string>
  jobs!: Table<CachedJob, string>
  constructor() { super('yreader-offline-v1'); this.version(1).stores({ chapters: 'key, owner, [owner+bookId]', books: 'key, owner' }); this.version(2).stores({ positions: 'key, owner' }); this.version(3).stores({ preferences: 'owner' }); this.version(4).stores({ jobs: 'key, owner' }) }
}
export const db = new ReaderDB()
export const bookKey = (owner: string, bookId: string) => `${owner}:${bookId}`
const chapterKey = (owner: string, bookId: string, index: number) => `${bookKey(owner, bookId)}:${index}`
export const chapterContext = (book: Model<'Book'>) => `${book.active_source_id ?? 'none'}:${book.version}`
export async function putChapter(owner: string, bookId: string, index: number, context: string, content: Model<'ChapterContent'>) {
  await db.chapters.put({ key: chapterKey(owner, bookId, index), owner, bookId, index, context, content })
}
export async function getChapter(owner: string, bookId: string, index: number, context: string) {
  const row = await db.chapters.get(chapterKey(owner, bookId, index))
  return row?.context === context ? row.content : undefined
}
export async function retainWindow(owner: string, bookId: string, center: number) {
  await db.chapters.where('[owner+bookId]').equals([owner, bookId]).filter(c => Math.abs(c.index - center) > 2).delete()
}
export async function clearUserCache(owner: string) {
  await db.transaction('rw', db.chapters, db.books, db.positions, db.preferences, db.jobs, async () => { await db.chapters.where('owner').equals(owner).delete(); await db.books.where('owner').equals(owner).delete(); await db.positions.where('owner').equals(owner).delete(); await db.preferences.delete(owner); await db.jobs.where('owner').equals(owner).delete() })
}

export async function clearBookCache(owner: string, bookId: string) {
  await db.transaction('rw', db.chapters, db.books, db.positions, async () => {
    await db.chapters.where('[owner+bookId]').equals([owner, bookId]).delete()
    await db.books.delete(bookKey(owner, bookId)); await db.positions.delete(bookKey(owner, bookId))
  })
}
