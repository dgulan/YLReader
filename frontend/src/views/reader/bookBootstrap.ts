import type { Model } from '@/api/client'
import type { CachedBook, LocalPosition } from '@/db/chapters'
import { canReadOffline } from './offline'

export interface BootstrapIO {
  fetchBook: (bookId: string, signal: AbortSignal) => Promise<Model<'Book'>>
  onBookLoaded?: (book: Model<'Book'>) => void
  fetchChapters: (bookId: string, signal: AbortSignal) => Promise<Model<'ChapterList'>>
  fetchProgress: (bookId: string, signal: AbortSignal) => Promise<Model<'Progress'>>
  readCachedBook: (owner: string, bookId: string) => Promise<CachedBook | undefined>
  writeCachedBook: (entry: CachedBook) => Promise<unknown>
  chapterContext: (book: Model<'Book'>) => string
  bookKey: (owner: string, bookId: string) => string
}

export interface BootstrapRequest {
  readonly owner: string
  readonly bookId: string
  readonly local: LocalPosition | undefined
  readonly signal: AbortSignal
}

export interface BootstrapResult {
  book: Model<'Book'>
  toc: Model<'ChapterList'>
  progress: Model<'Progress'>
  offline: boolean
}

/** Resolve book metadata, directory and progress without letting a recoverable TOC failure discard cached reading state. */
export async function loadBookBootstrap(request: BootstrapRequest, io: BootstrapIO): Promise<BootstrapResult> {
  const { owner, bookId, local, signal } = request
  signal.throwIfAborted()

  const bookRequest = io.fetchBook(bookId, signal)
  const readingRequests = Promise.allSettled([
    io.fetchChapters(bookId, signal),
    io.fetchProgress(bookId, signal),
  ])
  let liveBook: Model<'Book'> | undefined
  try {
    liveBook = await bookRequest
  } catch (error) {
    signal.throwIfAborted()
    if (!canReadOffline(error)) throw error
  }
  signal.throwIfAborted()

  if (!liveBook) {
    const cached = await io.readCachedBook(owner, bookId)
    signal.throwIfAborted()
    if (!cached || !local) throw new Error('尚无离线记录，请联网打开这本书')
    return {
      book: cached.book,
      toc: cached.toc,
      progress: local.base,
      offline: true,
    }
  }

  io.onBookLoaded?.(liveBook)
  const [tocResult, progressResult] = await readingRequests
  signal.throwIfAborted()

  if (progressResult.status === 'rejected' && !canReadOffline(progressResult.reason)) {
    throw progressResult.reason
  }
  if (tocResult.status === 'rejected' && !canReadOffline(tocResult.reason)) {
    throw tocResult.reason
  }

  let progress: Model<'Progress'>
  let offline = false
  if (progressResult.status === 'fulfilled') {
    progress = progressResult.value
  } else {
    if (!local) throw new Error('尚无离线记录，请联网打开这本书')
    progress = local.base
    offline = true
  }

  let toc: Model<'ChapterList'>
  if (tocResult.status === 'fulfilled') {
    toc = tocResult.value
    liveBook = { ...liveBook, total_chapters: toc.total }
    await io.writeCachedBook({
      key: io.bookKey(owner, bookId),
      owner,
      book: liveBook,
      toc,
    }).catch(() => undefined)
    signal.throwIfAborted()
  } else {
    const cached = await io.readCachedBook(owner, bookId)
    signal.throwIfAborted()
    if (cached) {
      if (io.chapterContext(liveBook) !== io.chapterContext(cached.book)) {
        throw new Error('书源或书籍已变化，尚无新版本缓存，请联网重试')
      }
      toc = cached.toc
      liveBook = { ...liveBook, total_chapters: toc.total }
    } else if (liveBook.total_chapters > 0) {
      // Live book and progress succeeded; synthesize a minimal TOC so cached/live chapter content can still open.
      toc = {
        book_id: liveBook.id,
        source_id: liveBook.active_source_id ?? '',
        total: liveBook.total_chapters,
        items: [],
      }
    } else if (!local) {
      throw new Error('尚无离线记录，请联网打开这本书')
    } else {
      throw tocResult.reason
    }
  }

  return {
    book: liveBook,
    toc,
    progress,
    offline,
  }
}
