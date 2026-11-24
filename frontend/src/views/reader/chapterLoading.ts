import type { Model } from '@/api/client'
import { canReadOffline } from './offline'

export interface ChapterRequest {
  readonly owner: string
  readonly bookId: string
  readonly context: string
  readonly index: number
  readonly signal: AbortSignal
  readonly refresh?: boolean
}
interface ChapterIO {
  fetch: (book: string, index: number, signal: AbortSignal, refresh?: boolean) => Promise<Model<'ChapterContent'>>
  read: (owner: string, book: string, index: number, context: string) => Promise<Model<'ChapterContent'> | undefined>
  write: (owner: string, book: string, index: number, context: string, content: Model<'ChapterContent'>) => Promise<unknown>
  cacheError: () => void
}
export async function loadChapter(request: ChapterRequest, io: ChapterIO) {
  // Destructure before awaiting even if a caller passes a reactive object.
  const { owner, bookId, context, index, signal, refresh } = request
  signal.throwIfAborted()
  let result: Model<'ChapterContent'>
  try { result = await io.fetch(bookId, index, signal, refresh) }
  catch (error) {
    signal.throwIfAborted()
    if (refresh || !canReadOffline(error)) throw error
    const cached = await io.read(owner, bookId, index, context)
    signal.throwIfAborted()
    if (!cached) throw new Error('当前章节未缓存，请联网后重试')
    return { result: cached, cached: true }
  }
  signal.throwIfAborted()
  try { await io.write(owner, bookId, index, context, result) }
  catch { signal.throwIfAborted(); io.cacheError() }
  signal.throwIfAborted()
  return { result, cached: false }
}

export async function preloadChapters(indices: readonly number[], signal: AbortSignal, load: (index: number) => Promise<unknown>) {
  let next = 0
  async function worker() {
    while (!signal.aborted && next < indices.length) {
      const index = indices[next++]!
      try { await load(index) } catch { /* Neighbor failures never replace current content. */ }
    }
  }
  await Promise.all([worker(), worker()])
}
