import 'fake-indexeddb/auto'
import { createApp, h, type App } from 'vue'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter, RouterView } from 'vue-router'
import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, setSession, type Model } from '@/api/client'
import { api } from '@/api/reader'
import { bookKey, chapterContext, db, putChapter } from '@/db/chapters'
import { loadBookBootstrap } from './bookBootstrap'
import ReaderView from './ReaderView.vue'

const userId = '00000000-0000-4000-8000-000000000001'
const bookId = '20000000-0000-4000-8000-000000000001'
const sourceBId = '10000000-0000-4000-8000-000000000002'

const bookB: Model<'Book'> = {
  id: bookId,
  name: '换源测试书',
  author: '测试作者',
  book_type: 'online',
  active_source_id: sourceBId,
  active_book_url: 'https://b.example.com/book',
  total_chapters: 2,
  version: 2,
} as Model<'Book'>

const tocB: Model<'ChapterList'> = {
  book_id: bookId,
  source_id: sourceBId,
  total: 2,
  items: [
    { id: 1, chapter_index: 0, sort_order: 0, title: '第1章', normalized_title: 'n:1', url: 'https://b.example.com/1', is_volume: false, is_overridden: false, word_count: 10 },
    { id: 2, chapter_index: 1, sort_order: 1, title: '第2章', normalized_title: 'n:2', url: 'https://b.example.com/2', is_volume: false, is_overridden: false, word_count: 10 },
  ],
}

const progress0: Model<'Progress'> = {
  book_id: bookId,
  chapter_index: 0,
  char_offset: 0,
  chapter_ratio: 0,
  version: 1,
} as Model<'Progress'>

const cachedContentB: Model<'ChapterContent'> = {
  chapter: tocB.items[0]!,
  content: '源B正文',
  cached: false,
  word_count: 4,
  source_id: sourceBId,
  is_overridden: false,
  prev_chapter_index: null,
  next_chapter_index: 1,
}

let app: App | undefined
let host: HTMLDivElement | undefined

async function mountReader() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/bookshelf', name: 'bookshelf', component: { render: () => h('div') } },
      { path: '/login', name: 'login', component: { render: () => h('div') } },
      { path: '/reader/:bookId', name: 'reader', component: ReaderView },
    ],
  })
  await router.push(`/reader/${bookId}`)
  host = document.createElement('div')
  document.body.append(host)
  app = createApp({ render: () => h(RouterView) })
  app.use(createPinia())
  app.use(router)
  app.mount(host)
  return host
}

beforeEach(async () => {
  if (!HTMLDialogElement.prototype.showModal) Object.defineProperty(HTMLDialogElement.prototype, 'showModal', { configurable: true, writable: true, value() { (this as HTMLDialogElement).open = true } })
  if (!HTMLDialogElement.prototype.close) Object.defineProperty(HTMLDialogElement.prototype, 'close', { configurable: true, writable: true, value() { (this as HTMLDialogElement).open = false } })
  await db.open()
  await db.chapters.clear()
  await db.books.clear()
  await db.positions.clear()
  await db.preferences.clear()
  setSession({
    access_token: 'test-token',
    refresh_token: 'test-refresh',
    token_type: 'Bearer',
    expires_in: 900,
    user: { id: userId, username: 'reader', role: 'user', token_version: 1, created_at: '2026-11-19T00:00:00Z' },
  })
})

afterEach(() => {
  app?.unmount()
  app = undefined
  host?.remove()
  host = undefined
  setSession(null)
  vi.restoreAllMocks()
})

afterAll(async () => {
  await db.delete()
})

describe('loadBookBootstrap', () => {
  it('falls back to cached TOC on 502 even when local position is not yet stored', async () => {
    const controller = new AbortController()
    const result = await loadBookBootstrap(
      { owner: userId, bookId, local: undefined, signal: controller.signal },
      {
        fetchBook: async () => ({ ...bookB }),
        fetchChapters: async () => { throw new ApiError(502, 'source_request_failed', null) },
        fetchProgress: async () => ({ ...progress0 }),
        readCachedBook: async () => ({ key: bookKey(userId, bookId), owner: userId, book: bookB, toc: tocB }),
        writeCachedBook: async () => undefined,
        chapterContext,
        bookKey,
      },
    )
    expect(result.offline).toBe(false)
    expect(result.toc.total).toBe(2)
    expect(result.progress.chapter_index).toBe(0)
  })

  it('never masks a 403 TOC error with cached directory', async () => {
    const controller = new AbortController()
    await expect(
      loadBookBootstrap(
        { owner: userId, bookId, local: undefined, signal: controller.signal },
        {
          fetchBook: async () => ({ ...bookB }),
          fetchChapters: async () => { throw new ApiError(403, 'forbidden', null) },
          fetchProgress: async () => ({ ...progress0 }),
          readCachedBook: async () => ({ key: bookKey(userId, bookId), owner: userId, book: bookB, toc: tocB }),
          writeCachedBook: async () => undefined,
          chapterContext,
          bookKey,
        },
      ),
    ).rejects.toMatchObject({ status: 403 })
  })
})

describe('ReaderView 502 TOC reload fallback', () => {
  it('restores and renders locally cached chapter body when TOC returns 502 on reload', async () => {
    const ctx = chapterContext(bookB)
    await db.books.put({ key: bookKey(userId, bookId), owner: userId, book: bookB, toc: tocB })
    await putChapter(userId, bookId, 0, ctx, cachedContentB)

    vi.spyOn(api, 'settings').mockResolvedValue({})
    vi.spyOn(api, 'book').mockResolvedValue({ ...bookB })
    vi.spyOn(api, 'chapters').mockRejectedValue(new ApiError(502, 'source_request_failed', null))
    vi.spyOn(api, 'progress').mockResolvedValue({ ...progress0 })
    vi.spyOn(api, 'content').mockRejectedValue(new ApiError(502, 'source_request_failed', null))

    const reader = await mountReader()

    await vi.waitFor(() => {
      const body = reader.querySelector('.chapter-body')?.textContent
      expect(body).toBe('源B正文')
    })
    expect(reader.querySelector('.reader-state[role="alert"]')).toBeNull()
  })
})

describe('ReaderView source switching without a usable TOC', () => {
  it('opens the same switcher from both entries after a rule failure and reads the replacement source', async () => {
    const sourceAId = '10000000-0000-4000-8000-000000000001'
    const bookA = { ...bookB, active_source_id: sourceAId, total_chapters: 0, version: 1 }
    const bookUrl = bookB.active_book_url!
    let switched = false
    let rejectToc!: (error: ApiError) => void
    const pendingToc = new Promise<Model<'ChapterList'>>((_, reject) => { rejectToc = reject })
    vi.spyOn(api, 'settings').mockResolvedValue({})
    vi.spyOn(api, 'book').mockImplementation(async () => ({ ...(switched ? bookB : bookA) }))
    const chapters = vi.spyOn(api, 'chapters').mockImplementation((_, __, source) =>
      source === sourceBId || switched ? Promise.resolve(tocB) : pendingToc)
    vi.spyOn(api, 'progress').mockImplementation(async () => ({ ...progress0, version: switched ? 2 : 1 }))
    const content = vi.spyOn(api, 'content').mockResolvedValue(cachedContentB)
    vi.spyOn(api, 'mappings').mockResolvedValue([{ source_id: sourceAId, source_name: '原书源' } as Model<'BookSourceMapping'>])
    const search = vi.spyOn(api, 'search').mockResolvedValue({ groups: [{
      source_id: sourceBId, source_name: '备用书源', items: [
        { name: bookB.name, author: '另一位作者', book_url: 'https://b.example.com/other' },
        { name: bookB.name, author: bookB.author, book_url: bookUrl },
      ],
    }] } as Model<'SearchResponse'>)
    const addMapping = vi.spyOn(api, 'addMapping').mockResolvedValue({ source_id: sourceBId, source_name: '备用书源', book_url: bookUrl } as Model<'BookSourceMapping'>)
    const switchSource = vi.spyOn(api, 'switchSource').mockImplementation(async (_, request) => {
      if (!request.dry_run) switched = true
      return { source_id: sourceBId, chapter_index: 0, confidence: 1 } as Model<'SwitchSourceResult'>
    })

    const reader = await mountReader()
    const button = (label: string, within: ParentNode = reader) =>
      [...within.querySelectorAll('button')].find(node => node.textContent?.trim() === label)
    await vi.waitFor(() => expect(reader.querySelector('.reader-book-title')?.textContent).toBe(bookB.name))
    expect(chapters).toHaveBeenCalled()
    expect(content).not.toHaveBeenCalled()
    rejectToc(new ApiError(422, 'rule_execution_failed', null))
    await vi.waitFor(() => expect(reader.querySelector('.reader-state[role="alert"]')?.textContent).toContain('请求失败（rule_execution_failed）'))
    const headerAction = button('换源', reader.querySelector('.reader-header')!)
    const errorAction = button('换源', reader.querySelector('.reader-state')!)
    expect(headerAction?.disabled).toBe(false)
    expect(errorAction?.disabled).toBe(false)

    const dialog = reader.querySelector<HTMLDialogElement>('.switch-dialog')!
    headerAction!.click()
    await vi.waitFor(() => expect(dialog.open && button('关闭', dialog)?.disabled === false).toBe(true))
    expect(dialog.textContent).toContain('当前源：原书源')
    button('关闭', dialog)!.click()
    expect(dialog.open).toBe(false)
    errorAction!.click()
    await vi.waitFor(() => expect(dialog.open && button('刷新书源', dialog)?.disabled === false).toBe(true))
    button('刷新书源', dialog)!.click()
    await vi.waitFor(() => expect(dialog.querySelector('button[data-row]')?.textContent).toContain('备用书源'))
    expect(search).toHaveBeenCalledWith(bookB.name, expect.any(AbortSignal))
    dialog.querySelector<HTMLButtonElement>('button[data-row]')!.click()
    await vi.waitFor(() => expect(button('预览切换', dialog)?.disabled).toBe(false))
    button('预览切换', dialog)!.click()
    await vi.waitFor(() => expect(button('确认切换并清除单章覆盖', dialog)?.disabled).toBe(false))
    expect(addMapping).toHaveBeenCalledWith(bookId, { source_id: sourceBId, book_url: bookUrl })
    button('确认切换并清除单章覆盖', dialog)!.click()

    await vi.waitFor(() => expect(reader.querySelector('.chapter-body')?.textContent).toBe('源B正文'))
    expect(dialog.open).toBe(false)
    expect(reader.querySelector('.reader-state[role="alert"]')).toBeNull()
    expect(switchSource.mock.calls.map(([, request]) => request.dry_run)).toEqual([true, false])
    expect(chapters.mock.calls.map(([, , source]) => source)).toEqual([undefined, sourceBId, undefined])
    expect(content.mock.calls[0]?.slice(0, 2)).toEqual([bookId, 0])
  })

  it.each(['online', 'local'] as const)('handles an empty TOC for a %s book', async bookType => {
    vi.spyOn(api, 'settings').mockResolvedValue({})
    vi.spyOn(api, 'book').mockResolvedValue({ ...bookB, book_type: bookType, total_chapters: 0 })
    vi.spyOn(api, 'chapters').mockResolvedValue({ ...tocB, total: 0, items: [] })
    vi.spyOn(api, 'progress').mockResolvedValue(progress0)

    const reader = await mountReader()
    await vi.waitFor(() => expect(reader.querySelector('.reader-state[role="alert"]')?.textContent).toContain('目录中没有可读章节'))
    const actions = [...reader.querySelectorAll('.reader-header button, .reader-state button')]
      .filter(node => node.textContent?.trim() === '换源')
    expect(actions).toHaveLength(bookType === 'online' ? 2 : 0)
  })
})
