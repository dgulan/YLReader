import { test, expect, type Page } from '@playwright/test'
import { writeFile } from 'node:fs/promises'

const a = '20000000-0000-4000-8000-000000000001', b = '20000000-0000-4000-8000-000000000002'
const resourceEvents = new WeakMap<Page, { event: string; path: string; at: number }[]>()
function gate() { let release!: () => void; const promise = new Promise<void>(resolve => { release = resolve }); return { promise, release } }

test.afterEach(async ({ page }, info) => {
  const diagnostic = await page.evaluate(() => ({ events: (window as unknown as { readerDiagnostic?: unknown[] }).readerDiagnostic ?? [], visibility: document.visibilityState, stage: document.querySelector('.reader-state')?.textContent, status: document.querySelector('.reader-status')?.textContent })).catch(() => ({ unavailable: true }))
  await writeFile(info.outputPath('reader-loading-diagnostics.json'), JSON.stringify({ ...diagnostic, resources: resourceEvents.get(page) ?? [] }, null, 2))
  // Unmount while mocks still exist, before Playwright removes routes and closes the page.
  const back = page.locator('.reader-header').getByRole('link', { name: '返回书架', exact: true })
  if (await back.count()) await back.click()
})

async function fixture(page: Page, options: { metadata?: boolean; neighbors?: boolean; holdSave?: boolean; failHeldSave?: boolean; conflict?: boolean; loseAcknowledgement?: boolean; holdReview?: boolean; holdFirstSettings?: boolean; holdBooks?: boolean; missingChapter?: number } = {}) {
  const resources: { event: string; path: string; at: number }[] = []
  resourceEvents.set(page, resources)
  for (const event of ['request', 'requestfinished', 'requestfailed'] as const) page.on(event, request => {
    if (resources.length < 4000) resources.push({ event, path: new URL(request.url()).pathname, at: Date.now() })
  })
  await page.addInitScript(() => {
    const events: { event: string; detail: string; at: number }[] = []
    ;(window as unknown as { readerDiagnostic: typeof events }).readerDiagnostic = events
    const log = (event: string, detail = '') => { if (events.length < 2000) events.push({ event, detail, at: Date.now() }) }
    const open = IDBFactory.prototype.open
    IDBFactory.prototype.open = function (...args) {
      log('idb-open', args[0]); const request = open.apply(this, args)
      for (const event of ['success', 'error', 'blocked']) request.addEventListener(event, () => log(`idb-open-${event}`, args[0]))
      return request
    }
    for (const method of ['get', 'getAll', 'put'] as const) {
      const original = IDBObjectStore.prototype[method]
      IDBObjectStore.prototype[method] = function (...args: unknown[]) {
        const name = this.name; log(`idb-${method}`, name)
        const request = Reflect.apply(original, this, args) as IDBRequest
        for (const event of ['success', 'error']) request.addEventListener(event, () => log(`idb-${method}-${event}`, name))
        return request
      }
    }
    const fetch = window.fetch
    window.fetch = async (...args) => {
      const path = new URL(String(args[0]), location.href).pathname
      log('fetch', path)
      try { const response = await fetch(...args); log('response', `${response.status} ${path}`); return response }
      catch (error) { log('fetch-error', path); throw error }
    }
    const frame = window.requestAnimationFrame
    window.requestAnimationFrame = callback => { log('frame'); return frame.call(window, at => { log('frame-done'); callback(at) }) }
    document.addEventListener('visibilitychange', () => log('visibility', document.visibilityState))
    window.addEventListener('unhandledrejection', () => log('unhandledrejection'))
  })
  const metadata = gate(), neighbors = gate(), requests: number[] = []
  const metadataStarted = gate()
  const saving = gate(), saves: { body: Record<string, unknown>; match: string | undefined }[] = []
  const review = gate(), reviewStarted = gate()
  const settings = gate(), settingsStarted = gate()
  const shelf = gate(), shelfStarted = gate()
  let settingsCalls = 0
  let offline = false
  let gateway = 0
  const progress = new Map<string, Record<string, unknown>>()
  async function waitUnlessCanceled(request: import('@playwright/test').Request, held: ReturnType<typeof gate>) {
    let cancel!: () => void
    const canceled = new Promise<void>(resolve => { cancel = resolve })
    const failed = (other: import('@playwright/test').Request) => { if (other === request) cancel() }
    page.on('requestfailed', failed)
    try { return await Promise.race([held.promise.then(() => true), canceled.then(() => false)]) }
    finally { page.off('requestfailed', failed) }
  }
  await page.addInitScript(() => sessionStorage.setItem('yreader-session', JSON.stringify({ access_token: 'ui-test', refresh_token: 'ui-test-refresh', user: { id: 'ui-owner', username: 'synthetic' } })))
  const book = (id: string) => ({ id, name: id === a ? '书籍 A' : '书籍 B', author: 'UI fixture', book_type: 'local', version: 1, active_source_id: '10000000-0000-4000-8000-000000000001', active_book_url: `local:${id}`, total_chapters: 5 })
  const chapter = (id: string, i: number) => ({ id: i + 1, chapter_index: i, sort_order: i, title: `${id === a ? 'A' : 'B'}第${i + 1}章`, normalized_title: `n:${i}`, url: `local:${id}:${i}`, is_volume: false, word_count: 1000 })
  await page.route('**/api/v1/**', async route => {
    const url = new URL(route.request().url()), path = url.pathname.replace('/api/v1', '')
    const isSave = path.endsWith('/progress') && route.request().method() === 'PUT'
    if (isSave) saves.push({ body: route.request().postDataJSON(), match: route.request().headers()['if-match'] })
    if (offline) return route.abort('internetdisconnected')
    if (gateway) return route.fulfill({ status: gateway, json: { error: { code: `http_${gateway}` } } })
    if (path === '/settings') {
      if (++settingsCalls === 1 && options.holdFirstSettings) { settingsStarted.release(); if (!await waitUnlessCanceled(route.request(), settings)) return }
      return route.fulfill({ json: { 'reader.theme': 'paper', 'reader.font_size': 20 } }).catch(() => {})
    }
    if (path === '/books') {
      if (options.holdBooks) { shelfStarted.release(); if (!await waitUnlessCanceled(route.request(), shelf)) return }
      return route.fulfill({ json: { items: [book(b)], total: 1, page: 1, page_size: 20 } }).catch(() => {})
    }
    const match = /^\/books\/([^/]+)(.*)$/.exec(path)
    if (!match || ![a, b].includes(match[1]!)) throw new Error('Unexpected fixture route')
    const id = match[1]!, suffix = match[2]!
    if (!suffix) {
      if (id === a && options.metadata) { metadataStarted.release(); if (!await waitUnlessCanceled(route.request(), metadata)) return }
      return route.fulfill({ json: book(id) }).catch(() => {}) // A canceled request may already be gone.
    }
    if (suffix === '/chapters') return route.fulfill({ json: { book_id: id, total: 5, items: Array.from({ length: 5 }, (_, i) => chapter(id, i)) } })
    if (suffix === '/progress') {
      if (!isSave && options.holdReview && saves.length) { reviewStarted.release(); await review.promise }
      if (isSave && options.holdSave) await saving.promise
      if (isSave && options.failHeldSave && saves.length === 1) return route.abort('internetdisconnected')
      if (isSave && options.conflict) {
        const current = { book_id: id, chapter_index: 4, char_offset: 12, chapter_ratio: .1, version: 8 }
        progress.set(id, current)
        return route.fulfill({ status: 409, json: { error: { code: 'progress_conflict', details: { current } } } })
      }
      const previous = progress.get(id) ?? { book_id: id, chapter_index: 2, char_offset: 0, chapter_ratio: 0, version: 7 }
      if (isSave && Number(route.request().headers()['if-match']?.replaceAll('"', '')) !== Number(previous.version)) return route.fulfill({ status: 409, json: { error: { code: 'progress_conflict', details: { current: previous } } } })
      const next = route.request().method() === 'PUT' ? { ...previous, ...route.request().postDataJSON(), version: Number(previous.version) + 1 } : previous
      progress.set(id, next)
      if (isSave && options.loseAcknowledgement && saves.length === 1) return route.abort('failed')
      return route.fulfill({ json: next })
    }
    if (suffix === '/content') {
      const i = Number(url.searchParams.get('chapter_index'))
      if (i === options.missingChapter) return route.fulfill({ status: 404, json: { error: { code: 'not_found' } } })
      if (id === a && i !== 2) { requests.push(i); if (options.neighbors && !await waitUnlessCanceled(route.request(), neighbors)) return }
      return route.fulfill({ json: { chapter: chapter(id, i), content: `${id === a ? '原书A' : '新书B'}正文😀\n`.repeat(250), cached: false, word_count: 1000, source_id: book(id).active_source_id, prev_chapter_index: i > 0 ? i - 1 : null, next_chapter_index: i < 4 ? i + 1 : null } }).catch(() => {})
    }
    throw new Error(`Unexpected fixture route: ${suffix}`)
  })
  return { metadata, metadataStarted, neighbors, saving, review, reviewStarted, settings, settingsStarted, shelf, shelfStarted, saves, progress, requests, goGateway: (status: number) => { gateway = status }, goOffline: () => { offline = true }, goOnline: () => { offline = false } }
}

for (const navigation of ['hard', 'router'] as const) test(`bookshelf list cancels before ${navigation} navigation without page errors`, async ({ page }) => {
  const errors: string[] = []
  page.on('pageerror', error => errors.push(error.message))
  await page.addInitScript(() => {
    const fetchBefore = window.fetch
    window.fetch = (input, options) => {
      const url = new URL(typeof input === 'string' ? input : input instanceof Request ? input.url : input.toString(), location.href)
      if (url.pathname === '/api/v1/books' && options?.signal) {
        sessionStorage.setItem('shelf-signal', 'yes')
        options.signal.addEventListener('abort', () => sessionStorage.setItem('shelf-aborted', 'yes'), { once: true })
      }
      return fetchBefore.call(window, input, options)
    }
  })
  const f = await fixture(page, { holdBooks: true })
  try {
    // Isolate the held shelf GET; settings teardown has its own independent case below.
    const settingsLoaded = page.waitForResponse(response => new URL(response.url()).pathname === '/api/v1/settings')
    await page.goto('/bookshelf')
    await f.shelfStarted.promise
    const settingsResponse = await settingsLoaded
    expect(settingsResponse.ok()).toBeTruthy()
    expect(await settingsResponse.finished()).toBeNull()
    expect(await page.evaluate(() => sessionStorage.getItem('shelf-signal'))).toBe('yes')
    if (navigation === 'hard') await page.goto(`/reader/${a}`)
    else await page.evaluate(path => {
      const root = document.getElementById('app') as unknown as { __vue_app__: { config: { globalProperties: { $router: { push: (path: string) => Promise<unknown> } } } } }
      return root.__vue_app__.config.globalProperties.$router.push(path)
    }, `/reader/${a}`)
    await expect(page.getByLabel('章节阅读区')).toHaveAttribute('aria-busy', 'false')
    await expect(page.getByRole('heading', { name: 'A第3章', exact: true })).toBeVisible()
    expect(await page.evaluate(() => sessionStorage.getItem('shelf-aborted'))).toBe('yes')
    expect(errors).toEqual([])
  } finally { f.shelf.release() }
})

test('hard navigation during an initial settings request preserves strict error checks', async ({ page }) => {
  const errors: string[] = []
  page.on('pageerror', error => errors.push(error.message))
  const f = await fixture(page, { holdFirstSettings: true })
  try {
    await page.goto('/bookshelf')
    await f.settingsStarted.promise
    await page.goto(`/reader/${a}`)
    f.settings.release()
    await expect(page.getByLabel('章节阅读区')).toHaveAttribute('aria-busy', 'false')
    await expect(page.getByRole('heading', { name: 'A第3章', exact: true })).toBeVisible()
    expect(errors).toEqual([])
  } finally { f.settings.release() }
})

async function cachedRows(page: Page, table: string) {
  return page.evaluate(table => new Promise<{ bookId?: string; content?: { content: string }; book?: { id: string }; offset?: number; dirty?: boolean; base?: { version: number } }[]>((resolve, reject) => {
    const open = indexedDB.open('yreader-offline-v1')
    open.onerror = () => reject(open.error)
    open.onsuccess = () => {
      const db = open.result, read = db.transaction(table).objectStore(table).getAll()
      read.onsuccess = () => { resolve(read.result); db.close() }; read.onerror = () => { reject(read.error); db.close() }
    }
  }), table)
}

async function warmBookshelfModule(page: Page) {
  // Exclude destination resource loading from the controlled save/cancel measurement.
  await page.evaluate(async () => {
    type Route = { name?: string; components?: { default?: unknown } }
    const root = document.getElementById('app') as unknown as { __vue_app__: { config: { globalProperties: { $router: { getRoutes: () => Route[] } } } } }
    const component = root.__vue_app__.config.globalProperties.$router.getRoutes().find(route => route.name === 'bookshelf')?.components?.default
    if (!component) throw new Error('Bookshelf route missing from fixture')
    if (typeof component === 'function') await component()
  })
}

test('leaving a loading book cancels its metadata and cannot replace the next book', async ({ page }) => {
  const canceled: string[] = []
  page.on('requestfailed', request => { if (new URL(request.url()).pathname === `/api/v1/books/${a}`) canceled.push(a) })
  const f = await fixture(page, { metadata: true })
  try {
    await page.goto(`/reader/${a}`)
    await expect(page.getByText('正在翻开这一页…')).toBeVisible()
    await f.metadataStarted.promise
    await page.getByRole('link', { name: '返回书架' }).click()
    await page.getByRole('button', { name: '开始阅读', exact: true }).click()
    await expect(page.getByRole('heading', { name: 'B第3章', exact: true })).toBeVisible()
    f.metadata.release()
    await expect.poll(() => canceled).toContain(a)
    await expect(page.getByLabel('章节阅读区')).toHaveAttribute('aria-busy', 'false')
    await expect(page.locator('.chapter-body')).toContainText('新书B正文')
    expect((await cachedRows(page, 'books')).map(row => row.book?.id)).toEqual([b])
    expect((await cachedRows(page, 'chapters')).every(row => row.bookId === b && !row.content?.content.includes('原书A'))).toBe(true)
  } finally { f.metadata.release(); f.neighbors.release() }
})

test('reconnection during a pending failed save retries once after that save settles', async ({ page }) => {
  const f = await fixture(page, { holdSave: true, failHeldSave: true })
  try {
    await readyReader(page)
    await page.getByRole('button', { name: '下一章', exact: true }).click()
    await expect.poll(() => f.saves.length).toBe(1)
    await page.evaluate(() => window.dispatchEvent(new Event('online')))
    expect(f.saves).toHaveLength(1)
    f.saving.release()
    await expect.poll(() => f.saves.length).toBe(2)
    await expect.poll(() => f.progress.get(a)?.chapter_index).toBe(3)
    expect(f.progress.get(a)?.version).toBe(8)
    await expect(page.getByText('进度已同步', { exact: true })).toBeVisible()
    expect(f.saves).toHaveLength(2)
  } finally { f.saving.release() }
})

test('preloading uses two slots and cancels queued work on navigation', async ({ page }) => {
  const f = await fixture(page, { neighbors: true })
  try {
    await page.goto(`/reader/${a}`)
    await expect(page.getByRole('heading', { name: 'A第3章', exact: true })).toBeVisible()
    await expect.poll(() => f.requests).toEqual([3, 1])
    await warmBookshelfModule(page)
    await page.getByRole('link', { name: '返回书架' }).click()
    f.neighbors.release()
    await expect(page.getByRole('heading', { name: /^我的书房/ })).toBeVisible()
    expect(f.requests).toEqual([3, 1])
    const rows = await cachedRows(page, 'chapters')
    expect(rows).toHaveLength(1)
    expect(rows[0]!.content?.content).toContain('原书A')
  } finally { f.metadata.release(); f.neighbors.release() }
})

test('leaving during a pending save waits for the local checkpoint rather than its network deadline', async ({ page }) => {
  const f = await fixture(page, { holdSave: true })
  try {
    await readyReader(page)
    await warmBookshelfModule(page)
    await page.clock.install()
    await hideAt(page, 650)
    await expect.poll(() => f.saves.length).toBe(1)
    await page.getByRole('link', { name: '返回书架' }).click()
    await expect(page).toHaveURL(/bookshelf/, { timeout: 3000 })
    const local = (await cachedRows(page, 'positions'))[0]!
    expect(local.dirty).toBe(true); expect(local.base?.version).toBe(7)
    f.saving.release()
    await expect.poll(() => f.progress.get(a)?.version).toBe(8)
    expect((await cachedRows(page, 'positions'))[0]!.base?.version).toBe(7)
    expect(f.saves).toHaveLength(1)
  } finally { f.saving.release() }
})

test('the five chapter cache remains readable after going offline', async ({ page }) => {
  const f = await fixture(page)
  await page.goto(`/reader/${a}`)
  await expect.poll(async () => (await cachedRows(page, 'chapters')).length).toBe(5)
  f.goOffline()
  await page.getByRole('button', { name: '下一章', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'A第4章', exact: true })).toBeVisible()
  await expect(page.getByText('离线缓存 · 第 4 章')).toBeVisible()
  await expect(page.locator('.chapter-body')).toContainText('原书A正文😀')
})

test('cached reading survives a gateway outage but never masks an authorization denial', async ({ page }) => {
  const f = await fixture(page)
  await readyReader(page)
  f.goGateway(503)
  await page.reload()
  await expect(page.getByLabel('章节阅读区')).toHaveAttribute('aria-busy', 'false')
  await expect(page.getByText('离线缓存 · 第 3 章')).toBeVisible()
  await expect(page.locator('.chapter-body')).toContainText('原书A正文')
  f.goGateway(403)
  await page.reload()
  await expect(page.locator('.reader-state[role=alert]')).toBeVisible()
  await expect(page.locator('.chapter-body')).not.toBeVisible()
})

test('a failed chapter load cannot capture the end of the hidden previous chapter', async ({ page }) => {
  const f = await fixture(page, { missingChapter: 3 })
  await page.goto(`/reader/${a}`)
  await expect(page.getByLabel('章节阅读区')).toHaveAttribute('aria-busy', 'false')
  await hideAt(page, 650)
  await expect.poll(async () => (await cachedRows(page, 'positions'))[0]?.dirty).toBe(false)
  const before = (await cachedRows(page, 'positions'))[0]!
  await page.getByRole('button', { name: '下一章', exact: true }).click()
  await expect(page.locator('.reader-state[role=alert]')).toBeVisible()
  await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pagehide')))
  expect((await cachedRows(page, 'positions'))[0]!.offset).toBe(before.offset)
  expect(f.saves).toHaveLength(1)
})

async function readyReader(page: Page) {
  await page.goto(`/reader/${a}`)
  await expect(page.getByLabel('章节阅读区')).toHaveAttribute('aria-busy', 'false')
  await expect.poll(async () => (await cachedRows(page, 'chapters')).length).toBe(5)
}

// Explicit synthetic lifecycle events, not OS suspension or guaranteed unload I/O.
async function hideAt(page: Page, top: number) {
  await page.evaluate(top => {
    const viewport = document.querySelector<HTMLElement>('.reader-viewport')!
    viewport.scrollTop = top
    viewport.dispatchEvent(new Event('scroll'))
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' })
    document.dispatchEvent(new Event('visibilitychange'))
    window.dispatchEvent(new PageTransitionEvent('pagehide'))
  }, top)
}

test('hidden offline checkpoints survive reload and synchronize on an explicit online event', async ({ page }) => {
  const f = await fixture(page)
  await readyReader(page)
  f.goOffline()
  await hideAt(page, 650)
  await expect.poll(() => f.saves.length).toBe(1)
  await expect(page.getByText('连接或响应异常，保存结果尚未确认；阅读位置已保存在本机', { exact: true })).toBeVisible()
  const local = (await cachedRows(page, 'positions'))[0]!
  expect(local.offset).toBeGreaterThan(0); expect(local.dirty).toBe(true); expect(local.base?.version).toBe(7)
  await page.reload()
  await expect(page.getByLabel('章节阅读区')).toHaveAttribute('aria-busy', 'false')
  expect((await cachedRows(page, 'positions'))[0]!.offset).toBe(local.offset)
  f.goOnline()
  await page.evaluate(() => window.dispatchEvent(new Event('online')))
  await expect.poll(async () => (await cachedRows(page, 'positions'))[0]?.dirty).toBe(false)
  expect(f.progress.get(a)?.char_offset).toBe(local.offset)
  expect((await cachedRows(page, 'positions'))[0]!.base?.version).toBe(8)
})

test('a new hidden position is stored locally while a slow save keeps its original version', async ({ page }) => {
  const f = await fixture(page, { holdSave: true })
  try {
    await readyReader(page)
    await hideAt(page, 650)
    await expect.poll(() => f.saves.length).toBe(1)
    const first = Number(f.saves[0]!.body.char_offset)
    await hideAt(page, 1250)
    await expect.poll(async () => (await cachedRows(page, 'positions'))[0]?.offset ?? 0).toBeGreaterThan(first)
    const latest = (await cachedRows(page, 'positions'))[0]!
    expect(latest.dirty).toBe(true); expect(latest.base?.version).toBe(7)
    expect(f.saves).toHaveLength(1)
    f.saving.release()
    await expect.poll(async () => (await cachedRows(page, 'positions'))[0]?.dirty).toBe(false)
    expect(f.saves.map(save => save.match)).toEqual(['"7"', '"8"'])
    expect(f.progress.get(a)?.char_offset).toBe(latest.offset)
    expect((await cachedRows(page, 'positions'))[0]!.base?.version).toBe(9)
  } finally { f.saving.release() }
})

test('a lifecycle save conflict survives reload until the user explicitly chooses the remote position', async ({ page }) => {
  const f = await fixture(page, { conflict: true })
  await readyReader(page)
  await hideAt(page, 650)
  await expect(page.getByRole('button', { name: '使用另一端位置', exact: true })).toBeVisible()
  expect(f.saves).toHaveLength(1)
  expect((await cachedRows(page, 'positions'))[0]!.base?.version).toBe(7)
  await page.reload()
  await expect(page.getByRole('button', { name: '使用另一端位置', exact: true })).toBeVisible()
  expect((await cachedRows(page, 'positions'))[0]!.base?.version).toBe(7)
  await page.getByRole('button', { name: '使用另一端位置', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'A第5章', exact: true })).toBeVisible()
  await expect.poll(async () => (await cachedRows(page, 'positions'))[0]?.dirty).toBe(false)
  expect(f.saves).toHaveLength(1)
})

test('a server acknowledgement remains successful when the local acknowledgement write fails', async ({ page }) => {
  const f = await fixture(page)
  await readyReader(page)
  await page.evaluate(() => {
    const state = { enabled: true }
    Object.assign(window, { checkpointStorageFault: state })
    const put = IDBObjectStore.prototype.put
    IDBObjectStore.prototype.put = function (...args: Parameters<IDBObjectStore['put']>) {
      const value = args[0] as { base?: { version: number } }
      if (this.name === 'positions' && state.enabled && (value.base?.version ?? 0) >= 8) throw new DOMException('synthetic quota', 'QuotaExceededError')
      return put.apply(this, args)
    }
  })
  await hideAt(page, 650)
  await expect(page.getByText('进度已同步，但本机记录更新失败，请保持页面打开并重试保存', { exact: true })).toBeVisible()
  expect(f.progress.get(a)?.version).toBe(8); expect(f.saves).toHaveLength(1)
  await page.evaluate(() => { (window as unknown as { checkpointStorageFault: { enabled: boolean } }).checkpointStorageFault.enabled = false })
  await page.getByRole('button', { name: '保存进度', exact: true }).click()
  await expect.poll(async () => (await cachedRows(page, 'positions'))[0]?.base?.version).toBe(8)
  expect((await cachedRows(page, 'positions'))[0]!.dirty).toBe(false)
  expect(f.saves).toHaveLength(1)
})

test('offline plus local storage failure never claims that the position is saved locally', async ({ page }) => {
  const f = await fixture(page)
  await readyReader(page)
  await page.evaluate(() => {
    const put = IDBObjectStore.prototype.put
    IDBObjectStore.prototype.put = function (...args: Parameters<IDBObjectStore['put']>) {
      if (this.name === 'positions') throw new DOMException('synthetic quota', 'QuotaExceededError')
      return put.apply(this, args)
    }
  })
  f.goOffline()
  await hideAt(page, 650)
  await expect(page.getByText('连接或响应异常，保存结果尚未确认；本机记录也未保存，请保持页面打开', { exact: true })).toBeVisible()
  await expect(page.getByText('当前离线，阅读位置已保存在本机', { exact: true })).toHaveCount(0)
  expect(f.progress.get(a)?.version).toBe(7)
  expect((await cachedRows(page, 'positions'))[0]!.offset).toBe(0)
})

test('a lost save acknowledgement is reviewed without treating matching coordinates as proof', async ({ page }) => {
  const f = await fixture(page, { loseAcknowledgement: true })
  await readyReader(page)
  await hideAt(page, 650)
  await expect(page.getByRole('button', { name: '核对服务器进度', exact: true })).toBeVisible()
  expect(f.progress.get(a)?.version).toBe(8)
  const before = (await cachedRows(page, 'positions'))[0]!
  expect(before.dirty).toBe(true); expect(before.base?.version).toBe(7)
  await page.getByRole('button', { name: '核对服务器进度', exact: true }).click()
  await expect(page.getByRole('button', { name: '使用另一端位置', exact: true })).toBeEnabled()
  expect(f.saves).toHaveLength(1)
  expect((await cachedRows(page, 'positions'))[0]!.base?.version).toBe(7)
  await page.getByRole('button', { name: '使用另一端位置', exact: true }).click()
  await expect.poll(async () => (await cachedRows(page, 'positions'))[0]?.dirty).toBe(false)
  expect((await cachedRows(page, 'positions'))[0]!.base?.version).toBe(8)
  expect(f.saves).toHaveLength(1)
})

test('an intervening remote update requires an explicit local choice using its current version', async ({ page }) => {
  const f = await fixture(page, { loseAcknowledgement: true })
  await readyReader(page)
  await hideAt(page, 650)
  await expect(page.getByRole('button', { name: '核对服务器进度', exact: true })).toBeVisible()
  const local = (await cachedRows(page, 'positions'))[0]!
  f.progress.set(a, { ...f.progress.get(a), chapter_index: 4, char_offset: 12, version: 9 })
  await page.getByRole('button', { name: '核对服务器进度', exact: true }).click()
  await expect(page.getByRole('button', { name: '保留本机位置', exact: true })).toBeEnabled()
  expect(f.saves).toHaveLength(1)
  expect((await cachedRows(page, 'positions'))[0]!.base?.version).toBe(7)
  await page.getByRole('button', { name: '保留本机位置', exact: true }).click()
  await expect.poll(async () => (await cachedRows(page, 'positions'))[0]?.dirty).toBe(false)
  expect(f.saves.map(save => save.match)).toEqual(['"7"', '"9"'])
  expect(f.progress.get(a)?.char_offset).toBe(local.offset)
  expect(f.progress.get(a)?.version).toBe(10)
})

test('a save deadline releases navigation while unchanged review keeps the result uncertain', async ({ page }) => {
  const f = await fixture(page, { holdSave: true })
  try {
    await readyReader(page)
    await page.clock.install()
    await hideAt(page, 650)
    await expect.poll(() => f.saves.length).toBe(1)
    await page.clock.fastForward(15_100)
    await expect(page.getByText('保存等待超时，结果尚未确认；阅读位置已保存在本机', { exact: true })).toBeVisible()
    expect((await cachedRows(page, 'positions'))[0]!.base?.version).toBe(7)
    await page.getByRole('button', { name: '核对服务器进度', exact: true }).click()
    await expect(page.getByText('尚未观察到新的服务器版本，原保存结果仍待确认；可重试保存或稍后再次核对', { exact: true })).toBeVisible()
    expect(f.saves).toHaveLength(1)
    await page.getByRole('link', { name: '返回书架' }).click()
    await expect(page.getByRole('heading', { name: /^我的书房/ })).toBeVisible()
    expect(f.saves).toHaveLength(1)
    expect((await cachedRows(page, 'positions'))[0]!.dirty).toBe(true)
    // The synthetic server may still finish after the client gave up waiting.
    f.saving.release()
    await expect.poll(() => f.progress.get(a)?.version).toBe(8)
    expect((await cachedRows(page, 'positions'))[0]!.base?.version).toBe(7)
  } finally { f.saving.release() }
})

test('a review response arriving after the session disappears cannot expose a conflict or authorize a save', async ({ page }) => {
  const f = await fixture(page, { loseAcknowledgement: true, holdReview: true })
  try {
    await readyReader(page)
    await hideAt(page, 650)
    await expect(page.getByRole('button', { name: '核对服务器进度', exact: true })).toBeVisible()
    const response = page.waitForResponse(r => new URL(r.url()).pathname === `/api/v1/books/${a}/progress` && r.request().method() === 'GET')
    await page.getByRole('button', { name: '核对服务器进度', exact: true }).click()
    await expect(page.getByRole('button', { name: '正在核对…', exact: true })).toBeDisabled()
    // Busy state precedes local persistence; wait until the GET is actually held.
    await f.reviewStarted.promise
    // Directly replace the authentication context to exercise the post-await guard.
    await page.evaluate(() => sessionStorage.removeItem('yreader-session'))
    f.review.release()
    await (await response).finished()
    await expect(page.getByRole('button', { name: '使用另一端位置', exact: true })).toHaveCount(0)
    expect(f.saves).toHaveLength(1)
    expect((await cachedRows(page, 'positions'))[0]!.base?.version).toBe(7)
    expect((await cachedRows(page, 'positions'))[0]!.dirty).toBe(true)
  } finally { f.review.release() }
})

test('cached reading survives a 502 TOC failure on reload and renders cached chapter body', async ({ page }) => {
  await fixture(page)
  await readyReader(page)
  await page.route(`**/api/v1/books/${a}/chapters?*`, route => route.fulfill({ status: 502, json: { error: { code: 'source_request_failed' } } }))
  await page.route(`**/api/v1/books/${a}/content?*`, route => route.fulfill({ status: 502, json: { error: { code: 'source_request_failed' } } }))
  const failedTOC = page.waitForResponse(r => r.url().includes(`/books/${a}/chapters?`) && r.status() === 502)
  await page.reload()
  await failedTOC
  await expect(page.getByLabel('章节阅读区')).toHaveAttribute('aria-busy', 'false')
  await expect(page.getByText('离线缓存 · 第 3 章')).toBeVisible()
  await expect(page.locator('.chapter-body')).toContainText('原书A正文')
})
